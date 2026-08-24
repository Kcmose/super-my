package maintenance

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProbeMaintenanceAggregatesExactlyAndCleansPerTarget(t *testing.T) {
	pool, ctx := probeMaintenancePool(t)
	holdProbeWatermarkIntegrationLock(t, pool)
	resetProbeMaintenanceWatermarks(t, pool)
	job, err := NewProbeResultMaintenance(pool, time.Minute)
	if err != nil {
		t.Fatalf("NewProbeResultMaintenance() error = %v", err)
	}
	nodeID := probeMaintenanceUUID(t)
	shortTarget := probeMaintenanceUUID(t)
	longTarget := probeMaintenanceUUID(t)
	seedProbeMaintenanceNode(t, pool, nodeID)
	seedProbeMaintenanceTarget(t, pool, nodeID, shortTarget, "short", 7776000)
	seedProbeMaintenanceTarget(t, pool, nodeID, longTarget, "long", 7776000)
	t.Cleanup(func() { cleanupProbeMaintenanceNode(t, pool, nodeID) })

	var databaseNow time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		t.Fatalf("query database time: %v", err)
	}
	bucket := alignedFloor(databaseNow.UTC(), time.Hour).Add(-4 * time.Hour)
	status500 := int32(500)
	status302 := int32(302)
	transportError := "timeout"
	insertProbeMaintenanceBatch(t, pool, nodeID, 1, []probeRawSeed{
		{targetID: shortTarget, at: bucket.Add(60 * time.Second), sent: 1, received: 1, sum: 50, minimum: int64Pointer(50), maximum: int64Pointer(50), httpStatus: &status500},
		{targetID: shortTarget, at: bucket.Add(61 * time.Second), sent: 1, received: 1, sum: 50, minimum: int64Pointer(50), maximum: int64Pointer(50), httpStatus: &status500},
		{targetID: shortTarget, at: bucket.Add(62 * time.Second), sent: 1, received: 1, sum: 200, minimum: int64Pointer(200), maximum: int64Pointer(200), httpStatus: &status500},
		{targetID: shortTarget, at: bucket.Add(63 * time.Second), sent: 1, received: 1, sum: 200, minimum: int64Pointer(200), maximum: int64Pointer(200), httpStatus: &status302},
		{targetID: shortTarget, at: bucket.Add(64 * time.Second), sent: 1, errorCode: &transportError},
		{targetID: shortTarget, at: bucket.Add(65 * time.Second), sent: 1, errorCode: &transportError},
		{targetID: longTarget, at: bucket.Add(60 * time.Second), sent: 1, received: 1, sum: 50, minimum: int64Pointer(50), maximum: int64Pointer(50), httpStatus: &status500},
		{targetID: longTarget, at: bucket.Add(61 * time.Second), sent: 1, received: 1, sum: 50, minimum: int64Pointer(50), maximum: int64Pointer(50), httpStatus: &status500},
		{targetID: longTarget, at: bucket.Add(62 * time.Second), sent: 1, received: 1, sum: 200, minimum: int64Pointer(200), maximum: int64Pointer(200), httpStatus: &status500},
		{targetID: longTarget, at: bucket.Add(63 * time.Second), sent: 1, received: 1, sum: 200, minimum: int64Pointer(200), maximum: int64Pointer(200), httpStatus: &status302},
		{targetID: longTarget, at: bucket.Add(64 * time.Second), sent: 1, errorCode: &transportError},
		{targetID: longTarget, at: bucket.Add(65 * time.Second), sent: 1, errorCode: &transportError},
	})

	five, err := job.Run5mOnce(ctx)
	if err != nil || !five.LockAcquired || five.Through.IsZero() {
		t.Fatalf("Run5mOnce() = %#v, error=%v", five, err)
	}
	assertAggregateFacts(t, pool, "probe_result_5m", shortTarget, bucket, 6, 6, 4, 3, "500", 50, 200)
	hourly, err := job.Run1hOnce(ctx)
	if err != nil || !hourly.LockAcquired || hourly.Through.IsZero() {
		t.Fatalf("Run1hOnce() = %#v, error=%v", hourly, err)
	}
	assertAggregateFacts(t, pool, "probe_result_1h", shortTarget, bucket, 6, 6, 4, 3, "500", 50, 200)
	for _, table := range []string{"probe_result_5m", "probe_result_1h"} {
		query := `UPDATE ` + table + ` SET http_error_count = received_count + 1 WHERE target_id = $1::uuid`
		if _, err := pool.Exec(ctx, query, shortTarget); err == nil {
			t.Fatalf("%s accepted http_error_count above received_count", table)
		}
	}

	if _, err := job.Run5mOnce(ctx); err != nil {
		t.Fatalf("repeat Run5mOnce() error = %v", err)
	}
	if _, err := job.Run1hOnce(ctx); err != nil {
		t.Fatalf("repeat Run1hOnce() error = %v", err)
	}
	assertAggregateFacts(t, pool, "probe_result_5m", shortTarget, bucket, 6, 6, 4, 3, "500", 50, 200)
	assertAggregateFacts(t, pool, "probe_result_1h", shortTarget, bucket, 6, 6, 4, 3, "500", 50, 200)

	if _, err := pool.Exec(ctx, `UPDATE probe_targets SET retention_seconds = 3600, updated_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, shortTarget); err != nil {
		t.Fatalf("shorten retention: %v", err)
	}
	retention, err := job.RunRetentionOnce(ctx)
	if err != nil || !retention.LockAcquired || retention.RawDeleted < 6 || retention.FiveMinDeleted < 1 || retention.HourlyDeleted < 1 {
		t.Fatalf("RunRetentionOnce() = %#v, error=%v", retention, err)
	}
	assertTargetLayerCounts(t, pool, shortTarget, 0, 0, 0)
	assertTargetLayerCounts(t, pool, longTarget, 6, 1, 1)

	if _, err := pool.Exec(ctx, `UPDATE probe_targets SET retention_seconds = 7776000, updated_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, shortTarget); err != nil {
		t.Fatalf("extend retention: %v", err)
	}
	assertTargetLayerCounts(t, pool, shortTarget, 0, 0, 0)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin aggregation lock holder: %v", err)
	}
	var held bool
	if err := holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, probeAggregate5mLock).Scan(&held); err != nil || !held {
		_ = holder.Rollback(ctx)
		t.Fatalf("hold 5m lock: held=%v error=%v", held, err)
	}
	contended, err := job.Run5mOnce(ctx)
	if rollbackErr := holder.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release 5m lock: %v", rollbackErr)
	}
	if err != nil || contended.LockAcquired || contended.RowsUpserted != 0 {
		t.Fatalf("contended Run5mOnce() = %#v, error=%v", contended, err)
	}

	holder, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin hourly aggregation lock holder: %v", err)
	}
	if err := holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, probeAggregate1hLock).Scan(&held); err != nil || !held {
		_ = holder.Rollback(ctx)
		t.Fatalf("hold 1h lock: held=%v error=%v", held, err)
	}
	hourlyContended, err := job.Run1hOnce(ctx)
	if rollbackErr := holder.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release 1h lock: %v", rollbackErr)
	}
	if err != nil || hourlyContended.LockAcquired || hourlyContended.RowsUpserted != 0 {
		t.Fatalf("contended Run1hOnce() = %#v, error=%v", hourlyContended, err)
	}

	holder, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retention lock holder: %v", err)
	}
	if err := holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, probeRetentionLock).Scan(&held); err != nil || !held {
		_ = holder.Rollback(ctx)
		t.Fatalf("hold retention lock: held=%v error=%v", held, err)
	}
	retentionContended, err := job.RunRetentionOnce(ctx)
	if rollbackErr := holder.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release retention lock: %v", rollbackErr)
	}
	if err != nil || retentionContended.LockAcquired || retentionContended.RawDeleted != 0 ||
		retentionContended.FiveMinDeleted != 0 || retentionContended.HourlyDeleted != 0 {
		t.Fatalf("contended RunRetentionOnce() = %#v, error=%v", retentionContended, err)
	}
}

