package nodemanagement

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/agent"
	"probe-api/internal/auth"
)

func TestDeleteNodeImmediatelyInvalidatesAgentTokenAndCachedIdentity(t *testing.T) {
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

	actorID, err := newUUID()
	if err != nil {
		t.Fatalf("generate administrator ID: %v", err)
	}
	prefix := "node-delete-e2e-" + strings.ReplaceAll(actorID, "-", "")[:12]
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, $3, 'admin', TRUE)
	`, actorID, prefix, auth.DummyPasswordHash()); err != nil {
		t.Fatalf("insert deletion test administrator: %v", err)
	}
	var nodeID string
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if nodeID != "" {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID)
		}
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM audit_logs WHERE request_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1::uuid`, actorID)
	})

	actor := auth.Identity{User: auth.User{
		ID: actorID, Username: prefix, Role: auth.RoleAdmin, Enabled: true,
	}}
	nodeService, err := NewService(pool, 45*time.Second)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	node, err := nodeService.Create(ctx, actor, CreateRequest{DisplayName: prefix + "-node"}, Metadata{
		SourceIP: "192.0.2.70", RequestID: prefix + "-create",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	nodeID = node.NodeID
	rotated, err := nodeService.RotateAgentToken(ctx, actor, nodeID, Metadata{
		SourceIP: "192.0.2.70", RequestID: prefix + "-rotate",
	})
	if err != nil {
		t.Fatalf("RotateAgentToken() error = %v", err)
	}

	agentService := agent.NewService(pool)
	identity, err := agentService.Authenticate(ctx, rotated.AgentToken)
	if err != nil {
		t.Fatalf("Authenticate() before node deletion error = %v", err)
	}
	if identity.NodeID != nodeID {
		t.Fatalf("Authenticate() node = %s, want %s", identity.NodeID, nodeID)
	}

	firstReport := deletionIntegrationReport(t, 1)
	response, err := agentService.Report(ctx, identity, firstReport, "192.0.2.71")
	if err != nil || response.Status != "accepted" {
		t.Fatalf("Report() before node deletion = %#v, error=%v", response, err)
	}
	if err := nodeService.Delete(ctx, actor, nodeID, Metadata{
		SourceIP: "192.0.2.70", RequestID: prefix + "-delete",
	}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := agentService.Authenticate(ctx, rotated.AgentToken); !errors.Is(err, agent.ErrUnauthorized) {
		t.Fatalf("Authenticate() after node deletion error = %v, want ErrUnauthorized", err)
	}
	secondReport := deletionIntegrationReport(t, 2)
	if _, err := agentService.Report(ctx, identity, secondReport, "192.0.2.71"); !errors.Is(err, agent.ErrUnauthorized) {
		t.Fatalf("Report() with cached identity after node deletion error = %v, want ErrUnauthorized", err)
	}

	var nodes, tokens, batches int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id = $1::uuid`, nodeID).Scan(&nodes); err != nil {
		t.Fatalf("count deleted node: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_tokens WHERE node_id = $1::uuid`, nodeID).Scan(&tokens); err != nil {
		t.Fatalf("count deleted node tokens: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM processed_batches WHERE node_id = $1::uuid`, nodeID).Scan(&batches); err != nil {
		t.Fatalf("count deleted node batches: %v", err)
	}
	if nodes != 0 || tokens != 0 || batches != 0 {
		t.Fatalf("deleted node retained node/token/batch rows = %d/%d/%d", nodes, tokens, batches)
	}
	nodeID = ""
}

func deletionIntegrationReport(t *testing.T, sequence int64) agent.ReportRequest {
	t.Helper()
	batchID, err := newUUID()
	if err != nil {
		t.Fatalf("generate report batch ID: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	return agent.ReportRequest{
		BatchID:       batchID,
		Sequence:      sequence,
		AgentTime:     now,
		AgentVersion:  "deletion-integration-test",
		ConfigVersion: 1,
		Metrics: []agent.MetricSample{{
			SampledAt: now, CPUPercent: 1, Load1: 0, Load5: 0, Load15: 0,
			UptimeSeconds: 1, MemoryTotalBytes: 1024, MemoryUsedBytes: 512,
			MemoryAvailableBytes: 512, SwapTotalBytes: 0, SwapUsedBytes: 0,
			NetworkRXBPS: 0, NetworkTXBPS: 0, NetworkRXBytes: 0, NetworkTXBytes: 0,
		}},
		Disks:        []agent.DiskSample{},
		ProbeResults: []agent.ProbeResult{},
	}
}
