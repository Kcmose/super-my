package maintenance

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDailyCleanupHonorsTokenExpiryAndIdempotencySafety(t *testing.T) {
	pool, ctx := dailyCleanupPool(t)
	holdProbeWatermarkIntegrationLock(t, pool)
	resetProbeMaintenanceWatermarks(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM job_watermarks WHERE job_name = $1`, dailyCleanupWatermark); err != nil {
		t.Fatalf("reset daily cleanup watermark: %v", err)
	}

	nodeID := dailyCleanupUUID(t)
	targetID := dailyCleanupUUID(t)
	userID := dailyCleanupUUID(t)
	staleRateKey := bytes.Repeat([]byte{0xd1}, 32)
	recentRateKey := bytes.Repeat([]byte{0xd2}, 32)
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, 'daily-cleanup-test')`, nodeID); err != nil {
		t.Fatalf("insert cleanup node: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, nodeID); err != nil {
		t.Fatalf("insert cleanup node settings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_targets (
			id, node_id, name, probe_type, host, port, path,
			interval_seconds, timeout_seconds, retention_seconds
		) VALUES ($1::uuid, $2::uuid, 'cleanup-target', 'https', '127.0.0.1', 443, '/', 30, 3, 3600)
	`, targetID, nodeID); err != nil {
		t.Fatalf("insert cleanup probe target: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, 'daily-cleanup-password-hash', 'admin', TRUE)
	`, userID, "daily-cleanup-"+strings.ReplaceAll(userID, "-", "")); err != nil {
		t.Fatalf("insert cleanup session user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1::uuid`, userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM login_rate_limits WHERE key_hash IN ($1, $2)`, staleRateKey, recentRateKey)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM job_watermarks WHERE job_name IN ('probe-result-5m', 'probe-result-1h', $1)`, dailyCleanupWatermark)
	})

	var databaseNow time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		t.Fatalf("query database time: %v", err)
	}
	databaseNow = databaseNow.UTC()

	expiredSessionID := dailyCleanupUUID(t)
	activeSessionID := dailyCleanupUUID(t)
	oldRevokedSessionID := dailyCleanupUUID(t)
	recentRevokedSessionID := dailyCleanupUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, token_hash, csrf_token_hash,
			created_at, last_seen_at, expires_at, revoked_at
		) VALUES
			($1::uuid, $5::uuid, $6, $10, $14::timestamptz - INTERVAL '2 hours', $14::timestamptz - INTERVAL '2 hours', $14::timestamptz, NULL),
			($2::uuid, $5::uuid, $7, $11, $14::timestamptz - INTERVAL '1 hour', $14::timestamptz - INTERVAL '1 hour', $14::timestamptz + INTERVAL '1 hour', NULL),
			($3::uuid, $5::uuid, $8, $12, $14::timestamptz - INTERVAL '26 hours', $14::timestamptz - INTERVAL '26 hours', $14::timestamptz + INTERVAL '1 hour', $14::timestamptz - INTERVAL '25 hours'),
			($4::uuid, $5::uuid, $9, $13, $14::timestamptz - INTERVAL '24 hours', $14::timestamptz - INTERVAL '24 hours', $14::timestamptz + INTERVAL '1 hour', $14::timestamptz - INTERVAL '23 hours')
	`, expiredSessionID, activeSessionID, oldRevokedSessionID, recentRevokedSessionID, userID,
		"session-token-"+expiredSessionID, "session-token-"+activeSessionID,
		"session-token-"+oldRevokedSessionID, "session-token-"+recentRevokedSessionID,
		"csrf-token-"+expiredSessionID, "csrf-token-"+activeSessionID,
		"csrf-token-"+oldRevokedSessionID, "csrf-token-"+recentRevokedSessionID,
		databaseNow); err != nil {
		t.Fatalf("insert session cleanup fixtures: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO login_rate_limits (scope, key_hash, window_started_at, attempt_count, updated_at)
		VALUES
			('source_ip', $1, $3::timestamptz - INTERVAL '12 minutes', 1, $3::timestamptz - INTERVAL '11 minutes'),
			('username', $2, $3::timestamptz - INTERVAL '6 minutes', 1, $3::timestamptz - INTERVAL '5 minutes')
	`, staleRateKey, recentRateKey, databaseNow); err != nil {
		t.Fatalf("insert login rate-limit cleanup fixtures: %v", err)
	}

	expiredEnrollmentID := dailyCleanupUUID(t)
	activeEnrollmentID := dailyCleanupUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO enrollment_tokens (id, node_id, token_hash, created_at, expires_at)
		VALUES
			($1::uuid, $3::uuid, $4, $6::timestamptz - INTERVAL '2 hours', $6::timestamptz - INTERVAL '1 hour'),
			($2::uuid, $3::uuid, $5, $6::timestamptz, $6::timestamptz + INTERVAL '1 hour')
	`, expiredEnrollmentID, activeEnrollmentID, nodeID,
		"expired-enrollment-"+expiredEnrollmentID, "active-enrollment-"+activeEnrollmentID,
		databaseNow); err != nil {
		t.Fatalf("insert enrollment token cleanup fixtures: %v", err)
	}

	expiredAgentID := dailyCleanupUUID(t)
	activeAgentID := dailyCleanupUUID(t)
	durableRevokedAgentID := dailyCleanupUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_tokens (id, node_id, token_hash, created_at, expires_at, revoked_at)
		VALUES
			($1::uuid, $4::uuid, $5, $8::timestamptz - INTERVAL '2 hours', $8::timestamptz - INTERVAL '1 hour', NULL),
			($2::uuid, $4::uuid, $6, $8::timestamptz, $8::timestamptz + INTERVAL '1 hour', NULL),
			($3::uuid, $4::uuid, $7, $8::timestamptz - INTERVAL '2 hours', NULL, $8::timestamptz - INTERVAL '1 hour')
	`, expiredAgentID, activeAgentID, durableRevokedAgentID, nodeID,
		"expired-agent-"+expiredAgentID, "active-agent-"+activeAgentID,
		"durable-revoked-agent-"+durableRevokedAgentID, databaseNow); err != nil {
		t.Fatalf("insert Agent token cleanup fixtures: %v", err)
	}

	oldEmptyBatchID := dailyCleanupUUID(t)
	recentBatchID := dailyCleanupUUID(t)
	oldReferencedBatchID := dailyCleanupUUID(t)
	oldAt := databaseNow.Add(-25 * time.Hour)
	recentAt := databaseNow.Add(-23 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO processed_batches (
			node_id, batch_id, sequence, agent_time, agent_version,
			config_version, payload_checksum, clock_status, received_at
		) VALUES
			($1::uuid, $2::uuid, 1, $5::timestamptz, 'test', 1, $6, 'ok', $5::timestamptz),
			($1::uuid, $3::uuid, 2, $7::timestamptz, 'test', 1, $8, 'ok', $7::timestamptz),
			($1::uuid, $4::uuid, 3, $5::timestamptz, 'test', 1, $9, 'ok', $5::timestamptz)
	`, nodeID, oldEmptyBatchID, recentBatchID, oldReferencedBatchID, oldAt,
		"checksum-"+oldEmptyBatchID, recentAt, "checksum-"+recentBatchID,
		"checksum-"+oldReferencedBatchID); err != nil {
		t.Fatalf("insert processed batch cleanup fixtures: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_result_raw (
			node_id, target_id, batch_id, sample_index,
			sampled_at, effective_at, received_at,
			sent_count, received_count, latency_sum_us,
			latency_min_us, latency_max_us, http_status_code, error_code
		) VALUES (
			$1::uuid, $2::uuid, $3::uuid, 0,
			$4::timestamptz, $4::timestamptz, $4::timestamptz,
			1, 1, 1000, 1000, 1000, 200, NULL
		)
	`, nodeID, targetID, oldReferencedBatchID, oldAt); err != nil {
		t.Fatalf("insert referenced raw probe result: %v", err)
	}

	job, err := NewDailyCleanup(pool, dailyCleanupTestConfig())
	if err != nil {
		t.Fatalf("NewDailyCleanup() error = %v", err)
	}
	first, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if !first.LockAcquired || !first.Executed || first.SessionsDeleted != 2 || first.LoginRateLimitsDeleted != 1 ||
		first.EnrollmentTokensDeleted != 1 || first.AgentTokensDeleted != 1 || first.ProcessedBatchesDeleted != 1 {
		t.Fatalf("first RunOnce() = %#v", first)
	}
	assertDailyCleanupExists(t, pool, "sessions", expiredSessionID, false)
	assertDailyCleanupExists(t, pool, "sessions", activeSessionID, true)
	assertDailyCleanupExists(t, pool, "sessions", oldRevokedSessionID, false)
	assertDailyCleanupExists(t, pool, "sessions", recentRevokedSessionID, true)
	assertDailyCleanupRateLimitExists(t, pool, staleRateKey, false)
	assertDailyCleanupRateLimitExists(t, pool, recentRateKey, true)
	assertDailyCleanupExists(t, pool, "enrollment_tokens", expiredEnrollmentID, false)
	assertDailyCleanupExists(t, pool, "enrollment_tokens", activeEnrollmentID, true)
	assertDailyCleanupExists(t, pool, "agent_tokens", expiredAgentID, false)
	assertDailyCleanupExists(t, pool, "agent_tokens", activeAgentID, true)
	assertDailyCleanupExists(t, pool, "agent_tokens", durableRevokedAgentID, true)
	assertDailyCleanupBatchExists(t, pool, nodeID, oldEmptyBatchID, false)
	assertDailyCleanupBatchExists(t, pool, nodeID, recentBatchID, true)
	assertDailyCleanupBatchExists(t, pool, nodeID, oldReferencedBatchID, true)
	blockedEnrollmentID := dailyCleanupUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO enrollment_tokens (id, node_id, token_hash, created_at, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::timestamptz - INTERVAL '2 hours', $4::timestamptz - INTERVAL '1 hour')
	`, blockedEnrollmentID, nodeID, "blocked-enrollment-"+blockedEnrollmentID, databaseNow); err != nil {
		t.Fatalf("insert scheduled-skip fixture: %v", err)
	}
	second, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if !second.LockAcquired || second.Executed || !second.PreviousSuccessAt.Equal(first.AsOf) ||
		second.SessionsDeleted != 0 || second.LoginRateLimitsDeleted != 0 ||
		second.EnrollmentTokensDeleted != 0 || second.AgentTokensDeleted != 0 || second.ProcessedBatchesDeleted != 0 {
		t.Fatalf("second RunOnce() before interval = %#v", second)
	}
	assertDailyCleanupExists(t, pool, "enrollment_tokens", blockedEnrollmentID, true)
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin daily cleanup lock holder: %v", err)
	}
	var held bool
	if err := holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, dailyCleanupLock).Scan(&held); err != nil || !held {
		_ = holder.Rollback(ctx)
		t.Fatalf("hold daily cleanup lock: held=%v error=%v", held, err)
	}
	contended, err := job.RunOnce(ctx)
	if rollbackErr := holder.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release daily cleanup lock: %v", rollbackErr)
	}
	if err != nil || contended.LockAcquired || contended.Executed || contended.SessionsDeleted != 0 ||
		contended.LoginRateLimitsDeleted != 0 || contended.EnrollmentTokensDeleted != 0 ||
		contended.AgentTokensDeleted != 0 || contended.ProcessedBatchesDeleted != 0 {
		t.Fatalf("contended RunOnce() = %#v, error=%v", contended, err)
	}
	assertDailyCleanupExists(t, pool, "enrollment_tokens", blockedEnrollmentID, true)

	probeJob, err := NewProbeResultMaintenance(pool, time.Minute)
	if err != nil {
		t.Fatalf("NewProbeResultMaintenance() error = %v", err)
	}
	if aggregated, err := probeJob.Run5mOnce(ctx); err != nil || !aggregated.LockAcquired {
		t.Fatalf("Run5mOnce() before ledger cleanup = %#v, error=%v", aggregated, err)
	}
	if retained, err := probeJob.RunRetentionOnce(ctx); err != nil || !retained.LockAcquired || retained.RawDeleted != 1 {
		t.Fatalf("RunRetentionOnce() before ledger cleanup = %#v, error=%v", retained, err)
	}
	assertDailyCleanupBatchExists(t, pool, nodeID, oldReferencedBatchID, true)
	if _, err := pool.Exec(ctx, `
		UPDATE job_watermarks
		SET watermark_at = CURRENT_TIMESTAMP - INTERVAL '24 hours 1 second',
		    updated_at = CURRENT_TIMESTAMP - INTERVAL '24 hours 1 second'
		WHERE job_name = $1
	`, dailyCleanupWatermark); err != nil {
		t.Fatalf("age daily cleanup success watermark: %v", err)
	}

	final, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatalf("final RunOnce() error = %v", err)
	}
	if !final.LockAcquired || !final.Executed || final.PreviousSuccessAt.IsZero() || final.SessionsDeleted != 0 ||
		final.LoginRateLimitsDeleted != 0 || final.EnrollmentTokensDeleted != 1 ||
		final.AgentTokensDeleted != 0 || final.ProcessedBatchesDeleted != 1 {
		t.Fatalf("final RunOnce() = %#v", final)
	}
	assertDailyCleanupBatchExists(t, pool, nodeID, oldReferencedBatchID, false)
	assertDailyCleanupBatchExists(t, pool, nodeID, recentBatchID, true)
}

func TestDailyCleanupFailureRollsBackWithoutSuccessWatermark(t *testing.T) {
	pool, ctx := dailyCleanupPool(t)
	nodeID := dailyCleanupUUID(t)
	tokenID := dailyCleanupUUID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, 'daily-cleanup-rollback-test')`, nodeID); err != nil {
		t.Fatalf("insert rollback test node: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO enrollment_tokens (id, node_id, token_hash, created_at, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, CURRENT_TIMESTAMP - INTERVAL '2 hours', CURRENT_TIMESTAMP - INTERVAL '1 hour')
	`, tokenID, nodeID, "rollback-enrollment-"+tokenID); err != nil {
		t.Fatalf("insert rollback test token: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM job_watermarks WHERE job_name = $1`, dailyCleanupWatermark)
	})
	if _, err := pool.Exec(ctx, `DELETE FROM job_watermarks WHERE job_name = $1`, dailyCleanupWatermark); err != nil {
		t.Fatalf("reset rollback test watermark: %v", err)
	}
	var previousSuccess time.Time
	if err := pool.QueryRow(ctx, `
		INSERT INTO job_watermarks (job_name, watermark_at, details, updated_at)
		VALUES ($1, CURRENT_TIMESTAMP - INTERVAL '25 hours', '{}'::jsonb, CURRENT_TIMESTAMP - INTERVAL '25 hours')
		RETURNING watermark_at
	`, dailyCleanupWatermark).Scan(&previousSuccess); err != nil {
		t.Fatalf("insert rollback test success watermark: %v", err)
	}
	previousSuccess = previousSuccess.UTC()

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cleanup failure blocker: %v", err)
	}
	var lockedID string
	if err := blocker.QueryRow(ctx, `
		SELECT id::text FROM enrollment_tokens
		WHERE id = $1::uuid
		FOR UPDATE
	`, tokenID).Scan(&lockedID); err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatalf("lock cleanup failure token: %v", err)
	}
	job, err := NewDailyCleanup(pool, dailyCleanupTestConfig())
	if err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatalf("NewDailyCleanup() error = %v", err)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := job.RunOnce(timeoutCtx); err == nil {
		_ = blocker.Rollback(ctx)
		t.Fatal("RunOnce() unexpectedly succeeded while an eligible row remained locked")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release cleanup failure blocker: %v", err)
	}
	assertDailyCleanupExists(t, pool, "enrollment_tokens", tokenID, true)
	var watermarkAfterFailure time.Time
	if err := pool.QueryRow(ctx, `SELECT watermark_at FROM job_watermarks WHERE job_name = $1`, dailyCleanupWatermark).Scan(&watermarkAfterFailure); err != nil {
		t.Fatalf("query rolled-back daily cleanup watermark: %v", err)
	}
	if !watermarkAfterFailure.UTC().Equal(previousSuccess) {
		t.Fatalf("failed daily cleanup advanced watermark from %s to %s", previousSuccess, watermarkAfterFailure)
	}

	// Query cancellation and client-side rollback can complete just before the
	// PostgreSQL backend makes the transaction advisory lock available to a
	// different pooled connection. Production Run retries lock contention; this
	// bounded poll proves the same eventual release without assuming zero delay.
	recovered := awaitDailyCleanupExecution(t, ctx, job, 10*time.Second)
	if recovered.EnrollmentTokensDeleted != 1 {
		t.Fatalf("recovered RunOnce() = %#v", recovered)
	}
	assertDailyCleanupExists(t, pool, "enrollment_tokens", tokenID, false)
}