func TestProbeAggregationFailureRollsBackAndLeavesRawSource(t *testing.T) {
	pool, ctx := probeMaintenancePool(t)
	holdProbeWatermarkIntegrationLock(t, pool)
	resetProbeMaintenanceWatermarks(t, pool)
	job, err := NewProbeResultMaintenance(pool, time.Minute)
	if err != nil {
		t.Fatalf("NewProbeResultMaintenance() error = %v", err)
	}
	nodeID := probeMaintenanceUUID(t)
	targetID := probeMaintenanceUUID(t)
	seedProbeMaintenanceNode(t, pool, nodeID)
	seedProbeMaintenanceTarget(t, pool, nodeID, targetID, "overflow", 1)
	t.Cleanup(func() { cleanupProbeMaintenanceNode(t, pool, nodeID) })
	var databaseNow time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		t.Fatalf("query database time: %v", err)
	}
	// The source is old enough to satisfy the physical retention predicate.
	// Since the aggregate transaction fails, its watermark initialization and
	// advancement roll back, and cleanup must leave the source untouched.
	bucket := alignedFloor(databaseNow.UTC(), 5*time.Minute).Add(-40 * time.Minute)
	insertProbeMaintenanceBatch(t, pool, nodeID, 1, []probeRawSeed{
		{targetID: targetID, at: bucket.Add(time.Second), sent: int64(9223372036854775807), received: 0, sum: 0},
		{targetID: targetID, at: bucket.Add(2 * time.Second), sent: int64(9223372036854775807), received: 0, sum: 0},
	})
	if _, err := job.Run5mOnce(ctx); err == nil {
		t.Fatal("Run5mOnce() unexpectedly accepted an overflowing additive fact")
	}
	if _, err := job.RunRetentionOnce(ctx); err != nil {
		t.Fatalf("RunRetentionOnce() after failed aggregation error = %v", err)
	}
	assertTargetLayerCounts(t, pool, targetID, 2, 0, 0)
}

