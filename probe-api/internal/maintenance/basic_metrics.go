package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const basicMetricRetentionLock int64 = 0x70726f62655f7269

type BasicMetricRetentionResult struct {
	AsOf              time.Time
	LockAcquired      bool
	MetricRowsDeleted int64
	DiskRowsDeleted   int64
}

type BasicMetricRetention struct {
	pool     *pgxpool.Pool
	interval time.Duration
}

func NewBasicMetricRetention(pool *pgxpool.Pool, interval time.Duration) (*BasicMetricRetention, error) {
	if pool == nil || interval <= 0 {
		return nil, errors.New("basic metric retention requires a database pool and positive interval")
	}
	return &BasicMetricRetention{pool: pool, interval: interval}, nil
}

func (job *BasicMetricRetention) RunOnce(ctx context.Context) (BasicMetricRetentionResult, error) {
	tx, err := job.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BasicMetricRetentionResult{}, fmt.Errorf("begin basic metric retention transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result BasicMetricRetentionResult
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP, pg_try_advisory_xact_lock($1::bigint)`, basicMetricRetentionLock).
		Scan(&result.AsOf, &result.LockAcquired); err != nil {
		return BasicMetricRetentionResult{}, fmt.Errorf("acquire basic metric retention lock: %w", err)
	}
	result.AsOf = result.AsOf.UTC()
	if result.LockAcquired {
		metricTag, deleteErr := tx.Exec(ctx, `
			DELETE FROM node_metric_ring
			WHERE effective_at <= $1::timestamptz - INTERVAL '5 minutes'
		`, result.AsOf)
		if deleteErr != nil {
			return BasicMetricRetentionResult{}, fmt.Errorf("delete expired metric ring rows: %w", deleteErr)
		}
		result.MetricRowsDeleted = metricTag.RowsAffected()

		diskTag, deleteErr := tx.Exec(ctx, `
			DELETE FROM node_disk_ring
			WHERE effective_at <= $1::timestamptz - INTERVAL '5 minutes'
		`, result.AsOf)
		if deleteErr != nil {
			return BasicMetricRetentionResult{}, fmt.Errorf("delete expired disk ring rows: %w", deleteErr)
		}
		result.DiskRowsDeleted = diskTag.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		return BasicMetricRetentionResult{}, fmt.Errorf("commit basic metric retention transaction: %w", err)
	}
	return result, nil
}

// Run executes immediately and then at the configured cadence. A transient
// failure is reported to onError and does not stop later retention attempts.
func (job *BasicMetricRetention) Run(ctx context.Context, onError func(error)) {
	run := func() {
		if _, err := job.RunOnce(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}
	run()
	ticker := time.NewTicker(job.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
