package agent

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const nilUUID = "00000000-0000-0000-0000-000000000000"

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func (s *Service) Authenticate(ctx context.Context, plaintext string) (Identity, error) {
	tokenID, parsed := ParseAgentToken(plaintext)
	lookupID := tokenID
	if !parsed {
		lookupID = nilUUID
	}

	var identity Identity
	var storedHash string
	err := s.pool.QueryRow(ctx, `
		SELECT at.id::text, at.node_id::text, at.token_hash
		FROM agent_tokens AS at
		JOIN nodes AS n ON n.id = at.node_id
		WHERE at.id = $1::uuid
		  AND at.revoked_at IS NULL
		  AND (at.expires_at IS NULL OR at.expires_at > CURRENT_TIMESTAMP)
		  AND n.enabled
	`, lookupID).Scan(&identity.TokenID, &identity.NodeID, &storedHash)
	rowFound := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		storedHash = ""
	} else if err != nil {
		return Identity{}, fmt.Errorf("query Agent token: %w", err)
	}

	matched := ConstantTimeHashEqual(storedHash, plaintext)
	if !parsed || !rowFound || !matched {
		return Identity{}, ErrUnauthorized
	}
	return identity, nil
}

func (s *Service) Enroll(ctx context.Context, request EnrollRequest, sourceIP string) (EnrollResponse, error) {
	if err := request.Validate(); err != nil {
		return EnrollResponse{}, err
	}
	tokenID, plaintext, tokenHash, err := NewAgentToken()
	if err != nil {
		return EnrollResponse{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EnrollResponse{}, fmt.Errorf("begin enrollment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var nodeID string
	enrollmentHash := HashOpaqueToken(request.EnrollmentToken)
	err = tx.QueryRow(ctx, `
		SELECT node_id::text
		FROM enrollment_tokens
		WHERE token_hash = $1
	`, enrollmentHash).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnrollResponse{}, ErrUnauthorized
	}
	if err != nil {
		return EnrollResponse{}, fmt.Errorf("find enrollment token: %w", err)
	}

	var configVersion int64
	var enabled bool
	err = tx.QueryRow(ctx, `
		SELECT config_version, enabled
		FROM nodes
		WHERE id = $1::uuid
		FOR UPDATE
	`, nodeID).Scan(&configVersion, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnrollResponse{}, ErrUnauthorized
	}
	if err != nil {
		return EnrollResponse{}, fmt.Errorf("lock enrollment node: %w", err)
	}

	var enrollmentID string
	var usedAt pgtype.Timestamptz
	var expired bool
	err = tx.QueryRow(ctx, `
		SELECT id::text, used_at, expires_at <= CURRENT_TIMESTAMP
		FROM enrollment_tokens
		WHERE token_hash = $1 AND node_id = $2::uuid
		FOR UPDATE
	`, enrollmentHash, nodeID).Scan(&enrollmentID, &usedAt, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnrollResponse{}, ErrUnauthorized
	}
	if err != nil {
		return EnrollResponse{}, fmt.Errorf("lock enrollment token: %w", err)
	}
	if usedAt.Valid {
		return EnrollResponse{}, ErrEnrollmentTokenUsed
	}
	if expired || !enabled {
		return EnrollResponse{}, ErrUnauthorized
	}

	var hasSettings bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM node_agent_settings WHERE node_id = $1::uuid)`, nodeID).Scan(&hasSettings); err != nil {
		return EnrollResponse{}, fmt.Errorf("check Agent settings: %w", err)
	}
	if !hasSettings {
		return EnrollResponse{}, errors.New("node is missing persistent Agent settings")
	}

	if _, err := tx.Exec(ctx, `
		UPDATE agent_tokens
		SET revoked_at = CURRENT_TIMESTAMP
		WHERE node_id = $1::uuid AND revoked_at IS NULL
	`, nodeID); err != nil {
		return EnrollResponse{}, fmt.Errorf("revoke previous Agent tokens: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_tokens (id, node_id, token_hash, label)
		VALUES ($1::uuid, $2::uuid, $3, 'enrollment')
	`, tokenID, nodeID, tokenHash); err != nil {
		return EnrollResponse{}, fmt.Errorf("insert Agent token: %w", err)
	}
	consumedTokens, err := tx.Exec(ctx, `
		UPDATE enrollment_tokens
		SET used_at = CURRENT_TIMESTAMP,
		    used_source_ip = CASE
		        WHEN id = $1::uuid THEN NULLIF($2, '')::inet
		        ELSE NULL
		    END
		WHERE node_id = $3::uuid AND used_at IS NULL
	`, enrollmentID, sourceIP, nodeID)
	if err != nil {
		return EnrollResponse{}, fmt.Errorf("consume enrollment token and invalidate siblings: %w", err)
	}
	if consumedTokens.RowsAffected() < 1 {
		return EnrollResponse{}, errors.New("enrollment token disappeared while locked")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE nodes
		SET hostname = $2,
		    agent_version = $3,
		    operating_system = $4,
		    architecture = $5,
		    enrolled_at = CURRENT_TIMESTAMP,
		    last_received_at = NULL,
		    last_agent_time = NULL,
		    last_accepted_sequence = 0,
		    last_source_ip = NULL,
		    clock_status = 'unknown',
		    clock_skew_seconds = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
	`, nodeID, request.Hostname, request.AgentVersion, request.OS, request.Arch); err != nil {
		return EnrollResponse{}, fmt.Errorf("update enrolled node: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return EnrollResponse{}, fmt.Errorf("commit enrollment transaction: %w", err)
	}
	return EnrollResponse{NodeID: nodeID, AgentToken: plaintext, ConfigVersion: configVersion}, nil
}

func (s *Service) LoadConfig(ctx context.Context, identity Identity, clientVersion int64) (Config, bool, error) {
	if clientVersion < 0 {
		return Config{}, false, &FieldError{Code: "invalid_request", Field: "version", Message: "must be non-negative"}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Config{}, false, fmt.Errorf("begin config transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	configuration := Config{ProbeTargets: make([]ProbeTarget, 0)}
	var enabled bool
	err = tx.QueryRow(ctx, `
		SELECT enabled
		FROM nodes
		WHERE id = $1::uuid
		FOR SHARE
	`, identity.NodeID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return Config{}, false, ErrUnauthorized
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("lock config node: %w", err)
	}
	if err := revalidateToken(ctx, tx, identity); err != nil {
		return Config{}, false, err
	}

	err = tx.QueryRow(ctx, `
		SELECT n.config_version,
		       CURRENT_TIMESTAMP,
		       s.collect_interval_seconds,
		       s.report_interval_seconds,
		       s.mountpoints,
		       s.include_virtual_interfaces,
		       s.config_refresh_interval_seconds,
		       s.max_memory_queue_seconds,
		       s.max_batch_samples
		FROM nodes AS n
		JOIN node_agent_settings AS s ON s.node_id = n.id
		WHERE n.id = $1::uuid AND n.enabled
	`, identity.NodeID).Scan(
		&configuration.ConfigVersion,
		&configuration.IssuedAt,
		&configuration.Metrics.CollectIntervalSeconds,
		&configuration.Metrics.ReportIntervalSeconds,
		&configuration.Metrics.Mountpoints,
		&configuration.Metrics.IncludeVirtualInterfaces,
		&configuration.Agent.ConfigRefreshIntervalSeconds,
		&configuration.Agent.MaxMemoryQueueSeconds,
		&configuration.Limits.MaxBatchSamples,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, false, ErrUnauthorized
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("load persistent Agent settings: %w", err)
	}
	configuration.IssuedAt = configuration.IssuedAt.UTC()
	if err := configuration.ValidateSettings(); err != nil {
		return Config{}, false, err
	}
	if clientVersion == configuration.ConfigVersion {
		if err := tx.Commit(ctx); err != nil {
			return Config{}, false, fmt.Errorf("commit config transaction: %w", err)
		}
		return Config{}, true, nil
	}
	if clientVersion > configuration.ConfigVersion {
		return Config{}, false, ErrConfigVersionAhead
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text,
		       name,
		       probe_type,
		       host,
		       port,
		       path,
		       interval_seconds,
		       timeout_seconds,
		       retention_seconds,
		       enabled,
		       config_version
		FROM probe_targets
		WHERE node_id = $1::uuid
		  AND probe_type IN ('tcp', 'http', 'https')
		ORDER BY created_at, id
	`, identity.NodeID)
	if err != nil {
		return Config{}, false, fmt.Errorf("query probe targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target ProbeTarget
		var port pgtype.Int4
		var targetPath pgtype.Text
		if err := rows.Scan(
			&target.ID,
			&target.Name,
			&target.Type,
			&target.Host,
			&port,
			&targetPath,
			&target.IntervalSeconds,
			&target.TimeoutSeconds,
			&target.RetentionSeconds,
			&target.Enabled,
			&target.ConfigVersion,
		); err != nil {
			return Config{}, false, fmt.Errorf("scan probe target: %w", err)
		}
		if port.Valid {
			value := port.Int32
			target.Port = &value
		}
		if targetPath.Valid {
			value := targetPath.String
			target.Path = &value
		}
		configuration.ProbeTargets = append(configuration.ProbeTargets, target)
	}
	if err := rows.Err(); err != nil {
		return Config{}, false, fmt.Errorf("iterate probe targets: %w", err)
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, false, fmt.Errorf("commit config transaction: %w", err)
	}
	return configuration, false, nil
}

func (s *Service) Report(ctx context.Context, identity Identity, request ReportRequest, sourceIP string) (ReportResponse, error) {
	if err := request.Validate(); err != nil {
		return ReportResponse{}, err
	}
	checksum, err := request.CanonicalChecksum()
	if err != nil {
		return ReportResponse{}, fmt.Errorf("canonicalize report: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReportResponse{}, fmt.Errorf("begin report transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var receivedAt time.Time
	var lastSequence int64
	var currentConfigVersion int64
	var configuredMountpoints []string
	err = tx.QueryRow(ctx, `
		SELECT CURRENT_TIMESTAMP,
		       n.last_accepted_sequence,
		       n.config_version,
		       s.mountpoints
		FROM nodes AS n
		JOIN node_agent_settings AS s ON s.node_id = n.id
		WHERE n.id = $1::uuid AND n.enabled
		FOR UPDATE OF n, s
	`, identity.NodeID).Scan(&receivedAt, &lastSequence, &currentConfigVersion, &configuredMountpoints)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportResponse{}, ErrUnauthorized
	}
	if err != nil {
		return ReportResponse{}, fmt.Errorf("lock report node: %w", err)
	}
	receivedAt = receivedAt.UTC()
	if err := revalidateToken(ctx, tx, identity); err != nil {
		return ReportResponse{}, err
	}

	var storedChecksum string
	var originalReceivedAt time.Time
	var originalClockStatus string
	err = tx.QueryRow(ctx, `
		SELECT payload_checksum, received_at, clock_status
		FROM processed_batches
		WHERE node_id = $1::uuid AND batch_id = $2::uuid
	`, identity.NodeID, request.BatchID).Scan(&storedChecksum, &originalReceivedAt, &originalClockStatus)
	if err == nil {
		if subtle.ConstantTimeCompare([]byte(storedChecksum), []byte(checksum)) != 1 {
			return ReportResponse{}, ErrIdempotencyKeyReused
		}
		return ReportResponse{
			BatchID:              request.BatchID,
			Status:               "duplicate",
			ReceivedAt:           originalReceivedAt.UTC(),
			ClockStatus:          originalClockStatus,
			CurrentConfigVersion: currentConfigVersion,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ReportResponse{}, fmt.Errorf("query processed batch: %w", err)
	}
	if request.Sequence <= lastSequence {
		return ReportResponse{}, ErrStaleSequence
	}
	configuredMounts := make(map[string]struct{}, len(configuredMountpoints))
	for _, mountpoint := range configuredMountpoints {
		configuredMounts[mountpoint] = struct{}{}
	}
	existingDiskRows, err := tx.Query(ctx, `SELECT mountpoint FROM node_disk_current WHERE node_id = $1::uuid`, identity.NodeID)
	if err != nil {
		return ReportResponse{}, fmt.Errorf("query existing node mountpoints: %w", err)
	}
	for existingDiskRows.Next() {
		var mountpoint string
		if err := existingDiskRows.Scan(&mountpoint); err != nil {
			existingDiskRows.Close()
			return ReportResponse{}, fmt.Errorf("scan existing node mountpoint: %w", err)
		}
		configuredMounts[mountpoint] = struct{}{}
	}
	if err := existingDiskRows.Err(); err != nil {
		existingDiskRows.Close()
		return ReportResponse{}, fmt.Errorf("iterate existing node mountpoints: %w", err)
	}
	existingDiskRows.Close()
	for index := range request.Disks {
		if _, ok := configuredMounts[request.Disks[index].Mountpoint]; !ok {
			return ReportResponse{}, &FieldError{Code: "mountpoint_not_configured", Field: fmt.Sprintf("disks[%d].mountpoint", index), Message: "mountpoint is not present in the node configuration"}
		}
	}

	targetTypes, err := loadTargetTypes(ctx, tx, identity.NodeID)
	if err != nil {
		return ReportResponse{}, err
	}
	for index := range request.ProbeResults {
		result := request.ProbeResults[index]
		targetType, ok := targetTypes[result.TargetID]
		if !ok {
			return ReportResponse{}, &FieldError{Code: "probe_target_not_configured", Field: fmt.Sprintf("probe_results[%d].target_id", index), Message: "target does not belong to the authenticated node"}
		}
		if err := validateConfiguredProbeResult(result, targetType, index); err != nil {
			return ReportResponse{}, err
		}
	}

	clockStatus, clockSkewSeconds := ClockStatus(request.AgentTime, receivedAt)
	if _, err := tx.Exec(ctx, `
		INSERT INTO processed_batches (
			node_id, batch_id, sequence, agent_time, agent_version,
			config_version, payload_checksum, clock_status, received_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9)
	`, identity.NodeID, request.BatchID, request.Sequence, request.AgentTime.UTC(), request.AgentVersion, request.ConfigVersion, checksum, clockStatus, receivedAt); err != nil {
		return ReportResponse{}, fmt.Errorf("insert processed batch: %w", err)
	}

	for index := range request.Metrics {
		sample := request.Metrics[index]
		effectiveAt := EffectiveTime(sample.SampledAt, request.AgentTime, receivedAt, clockStatus)
		if err := writeMetric(ctx, tx, identity.NodeID, sample, effectiveAt, receivedAt); err != nil {
			return ReportResponse{}, fmt.Errorf("write metric sample %d: %w", index, err)
		}
	}
	for index := range request.Disks {
		sample := request.Disks[index]
		effectiveAt := EffectiveTime(sample.SampledAt, request.AgentTime, receivedAt, clockStatus)
		if err := writeDisk(ctx, tx, identity.NodeID, sample, effectiveAt, receivedAt); err != nil {
			return ReportResponse{}, fmt.Errorf("write disk sample %d: %w", index, err)
		}
	}
	for index := range request.ProbeResults {
		sample := request.ProbeResults[index]
		effectiveAt := EffectiveTime(sample.SampledAt, request.AgentTime, receivedAt, clockStatus)
		if _, err := tx.Exec(ctx, `
			INSERT INTO probe_result_raw (
				node_id, target_id, batch_id, sample_index,
				sampled_at, effective_at, received_at,
				sent_count, received_count, latency_sum_us,
				latency_min_us, latency_max_us, http_status_code, error_code
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4,
				$5, $6, $7, $8, $9, $10, $11, $12, $13, $14
			)
		`, identity.NodeID, sample.TargetID, request.BatchID, index,
			sample.SampledAt.UTC(), effectiveAt, receivedAt,
			sample.SentCount, sample.ReceivedCount, sample.LatencySumUS,
			sample.LatencyMinUS.Value, sample.LatencyMaxUS.Value,
			sample.HTTPStatusCode.Value, sample.ErrorCode.Value,
		); err != nil {
			return ReportResponse{}, fmt.Errorf("insert probe result %d: %w", index, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE nodes
		SET last_received_at = $2,
		    last_agent_time = $3,
		    last_accepted_sequence = $4,
		    last_source_ip = NULLIF($5, '')::inet,
		    clock_status = $6,
		    clock_skew_seconds = $7,
		    agent_version = $8,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
	`, identity.NodeID, receivedAt, request.AgentTime.UTC(), request.Sequence, sourceIP, clockStatus, clockSkewSeconds, request.AgentVersion); err != nil {
		return ReportResponse{}, fmt.Errorf("advance node report state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ReportResponse{}, fmt.Errorf("commit report transaction: %w", err)
	}
	return ReportResponse{
		BatchID:              request.BatchID,
		Status:               "accepted",
		ReceivedAt:           receivedAt,
		ClockStatus:          clockStatus,
		CurrentConfigVersion: currentConfigVersion,
	}, nil
}

func validateConfiguredProbeResult(result ProbeResult, targetType string, index int) error {
	prefix := fmt.Sprintf("probe_results[%d]", index)
	if result.SentCount != 1 || (result.ReceivedCount != 0 && result.ReceivedCount != 1) {
		return &FieldError{Code: "invalid_probe_count", Field: prefix + ".sent_count", Message: "TCP and HTTP(S) targets require one probe attempt per result"}
	}
	hasHTTPStatus := result.HTTPStatusCode.Value != nil
	switch targetType {
	case "tcp":
		if hasHTTPStatus {
			return &FieldError{Code: "invalid_http_status", Field: prefix + ".http_status_code", Message: "TCP targets require a null HTTP status"}
		}
	case "http", "https":
		if result.ReceivedCount == 1 && !hasHTTPStatus {
			return &FieldError{Code: "invalid_http_status", Field: prefix + ".http_status_code", Message: "received HTTP responses require a status code"}
		}
		if result.ReceivedCount == 0 && hasHTTPStatus {
			return &FieldError{Code: "invalid_http_status", Field: prefix + ".http_status_code", Message: "unreceived HTTP responses require a null status code"}
		}
	default:
		return &FieldError{Code: "probe_target_not_configured", Field: prefix + ".target_id", Message: "target type is not enabled"}
	}
	hasErrorCode := result.ErrorCode.Value != nil
	if result.ReceivedCount == 0 && !hasErrorCode {
		return &FieldError{Code: "invalid_probe_error", Field: prefix + ".error_code", Message: "unreceived probes require a stable error code"}
	}
	if result.ReceivedCount == 1 && hasErrorCode {
		return &FieldError{Code: "invalid_probe_error", Field: prefix + ".error_code", Message: "received probes require a null error code"}
	}
	return nil
}

func revalidateToken(ctx context.Context, tx pgx.Tx, identity Identity) error {
	var valid bool
	err := tx.QueryRow(ctx, `
		SELECT TRUE
		FROM agent_tokens
		WHERE id = $1::uuid
		  AND node_id = $2::uuid
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		FOR SHARE
	`, identity.TokenID, identity.NodeID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("revalidate Agent token: %w", err)
	}
	if !valid {
		return ErrUnauthorized
	}
	return nil
}

func loadTargetTypes(ctx context.Context, tx pgx.Tx, nodeID string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, probe_type
		FROM probe_targets
		WHERE node_id = $1::uuid
		  AND probe_type IN ('tcp', 'http', 'https')
	`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("query node probe targets: %w", err)
	}
	defer rows.Close()
	targets := make(map[string]string)
	for rows.Next() {
		var id, probeType string
		if err := rows.Scan(&id, &probeType); err != nil {
			return nil, fmt.Errorf("scan node probe target: %w", err)
		}
		targets[id] = probeType
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node probe targets: %w", err)
	}
	return targets, nil
}

func writeMetric(ctx context.Context, tx pgx.Tx, nodeID string, sample MetricSample, effectiveAt, receivedAt time.Time) error {
	arguments := []any{
		nodeID, sample.SampledAt.UTC(), effectiveAt, receivedAt,
		sample.CPUPercent, sample.Load1, sample.Load5, sample.Load15, sample.UptimeSeconds,
		sample.MemoryTotalBytes, sample.MemoryUsedBytes, sample.MemoryAvailableBytes,
		sample.SwapTotalBytes, sample.SwapUsedBytes,
		sample.NetworkRXBPS, sample.NetworkTXBPS, sample.NetworkRXBytes, sample.NetworkTXBytes,
	}
	if _, err := tx.Exec(ctx, metricCurrentSQL, arguments...); err != nil {
		return err
	}
	slot := ringSlot(effectiveAt)
	ringArguments := append([]any{nodeID, slot}, arguments[1:]...)
	_, err := tx.Exec(ctx, metricRingSQL, ringArguments...)
	return err
}

func writeDisk(ctx context.Context, tx pgx.Tx, nodeID string, sample DiskSample, effectiveAt, receivedAt time.Time) error {
	arguments := []any{nodeID, sample.Mountpoint, sample.SampledAt.UTC(), effectiveAt, receivedAt, sample.TotalBytes, sample.UsedBytes, sample.AvailableBytes}
	if _, err := tx.Exec(ctx, diskCurrentSQL, arguments...); err != nil {
		return err
	}
	slot := ringSlot(effectiveAt)
	ringArguments := []any{nodeID, sample.Mountpoint, slot, sample.SampledAt.UTC(), effectiveAt, receivedAt, sample.TotalBytes, sample.UsedBytes, sample.AvailableBytes}
	_, err := tx.Exec(ctx, diskRingSQL, ringArguments...)
	return err
}

func ringSlot(value time.Time) int16 {
	seconds := value.Unix()
	epoch5Seconds := seconds / 5
	if seconds%5 < 0 {
		epoch5Seconds--
	}
	slot := epoch5Seconds % 60
	if slot < 0 {
		slot += 60
	}
	return int16(slot)
}

const metricCurrentSQL = `
	INSERT INTO node_metric_current (
		node_id, sampled_at, effective_at, received_at,
		cpu_percent, load_1, load_5, load_15, uptime_seconds,
		memory_total_bytes, memory_used_bytes, memory_available_bytes,
		swap_total_bytes, swap_used_bytes,
		network_rx_bps, network_tx_bps, network_rx_bytes, network_tx_bytes
	) VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
	ON CONFLICT (node_id) DO UPDATE SET
		sampled_at=EXCLUDED.sampled_at, effective_at=EXCLUDED.effective_at, received_at=EXCLUDED.received_at,
		cpu_percent=EXCLUDED.cpu_percent, load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5,
		load_15=EXCLUDED.load_15, uptime_seconds=EXCLUDED.uptime_seconds,
		memory_total_bytes=EXCLUDED.memory_total_bytes, memory_used_bytes=EXCLUDED.memory_used_bytes,
		memory_available_bytes=EXCLUDED.memory_available_bytes, swap_total_bytes=EXCLUDED.swap_total_bytes,
		swap_used_bytes=EXCLUDED.swap_used_bytes, network_rx_bps=EXCLUDED.network_rx_bps,
		network_tx_bps=EXCLUDED.network_tx_bps, network_rx_bytes=EXCLUDED.network_rx_bytes,
		network_tx_bytes=EXCLUDED.network_tx_bytes
	WHERE EXCLUDED.effective_at > node_metric_current.effective_at
	   OR (EXCLUDED.effective_at = node_metric_current.effective_at AND EXCLUDED.received_at > node_metric_current.received_at)
`

const metricRingSQL = `
	INSERT INTO node_metric_ring (
		node_id, slot, sampled_at, effective_at, received_at,
		cpu_percent, load_1, load_5, load_15, uptime_seconds,
		memory_total_bytes, memory_used_bytes, memory_available_bytes,
		swap_total_bytes, swap_used_bytes,
		network_rx_bps, network_tx_bps, network_rx_bytes, network_tx_bytes
	) VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
	ON CONFLICT (node_id, slot) DO UPDATE SET
		sampled_at=EXCLUDED.sampled_at, effective_at=EXCLUDED.effective_at, received_at=EXCLUDED.received_at,
		cpu_percent=EXCLUDED.cpu_percent, load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5,
		load_15=EXCLUDED.load_15, uptime_seconds=EXCLUDED.uptime_seconds,
		memory_total_bytes=EXCLUDED.memory_total_bytes, memory_used_bytes=EXCLUDED.memory_used_bytes,
		memory_available_bytes=EXCLUDED.memory_available_bytes, swap_total_bytes=EXCLUDED.swap_total_bytes,
		swap_used_bytes=EXCLUDED.swap_used_bytes, network_rx_bps=EXCLUDED.network_rx_bps,
		network_tx_bps=EXCLUDED.network_tx_bps, network_rx_bytes=EXCLUDED.network_rx_bytes,
		network_tx_bytes=EXCLUDED.network_tx_bytes
	WHERE EXCLUDED.effective_at > node_metric_ring.effective_at
	   OR (EXCLUDED.effective_at = node_metric_ring.effective_at AND EXCLUDED.received_at > node_metric_ring.received_at)
`

const diskCurrentSQL = `
	INSERT INTO node_disk_current (node_id,mountpoint,sampled_at,effective_at,received_at,total_bytes,used_bytes,available_bytes)
	VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8)
	ON CONFLICT (node_id,mountpoint) DO UPDATE SET
		sampled_at=EXCLUDED.sampled_at, effective_at=EXCLUDED.effective_at, received_at=EXCLUDED.received_at,
		total_bytes=EXCLUDED.total_bytes, used_bytes=EXCLUDED.used_bytes, available_bytes=EXCLUDED.available_bytes
	WHERE EXCLUDED.effective_at > node_disk_current.effective_at
	   OR (EXCLUDED.effective_at = node_disk_current.effective_at AND EXCLUDED.received_at > node_disk_current.received_at)
`

const diskRingSQL = `
	INSERT INTO node_disk_ring (node_id,mountpoint,slot,sampled_at,effective_at,received_at,total_bytes,used_bytes,available_bytes)
	VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9)
	ON CONFLICT (node_id,mountpoint,slot) DO UPDATE SET
		sampled_at=EXCLUDED.sampled_at, effective_at=EXCLUDED.effective_at, received_at=EXCLUDED.received_at,
		total_bytes=EXCLUDED.total_bytes, used_bytes=EXCLUDED.used_bytes, available_bytes=EXCLUDED.available_bytes
	WHERE EXCLUDED.effective_at > node_disk_ring.effective_at
	   OR (EXCLUDED.effective_at = node_disk_ring.effective_at AND EXCLUDED.received_at > node_disk_ring.received_at)
`
