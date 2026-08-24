package panel

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

func TestServicePanelQueriesAndRetentionBoundary(t *testing.T) {
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
	service, err := NewService(pool, 45*time.Second)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	baseline, err := service.ListNodes(ctx, ListNodesRequest{Limit: 1})
	if err != nil {
		t.Fatalf("baseline ListNodes() error = %v", err)
	}

	type seededNode struct {
		id     string
		status Status
	}
	seeded := []seededNode{
		{id: mustPanelUUID(t), status: StatusDisabled},
		{id: mustPanelUUID(t), status: StatusUnregistered},
		{id: mustPanelUUID(t), status: StatusOffline},
		{id: mustPanelUUID(t), status: StatusSkewed},
		{id: mustPanelUUID(t), status: StatusOnline},
	}
	for _, node := range seeded {
		enabled := node.status != StatusDisabled
		var enrolled any
		var received any
		clock := "unknown"
		if node.status != StatusUnregistered {
			enrolled = time.Now().UTC().Add(-time.Hour)
		}
		switch node.status {
		case StatusOffline:
			received = time.Now().UTC().Add(-time.Minute)
			clock = "ok"
		case StatusSkewed:
			received = time.Now().UTC()
			clock = "skewed"
		case StatusOnline:
			received = time.Now().UTC()
			clock = "ok"
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO nodes (id, display_name, enabled, enrolled_at, last_received_at, clock_status)
			VALUES ($1::uuid, $2, $3, $4::timestamptz, $5::timestamptz, $6)
		`, node.id, "panel-"+string(node.status), enabled, enrolled, received, clock); err != nil {
			t.Fatalf("insert %s node: %v", node.status, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO node_agent_settings (node_id) VALUES ($1::uuid)`, node.id); err != nil {
			t.Fatalf("insert %s settings: %v", node.status, err)
		}
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, node := range seeded {
			if _, err := pool.Exec(cleanupContext, `DELETE FROM nodes WHERE id = $1::uuid`, node.id); err != nil {
				t.Errorf("cleanup node %s: %v", node.id, err)
			}
		}
	})

	statusIDs := make(map[Status]string, len(seeded))
	for _, node := range seeded {
		statusIDs[node.status] = node.id
	}
	insertPanelCurrentMetric(t, pool, statusIDs[StatusDisabled], 1000, 2000)
	insertPanelCurrentMetric(t, pool, statusIDs[StatusOffline], 100, 200)
	insertPanelCurrentMetric(t, pool, statusIDs[StatusSkewed], 7, 9)
	insertPanelCurrentMetric(t, pool, statusIDs[StatusOnline], 11, 13)

	first, err := service.ListNodes(ctx, ListNodesRequest{Limit: 2})
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if first.Summary.Total != baseline.Summary.Total+5 ||
		first.Summary.Disabled != baseline.Summary.Disabled+1 ||
		first.Summary.Unregistered != baseline.Summary.Unregistered+1 ||
		first.Summary.Offline != baseline.Summary.Offline+1 ||
		first.Summary.Skewed != baseline.Summary.Skewed+1 ||
		first.Summary.Online != baseline.Summary.Online+1 {
		t.Fatalf("summary = %#v, baseline = %#v", first.Summary, baseline.Summary)
	}
	if math.Abs((first.Summary.NetworkRXBPS-baseline.Summary.NetworkRXBPS)-18) > 0.000001 ||
		math.Abs((first.Summary.NetworkTXBPS-baseline.Summary.NetworkTXBPS)-22) > 0.000001 {
		t.Fatalf("summary throughput includes stale states: got rx=%v tx=%v baseline=%#v",
			first.Summary.NetworkRXBPS, first.Summary.NetworkTXBPS, baseline.Summary)
	}

	seen := make(map[string]bool)
	page := first
	for {
		for _, node := range page.Nodes {
			if seen[node.NodeID] {
				t.Fatalf("cursor pagination duplicated node %s", node.NodeID)
			}
			seen[node.NodeID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor, err := DecodeCursor(*page.NextCursor)
		if err != nil {
			t.Fatalf("DecodeCursor() error = %v", err)
		}
		page, err = service.ListNodes(ctx, ListNodesRequest{Limit: 2, Cursor: &cursor})
		if err != nil {
			t.Fatalf("next ListNodes() error = %v", err)
		}
	}
	for _, node := range seeded {
		if !seen[node.id] {
			t.Fatalf("pagination omitted seeded node %s", node.id)
		}
	}

	onlineStatus := StatusOnline
	onlinePage, err := service.ListNodes(ctx, ListNodesRequest{Limit: MaxListLimit, Status: &onlineStatus})
	if err != nil {
		t.Fatalf("online ListNodes() error = %v", err)
	}
	for _, node := range onlinePage.Nodes {
		if node.Status != StatusOnline {
			t.Fatalf("online filter returned %q", node.Status)
		}
	}
	detail, err := service.GetNode(ctx, statusIDs[StatusOnline])
	if err != nil {
		t.Fatalf("GetNode() error = %v", err)
	}
	if detail.Status != StatusOnline || detail.CurrentMetrics == nil || detail.CurrentMetrics.TotalTrafficBytes != 300 {
		t.Fatalf("GetNode() = %#v", detail)
	}
	if detail.RootDisk != nil {
		t.Fatalf("GetNode() fabricated root disk = %#v", detail.RootDisk)
	}

	nodeID := statusIDs[StatusOnline]
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_metric_ring (
			node_id, slot, sampled_at, effective_at, received_at, cpu_percent,
			load_1, load_5, load_15, uptime_seconds, memory_total_bytes,
			memory_used_bytes, memory_available_bytes, swap_total_bytes,
			swap_used_bytes, network_rx_bps, network_tx_bps, network_rx_bytes, network_tx_bytes
		)
		SELECT $1::uuid, value.slot, CURRENT_TIMESTAMP + value.offset_value,
		       CURRENT_TIMESTAMP + value.offset_value, CURRENT_TIMESTAMP,
		       value.cpu, 1, 1, 1, 100, 1000, 400, 600, 100, 20, 1, 2, 100, 200
		FROM (VALUES
			(0::smallint, INTERVAL '-6 minutes', 1::numeric),
			(1::smallint, INTERVAL '-5 minutes', 2::numeric),
			(2::smallint, INTERVAL '-4 minutes', 3::numeric),
			(3::smallint, INTERVAL '-2 minutes', 4::numeric),
			(4::smallint, INTERVAL '1 minute', 5::numeric)
		) AS value(slot, offset_value, cpu)
	`, nodeID); err != nil {
		t.Fatalf("insert metric history: %v", err)
	}
	metrics, err := service.Metrics(ctx, nodeID, TimeRange{})
	if err != nil {
		t.Fatalf("Metrics() error = %v", err)
	}
	if len(metrics.Points) != 2 || metrics.Points[0].CPUPercent != 3 || metrics.Points[1].CPUPercent != 4 {
		t.Fatalf("Metrics() points = %#v", metrics.Points)
	}
	cutoff := metrics.AsOf.Add(-5 * time.Minute)
	for _, point := range metrics.Points {
		if !point.EffectiveAt.After(cutoff) || point.EffectiveAt.After(metrics.AsOf) {
			t.Fatalf("metric escaped visible window: %v, as_of=%v", point.EffectiveAt, metrics.AsOf)
		}
	}
	from := metrics.AsOf.Add(-3 * time.Minute)
	to := metrics.AsOf.Add(-time.Minute)
	cropped, err := service.Metrics(ctx, nodeID, TimeRange{From: &from, To: &to})
	if err != nil || len(cropped.Points) != 1 || cropped.Points[0].CPUPercent != 4 {
		t.Fatalf("cropped Metrics() = %#v, error=%v", cropped, err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO node_disk_current (node_id, mountpoint, sampled_at, effective_at, received_at, total_bytes, used_bytes, available_bytes)
		VALUES
			($1::uuid, '/', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1000, 400, 600),
			($1::uuid, '/data', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 2000, 500, 1500)
	`, nodeID); err != nil {
		t.Fatalf("insert current disks: %v", err)
	}
	detailWithDisk, err := service.GetNode(ctx, nodeID)
	if err != nil || detailWithDisk.RootDisk == nil || detailWithDisk.RootDisk.Mountpoint != "/" ||
		detailWithDisk.RootDisk.TotalBytes != 1000 || detailWithDisk.RootDisk.UsedBytes != 400 {
		t.Fatalf("GetNode() root disk = %#v, error=%v", detailWithDisk.RootDisk, err)
	}
	listWithDisk, err := service.ListNodes(ctx, ListNodesRequest{Limit: MaxListLimit, Status: &onlineStatus})
	if err != nil {
		t.Fatalf("ListNodes() after root disk error = %v", err)
	}
	rootSeen := false
	for _, node := range listWithDisk.Nodes {
		if node.NodeID == nodeID {
			rootSeen = node.RootDisk != nil && node.RootDisk.Mountpoint == "/" && node.RootDisk.AvailableBytes == 600
		}
	}
	if !rootSeen {
		t.Fatal("ListNodes() did not project the root disk")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_disk_ring (node_id, mountpoint, slot, sampled_at, effective_at, received_at, total_bytes, used_bytes, available_bytes)
		VALUES
			($1::uuid, '/', 0, CURRENT_TIMESTAMP - INTERVAL '5 minutes', CURRENT_TIMESTAMP - INTERVAL '5 minutes', CURRENT_TIMESTAMP, 1000, 100, 900),
			($1::uuid, '/', 1, CURRENT_TIMESTAMP - INTERVAL '1 minute', CURRENT_TIMESTAMP - INTERVAL '1 minute', CURRENT_TIMESTAMP, 1000, 400, 600),
			($1::uuid, '/data', 0, CURRENT_TIMESTAMP - INTERVAL '4 minutes', CURRENT_TIMESTAMP - INTERVAL '4 minutes', CURRENT_TIMESTAMP, 2000, 400, 1600),
			($1::uuid, '/data', 1, CURRENT_TIMESTAMP - INTERVAL '2 minutes', CURRENT_TIMESTAMP - INTERVAL '2 minutes', CURRENT_TIMESTAMP, 2000, 500, 1500),
			($1::uuid, '/ring-only', 0, CURRENT_TIMESTAMP - INTERVAL '1 minute', CURRENT_TIMESTAMP - INTERVAL '1 minute', CURRENT_TIMESTAMP, 3000, 600, 2400)
	`, nodeID); err != nil {
		t.Fatalf("insert disk history: %v", err)
	}
	disks, err := service.Disks(ctx, nodeID, TimeRange{})
	if err != nil {
		t.Fatalf("Disks() error = %v", err)
	}
	if len(disks.Disks) != 3 || disks.Disks[0].Mountpoint != "/" || len(disks.Disks[0].Points) != 1 ||
		disks.Disks[1].Mountpoint != "/data" || len(disks.Disks[1].Points) != 2 ||
		disks.Disks[2].Mountpoint != "/ring-only" || disks.Disks[2].Current != nil || len(disks.Disks[2].Points) != 1 {
		t.Fatalf("Disks() = %#v", disks.Disks)
	}
	for _, disk := range disks.Disks {
		if len(disk.Points) > MaxHistoryPoints {
			t.Fatalf("mount %s returned %d points", disk.Mountpoint, len(disk.Points))
		}
		for _, point := range disk.Points {
			if !point.EffectiveAt.After(disks.AsOf.Add(-5*time.Minute)) || point.EffectiveAt.After(disks.AsOf) {
				t.Fatalf("disk point escaped visible window: %#v", point)
			}
		}
	}

	missing, err := service.Metrics(ctx, mustPanelUUID(t), TimeRange{})
	if err == nil || missing.NodeID != "" {
		t.Fatalf("missing Metrics() = %#v, error=%v", missing, err)
	}
}

func insertPanelCurrentMetric(t *testing.T, pool *pgxpool.Pool, nodeID string, rxBPS, txBPS float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO node_metric_current (
			node_id, sampled_at, effective_at, received_at, cpu_percent,
			load_1, load_5, load_15, uptime_seconds, memory_total_bytes,
			memory_used_bytes, memory_available_bytes, swap_total_bytes,
			swap_used_bytes, network_rx_bps, network_tx_bps, network_rx_bytes, network_tx_bytes
		) VALUES (
			$1::uuid, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 10,
			1, 1, 1, 100, 1000, 400, 600, 100, 20, $2, $3, 100, 200
		)
	`, nodeID, rxBPS, txBPS); err != nil {
		t.Fatalf("insert current metric for %s: %v", nodeID, err)
	}
}

func mustPanelUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
