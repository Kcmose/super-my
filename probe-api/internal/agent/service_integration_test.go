package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceEnrollmentConfigReportLifecycle(t *testing.T) {
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
	service := NewService(pool)

	nodeID := mustUUID(t)
	enrollmentID := mustUUID(t)
	enrollmentToken := strings.Repeat("e", 32)
	siblingEnrollmentID := mustUUID(t)
	siblingEnrollmentToken := strings.Repeat("d", 32)
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, 'integration-node')`, nodeID); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupContext, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID); err != nil {
			t.Errorf("cleanup integration node: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, nodeID); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE node_agent_settings
		SET report_interval_seconds = 10, max_memory_queue_seconds = 9
		WHERE node_id = $1::uuid
	`, nodeID); err == nil {
		t.Fatal("database accepted a memory queue shorter than the report interval")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO enrollment_tokens (id, node_id, token_hash, expires_at)
		VALUES
		    ($1::uuid, $2::uuid, $3, CURRENT_TIMESTAMP + INTERVAL '1 hour'),
		    ($4::uuid, $2::uuid, $5, CURRENT_TIMESTAMP + INTERVAL '1 hour')
	`, enrollmentID, nodeID, HashOpaqueToken(enrollmentToken), siblingEnrollmentID, HashOpaqueToken(siblingEnrollmentToken)); err != nil {
		t.Fatalf("insert sibling enrollment tokens: %v", err)
	}

	enrollment := EnrollRequest{EnrollmentToken: enrollmentToken, Hostname: "node-1", AgentVersion: "1.0.0", OS: "linux", Arch: "amd64"}
	type enrollmentResult struct {
		response EnrollResponse
		err      error
	}
	results := make(chan enrollmentResult, 2)
	var wait sync.WaitGroup
	enrollmentContext, cancelEnrollment := context.WithTimeout(ctx, 10*time.Second)
	defer cancelEnrollment()
	for _, concurrentToken := range []string{enrollmentToken, siblingEnrollmentToken} {
		wait.Add(1)
		go func(token string) {
			defer wait.Done()
			request := enrollment
			request.EnrollmentToken = token
			response, err := service.Enroll(enrollmentContext, request, "192.0.2.10")
			results <- enrollmentResult{response: response, err: err}
		}(concurrentToken)
	}
	wait.Wait()
	close(results)
	var enrolled EnrollResponse
	successes := 0
	usedConflicts := 0
	for result := range results {
		if result.err == nil {
			successes++
			enrolled = result.response
		} else if errors.Is(result.err, ErrEnrollmentTokenUsed) {
			usedConflicts++
		} else {
			t.Fatalf("concurrent Enroll() error = %v", result.err)
		}
	}
	if successes != 1 || usedConflicts != 1 {
		t.Fatalf("concurrent Enroll() successes=%d used_conflicts=%d", successes, usedConflicts)
	}
	if enrolled.NodeID != nodeID || enrolled.AgentToken == "" || enrolled.ConfigVersion != 1 {
		t.Fatalf("Enroll() = %#v", enrolled)
	}
	var storedHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM agent_tokens WHERE node_id = $1::uuid AND revoked_at IS NULL`, nodeID).Scan(&storedHash); err != nil {
		t.Fatalf("read token hash: %v", err)
	}
	if storedHash == enrolled.AgentToken || !ConstantTimeHashEqual(storedHash, enrolled.AgentToken) {
		t.Fatal("database did not retain only the matching Agent token hash")
	}
	for _, invalidatedToken := range []string{enrollmentToken, siblingEnrollmentToken} {
		request := enrollment
		request.EnrollmentToken = invalidatedToken
		if _, err := service.Enroll(ctx, request, "192.0.2.10"); !errors.Is(err, ErrEnrollmentTokenUsed) {
			t.Fatalf("reused or sibling Enroll() error = %v, want ErrEnrollmentTokenUsed", err)
		}
	}

	identity, err := service.Authenticate(ctx, enrolled.AgentToken)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	configuration, notModified, err := service.LoadConfig(ctx, identity, 0)
	if err != nil || notModified || configuration.ConfigVersion != 1 || len(configuration.Metrics.Mountpoints) != 1 || configuration.Metrics.Mountpoints[0] != "/" {
		t.Fatalf("LoadConfig(0) = %#v, %v, %v", configuration, notModified, err)
	}
	if _, notModified, err := service.LoadConfig(ctx, identity, 1); err != nil || !notModified {
		t.Fatalf("LoadConfig(1) notModified = %v, error = %v", notModified, err)
	}
	if _, _, err := service.LoadConfig(ctx, identity, 2); !errors.Is(err, ErrConfigVersionAhead) {
		t.Fatalf("LoadConfig(ahead) error = %v", err)
	}

	targetID := mustUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_targets (
			id, node_id, name, probe_type, host, port, path,
			interval_seconds, timeout_seconds, retention_seconds
		) VALUES ($1::uuid, $2::uuid, 'HTTPS', 'https', 'example.com', 443, '/', 30, 3, 86400)
	`, targetID, nodeID); err != nil {
		t.Fatalf("insert target: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE nodes SET config_version = 2 WHERE id = $1::uuid`, nodeID); err != nil {
		t.Fatalf("advance config version: %v", err)
	}
	for _, test := range []struct {
		name     string
		mutate   func(*ProbeResult)
		wantCode string
	}{
		{name: "multiple attempts", wantCode: "invalid_probe_count", mutate: func(result *ProbeResult) {
			result.SentCount = 2
		}},
		{name: "missing HTTP status", wantCode: "invalid_http_status", mutate: func(result *ProbeResult) {
			result.HTTPStatusCode.Value = nil
		}},
		{name: "transport failure without error", wantCode: "invalid_probe_error", mutate: func(result *ProbeResult) {
			result.ReceivedCount = 0
			result.LatencySumUS = 0
			result.LatencyMinUS.Value = nil
			result.LatencyMaxUS.Value = nil
			result.HTTPStatusCode.Value = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := integrationReport(t, targetID)
			test.mutate(&invalid.ProbeResults[0])
			var fieldError *FieldError
			if _, err := service.Report(ctx, identity, invalid, "192.0.2.10"); !errors.As(err, &fieldError) || fieldError.Code != test.wantCode {
				t.Fatalf("Report() error = %v, want %s", err, test.wantCode)
			}
		})
	}

	report := integrationReport(t, targetID)
	accepted, err := service.Report(ctx, identity, report, "192.0.2.10")
	if err != nil || accepted.Status != "accepted" || accepted.CurrentConfigVersion != 2 {
		t.Fatalf("Report() = %#v, %v", accepted, err)
	}
	duplicate, err := service.Report(ctx, identity, report, "192.0.2.10")
	if err != nil || duplicate.Status != "duplicate" || !duplicate.ReceivedAt.Equal(accepted.ReceivedAt) || duplicate.ClockStatus != accepted.ClockStatus {
		t.Fatalf("duplicate Report() = %#v, %v", duplicate, err)
	}
	assertRowCount(t, pool, nodeID, "processed_batches", 1)
	assertRowCount(t, pool, nodeID, "node_metric_current", 1)
	assertRowCount(t, pool, nodeID, "node_metric_ring", 1)
	assertRowCount(t, pool, nodeID, "node_disk_current", 1)
	assertRowCount(t, pool, nodeID, "node_disk_ring", 1)
	assertRowCount(t, pool, nodeID, "probe_result_raw", 1)

	reused := report
	reused.AgentVersion = "1.0.1"
	if _, err := service.Report(ctx, identity, reused, "192.0.2.10"); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("reused batch Report() error = %v", err)
	}
	stale := report
	stale.BatchID = mustUUID(t)
	if _, err := service.Report(ctx, identity, stale, "192.0.2.10"); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("stale Report() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM probe_targets WHERE id = $1::uuid`, targetID); err != nil {
		t.Fatalf("delete probe target: %v", err)
	}
	deletedTargetReport := integrationReport(t, targetID)
	deletedTargetReport.Sequence = 2
	var deletedTargetError *FieldError
	if _, err := service.Report(ctx, identity, deletedTargetReport, "192.0.2.10"); !errors.As(err, &deletedTargetError) || deletedTargetError.Code != "probe_target_not_configured" {
		t.Fatalf("deleted target Report() error = %v, want probe_target_not_configured", err)
	}
	assertRowCount(t, pool, nodeID, "processed_batches", 1)
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_targets (
			id, node_id, name, probe_type, host, port, path,
			interval_seconds, timeout_seconds, retention_seconds
		) VALUES ($1::uuid, $2::uuid, 'HTTPS', 'https', 'example.com', 443, '/', 30, 3, 86400)
	`, targetID, nodeID); err != nil {
		t.Fatalf("restore probe target for re-enrollment test: %v", err)
	}

	secondEnrollmentToken := strings.Repeat("f", 32)
	if _, err := pool.Exec(ctx, `
		INSERT INTO enrollment_tokens (id, node_id, token_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, CURRENT_TIMESTAMP + INTERVAL '1 hour')
	`, mustUUID(t), nodeID, HashOpaqueToken(secondEnrollmentToken)); err != nil {
		t.Fatalf("insert second enrollment token: %v", err)
	}
	secondEnrollment := enrollment
	secondEnrollment.EnrollmentToken = secondEnrollmentToken
	secondEnrolled, err := service.Enroll(ctx, secondEnrollment, "192.0.2.10")
	if err != nil {
		t.Fatalf("second epoch Enroll() error = %v", err)
	}
	afterEpoch := report
	afterEpoch.BatchID = mustUUID(t)
	afterEpoch.Sequence = 2
	if _, err := service.Report(ctx, identity, afterEpoch, "192.0.2.10"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old epoch Report() error = %v, want ErrUnauthorized", err)
	}
	secondIdentity, err := service.Authenticate(ctx, secondEnrolled.AgentToken)
	if err != nil {
		t.Fatalf("second epoch Authenticate() error = %v", err)
	}
	secondEpochReport := integrationReport(t, targetID)
	if response, err := service.Report(ctx, secondIdentity, secondEpochReport, "192.0.2.10"); err != nil || response.Status != "accepted" {
		t.Fatalf("second epoch sequence=1 Report() = %#v, %v", response, err)
	}
}

func integrationReport(t *testing.T, targetID string) ReportRequest {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	latency := int64(1000)
	status := 200
	return ReportRequest{
		BatchID:       mustUUID(t),
		Sequence:      1,
		AgentTime:     now,
		AgentVersion:  "1.0.0",
		ConfigVersion: 2,
		Metrics: []MetricSample{{
			SampledAt: now, CPUPercent: 12.5, Load1: 1, Load5: 1, Load15: 1,
			UptimeSeconds: 10, MemoryTotalBytes: 100, MemoryUsedBytes: 50,
			MemoryAvailableBytes: 50, SwapTotalBytes: 10, SwapUsedBytes: 0,
			NetworkRXBPS: 1, NetworkTXBPS: 2, NetworkRXBytes: 3, NetworkTXBytes: 4,
		}},
		Disks: []DiskSample{{SampledAt: now, Mountpoint: "/", TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50}},
		ProbeResults: []ProbeResult{{
			TargetID: targetID, SampledAt: now, SentCount: 1, ReceivedCount: 1, LatencySumUS: latency,
			LatencyMinUS: NullableInt64{Set: true, Value: &latency}, LatencyMaxUS: NullableInt64{Set: true, Value: &latency},
			HTTPStatusCode: NullableInt{Set: true, Value: &status}, ErrorCode: NullableString{Set: true, Value: nil},
		}},
	}
}

func mustUUID(t *testing.T) string {
	t.Helper()
	value, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertRowCount(t *testing.T, pool *pgxpool.Pool, nodeID, table string, expected int) {
	t.Helper()
	allowed := map[string]bool{
		"processed_batches": true, "node_metric_current": true, "node_metric_ring": true,
		"node_disk_current": true, "node_disk_ring": true, "probe_result_raw": true,
	}
	if !allowed[table] {
		t.Fatalf("unsafe integration table %q", table)
	}
	var count int
	query := "SELECT count(*) FROM " + table + " WHERE node_id = $1::uuid"
	if err := pool.QueryRow(context.Background(), query, nodeID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != expected {
		t.Fatalf("%s row count = %d, want %d", table, count, expected)
	}
}
