package panel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const nodeColumns = `
	n.id::text, n.display_name, n.hostname, n.enabled, n.config_version,
	s.collect_interval_seconds, s.report_interval_seconds, s.mountpoints,
	s.include_virtual_interfaces, s.config_refresh_interval_seconds,
	s.max_memory_queue_seconds, s.max_batch_samples,
	n.agent_version, n.operating_system, n.architecture, n.country_code,
	n.region_key, n.location, n.enrolled_at, n.last_received_at,
	n.clock_status, n.clock_skew_seconds, n.created_at, n.updated_at,
	m.sampled_at, m.effective_at, m.received_at,
	m.cpu_percent::double precision, m.load_1::double precision,
	m.load_5::double precision, m.load_15::double precision,
	m.uptime_seconds::double precision, m.memory_total_bytes,
	m.memory_used_bytes, m.memory_available_bytes, m.swap_total_bytes,
	m.swap_used_bytes, m.network_rx_bps::double precision,
	m.network_tx_bps::double precision, m.network_rx_bytes, m.network_tx_bytes,
	d.sampled_at, d.effective_at, d.received_at, d.mountpoint,
	d.total_bytes, d.used_bytes, d.available_bytes`

const classifiedStatusSQL = `CASE
	WHEN NOT n.enabled THEN 'disabled'
	WHEN n.enrolled_at IS NULL THEN 'unregistered'
	WHEN n.last_received_at IS NULL
	  OR n.last_received_at <= $1::timestamptz - ($2::double precision * INTERVAL '1 second') THEN 'offline'
	WHEN n.clock_status = 'skewed' THEN 'skewed'
	ELSE 'online'
END`

type Service struct {
	pool         *pgxpool.Pool
	offlineAfter time.Duration
}

func NewService(pool *pgxpool.Pool, offlineAfter time.Duration) (*Service, error) {
	if pool == nil || offlineAfter <= 0 {
		return nil, ErrInvalidArgument
	}
	return &Service{pool: pool, offlineAfter: offlineAfter}, nil
}

