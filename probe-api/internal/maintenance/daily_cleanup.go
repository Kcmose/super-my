package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dailyCleanupLock               int64 = 0x70726f6265646179
	dailyCleanupWatermark                = "daily-cleanup"
	processedBatchMinimumRetention       = 24 * time.Hour
)

type DailyCleanupResult struct {
	AsOf                    time.Time
	LockAcquired            bool
	Executed                bool
	PreviousSuccessAt       time.Time
	SessionsDeleted         int64
	LoginRateLimitsDeleted  int64
	EnrollmentTokensDeleted int64
	AgentTokensDeleted      int64
	ProcessedBatchesDeleted int64
}

type DailyCleanupConfig struct {
	Interval                time.Duration
	RevokedSessionRetention time.Duration
	LoginIPWindow           time.Duration
	LoginUsernameWindow     time.Duration
}

type DailyCleanup struct {
	pool   *pgxpool.Pool
	config DailyCleanupConfig
}

func NewDailyCleanup(pool *pgxpool.Pool, config DailyCleanupConfig) (*DailyCleanup, error) {
	if pool == nil || config.Interval <= 0 || config.RevokedSessionRetention <= 0 ||
		config.LoginIPWindow <= 0 || config.LoginUsernameWindow <= 0 {
		return nil, errors.New("daily cleanup requires a database pool and positive durations")
	}
	return &DailyCleanup{pool: pool, config: config}, nil
}

