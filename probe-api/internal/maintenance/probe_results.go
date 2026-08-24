package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	probeAggregate5mLock int64 = 0x70726f6265356d31
	probeAggregate1hLock int64 = 0x70726f6265316831
	probeRetentionLock   int64 = 0x70726f6265726574

	probeAggregate5mWatermark = "probe-result-5m"
	probeAggregate1hWatermark = "probe-result-1h"
)

var ErrProbeWatermarkInvariant = errors.New("probe maintenance watermark invariant violated")

type ProbeAggregationResult struct {
	AsOf         time.Time
	Through      time.Time
	LockAcquired bool
	RowsUpserted int64
}

type ProbeRetentionResult struct {
	AsOf           time.Time
	LockAcquired   bool
	RawDeleted     int64
	FiveMinDeleted int64
	HourlyDeleted  int64
}

type ProbeResultMaintenance struct {
	pool         *pgxpool.Pool
	tickInterval time.Duration
}

func NewProbeResultMaintenance(pool *pgxpool.Pool, tickInterval time.Duration) (*ProbeResultMaintenance, error) {
	if pool == nil || tickInterval <= 0 {
		return nil, errors.New("probe result maintenance requires a database pool and positive tick interval")
	}
	return &ProbeResultMaintenance{pool: pool, tickInterval: tickInterval}, nil
}

func (job *ProbeResultMaintenance) Run5mOnce(ctx context.Context) (ProbeAggregationResult, error) {
	return job.runAggregation(ctx, aggregation5m)
}

func (job *ProbeResultMaintenance) Run1hOnce(ctx context.Context) (ProbeAggregationResult, error) {
	return job.runAggregation(ctx, aggregation1h)
}

type aggregationKind struct {
	name             string
	lock             int64
	bucketWidth      time.Duration
	recompute        time.Duration
	sourceTable      string
	destinationTable string
}

var (
	aggregation5m = aggregationKind{
		name: probeAggregate5mWatermark, lock: probeAggregate5mLock,
		bucketWidth: 5 * time.Minute, recompute: 30 * time.Minute,
		sourceTable: "probe_result_raw", destinationTable: "probe_result_5m",
	}
	aggregation1h = aggregationKind{
		name: probeAggregate1hWatermark, lock: probeAggregate1hLock,
		bucketWidth: time.Hour, recompute: 3 * time.Hour,
		sourceTable: "probe_result_5m", destinationTable: "probe_result_1h",
	}
)