func awaitDailyCleanupExecution(t *testing.T, ctx context.Context, job *DailyCleanup, timeout time.Duration) DailyCleanupResult {
	t.Helper()
	retryCtx, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	var lastResult DailyCleanupResult
	var lastError error
	for {
		attemptCtx, cancel := context.WithTimeout(retryCtx, time.Second)
		result, err := job.RunOnce(attemptCtx)
		cancel()
		lastResult = result
		lastError = err
		if err == nil && result.LockAcquired && result.Executed {
			return result
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			t.Fatalf("daily cleanup lock was not eventually released: last_result=%#v last_error=%v timeout_error=%v", lastResult, lastError, retryCtx.Err())
		case <-timer.C:
		}
	}
}

func dailyCleanupTestConfig() DailyCleanupConfig {
	return DailyCleanupConfig{
		Interval:                24 * time.Hour,
		RevokedSessionRetention: 24 * time.Hour,
		LoginIPWindow:           time.Minute,
		LoginUsernameWindow:     5 * time.Minute,
	}
}

func dailyCleanupPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("PROBE_API_INTEGRATION_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("PROBE_API_INTEGRATION_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func assertDailyCleanupExists(t *testing.T, pool *pgxpool.Pool, table, id string, want bool) {
	t.Helper()
	if table != "enrollment_tokens" && table != "agent_tokens" && table != "sessions" {
		t.Fatalf("unsafe token table %q", table)
	}
	var got bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1::uuid)`, id).Scan(&got); err != nil {
		t.Fatalf("query %s fixture: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s %s exists = %v, want %v", table, id, got, want)
	}
}

func assertDailyCleanupRateLimitExists(t *testing.T, pool *pgxpool.Pool, key []byte, want bool) {
	t.Helper()
	var got bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM login_rate_limits WHERE key_hash = $1)
	`, key).Scan(&got); err != nil {
		t.Fatalf("query login rate-limit fixture: %v", err)
	}
	if got != want {
		t.Fatalf("login rate-limit fixture exists = %v, want %v", got, want)
	}
}

func assertDailyCleanupBatchExists(t *testing.T, pool *pgxpool.Pool, nodeID, batchID string, want bool) {
	t.Helper()
	var got bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM processed_batches
			WHERE node_id = $1::uuid AND batch_id = $2::uuid
		)
	`, nodeID, batchID).Scan(&got); err != nil {
		t.Fatalf("query processed batch fixture: %v", err)
	}
	if got != want {
		t.Fatalf("processed batch %s exists = %v, want %v", batchID, got, want)
	}
}

func dailyCleanupUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