func (job *DailyCleanup) RunOnce(ctx context.Context) (result DailyCleanupResult, returnErr error) {
	tx, err := job.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DailyCleanupResult{}, fmt.Errorf("begin daily cleanup transaction: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// pgxpool.Tx.Rollback returns the underlying connection to the pool; the
		// pool discards it when rollback leaves it unusable. Surface every real
		// rollback failure, while ErrTxClosed is expected after a successful commit.
		rollbackErr := tx.Rollback(rollbackCtx)
		if rollbackErr == nil || errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return
		}
		wrapped := fmt.Errorf("rollback daily cleanup transaction: %w", rollbackErr)
		if returnErr != nil {
			returnErr = errors.Join(returnErr, wrapped)
			return
		}
		result = DailyCleanupResult{}
		returnErr = wrapped
	}()

	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP, pg_try_advisory_xact_lock($1::bigint)`, dailyCleanupLock).
		Scan(&result.AsOf, &result.LockAcquired); err != nil {
		return DailyCleanupResult{}, fmt.Errorf("acquire daily cleanup lock: %w", err)
	}
	result.AsOf = result.AsOf.UTC()
	if !result.LockAcquired {
		if err := tx.Commit(ctx); err != nil {
			return DailyCleanupResult{}, fmt.Errorf("commit contended daily cleanup: %w", err)
		}
		return result, nil
	}

	intervalSeconds := durationSecondsCeil(job.config.Interval)
	if _, err := tx.Exec(ctx, `
		INSERT INTO job_watermarks (job_name, watermark_at, details)
		VALUES ($1, NULL, jsonb_build_object('interval_seconds', $2::bigint))
		ON CONFLICT (job_name) DO NOTHING
	`, dailyCleanupWatermark, intervalSeconds); err != nil {
		return DailyCleanupResult{}, fmt.Errorf("initialize daily cleanup watermark: %w", err)
	}
	var previous pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		SELECT watermark_at
		FROM job_watermarks
		WHERE job_name = $1
		FOR UPDATE
	`, dailyCleanupWatermark).Scan(&previous); err != nil {
		return DailyCleanupResult{}, fmt.Errorf("lock daily cleanup watermark: %w", err)
	}
	if previous.Valid {
		result.PreviousSuccessAt = previous.Time.UTC()
		if result.PreviousSuccessAt.After(result.AsOf) {
			return DailyCleanupResult{}, errors.New("daily cleanup watermark is in the future")
		}
		if result.AsOf.Before(result.PreviousSuccessAt.Add(job.config.Interval)) {
			if err := tx.Commit(ctx); err != nil {
				return DailyCleanupResult{}, fmt.Errorf("commit scheduled daily cleanup skip: %w", err)
			}
			return result, nil
		}
	}

	revokedRetentionSeconds := durationSecondsCeil(job.config.RevokedSessionRetention)
	sessionTag, err := tx.Exec(ctx, `
		DELETE FROM sessions
		WHERE expires_at <= $1::timestamptz
		   OR (revoked_at IS NOT NULL
		       AND revoked_at <= $1::timestamptz - ($2 * INTERVAL '1 second'))
	`, result.AsOf, revokedRetentionSeconds)
	if err != nil {
		return DailyCleanupResult{}, fmt.Errorf("delete expired sessions: %w", err)
	}
	result.SessionsDeleted = sessionTag.RowsAffected()

	maxLoginWindow := job.config.LoginIPWindow
	if job.config.LoginUsernameWindow > maxLoginWindow {
		maxLoginWindow = job.config.LoginUsernameWindow
	}
	loginRateRetentionSeconds := 2 * durationSecondsCeil(maxLoginWindow)
	loginRateTag, err := tx.Exec(ctx, `
		DELETE FROM login_rate_limits
		WHERE updated_at <= $1::timestamptz - ($2 * INTERVAL '1 second')
	`, result.AsOf, loginRateRetentionSeconds)
	if err != nil {
		return DailyCleanupResult{}, fmt.Errorf("delete expired login rate limits: %w", err)
	}
	result.LoginRateLimitsDeleted = loginRateTag.RowsAffected()

	enrollmentTag, err := tx.Exec(ctx, `
		DELETE FROM enrollment_tokens
		WHERE expires_at <= $1::timestamptz
	`, result.AsOf)
	if err != nil {
		return DailyCleanupResult{}, fmt.Errorf("delete expired enrollment tokens: %w", err)
	}
	result.EnrollmentTokensDeleted = enrollmentTag.RowsAffected()

	agentTag, err := tx.Exec(ctx, `
		DELETE FROM agent_tokens
		WHERE expires_at IS NOT NULL
		  AND expires_at <= $1::timestamptz
	`, result.AsOf)
	if err != nil {
		return DailyCleanupResult{}, fmt.Errorf("delete expired Agent tokens: %w", err)
	}
	result.AgentTokensDeleted = agentTag.RowsAffected()

	// The node sequence high-water mark remains on nodes after this exact-key
	// ledger expires. A batch is eligible only after the frozen 24-hour window
	// and after protected probe retention has removed every referencing raw row.
	// The foreign key remains the final guard against a concurrent reference.
	minimumRetentionSeconds := durationSecondsCeil(processedBatchMinimumRetention)
	batchTag, err := tx.Exec(ctx, `
		DELETE FROM processed_batches AS batch
		WHERE batch.received_at <= $1::timestamptz - ($2 * INTERVAL '1 second')
		  AND NOT EXISTS (
			SELECT 1
			FROM probe_result_raw AS raw
			WHERE raw.node_id = batch.node_id
			  AND raw.batch_id = batch.batch_id
		  )
	`, result.AsOf, minimumRetentionSeconds)
	if err != nil {
		return DailyCleanupResult{}, fmt.Errorf("delete expired processed batches: %w", err)
	}
	result.ProcessedBatchesDeleted = batchTag.RowsAffected()

	if _, err := tx.Exec(ctx, `
		UPDATE job_watermarks
		SET watermark_at = $2::timestamptz,
		    details = jsonb_build_object(
			'interval_seconds', $3::bigint,
			'sessions_deleted', $4::bigint,
			'login_rate_limits_deleted', $5::bigint,
			'enrollment_tokens_deleted', $6::bigint,
			'agent_tokens_deleted', $7::bigint,
			'processed_batches_deleted', $8::bigint
		    ),
		    updated_at = $2::timestamptz
		WHERE job_name = $1
	`, dailyCleanupWatermark, result.AsOf, intervalSeconds, result.SessionsDeleted,
		result.LoginRateLimitsDeleted, result.EnrollmentTokensDeleted,
		result.AgentTokensDeleted, result.ProcessedBatchesDeleted); err != nil {
		return DailyCleanupResult{}, fmt.Errorf("advance daily cleanup watermark: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return DailyCleanupResult{}, fmt.Errorf("commit daily cleanup: %w", err)
	}
	result.Executed = true
	return result, nil
}

func durationSecondsCeil(value time.Duration) int64 {
	return int64((value-1)/time.Second) + 1
}

// Run checks immediately and then at the configured cadence. The advisory lock
// excludes concurrent instances while the persistent success watermark also
// prevents staggered starts and restarts from running the job too frequently.
func (job *DailyCleanup) Run(ctx context.Context, onError func(error)) {
	delay := time.Duration(0)
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		result, err := job.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if onError != nil {
				onError(err)
			}
			delay = dailyCleanupRetryInterval(job.config.Interval)
			continue
		}
		if !result.LockAcquired {
			delay = dailyCleanupRetryInterval(job.config.Interval)
			continue
		}
		if !result.Executed && !result.PreviousSuccessAt.IsZero() {
			delay = result.PreviousSuccessAt.Add(job.config.Interval).Sub(result.AsOf)
			if delay <= 0 {
				delay = dailyCleanupRetryInterval(job.config.Interval)
			}
			continue
		}
		delay = job.config.Interval
	}
}

func dailyCleanupRetryInterval(interval time.Duration) time.Duration {
	if interval < time.Minute {
		return interval
	}
	return time.Minute
}