func (job *ProbeResultMaintenance) runAggregation(ctx context.Context, kind aggregationKind) (ProbeAggregationResult, error) {
	tx, err := job.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ProbeAggregationResult{}, fmt.Errorf("begin %s aggregation transaction: %w", kind.name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result ProbeAggregationResult
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP, pg_try_advisory_xact_lock($1::bigint)`, kind.lock).
		Scan(&result.AsOf, &result.LockAcquired); err != nil {
		return ProbeAggregationResult{}, fmt.Errorf("acquire %s aggregation lock: %w", kind.name, err)
	}
	result.AsOf = result.AsOf.UTC()
	if !result.LockAcquired {
		if err := tx.Commit(ctx); err != nil {
			return ProbeAggregationResult{}, fmt.Errorf("commit contended %s aggregation: %w", kind.name, err)
		}
		return result, nil
	}

	through := alignedFloor(result.AsOf, kind.bucketWidth)
	if kind.name == probeAggregate1hWatermark {
		fiveMinuteWatermark, watermarkErr := readWatermark(ctx, tx, probeAggregate5mWatermark, false)
		if watermarkErr != nil {
			return ProbeAggregationResult{}, watermarkErr
		}
		if fiveMinuteWatermark == nil {
			through = time.Time{}
		} else {
			if !fiveMinuteWatermark.Equal(alignedFloor(*fiveMinuteWatermark, 5*time.Minute)) ||
				fiveMinuteWatermark.After(alignedFloor(result.AsOf, 5*time.Minute)) {
				return ProbeAggregationResult{}, fmt.Errorf("%w: five-minute source watermark is invalid", ErrProbeWatermarkInvariant)
			}
			fiveThrough := alignedFloor(*fiveMinuteWatermark, time.Hour)
			if fiveThrough.Before(through) {
				through = fiveThrough
			}
		}
	}
	result.Through = through

	watermark, err := readWatermark(ctx, tx, kind.name, true)
	if err != nil {
		return ProbeAggregationResult{}, err
	}
	if watermark != nil && (!watermark.Equal(alignedFloor(*watermark, kind.bucketWidth)) ||
		(!through.IsZero() && watermark.After(through))) {
		return ProbeAggregationResult{}, fmt.Errorf("%w: %s is outside the closed bucket boundary", ErrProbeWatermarkInvariant, kind.name)
	}
	if through.IsZero() {
		if watermark != nil {
			return ProbeAggregationResult{}, fmt.Errorf("%w: %s exists without its source watermark", ErrProbeWatermarkInvariant, kind.name)
		}
		if err := tx.Commit(ctx); err != nil {
			return ProbeAggregationResult{}, fmt.Errorf("commit waiting %s aggregation: %w", kind.name, err)
		}
		return result, nil
	}

	start, err := aggregationStart(ctx, tx, kind, watermark, through)
	if err != nil {
		return ProbeAggregationResult{}, err
	}
	if start.Before(through) {
		var tag pgconn.CommandTag
		switch kind.name {
		case probeAggregate5mWatermark:
			tag, err = aggregateFiveMinutes(ctx, tx, start, through)
		case probeAggregate1hWatermark:
			tag, err = aggregateHourly(ctx, tx, start, through)
		default:
			return ProbeAggregationResult{}, ErrProbeWatermarkInvariant
		}
		if err != nil {
			return ProbeAggregationResult{}, fmt.Errorf("aggregate %s buckets: %w", kind.name, err)
		}
		result.RowsUpserted = tag.RowsAffected()
	}
	if err := writeWatermark(ctx, tx, kind, through); err != nil {
		return ProbeAggregationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProbeAggregationResult{}, fmt.Errorf("commit %s aggregation: %w", kind.name, err)
	}
	return result, nil
}

func aggregationStart(ctx context.Context, tx pgx.Tx, kind aggregationKind, watermark *time.Time, through time.Time) (time.Time, error) {
	start := through.Add(-kind.recompute)
	if watermark != nil && watermark.Before(start) {
		start = *watermark
	}
	if watermark == nil {
		var earliest pgtype.Timestamptz
		var query string
		if kind.name == probeAggregate5mWatermark {
			query = `SELECT min(date_bin(INTERVAL '5 minutes', effective_at, TIMESTAMPTZ '1970-01-01 00:00:00+00'))
			         FROM probe_result_raw WHERE effective_at < $1::timestamptz`
		} else {
			query = `SELECT min(date_bin(INTERVAL '1 hour', bucket_start, TIMESTAMPTZ '1970-01-01 00:00:00+00'))
			         FROM probe_result_5m WHERE bucket_start < $1::timestamptz`
		}
		if err := tx.QueryRow(ctx, query, through).Scan(&earliest); err != nil {
			return time.Time{}, fmt.Errorf("query earliest %s source bucket: %w", kind.name, err)
		}
		if earliest.Valid {
			start = earliest.Time.UTC()
		} else {
			start = through
		}
	}
	return start.UTC(), nil
}

func aggregateFiveMinutes(ctx context.Context, tx pgx.Tx, start, through time.Time) (pgconn.CommandTag, error) {
	return tx.Exec(ctx, `
		INSERT INTO probe_result_5m (
			target_id, bucket_start, result_count, sent_count, received_count,
			http_error_count, latency_sum_us, latency_min_us, latency_max_us
		)
		SELECT target_id,
		       date_bin(INTERVAL '5 minutes', effective_at, TIMESTAMPTZ '1970-01-01 00:00:00+00') AS bucket_start,
		       count(*)::integer,
		       sum(sent_count), sum(received_count),
		       sum(CASE
			   WHEN http_status_code < 200 OR http_status_code >= 400 THEN received_count
			   ELSE 0
		       END),
		       sum(latency_sum_us),
		       min(latency_min_us), max(latency_max_us)
		FROM probe_result_raw
		WHERE effective_at >= $1::timestamptz
		  AND effective_at < $2::timestamptz
		GROUP BY target_id, date_bin(INTERVAL '5 minutes', effective_at, TIMESTAMPTZ '1970-01-01 00:00:00+00')
		ON CONFLICT (target_id, bucket_start) DO UPDATE
		SET result_count = EXCLUDED.result_count,
		    sent_count = EXCLUDED.sent_count,
		    received_count = EXCLUDED.received_count,
		    http_error_count = EXCLUDED.http_error_count,
		    latency_sum_us = EXCLUDED.latency_sum_us,
		    latency_min_us = EXCLUDED.latency_min_us,
		    latency_max_us = EXCLUDED.latency_max_us
	`, start, through)
}

func aggregateHourly(ctx context.Context, tx pgx.Tx, start, through time.Time) (pgconn.CommandTag, error) {
	return tx.Exec(ctx, `
		INSERT INTO probe_result_1h (
			target_id, bucket_start, result_count, sent_count, received_count,
			http_error_count, latency_sum_us, latency_min_us, latency_max_us
		)
		SELECT target_id,
		       date_bin(INTERVAL '1 hour', bucket_start, TIMESTAMPTZ '1970-01-01 00:00:00+00') AS bucket_start,
		       sum(result_count)::integer,
		       sum(sent_count), sum(received_count), sum(http_error_count),
		       sum(latency_sum_us),
		       min(latency_min_us), max(latency_max_us)
		FROM probe_result_5m
		WHERE bucket_start >= $1::timestamptz
		  AND bucket_start < $2::timestamptz
		GROUP BY target_id, date_bin(INTERVAL '1 hour', bucket_start, TIMESTAMPTZ '1970-01-01 00:00:00+00')
		ON CONFLICT (target_id, bucket_start) DO UPDATE
		SET result_count = EXCLUDED.result_count,
		    sent_count = EXCLUDED.sent_count,
		    received_count = EXCLUDED.received_count,
		    http_error_count = EXCLUDED.http_error_count,
		    latency_sum_us = EXCLUDED.latency_sum_us,
		    latency_min_us = EXCLUDED.latency_min_us,
		    latency_max_us = EXCLUDED.latency_max_us
	`, start, through)
}

func readWatermark(ctx context.Context, tx pgx.Tx, name string, create bool) (*time.Time, error) {
	if create {
		if _, err := tx.Exec(ctx, `
			INSERT INTO job_watermarks (job_name, watermark_at, details)
			VALUES ($1, NULL, '{}'::jsonb)
			ON CONFLICT (job_name) DO NOTHING
		`, name); err != nil {
			return nil, fmt.Errorf("initialize %s watermark: %w", name, err)
		}
	}
	var value pgtype.Timestamptz
	query := `SELECT watermark_at FROM job_watermarks WHERE job_name = $1`
	if create {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, name).Scan(&value)
	if err == pgx.ErrNoRows && !create {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s watermark: %w", name, err)
	}
	if !value.Valid {
		return nil, nil
	}
	result := value.Time.UTC()
	return &result, nil
}

func writeWatermark(ctx context.Context, tx pgx.Tx, kind aggregationKind, through time.Time) error {
	if _, err := tx.Exec(ctx, `
		UPDATE job_watermarks
		SET watermark_at = $2::timestamptz,
		    details = jsonb_build_object(
			'source_table', $3::text,
			'destination_table', $4::text,
			'bucket_seconds', $5::bigint
		    ),
		    updated_at = CURRENT_TIMESTAMP
		WHERE job_name = $1
	`, kind.name, through, kind.sourceTable, kind.destinationTable, int64(kind.bucketWidth/time.Second)); err != nil {
		return fmt.Errorf("advance %s watermark: %w", kind.name, err)
	}
	return nil
}

func (job *ProbeResultMaintenance) RunRetentionOnce(ctx context.Context) (ProbeRetentionResult, error) {
	tx, err := job.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ProbeRetentionResult{}, fmt.Errorf("begin probe retention transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result ProbeRetentionResult
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP, pg_try_advisory_xact_lock($1::bigint)`, probeRetentionLock).
		Scan(&result.AsOf, &result.LockAcquired); err != nil {
		return ProbeRetentionResult{}, fmt.Errorf("acquire probe retention lock: %w", err)
	}
	result.AsOf = result.AsOf.UTC()
	if !result.LockAcquired {
		if err := tx.Commit(ctx); err != nil {
			return ProbeRetentionResult{}, fmt.Errorf("commit contended probe retention: %w", err)
		}
		return result, nil
	}

	fiveMinuteWatermark, err := readWatermark(ctx, tx, probeAggregate5mWatermark, false)
	if err != nil {
		return ProbeRetentionResult{}, err
	}
	hourlyWatermark, err := readWatermark(ctx, tx, probeAggregate1hWatermark, false)
	if err != nil {
		return ProbeRetentionResult{}, err
	}
	if err := validateCleanupWatermarks(result.AsOf, fiveMinuteWatermark, hourlyWatermark); err != nil {
		return ProbeRetentionResult{}, err
	}

	if fiveMinuteWatermark != nil {
		// The 30-minute safety horizon matches the deterministic reprocessing
		// window, so a short target retention cannot erase bounded late input
		// before a successful final recomputation.
		tag, deleteErr := tx.Exec(ctx, `
			DELETE FROM probe_result_raw AS result
			USING probe_targets AS target
			WHERE result.target_id = target.id
			  AND result.effective_at < GREATEST(
				$1::timestamptz - make_interval(secs => target.retention_seconds),
				$1::timestamptz - INTERVAL '24 hours'
			  )
			  AND result.effective_at < $1::timestamptz - INTERVAL '30 minutes'
			  AND result.received_at <= (
				SELECT updated_at FROM job_watermarks WHERE job_name = 'probe-result-5m'
			  )
			  AND date_bin(INTERVAL '5 minutes', result.effective_at, TIMESTAMPTZ '1970-01-01 00:00:00+00')
			      + INTERVAL '5 minutes' <= $2::timestamptz
		`, result.AsOf, *fiveMinuteWatermark)
		if deleteErr != nil {
			return ProbeRetentionResult{}, fmt.Errorf("delete retained raw probe results: %w", deleteErr)
		}
		result.RawDeleted = tag.RowsAffected()
	}
	if hourlyWatermark != nil {
		// Keep the 5m source for the hourly job's three-hour recomputation
		// horizon even when a target retention was shortened below that age.
		tag, deleteErr := tx.Exec(ctx, `
			DELETE FROM probe_result_5m AS result
			USING probe_targets AS target
			WHERE result.target_id = target.id
			  AND result.bucket_start < GREATEST(
				$1::timestamptz - make_interval(secs => target.retention_seconds),
				$1::timestamptz - INTERVAL '7 days'
			  )
			  AND result.bucket_start < $1::timestamptz - INTERVAL '3 hours'
			  AND (
				SELECT updated_at FROM job_watermarks WHERE job_name = 'probe-result-1h'
			  ) >= (
				SELECT updated_at FROM job_watermarks WHERE job_name = 'probe-result-5m'
			  )
			  AND date_bin(INTERVAL '1 hour', result.bucket_start, TIMESTAMPTZ '1970-01-01 00:00:00+00')
			      + INTERVAL '1 hour' <= $2::timestamptz
		`, result.AsOf, *hourlyWatermark)
		if deleteErr != nil {
			return ProbeRetentionResult{}, fmt.Errorf("delete retained five-minute probe results: %w", deleteErr)
		}
		result.FiveMinDeleted = tag.RowsAffected()
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM probe_result_1h AS result
		USING probe_targets AS target
		WHERE result.target_id = target.id
		  AND result.bucket_start < $1::timestamptz - make_interval(secs => target.retention_seconds)
	`, result.AsOf)
	if err != nil {
		return ProbeRetentionResult{}, fmt.Errorf("delete retained hourly probe results: %w", err)
	}
	result.HourlyDeleted = tag.RowsAffected()

	if err := tx.Commit(ctx); err != nil {
		return ProbeRetentionResult{}, fmt.Errorf("commit probe retention: %w", err)
	}
	return result, nil
}

func validateCleanupWatermarks(asOf time.Time, fiveMinute, hourly *time.Time) error {
	if hourly != nil && fiveMinute == nil {
		return fmt.Errorf("%w: hourly watermark exists without five-minute watermark", ErrProbeWatermarkInvariant)
	}
	if fiveMinute != nil && (!fiveMinute.Equal(alignedFloor(*fiveMinute, 5*time.Minute)) ||
		fiveMinute.After(alignedFloor(asOf, 5*time.Minute))) {
		return fmt.Errorf("%w: five-minute watermark is invalid", ErrProbeWatermarkInvariant)
	}
	if hourly != nil && (!hourly.Equal(alignedFloor(*hourly, time.Hour)) ||
		hourly.After(alignedFloor(asOf, time.Hour))) {
		return fmt.Errorf("%w: hourly watermark is invalid", ErrProbeWatermarkInvariant)
	}
	if fiveMinute != nil && hourly != nil && hourly.After(alignedFloor(*fiveMinute, time.Hour)) {
		return fmt.Errorf("%w: hourly watermark exceeds complete five-minute input", ErrProbeWatermarkInvariant)
	}
	return nil
}

func alignedFloor(value time.Time, width time.Duration) time.Time {
	seconds := int64(width / time.Second)
	return time.Unix((value.UTC().Unix()/seconds)*seconds, 0).UTC()
}

// Run executes a recovery pass immediately, then checks the five-minute and
// hourly schedule at tickInterval. Each Run*Once call obtains its own database
// advisory lock, so multiple API instances remain safe.
func (job *ProbeResultMaintenance) Run(ctx context.Context, onError func(error)) {
	run := func(hourly bool) bool {
		fiveMinuteResult, err := job.Run5mOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				reportProbeMaintenanceError(onError, err)
			}
			return false
		}
		if !fiveMinuteResult.LockAcquired {
			return false
		}
		if !hourly {
			return true
		}
		hourlyResult, err := job.Run1hOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				reportProbeMaintenanceError(onError, err)
			}
			return false
		}
		if !hourlyResult.LockAcquired {
			return false
		}
		retentionResult, err := job.RunRetentionOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				reportProbeMaintenanceError(onError, err)
			}
			return false
		}
		if !retentionResult.LockAcquired {
			return false
		}
		return true
	}
	initialComplete := run(true)
	lastFiveMinute := alignedFloor(time.Now().UTC(), 5*time.Minute)
	lastHour := alignedFloor(time.Now().UTC(), time.Hour)
	if !initialComplete {
		lastFiveMinute = lastFiveMinute.Add(-5 * time.Minute)
		lastHour = lastHour.Add(-time.Hour)
	}
	ticker := time.NewTicker(job.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			fiveMinute := alignedFloor(now.UTC(), 5*time.Minute)
			hour := alignedFloor(now.UTC(), time.Hour)
			if !fiveMinute.After(lastFiveMinute) {
				continue
			}
			hourly := hour.After(lastHour)
			complete := run(hourly)
			if !complete {
				continue
			}
			lastFiveMinute = fiveMinute
			if hourly && complete {
				lastHour = hour
			}
		}
	}
}

func reportProbeMaintenanceError(onError func(error), err error) {
	if onError != nil {
		onError(err)
	}
}
