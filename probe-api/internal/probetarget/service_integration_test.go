package probetarget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/agent"
	"probe-api/internal/auth"
)

func TestServiceIntegrationLimitsVersionsAuditAndHardDelete(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	service := NewService(pool)
	prefix := "target-admin-" + strings.ReplaceAll(mustUUID(t), "-", "")
	admin := insertIntegrationUser(t, pool, prefix+"-admin", auth.RoleAdmin)
	viewer := auth.Identity{User: auth.User{
		ID: mustUUID(t), Username: prefix + "-legacy-viewer", Role: auth.RoleViewer, Enabled: true,
	}}
	nodeID := insertIntegrationNode(t, pool, prefix+"-limit")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM audit_logs WHERE request_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE display_name LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE username LIKE $1`, prefix+"%")
	})

	if _, err := service.List(ctx, viewer, ListRequest{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer List() error = %v, want ErrForbidden", err)
	}
	viewerPort := int32(80)
	if _, err := service.Create(ctx, viewer, CreateRequest{
		NodeID: nodeID, Name: "viewer", Type: TypeTCP, Host: "example.com", Port: &viewerPort,
		IntervalSeconds: 10, TimeoutSeconds: 1, RetentionSeconds: 1, Enabled: true,
	}, Metadata{RequestID: prefix + "-viewer"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer Create() error = %v, want ErrForbidden", err)
	}

	for index := 0; index < MaxTargetsPerNode-1; index++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_targets (
				id, node_id, name, probe_type, host, port, path,
				interval_seconds, timeout_seconds, retention_seconds, enabled
			) VALUES ($1::uuid, $2::uuid, $3, 'tcp', 'example.com', $4, NULL, 30, 3, 86400, TRUE)
		`, mustUUID(t), nodeID, fmt.Sprintf("seed-%02d", index), 10000+index); err != nil {
			t.Fatalf("insert seed target %d: %v", index, err)
		}
	}

	type createResult struct {
		target Target
		err    error
	}
	results := make(chan createResult, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			port := int32(443)
			target, err := service.Create(ctx, admin, CreateRequest{
				NodeID: nodeID, Name: fmt.Sprintf("concurrent-%d", index), Type: TypeHTTPS,
				Host: "example.com", Port: &port, Path: nil,
				IntervalSeconds: 10, TimeoutSeconds: 10,
				RetentionSeconds: MaxRetentionSeconds, Enabled: true,
			}, Metadata{SourceIP: "192.0.2.10", RequestID: fmt.Sprintf("%s-create-%d", prefix, index)})
			results <- createResult{target: target, err: err}
		}(index)
	}
	wait.Wait()
	close(results)
	var created Target
	successes := 0
	limitFailures := 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			created = result.target
		case errors.Is(result.err, ErrLimitExceeded):
			limitFailures++
		default:
			t.Fatalf("concurrent Create() error = %v", result.err)
		}
	}
	if successes != 1 || limitFailures != 1 {
		t.Fatalf("concurrent Create() successes=%d limit_failures=%d", successes, limitFailures)
	}
	if created.Path == nil || *created.Path != "/" || created.RetentionSeconds != MaxRetentionSeconds || created.ConfigVersion != 1 {
		t.Fatalf("created target = %#v", created)
	}
	assertInt64(t, pool, `SELECT count(*) FROM probe_targets WHERE node_id = $1::uuid`, int64(MaxTargetsPerNode), nodeID)
	assertInt64(t, pool, `SELECT config_version FROM nodes WHERE id = $1::uuid`, 2, nodeID)

	seenTargets := make(map[string]struct{}, MaxTargetsPerNode)
	var cursor *Cursor
	for {
		page, err := service.List(ctx, admin, ListRequest{NodeID: &nodeID, Limit: 7, Cursor: cursor})
		if err != nil {
			t.Fatalf("paginated List() error = %v", err)
		}
		for _, target := range page.Targets {
			if _, duplicate := seenTargets[target.TargetID]; duplicate {
				t.Fatalf("paginated List() returned duplicate target %s", target.TargetID)
			}
			seenTargets[target.TargetID] = struct{}{}
		}
		if page.NextCursor == nil {
			break
		}
		decoded, err := DecodeCursor(*page.NextCursor)
		if err != nil {
			t.Fatalf("DecodeCursor() error = %v", err)
		}
		cursor = &decoded
	}
	if len(seenTargets) != MaxTargetsPerNode {
		t.Fatalf("paginated List() returned %d targets, want %d", len(seenTargets), MaxTargetsPerNode)
	}

	// The same persisted configuration is what the authenticated Agent receives.
	tokenID, plaintext, tokenHash, err := agent.NewAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_tokens (id, node_id, token_hash) VALUES ($1::uuid, $2::uuid, $3)`, tokenID, nodeID, tokenHash); err != nil {
		t.Fatalf("insert Agent token: %v", err)
	}
	agentService := agent.NewService(pool)
	identity, err := agentService.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	configuration, notModified, err := agentService.LoadConfig(ctx, identity, 0)
	if err != nil || notModified || len(configuration.ProbeTargets) != MaxTargetsPerNode {
		t.Fatalf("LoadConfig() targets=%d notModified=%v error=%v", len(configuration.ProbeTargets), notModified, err)
	}
	for _, target := range configuration.ProbeTargets {
		if target.Type != "tcp" && target.Type != "http" && target.Type != "https" {
			t.Fatalf("Agent config exposed disabled type %q", target.Type)
		}
	}

	falseValue := false
	timeout := int32(9)
	updates := []UpdateRequest{{Enabled: &falseValue}, {TimeoutSeconds: &timeout}}
	updateErrors := make(chan error, len(updates))
	for index := range updates {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := service.Update(ctx, admin, created.TargetID, updates[index], Metadata{
				SourceIP: "192.0.2.10", RequestID: fmt.Sprintf("%s-update-%d", prefix, index),
			})
			updateErrors <- err
		}(index)
	}
	wait.Wait()
	close(updateErrors)
	for err := range updateErrors {
		if err != nil {
			t.Fatalf("concurrent Update() error = %v", err)
		}
	}
	assertInt64(t, pool, `SELECT config_version FROM probe_targets WHERE id = $1::uuid`, 3, created.TargetID)
	assertInt64(t, pool, `SELECT config_version FROM nodes WHERE id = $1::uuid`, 4, nodeID)

	batchID := mustUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO processed_batches (
			node_id, batch_id, sequence, agent_time, agent_version,
			config_version, payload_checksum, clock_status
		) VALUES ($1::uuid, $2::uuid, 1, CURRENT_TIMESTAMP, 'integration', 1, 'checksum', 'ok')
	`, nodeID, batchID); err != nil {
		t.Fatalf("insert processed batch: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO probe_result_raw (
			node_id, target_id, batch_id, sample_index, sampled_at, effective_at, received_at,
			sent_count, received_count, latency_sum_us, latency_min_us, latency_max_us
		) VALUES ($1::uuid, $2::uuid, $3::uuid, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP,
			CURRENT_TIMESTAMP, 1, 1, 1000, 1000, 1000)
	`, nodeID, created.TargetID, batchID); err != nil {
		t.Fatalf("insert raw result: %v", err)
	}
	for _, table := range []string{"probe_result_5m", "probe_result_1h"} {
		query := `INSERT INTO ` + table + ` (target_id, bucket_start, result_count, sent_count, received_count, latency_sum_us, latency_min_us, latency_max_us) VALUES ($1::uuid, CURRENT_TIMESTAMP, 1, 1, 1, 1000, 1000, 1000)`
		if _, err := pool.Exec(ctx, query, created.TargetID); err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}
	if err := service.Delete(ctx, admin, created.TargetID, Metadata{SourceIP: "192.0.2.10", RequestID: prefix + "-delete"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertInt64(t, pool, `SELECT count(*) FROM probe_targets WHERE id = $1::uuid`, 0, created.TargetID)
	assertInt64(t, pool, `SELECT count(*) FROM probe_result_raw WHERE target_id = $1::uuid`, 0, created.TargetID)
	assertInt64(t, pool, `SELECT count(*) FROM probe_result_5m WHERE target_id = $1::uuid`, 0, created.TargetID)
	assertInt64(t, pool, `SELECT count(*) FROM probe_result_1h WHERE target_id = $1::uuid`, 0, created.TargetID)
	assertInt64(t, pool, `SELECT config_version FROM nodes WHERE id = $1::uuid`, 5, nodeID)

	var beforeText, afterText string
	if err := pool.QueryRow(ctx, `
		SELECT before_summary::text, after_summary::text
		FROM audit_logs WHERE request_id = $1 AND action = 'probe_target.delete'
	`, prefix+"-delete").Scan(&beforeText, &afterText); err != nil {
		t.Fatalf("read deletion audit: %v", err)
	}
	var before, after map[string]any
	if json.Unmarshal([]byte(beforeText), &before) != nil || json.Unmarshal([]byte(afterText), &after) != nil {
		t.Fatalf("invalid audit JSON before=%s after=%s", beforeText, afterText)
	}
	if before["target_id"] != created.TargetID || after["deleted"] != true || after["config_version"] != float64(4) {
		t.Fatalf("deletion audit before=%v after=%v", before, after)
	}
}

func TestServiceIntegrationDatabaseAndAPIBoundaries(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	prefix := "target-boundary-" + strings.ReplaceAll(mustUUID(t), "-", "")
	admin := insertIntegrationUser(t, pool, prefix+"-admin", auth.RoleAdmin)
	nodeID := insertIntegrationNode(t, pool, prefix+"-node")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM audit_logs WHERE request_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE display_name LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE username LIKE $1`, prefix+"%")
	})
	port := int32(1)
	target, err := NewService(pool).Create(ctx, admin, CreateRequest{
		NodeID: nodeID, Name: "minimum", Type: TypeTCP, Host: "2001:db8::1", Port: &port,
		IntervalSeconds: 10, TimeoutSeconds: 1, RetentionSeconds: 1, Enabled: true,
	}, Metadata{SourceIP: "2001:db8::10", RequestID: prefix + "-minimum"})
	if err != nil || target.Port == nil || *target.Port != 1 || target.RetentionSeconds != 1 {
		t.Fatalf("minimum Create() = %#v, %v", target, err)
	}
	maximumPort := int32(65535)
	maximumInterval := int32(86400)
	maximumTimeout := int32(60)
	maximumRetention := int32(MaxRetentionSeconds)
	maximum, err := NewService(pool).Update(ctx, admin, target.TargetID, UpdateRequest{
		Port: NullableInt32{Set: true, Value: &maximumPort}, IntervalSeconds: &maximumInterval,
		TimeoutSeconds: &maximumTimeout, RetentionSeconds: &maximumRetention,
	}, Metadata{SourceIP: "2001:db8::10", RequestID: prefix + "-maximum"})
	if err != nil || maximum.Port == nil || *maximum.Port != 65535 || maximum.IntervalSeconds != 86400 || maximum.TimeoutSeconds != 60 || maximum.RetentionSeconds != MaxRetentionSeconds {
		t.Fatalf("maximum Update() = %#v, %v", maximum, err)
	}
	assertInt64(t, pool, `SELECT config_version FROM probe_targets WHERE id = $1::uuid`, 2, target.TargetID)
	assertInt64(t, pool, `SELECT config_version FROM nodes WHERE id = $1::uuid`, 3, nodeID)

	for _, testCase := range []struct {
		name      string
		probeType string
		retention int
	}{
		{name: "icmp", probeType: "icmp", retention: 1},
		{name: "retention", probeType: "tcp", retention: MaxRetentionSeconds + 1},
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO probe_targets (
				id, node_id, name, probe_type, host, port, path,
				interval_seconds, timeout_seconds, retention_seconds
			) VALUES ($1::uuid, $2::uuid, $3, $4, 'example.com', 80, NULL, 10, 1, $5)
		`, mustUUID(t), nodeID, testCase.name, testCase.probeType, testCase.retention)
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
			t.Fatalf("database accepted %s boundary: %v", testCase.name, err)
		}
	}
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("PROBE_API_INTEGRATION_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("PROBE_API_INTEGRATION_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertIntegrationUser(t *testing.T, pool *pgxpool.Pool, username string, role auth.Role) auth.Identity {
	t.Helper()
	userID := mustUUID(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, 'integration-hash', $3, TRUE)
	`, userID, username, role); err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	return auth.Identity{User: auth.User{ID: userID, Username: username, Role: role, Enabled: true}}
}

func insertIntegrationNode(t *testing.T, pool *pgxpool.Pool, displayName string) string {
	t.Helper()
	nodeID := mustUUID(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO nodes (id, display_name) VALUES ($1::uuid, $2)`, nodeID, displayName); err != nil {
		t.Fatalf("insert integration node: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, nodeID); err != nil {
		t.Fatalf("insert integration Agent settings: %v", err)
	}
	return nodeID
}

func mustUUID(t *testing.T) string {
	t.Helper()
	value, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertInt64(t *testing.T, pool *pgxpool.Pool, query string, want int64, arguments ...any) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), query, arguments...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}
