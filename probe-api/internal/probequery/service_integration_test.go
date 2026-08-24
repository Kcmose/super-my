package probequery

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceListsOwnedTargetsAndQueriesAllTrendLayers(t *testing.T) {
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
	holdProbeWatermarkIntegrationLock(t, pool)
	service, err := NewService(pool)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	nodeID := probeQueryUUID(t)
	otherNodeID := probeQueryUUID(t)
	tcpTargetID := probeQueryUUID(t)
	httpTargetID := probeQueryUUID(t)
	otherTargetID := probeQueryUUID(t)
	for _, node := range []string{nodeID, otherNodeID} {
		if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, 'probe-query-test')`, node); err != nil {
			t.Fatalf("insert node: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, node); err != nil {
			t.Fatalf("insert settings: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, node := range []string{nodeID, otherNodeID} {
			if _, err := pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE id = $1::uuid`, node); err != nil {
				t.Errorf("cleanup node: %v", err)
			}
		}
	})
	seedProbeQueryTarget(t, pool, nodeID, tcpTargetID, "a-tcp", "tcp", 443, nil, -2*time.Minute)
	path := "/health"
	seedProbeQueryTarget(t, pool, nodeID, httpTargetID, "b-http", "http", 8080, &path, -time.Minute)
	seedProbeQueryTarget(t, pool, otherNodeID, otherTargetID, "other", "tcp", 22, nil, 0)

	targets, err := service.ListTargets(ctx, nodeID)
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	if targets.NodeID != nodeID || len(targets.Targets) != 2 ||
		targets.Targets[0].TargetID != tcpTargetID || targets.Targets[1].TargetID != httpTargetID ||
		targets.Targets[0].Type != "tcp" || targets.Targets[1].Type != "http" {
		t.Fatalf("ListTargets() = %#v", targets)
	}
	if _, err := service.ListTargets(ctx, probeQueryUUID(t)); err != ErrNotFound {
		t.Fatalf("missing ListTargets() error = %v, want ErrNotFound", err)
	}

	var asOf time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&asOf); err != nil {
		t.Fatalf("query database time: %v", err)
	}
	asOf = asOf.UTC()
	setProbeQueryWatermarks(t, pool, &asOf)
	rawAt := asOf.Add(-30 * time.Minute)
	seedProbeQueryRaw(t, pool, nodeID, tcpTargetID, 1, rawAt, 1, 1, 100, intPointer(100), intPointer(100), nil)
	httpFailureStatus := int32(500)
	seedProbeQueryRaw(t, pool, nodeID, httpTargetID, 2, rawAt.Add(time.Minute), 1, 1, 120, intPointer(120), intPointer(120), &httpFailureStatus)
	fiveAt := time.Unix((asOf.Add(-48*time.Hour).Unix()/300)*300, 0).UTC()
	hourAt := time.Unix((asOf.Add(-8*24*time.Hour).Unix()/3600)*3600, 0).UTC()
	for _, aggregate := range []struct {
		targetID   string
		fiveErrors int64
		hourErrors int64
	}{
		{targetID: tcpTargetID},
		{targetID: httpTargetID, fiveErrors: 2, hourErrors: 3},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_result_5m (
				target_id, bucket_start, result_count, sent_count, received_count,
				http_error_count, latency_sum_us, latency_min_us, latency_max_us
			) VALUES ($1::uuid, $2::timestamptz, 2, 10, 8, $3, 800, 50, 200)
		`, aggregate.targetID, fiveAt, aggregate.fiveErrors); err != nil {
			t.Fatalf("insert five-minute point: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_result_1h (
				target_id, bucket_start, result_count, sent_count, received_count,
				http_error_count, latency_sum_us, latency_min_us, latency_max_us
			) VALUES ($1::uuid, $2::timestamptz, 4, 20, 15, $3, 1800, 40, 250)
		`, aggregate.targetID, hourAt, aggregate.hourErrors); err != nil {
			t.Fatalf("insert hourly point: %v", err)
		}
	}

	raw, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: asOf.Add(-time.Hour), To: asOf,
		Resolution: ResolutionAuto,
	})
	if err != nil || raw.Resolution != ResolutionRaw || len(raw.Points) != 1 ||
		raw.Points[0].LatencySumUS.String() != "100" || raw.Points[0].AverageLatencyUS == nil ||
		raw.Points[0].ReceivedCount != 1 || raw.Points[0].HTTPStatusCode != nil ||
		raw.Points[0].HTTPErrorCount != 0 || raw.Points[0].LossRate != 0 || raw.Points[0].FailureRate != 0 {
		t.Fatalf("raw Probes() = %#v, error=%v", raw, err)
	}
	httpRaw, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: httpTargetID, From: asOf.Add(-time.Hour), To: asOf,
		Resolution: ResolutionRaw,
	})
	if err != nil || len(httpRaw.Points) != 1 || httpRaw.Points[0].AverageLatencyUS == nil ||
		httpRaw.Points[0].HTTPStatusCode == nil || *httpRaw.Points[0].HTTPStatusCode != 500 ||
		httpRaw.Points[0].HTTPErrorCount != 1 || httpRaw.Points[0].LossRate != 0 || httpRaw.Points[0].FailureRate != 1 {
		t.Fatalf("HTTP raw Probes() = %#v, error=%v", httpRaw, err)
	}
	seedProbeQueryRawBurst(t, pool, nodeID, tcpTargetID, asOf.Add(-20*time.Minute), MaxPoints+1)
	fallback, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: asOf.Add(-time.Hour), To: asOf,
		Resolution: ResolutionAuto,
	})
	if err != nil || fallback.Resolution != Resolution5m {
		t.Fatalf("raw overflow auto fallback = %#v, error=%v", fallback, err)
	}
	if _, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: asOf.Add(-time.Hour), To: asOf,
		Resolution: ResolutionRaw,
	}); err != ErrResolutionUnavailable {
		t.Fatalf("raw overflow explicit error = %v, want ErrResolutionUnavailable", err)
	}
	five, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: fiveAt.Add(-time.Minute), To: fiveAt.Add(6 * time.Minute),
		Resolution: ResolutionAuto,
	})
	if err != nil || five.Resolution != Resolution5m || len(five.Points) != 1 || five.Points[0].ResultCount != 2 ||
		five.Points[0].AverageLatencyUS == nil || math.Abs(*five.Points[0].AverageLatencyUS-100) > 0.000001 ||
		math.Abs(five.Points[0].LossRate-0.2) > 0.000001 || math.Abs(five.Points[0].FailureRate-0.2) > 0.000001 ||
		five.Points[0].HTTPStatusCode != nil || five.Points[0].HTTPErrorCount != 0 {
		t.Fatalf("five-minute Probes() = %#v, error=%v", five, err)
	}
	httpFive, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: httpTargetID, From: fiveAt.Add(-time.Minute), To: fiveAt.Add(6 * time.Minute),
		Resolution: Resolution5m,
	})
	if err != nil || len(httpFive.Points) != 1 || httpFive.Points[0].HTTPStatusCode != nil ||
		httpFive.Points[0].HTTPErrorCount != 2 || math.Abs(httpFive.Points[0].LossRate-0.2) > 0.000001 ||
		math.Abs(httpFive.Points[0].FailureRate-0.4) > 0.000001 {
		t.Fatalf("HTTP five-minute Probes() = %#v, error=%v", httpFive, err)
	}
	hourly, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: hourAt, To: hourAt.Add(time.Hour),
		Resolution: ResolutionAuto,
	})
	if err != nil || hourly.Resolution != Resolution1h || len(hourly.Points) != 1 || hourly.Points[0].SentCount != 20 ||
		hourly.Points[0].AverageLatencyUS == nil || math.Abs(*hourly.Points[0].AverageLatencyUS-120) > 0.000001 ||
		math.Abs(hourly.Points[0].LossRate-0.25) > 0.000001 || math.Abs(hourly.Points[0].FailureRate-0.25) > 0.000001 ||
		hourly.Points[0].HTTPStatusCode != nil || hourly.Points[0].HTTPErrorCount != 0 {
		t.Fatalf("hourly Probes() = %#v, error=%v", hourly, err)
	}
	httpHourly, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: httpTargetID, From: hourAt, To: hourAt.Add(time.Hour),
		Resolution: Resolution1h,
	})
	if err != nil || len(httpHourly.Points) != 1 || httpHourly.Points[0].HTTPStatusCode != nil ||
		httpHourly.Points[0].HTTPErrorCount != 3 || math.Abs(httpHourly.Points[0].LossRate-0.25) > 0.000001 ||
		math.Abs(httpHourly.Points[0].FailureRate-0.4) > 0.000001 {
		t.Fatalf("HTTP hourly Probes() = %#v, error=%v", httpHourly, err)
	}
	setProbeQueryWatermarks(t, pool, nil)
	if _, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: fiveAt, To: fiveAt.Add(time.Hour),
		Resolution: Resolution5m,
	}); err != ErrResolutionUnavailable {
		t.Fatalf("uncovered explicit 5m error = %v, want ErrResolutionUnavailable", err)
	}
	if _, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: hourAt, To: hourAt.Add(time.Hour),
		Resolution: ResolutionAuto,
	}); err != ErrResolutionUnavailable {
		t.Fatalf("uncovered auto error = %v, want ErrResolutionUnavailable", err)
	}
	setProbeQueryWatermarks(t, pool, &asOf)

	_, err = service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: otherTargetID, From: asOf.Add(-time.Hour), To: asOf,
		Resolution: ResolutionAuto,
	})
	if err != ErrNotFound {
		t.Fatalf("cross-node Probes() error = %v, want ErrNotFound", err)
	}
	_, err = service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: fiveAt, To: fiveAt.Add(time.Hour),
		Resolution: ResolutionRaw,
	})
	if err != ErrResolutionUnavailable {
		t.Fatalf("old explicit raw error = %v, want ErrResolutionUnavailable", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE probe_targets SET retention_seconds = 86400, updated_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, tcpTargetID); err != nil {
		t.Fatalf("shorten query retention: %v", err)
	}
	clipped, err := service.Probes(ctx, ProbeSeriesRequest{
		NodeID: nodeID, TargetID: tcpTargetID, From: hourAt, To: hourAt.Add(time.Hour),
		Resolution: ResolutionAuto,
	})
	if err != nil || len(clipped.Points) != 0 || !clipped.From.Equal(clipped.To) {
		t.Fatalf("clipped Probes() = %#v, error=%v", clipped, err)
	}
}