type probeRawSeed struct {
	targetID   string
	at         time.Time
	sent       int64
	received   int64
	sum        int64
	minimum    *int64
	maximum    *int64
	httpStatus *int32
	errorCode  *string
}

func probeMaintenancePool(t *testing.T) (*pgxpool.Pool, context.Context) {
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

func resetProbeMaintenanceWatermarks(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		DELETE FROM job_watermarks
		WHERE job_name IN ('probe-result-5m', 'probe-result-1h')
	`); err != nil {
		t.Fatalf("reset probe maintenance watermarks: %v", err)
	}
}

func seedProbeMaintenanceNode(t *testing.T, pool *pgxpool.Pool, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, 'probe-maintenance-test')`, nodeID); err != nil {
		t.Fatalf("insert maintenance node: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, nodeID); err != nil {
		t.Fatalf("insert maintenance settings: %v", err)
	}
}

func seedProbeMaintenanceTarget(t *testing.T, pool *pgxpool.Pool, nodeID, targetID, name string, retention int32) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO probe_targets (
			id, node_id, name, probe_type, host, port, path,
			interval_seconds, timeout_seconds, retention_seconds
		) VALUES ($1::uuid, $2::uuid, $3, 'https', '127.0.0.1', 443, '/', 30, 3, $4)
	`, targetID, nodeID, name, retention); err != nil {
		t.Fatalf("insert maintenance target: %v", err)
	}
}

func insertProbeMaintenanceBatch(t *testing.T, pool *pgxpool.Pool, nodeID string, sequence int64, seeds []probeRawSeed) {
	t.Helper()
	ctx := context.Background()
	batchID := probeMaintenanceUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO processed_batches (
			node_id, batch_id, sequence, agent_time, agent_version,
			config_version, payload_checksum, clock_status, received_at
		) VALUES ($1::uuid, $2::uuid, $3, CURRENT_TIMESTAMP, 'test', 1, $4, 'ok', CURRENT_TIMESTAMP)
	`, nodeID, batchID, sequence, "checksum-"+batchID); err != nil {
		t.Fatalf("insert maintenance batch: %v", err)
	}
	for index, seed := range seeds {
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_result_raw (
				node_id, target_id, batch_id, sample_index,
				sampled_at, effective_at, received_at,
				sent_count, received_count, latency_sum_us,
				latency_min_us, latency_max_us, http_status_code, error_code
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4,
				$5::timestamptz, $5::timestamptz, CURRENT_TIMESTAMP,
				$6, $7, $8, $9, $10, $11, $12
			)
		`, nodeID, seed.targetID, batchID, index, seed.at, seed.sent, seed.received, seed.sum, seed.minimum, seed.maximum, seed.httpStatus, seed.errorCode); err != nil {
			t.Fatalf("insert raw seed %d: %v", index, err)
		}
	}
}

func assertAggregateFacts(t *testing.T, pool *pgxpool.Pool, table, targetID string, bucket time.Time,
	resultCount int32, sent, received, httpErrors int64, latencySum string, minimum, maximum int64,
) {
	t.Helper()
	if table != "probe_result_5m" && table != "probe_result_1h" {
		t.Fatalf("invalid aggregate table %q", table)
	}
	query := `SELECT result_count, sent_count, received_count, http_error_count, latency_sum_us::text,
	                 latency_min_us, latency_max_us FROM ` + table + `
	          WHERE target_id = $1::uuid AND bucket_start = $2::timestamptz`
	var gotResult int32
	var gotSent, gotReceived, gotHTTPErrors, gotMinimum, gotMaximum int64
	var gotSum string
	if err := pool.QueryRow(context.Background(), query, targetID, bucket).Scan(
		&gotResult, &gotSent, &gotReceived, &gotHTTPErrors, &gotSum, &gotMinimum, &gotMaximum,
	); err != nil {
		t.Fatalf("query %s aggregate: %v", table, err)
	}
	if gotResult != resultCount || gotSent != sent || gotReceived != received || gotHTTPErrors != httpErrors || gotSum != latencySum ||
		gotMinimum != minimum || gotMaximum != maximum {
		t.Fatalf("%s facts = result=%d sent=%d received=%d http_errors=%d sum=%s min=%d max=%d",
			table, gotResult, gotSent, gotReceived, gotHTTPErrors, gotSum, gotMinimum, gotMaximum)
	}
}

func assertTargetLayerCounts(t *testing.T, pool *pgxpool.Pool, targetID string, raw, fiveMinute, hourly int) {
	t.Helper()
	ctx := context.Background()
	for table, want := range map[string]int{
		"probe_result_raw": raw, "probe_result_5m": fiveMinute, "probe_result_1h": hourly,
	} {
		var got int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE target_id = $1::uuid`, targetID).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

func cleanupProbeMaintenanceNode(t *testing.T, pool *pgxpool.Pool, nodeID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID); err != nil {
		t.Errorf("cleanup maintenance node: %v", err)
	}
}

func probeMaintenanceUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func int64Pointer(value int64) *int64 {
	return &value
}

const probeWatermarkIntegrationLock int64 = 0x70726f6265747374

func holdProbeWatermarkIntegrationLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	connection, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire probe watermark integration lock connection: %v", err)
	}
	locked := false
	t.Cleanup(func() {
		defer connection.Release()
		if !locked {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var unlocked bool
		if err := connection.QueryRow(ctx, `SELECT pg_advisory_unlock($1::bigint)`, probeWatermarkIntegrationLock).Scan(&unlocked); err != nil || !unlocked {
			t.Errorf("release probe watermark integration lock: unlocked=%v error=%v", unlocked, err)
		}
	})
	if _, err := connection.Exec(context.Background(), `SELECT pg_advisory_lock($1::bigint)`, probeWatermarkIntegrationLock); err != nil {
		t.Fatalf("hold probe watermark integration lock: %v", err)
	}
	locked = true
}