func (s *Service) ListNodes(ctx context.Context, request ListNodesRequest) (NodeListResponse, error) {
	if request.Limit < 1 || request.Limit > MaxListLimit {
		return NodeListResponse{}, ErrInvalidArgument
	}
	if request.Cursor != nil && (request.Cursor.CreatedAt.IsZero() || !ValidUUID(request.Cursor.NodeID)) {
		return NodeListResponse{}, ErrInvalidCursor
	}
	if request.Status != nil && !ValidStatus(*request.Status) {
		return NodeListResponse{}, ErrInvalidArgument
	}

	tx, err := s.beginRead(ctx)
	if err != nil {
		return NodeListResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	asOf, err := queryAsOf(ctx, tx)
	if err != nil {
		return NodeListResponse{}, err
	}
	summary, err := s.querySummary(ctx, tx, asOf)
	if err != nil {
		return NodeListResponse{}, err
	}

	status := ""
	if request.Status != nil {
		status = string(*request.Status)
	}
	var cursorAt any
	var cursorID any
	if request.Cursor != nil {
		cursorAt = request.Cursor.CreatedAt.UTC()
		cursorID = request.Cursor.NodeID
	}
	query := `SELECT ` + nodeColumns + `
		FROM nodes AS n
		LEFT JOIN node_agent_settings AS s ON s.node_id = n.id
		LEFT JOIN node_metric_current AS m ON m.node_id = n.id
		LEFT JOIN node_disk_current AS d ON d.node_id = n.id AND d.mountpoint = '/'
		WHERE ($3::text = '' OR (` + classifiedStatusSQL + `) = $3::text)
		  AND ($4::timestamptz IS NULL OR n.created_at < $4::timestamptz
		       OR (n.created_at = $4::timestamptz AND n.id < $5::uuid))
		ORDER BY n.created_at DESC, n.id DESC
		LIMIT $6`
	rows, err := tx.Query(ctx, query, asOf, s.offlineAfter.Seconds(), status, cursorAt, cursorID, request.Limit+1)
	if err != nil {
		return NodeListResponse{}, fmt.Errorf("query panel nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]NodeSummary, 0, request.Limit)
	for rows.Next() {
		node, scanErr := scanNode(rows, asOf, s.offlineAfter)
		if scanErr != nil {
			return NodeListResponse{}, fmt.Errorf("scan panel node: %w", scanErr)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return NodeListResponse{}, fmt.Errorf("iterate panel nodes: %w", err)
	}
	rows.Close()

	var nextCursor *string
	if len(nodes) > request.Limit {
		nodes = nodes[:request.Limit]
		last := nodes[len(nodes)-1]
		encoded, encodeErr := EncodeCursor(Cursor{CreatedAt: last.CreatedAt, NodeID: last.NodeID})
		if encodeErr != nil {
			return NodeListResponse{}, encodeErr
		}
		nextCursor = &encoded
	}
	if err := tx.Commit(ctx); err != nil {
		return NodeListResponse{}, fmt.Errorf("commit panel nodes query: %w", err)
	}
	return NodeListResponse{Nodes: nodes, NextCursor: nextCursor, Summary: summary}, nil
}

func (s *Service) GetNode(ctx context.Context, nodeID string) (NodeSummary, error) {
	if !ValidUUID(nodeID) {
		return NodeSummary{}, ErrInvalidArgument
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return NodeSummary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	asOf, err := queryAsOf(ctx, tx)
	if err != nil {
		return NodeSummary{}, err
	}
	query := `SELECT ` + nodeColumns + `
		FROM nodes AS n
		LEFT JOIN node_agent_settings AS s ON s.node_id = n.id
		LEFT JOIN node_metric_current AS m ON m.node_id = n.id
		LEFT JOIN node_disk_current AS d ON d.node_id = n.id AND d.mountpoint = '/'
		WHERE n.id = $1::uuid`
	node, err := scanNode(tx.QueryRow(ctx, query, nodeID), asOf, s.offlineAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeSummary{}, ErrNotFound
	}
	if err != nil {
		return NodeSummary{}, fmt.Errorf("query panel node: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return NodeSummary{}, fmt.Errorf("commit panel node query: %w", err)
	}
	return node, nil
}

func (s *Service) Metrics(ctx context.Context, nodeID string, requested TimeRange) (MetricSeriesResponse, error) {
	if !ValidUUID(nodeID) || invalidTimeRange(requested) {
		return MetricSeriesResponse{}, ErrInvalidArgument
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return MetricSeriesResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	asOf, exists, err := queryAsOfAndNode(ctx, tx, nodeID)
	if err != nil {
		return MetricSeriesResponse{}, err
	}
	if !exists {
		return MetricSeriesResponse{}, ErrNotFound
	}
	from, to := visibleWindow(asOf, requested)
	rows, err := tx.Query(ctx, `
		SELECT sampled_at, effective_at, received_at,
		       cpu_percent::double precision, load_1::double precision,
		       load_5::double precision, load_15::double precision,
		       uptime_seconds::double precision, memory_total_bytes,
		       memory_used_bytes, memory_available_bytes, swap_total_bytes,
		       swap_used_bytes, network_rx_bps::double precision,
		       network_tx_bps::double precision, network_rx_bytes, network_tx_bytes
		FROM node_metric_ring
		WHERE node_id = $1::uuid
		  AND effective_at > $2::timestamptz - INTERVAL '5 minutes'
		  AND effective_at <= $2::timestamptz
		  AND ($3::timestamptz IS NULL OR effective_at >= $3::timestamptz)
		  AND ($4::timestamptz IS NULL OR effective_at < $4::timestamptz)
		ORDER BY effective_at ASC, received_at ASC, slot ASC
		LIMIT 61
	`, nodeID, asOf, timeArgument(requested.From), timeArgument(requested.To))
	if err != nil {
		return MetricSeriesResponse{}, fmt.Errorf("query metric history: %w", err)
	}
	defer rows.Close()
	points := make([]MetricPoint, 0, MaxHistoryPoints)
	for rows.Next() {
		point, scanErr := scanMetric(rows)
		if scanErr != nil {
			return MetricSeriesResponse{}, fmt.Errorf("scan metric history: %w", scanErr)
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return MetricSeriesResponse{}, fmt.Errorf("iterate metric history: %w", err)
	}
	if len(points) > MaxHistoryPoints {
		return MetricSeriesResponse{}, ErrInvariant
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return MetricSeriesResponse{}, fmt.Errorf("commit metric history query: %w", err)
	}
	return MetricSeriesResponse{NodeID: nodeID, AsOf: asOf, From: from, To: to, Points: points}, nil
}

func (s *Service) Disks(ctx context.Context, nodeID string, requested TimeRange) (DiskSeriesResponse, error) {
	if !ValidUUID(nodeID) || invalidTimeRange(requested) {
		return DiskSeriesResponse{}, ErrInvalidArgument
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return DiskSeriesResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	asOf, exists, err := queryAsOfAndNode(ctx, tx, nodeID)
	if err != nil {
		return DiskSeriesResponse{}, err
	}
	if !exists {
		return DiskSeriesResponse{}, ErrNotFound
	}
	from, to := visibleWindow(asOf, requested)
	series := make(map[string]*DiskSeries)
	currentRows, err := tx.Query(ctx, `
		SELECT sampled_at, effective_at, received_at, mountpoint,
		       total_bytes, used_bytes, available_bytes
		FROM node_disk_current
		WHERE node_id = $1::uuid
		ORDER BY mountpoint ASC
	`, nodeID)
	if err != nil {
		return DiskSeriesResponse{}, fmt.Errorf("query current disks: %w", err)
	}
	for currentRows.Next() {
		point, scanErr := scanDisk(currentRows)
		if scanErr != nil {
			currentRows.Close()
			return DiskSeriesResponse{}, fmt.Errorf("scan current disk: %w", scanErr)
		}
		pointCopy := point
		series[point.Mountpoint] = &DiskSeries{Mountpoint: point.Mountpoint, Current: &pointCopy, Points: make([]DiskPoint, 0)}
	}
	if err := currentRows.Err(); err != nil {
		currentRows.Close()
		return DiskSeriesResponse{}, fmt.Errorf("iterate current disks: %w", err)
	}
	currentRows.Close()

	historyRows, err := tx.Query(ctx, `
		SELECT sampled_at, effective_at, received_at, mountpoint,
		       total_bytes, used_bytes, available_bytes
		FROM node_disk_ring
		WHERE node_id = $1::uuid
		  AND effective_at > $2::timestamptz - INTERVAL '5 minutes'
		  AND effective_at <= $2::timestamptz
		  AND ($3::timestamptz IS NULL OR effective_at >= $3::timestamptz)
		  AND ($4::timestamptz IS NULL OR effective_at < $4::timestamptz)
		ORDER BY mountpoint ASC, effective_at ASC, received_at ASC, slot ASC
	`, nodeID, asOf, timeArgument(requested.From), timeArgument(requested.To))
	if err != nil {
		return DiskSeriesResponse{}, fmt.Errorf("query disk history: %w", err)
	}
	for historyRows.Next() {
		point, scanErr := scanDisk(historyRows)
		if scanErr != nil {
			historyRows.Close()
			return DiskSeriesResponse{}, fmt.Errorf("scan disk history: %w", scanErr)
		}
		group := series[point.Mountpoint]
		if group == nil {
			group = &DiskSeries{Mountpoint: point.Mountpoint, Points: make([]DiskPoint, 0)}
			series[point.Mountpoint] = group
		}
		group.Points = append(group.Points, point)
		if len(group.Points) > MaxHistoryPoints {
			historyRows.Close()
			return DiskSeriesResponse{}, ErrInvariant
		}
	}
	if err := historyRows.Err(); err != nil {
		historyRows.Close()
		return DiskSeriesResponse{}, fmt.Errorf("iterate disk history: %w", err)
	}
	historyRows.Close()

	mountpoints := make([]string, 0, len(series))
	for mountpoint := range series {
		mountpoints = append(mountpoints, mountpoint)
	}
	sort.Strings(mountpoints)
	disks := make([]DiskSeries, 0, len(mountpoints))
	for _, mountpoint := range mountpoints {
		disks = append(disks, *series[mountpoint])
	}
	if err := tx.Commit(ctx); err != nil {
		return DiskSeriesResponse{}, fmt.Errorf("commit disk history query: %w", err)
	}
	return DiskSeriesResponse{NodeID: nodeID, AsOf: asOf, From: from, To: to, Disks: disks}, nil
}

func (s *Service) beginRead(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin panel read transaction: %w", err)
	}
	return tx, nil
}

func queryAsOf(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var asOf time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&asOf); err != nil {
		return time.Time{}, fmt.Errorf("query panel as_of: %w", err)
	}
	return asOf.UTC(), nil
}

func queryAsOfAndNode(ctx context.Context, tx pgx.Tx, nodeID string) (time.Time, bool, error) {
	var asOf time.Time
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP, EXISTS (SELECT 1 FROM nodes WHERE id = $1::uuid)`, nodeID).Scan(&asOf, &exists); err != nil {
		return time.Time{}, false, fmt.Errorf("query panel window: %w", err)
	}
	return asOf.UTC(), exists, nil
}

func (s *Service) querySummary(ctx context.Context, tx pgx.Tx, asOf time.Time) (PanelSummary, error) {
	query := `WITH classified AS (
		SELECT ` + classifiedStatusSQL + ` AS status,
		       m.network_rx_bps::double precision AS network_rx_bps,
		       m.network_tx_bps::double precision AS network_tx_bps
		FROM nodes AS n
		LEFT JOIN node_metric_current AS m ON m.node_id = n.id
	)
	SELECT count(*)::bigint,
	       count(*) FILTER (WHERE status = 'online')::bigint,
	       count(*) FILTER (WHERE status = 'offline')::bigint,
	       count(*) FILTER (WHERE status = 'unregistered')::bigint,
	       count(*) FILTER (WHERE status = 'disabled')::bigint,
	       count(*) FILTER (WHERE status = 'skewed')::bigint,
	       COALESCE(sum(network_rx_bps) FILTER (WHERE status IN ('online', 'skewed')), 0)::double precision,
	       COALESCE(sum(network_tx_bps) FILTER (WHERE status IN ('online', 'skewed')), 0)::double precision
	FROM classified`
	var summary PanelSummary
	if err := tx.QueryRow(ctx, query, asOf, s.offlineAfter.Seconds()).Scan(
		&summary.Total, &summary.Online, &summary.Offline, &summary.Unregistered,
		&summary.Disabled, &summary.Skewed, &summary.NetworkRXBPS, &summary.NetworkTXBPS,
	); err != nil {
		return PanelSummary{}, fmt.Errorf("query panel summary: %w", err)
	}
	return summary, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanNode(scanner rowScanner, asOf time.Time, offlineAfter time.Duration) (NodeSummary, error) {
	var node NodeSummary
	var hostname, agentVersion, operatingSystem, architecture pgtype.Text
	var countryCode, regionKey, location pgtype.Text
	var enrolledAt, lastReceivedAt pgtype.Timestamptz
	var clockSkew pgtype.Int8
	var currentSampled, currentEffective, currentReceived pgtype.Timestamptz
	var currentCPU, currentLoad1, currentLoad5, currentLoad15, currentUptime pgtype.Float8
	var currentMemoryTotal, currentMemoryUsed, currentMemoryAvailable pgtype.Int8
	var currentSwapTotal, currentSwapUsed pgtype.Int8
	var currentRXBPS, currentTXBPS pgtype.Float8
	var currentRXBytes, currentTXBytes pgtype.Int8
	var rootSampled, rootEffective, rootReceived pgtype.Timestamptz
	var rootMountpoint pgtype.Text
	var rootTotal, rootUsed, rootAvailable pgtype.Int8
	err := scanner.Scan(
		&node.NodeID, &node.DisplayName, &hostname, &node.Enabled, &node.ConfigVersion,
		&node.AgentSettings.Metrics.CollectIntervalSeconds, &node.AgentSettings.Metrics.ReportIntervalSeconds,
		&node.AgentSettings.Metrics.Mountpoints, &node.AgentSettings.Metrics.IncludeVirtualInterfaces,
		&node.AgentSettings.Agent.ConfigRefreshIntervalSeconds, &node.AgentSettings.Agent.MaxMemoryQueueSeconds,
		&node.AgentSettings.Limits.MaxBatchSamples, &agentVersion, &operatingSystem, &architecture,
		&countryCode, &regionKey, &location, &enrolledAt, &lastReceivedAt,
		&node.ClockStatus, &clockSkew, &node.CreatedAt, &node.UpdatedAt,
		&currentSampled, &currentEffective, &currentReceived, &currentCPU,
		&currentLoad1, &currentLoad5, &currentLoad15, &currentUptime,
		&currentMemoryTotal, &currentMemoryUsed, &currentMemoryAvailable,
		&currentSwapTotal, &currentSwapUsed, &currentRXBPS, &currentTXBPS,
		&currentRXBytes, &currentTXBytes,
		&rootSampled, &rootEffective, &rootReceived, &rootMountpoint,
		&rootTotal, &rootUsed, &rootAvailable,
	)
	if err != nil {
		return NodeSummary{}, err
	}
	node.Hostname = textPointer(hostname)
	node.AgentVersion = textPointer(agentVersion)
	node.OperatingSystem = textPointer(operatingSystem)
	node.Architecture = textPointer(architecture)
	node.CountryCode = textPointer(countryCode)
	node.RegionKey = textPointer(regionKey)
	node.Location = textPointer(location)
	node.EnrolledAt = timePointer(enrolledAt)
	node.LastReceivedAt = timePointer(lastReceivedAt)
	node.ClockSkewSeconds = intPointer(clockSkew)
	node.CreatedAt = node.CreatedAt.UTC()
	node.UpdatedAt = node.UpdatedAt.UTC()
	node.Status = StatusAt(node.Enabled, node.EnrolledAt, node.LastReceivedAt, node.ClockStatus, asOf, offlineAfter)
	if currentSampled.Valid {
		if !allMetricValuesValid(currentEffective, currentReceived, currentCPU, currentLoad1, currentLoad5, currentLoad15,
			currentUptime, currentMemoryTotal, currentMemoryUsed, currentMemoryAvailable, currentSwapTotal,
			currentSwapUsed, currentRXBPS, currentTXBPS, currentRXBytes, currentTXBytes) {
			return NodeSummary{}, ErrInvariant
		}
		node.CurrentMetrics = &MetricPoint{
			SampledAt: currentSampled.Time.UTC(), EffectiveAt: currentEffective.Time.UTC(), ReceivedAt: currentReceived.Time.UTC(),
			CPUPercent: currentCPU.Float64, Load1: currentLoad1.Float64, Load5: currentLoad5.Float64, Load15: currentLoad15.Float64,
			UptimeSeconds: currentUptime.Float64, MemoryTotalBytes: currentMemoryTotal.Int64,
			MemoryUsedBytes: currentMemoryUsed.Int64, MemoryAvailableBytes: currentMemoryAvailable.Int64,
			SwapTotalBytes: currentSwapTotal.Int64, SwapUsedBytes: currentSwapUsed.Int64,
			NetworkRXBPS: currentRXBPS.Float64, NetworkTXBPS: currentTXBPS.Float64,
			NetworkRXBytes: currentRXBytes.Int64, NetworkTXBytes: currentTXBytes.Int64,
			TotalTrafficBytes: uint64(currentRXBytes.Int64) + uint64(currentTXBytes.Int64),
		}
	}
	if rootSampled.Valid {
		if !rootEffective.Valid || !rootReceived.Valid || !rootMountpoint.Valid ||
			!rootTotal.Valid || !rootUsed.Valid || !rootAvailable.Valid || rootMountpoint.String != "/" {
			return NodeSummary{}, ErrInvariant
		}
		node.RootDisk = &DiskPoint{
			SampledAt: rootSampled.Time.UTC(), EffectiveAt: rootEffective.Time.UTC(), ReceivedAt: rootReceived.Time.UTC(),
			Mountpoint: rootMountpoint.String, TotalBytes: rootTotal.Int64,
			UsedBytes: rootUsed.Int64, AvailableBytes: rootAvailable.Int64,
		}
	}
	return node, nil
}

func scanMetric(scanner rowScanner) (MetricPoint, error) {
	var point MetricPoint
	if err := scanner.Scan(
		&point.SampledAt, &point.EffectiveAt, &point.ReceivedAt,
		&point.CPUPercent, &point.Load1, &point.Load5, &point.Load15,
		&point.UptimeSeconds, &point.MemoryTotalBytes, &point.MemoryUsedBytes,
		&point.MemoryAvailableBytes, &point.SwapTotalBytes, &point.SwapUsedBytes,
		&point.NetworkRXBPS, &point.NetworkTXBPS, &point.NetworkRXBytes, &point.NetworkTXBytes,
	); err != nil {
		return MetricPoint{}, err
	}
	point.SampledAt = point.SampledAt.UTC()
	point.EffectiveAt = point.EffectiveAt.UTC()
	point.ReceivedAt = point.ReceivedAt.UTC()
	point.TotalTrafficBytes = uint64(point.NetworkRXBytes) + uint64(point.NetworkTXBytes)
	return point, nil
}

func scanDisk(scanner rowScanner) (DiskPoint, error) {
	var point DiskPoint
	if err := scanner.Scan(&point.SampledAt, &point.EffectiveAt, &point.ReceivedAt, &point.Mountpoint,
		&point.TotalBytes, &point.UsedBytes, &point.AvailableBytes); err != nil {
		return DiskPoint{}, err
	}
	point.SampledAt = point.SampledAt.UTC()
	point.EffectiveAt = point.EffectiveAt.UTC()
	point.ReceivedAt = point.ReceivedAt.UTC()
	return point, nil
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func intPointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func invalidTimeRange(value TimeRange) bool {
	if value.From != nil && value.From.IsZero() || value.To != nil && value.To.IsZero() {
		return true
	}
	return value.From != nil && value.To != nil && !value.From.Before(*value.To)
}

func visibleWindow(asOf time.Time, requested TimeRange) (time.Time, time.Time) {
	cutoff := asOf.Add(-5 * time.Minute)
	from := cutoff
	to := asOf
	if requested.From != nil && requested.From.After(from) {
		from = requested.From.UTC()
	}
	if requested.To != nil && requested.To.Before(to) {
		to = requested.To.UTC()
	}
	if !from.Before(to) {
		if !to.After(cutoff) {
			return cutoff, cutoff
		}
		return asOf, asOf
	}
	return from.UTC(), to.UTC()
}

func timeArgument(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func allMetricValuesValid(currentEffective, currentReceived pgtype.Timestamptz,
	currentCPU, currentLoad1, currentLoad5, currentLoad15, currentUptime pgtype.Float8,
	currentMemoryTotal, currentMemoryUsed, currentMemoryAvailable, currentSwapTotal, currentSwapUsed pgtype.Int8,
	currentRXBPS, currentTXBPS pgtype.Float8, currentRXBytes, currentTXBytes pgtype.Int8,
) bool {
	return currentEffective.Valid && currentReceived.Valid && currentCPU.Valid && currentLoad1.Valid && currentLoad5.Valid &&
		currentLoad15.Valid && currentUptime.Valid && currentMemoryTotal.Valid && currentMemoryUsed.Valid &&
		currentMemoryAvailable.Valid && currentSwapTotal.Valid && currentSwapUsed.Valid && currentRXBPS.Valid &&
		currentTXBPS.Valid && currentRXBytes.Valid && currentTXBytes.Valid
}
