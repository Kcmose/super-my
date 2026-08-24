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

func TestBasicMetricRetentionRunOnceUsesStrictFiveMinuteCutoff(t *testing.T) {
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
	nodeID := maintenanceTestUUID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, 'retention-test')`, nodeID); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupContext, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID); err != nil {
			t.Errorf("cleanup node: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, nodeID); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_metric_ring (
			node_id, slot, sampled_at, effective_at, received_at, cpu_percent,
			load_1, load_5, load_15, uptime_seconds, memory_total_bytes,
			memory_used_bytes, memory_available_bytes, swap_total_bytes,
			swap_used_bytes, network_rx_bps, network_tx_bps, network_rx_bytes, network_tx_bytes
		) VALUES
			($1::uuid, 0, CURRENT_TIMESTAMP - INTERVAL '6 minutes', CURRENT_TIMESTAMP - INTERVAL '6 minutes', CURRENT_TIMESTAMP, 1, 1, 1, 1, 1, 10, 5, 5, 0, 0, 1, 1, 1, 1),
			($1::uuid, 1, CURRENT_TIMESTAMP - INTERVAL '4 minutes', CURRENT_TIMESTAMP - INTERVAL '4 minutes', CURRENT_TIMESTAMP, 2, 1, 1, 1, 1, 10, 5, 5, 0, 0, 1, 1, 1, 1)
	`, nodeID); err != nil {
		t.Fatalf("insert metric rows: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_disk_ring (node_id, mountpoint, slot, sampled_at, effective_at, received_at, total_bytes, used_bytes, available_bytes)
		VALUES
			($1::uuid, '/', 0, CURRENT_TIMESTAMP - INTERVAL '6 minutes', CURRENT_TIMESTAMP - INTERVAL '6 minutes', CURRENT_TIMESTAMP, 10, 5, 5),
			($1::uuid, '/', 1, CURRENT_TIMESTAMP - INTERVAL '4 minutes', CURRENT_TIMESTAMP - INTERVAL '4 minutes', CURRENT_TIMESTAMP, 10, 5, 5)
	`, nodeID); err != nil {
		t.Fatalf("insert disk rows: %v", err)
	}

	job, err := NewBasicMetricRetention(pool, time.Minute)
	if err != nil {
		t.Fatalf("NewBasicMetricRetention() error = %v", err)
	}
	result, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !result.LockAcquired || result.MetricRowsDeleted < 1 || result.DiskRowsDeleted < 1 || result.AsOf.IsZero() {
		t.Fatalf("RunOnce() = %#v", result)
	}
	var metricCount, diskCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM node_metric_ring WHERE node_id = $1::uuid`, nodeID).Scan(&metricCount); err != nil {
		t.Fatalf("count metric rows: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM node_disk_ring WHERE node_id = $1::uuid`, nodeID).Scan(&diskCount); err != nil {
		t.Fatalf("count disk rows: %v", err)
	}
	if metricCount != 1 || diskCount != 1 {
		t.Fatalf("remaining metric=%d disk=%d", metricCount, diskCount)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	var held bool
	if err := holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, basicMetricRetentionLock).Scan(&held); err != nil || !held {
		_ = holder.Rollback(ctx)
		t.Fatalf("hold retention lock: held=%v error=%v", held, err)
	}
	contended, err := job.RunOnce(ctx)
	if rollbackErr := holder.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release retention lock: %v", rollbackErr)
	}
	if err != nil || contended.LockAcquired || contended.MetricRowsDeleted != 0 || contended.DiskRowsDeleted != 0 {
		t.Fatalf("contended RunOnce() = %#v, error=%v", contended, err)
	}
}

func maintenanceTestUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