func seedProbeQueryTarget(t *testing.T, pool *pgxpool.Pool, nodeID, targetID, name, probeType string, port int32, path *string, createdOffset time.Duration) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO probe_targets (
			id, node_id, name, probe_type, host, port, path,
			interval_seconds, timeout_seconds, retention_seconds, created_at, updated_at
		) VALUES (
			$1::uuid, $2::uuid, $3, $4, '127.0.0.1', $5, $6,
			30, 3, 7776000, CURRENT_TIMESTAMP + ($7::double precision * INTERVAL '1 second'),
			CURRENT_TIMESTAMP + ($7::double precision * INTERVAL '1 second')
		)
	`, targetID, nodeID, name, probeType, port, path, createdOffset.Seconds()); err != nil {
		t.Fatalf("insert probe target: %v", err)
	}
}

func seedProbeQueryRaw(t *testing.T, pool *pgxpool.Pool, nodeID, targetID string, sequence int64, at time.Time,
	sent, received, sum int64, minimum, maximum *int64, httpStatus *int32,
) {
	t.Helper()
	ctx := context.Background()
	batchID := probeQueryUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO processed_batches (
			node_id, batch_id, sequence, agent_time, agent_version,
			config_version, payload_checksum, clock_status, received_at
		) VALUES ($1::uuid, $2::uuid, $3, CURRENT_TIMESTAMP, 'test', 1, $4, 'ok', CURRENT_TIMESTAMP)
	`, nodeID, batchID, sequence, "checksum-"+batchID); err != nil {
		t.Fatalf("insert processed batch: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_result_raw (
			node_id, target_id, batch_id, sample_index,
			sampled_at, effective_at, received_at,
			sent_count, received_count, latency_sum_us,
			latency_min_us, latency_max_us, http_status_code
		) VALUES ($1::uuid, $2::uuid, $3::uuid, 0, $4, $4, CURRENT_TIMESTAMP, $5, $6, $7, $8, $9, $10)
	`, nodeID, targetID, batchID, at, sent, received, sum, minimum, maximum, httpStatus); err != nil {
		t.Fatalf("insert raw point: %v", err)
	}
}

func seedProbeQueryRawBurst(t *testing.T, pool *pgxpool.Pool, nodeID, targetID string, at time.Time, count int) {
	t.Helper()
	ctx := context.Background()
	batchID := probeQueryUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO processed_batches (
			node_id, batch_id, sequence, agent_time, agent_version,
			config_version, payload_checksum, clock_status, received_at
		) VALUES ($1::uuid, $2::uuid, 100, CURRENT_TIMESTAMP, 'test', 1, $3, 'ok', CURRENT_TIMESTAMP)
	`, nodeID, batchID, "checksum-"+batchID); err != nil {
		t.Fatalf("insert burst processed batch: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_result_raw (
			node_id, target_id, batch_id, sample_index,
			sampled_at, effective_at, received_at,
			sent_count, received_count, latency_sum_us,
			latency_min_us, latency_max_us
		)
		SELECT $1::uuid, $2::uuid, $3::uuid, sample_index,
		       $4::timestamptz + sample_index * INTERVAL '1 millisecond',
		       $4::timestamptz + sample_index * INTERVAL '1 millisecond',
		       CURRENT_TIMESTAMP, 1, 1, 100, 100, 100
		FROM generate_series(0, $5::integer - 1) AS sample_index
	`, nodeID, targetID, batchID, at, count); err != nil {
		t.Fatalf("insert raw burst: %v", err)
	}
}

func setProbeQueryWatermarks(t *testing.T, pool *pgxpool.Pool, asOf *time.Time) {
	t.Helper()
	var fiveMinute, hourly any
	if asOf != nil {
		fiveMinute = alignedFloor(*asOf, 5*time.Minute)
		hourly = alignedFloor(*asOf, time.Hour)
	}
	for name, value := range map[string]any{
		"probe-result-5m": fiveMinute,
		"probe-result-1h": hourly,
	} {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO job_watermarks (job_name, watermark_at, details)
			VALUES ($1, $2::timestamptz, jsonb_build_object('integration_test', 'probequery'))
			ON CONFLICT (job_name) DO UPDATE
			SET watermark_at = EXCLUDED.watermark_at,
			    details = EXCLUDED.details,
			    updated_at = CURRENT_TIMESTAMP
		`, name, value); err != nil {
			t.Fatalf("set %s watermark: %v", name, err)
		}
	}
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

func probeQueryUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func intPointer(value int64) *int64 {
	return &value
}
