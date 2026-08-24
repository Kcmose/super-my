package nodemanagement

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/agent"
	"probe-api/internal/auditlog"
	"probe-api/internal/auth"
	"probe-api/internal/panel"
)

type Service struct {
	pool         *pgxpool.Pool
	offlineAfter time.Duration
}

func NewService(pool *pgxpool.Pool, offlineAfter time.Duration) (*Service, error) {
	if pool == nil || offlineAfter <= 0 || offlineAfter > 24*time.Hour {
		return nil, errors.New("node management configuration is invalid")
	}
	return &Service{pool: pool, offlineAfter: offlineAfter}, nil
}

func (service *Service) Create(ctx context.Context, actor auth.Identity, request CreateRequest, metadata Metadata) (Node, error) {
	if err := authorize(actor); err != nil {
		return Node{}, err
	}
	if err := validateNodeValues(request.DisplayName, request.CountryCode, request.RegionKey, request.Location); err != nil {
		return Node{}, err
	}
	settings := DefaultAgentSettings()
	if request.AgentSettings != nil {
		settings = cloneSettings(*request.AgentSettings)
	}
	if err := validateSettings(settings); err != nil {
		return Node{}, err
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	nodeID, err := newUUID()
	if err != nil {
		return Node{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Node{}, fmt.Errorf("begin node creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return Node{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO nodes (id, display_name, enabled, country_code, region_key, location, created_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid)
	`, nodeID, request.DisplayName, enabled, request.CountryCode, request.RegionKey, request.Location, actor.User.ID)
	if err != nil {
		return Node{}, classifyWriteError(err)
	}
	if err := insertSettings(ctx, tx, nodeID, settings); err != nil {
		return Node{}, classifyWriteError(err)
	}
	node, err := service.loadNode(ctx, tx, nodeID, false)
	if err != nil {
		return Node{}, err
	}
	if err := auditlog.Write(ctx, tx, actor, metadata, "node.create", "node", nodeID, nil, nodeAudit(node)); err != nil {
		return Node{}, mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Node{}, fmt.Errorf("commit node creation: %w", err)
	}
	return node, nil
}

func (service *Service) Update(ctx context.Context, actor auth.Identity, nodeID string, request UpdateRequest, metadata Metadata) (Node, error) {
	if err := authorize(actor); err != nil {
		return Node{}, err
	}
	if !ValidUUID(nodeID) {
		return Node{}, invalidField("node_id", "must be a canonical lowercase UUID")
	}
	if request.DisplayName == nil && request.Enabled == nil && !request.CountryCode.Set && !request.RegionKey.Set && !request.Location.Set && request.AgentSettings == nil {
		return Node{}, invalidField("request", "at least one field is required")
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Node{}, fmt.Errorf("begin node update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return Node{}, err
	}
	current, err := service.loadNode(ctx, tx, nodeID, true)
	if err != nil {
		return Node{}, err
	}
	displayName := current.DisplayName
	enabled := current.Enabled
	countryCode := cloneString(current.CountryCode)
	regionKey := cloneString(current.RegionKey)
	location := cloneString(current.Location)
	settings := cloneSettings(current.AgentSettings)
	if request.DisplayName != nil {
		displayName = *request.DisplayName
	}
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	if request.CountryCode.Set {
		countryCode = cloneString(request.CountryCode.Value)
	}
	if request.RegionKey.Set {
		regionKey = cloneString(request.RegionKey.Value)
	}
	if request.Location.Set {
		location = cloneString(request.Location.Value)
	}
	if request.AgentSettings != nil {
		settings = cloneSettings(*request.AgentSettings)
	}
	if err := validateNodeValues(displayName, countryCode, regionKey, location); err != nil {
		return Node{}, err
	}
	if err := validateSettings(settings); err != nil {
		return Node{}, err
	}
	settingsChanged := !reflect.DeepEqual(current.AgentSettings, settings)
	if settingsChanged && current.ConfigVersion == math.MaxInt64 {
		return Node{}, ErrConflict
	}
	nextVersion := current.ConfigVersion
	if settingsChanged {
		nextVersion++
	}
	_, err = tx.Exec(ctx, `
		UPDATE nodes
		SET display_name = $2,
		    enabled = $3,
		    country_code = $4,
		    region_key = $5,
		    location = $6,
		    config_version = $7,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
	`, nodeID, displayName, enabled, countryCode, regionKey, location, nextVersion)
	if err != nil {
		return Node{}, classifyWriteError(err)
	}
	if settingsChanged {
		if err := updateSettings(ctx, tx, nodeID, settings); err != nil {
			return Node{}, classifyWriteError(err)
		}
	}
	updated, err := service.loadNode(ctx, tx, nodeID, false)
	if err != nil {
		return Node{}, err
	}
	if err := auditlog.Write(ctx, tx, actor, metadata, "node.update", "node", nodeID, nodeAudit(current), nodeAudit(updated)); err != nil {
		return Node{}, mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Node{}, fmt.Errorf("commit node update: %w", err)
	}
	return updated, nil
}

func (service *Service) Delete(ctx context.Context, actor auth.Identity, nodeID string, metadata Metadata) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if !ValidUUID(nodeID) {
		return invalidField("node_id", "must be a canonical lowercase UUID")
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin node deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return err
	}
	current, err := service.loadNode(ctx, tx, nodeID, true)
	if err != nil {
		return err
	}
	after := map[string]any{"node_id": nodeID, "deleted": true}
	if _, err := tx.Exec(ctx, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID); err != nil {
		return classifyWriteError(err)
	}
	if err := auditlog.Write(ctx, tx, actor, metadata, "node.delete", "node", nodeID, nodeAudit(current), after); err != nil {
		return mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit node deletion: %w", err)
	}
	return nil
}

func (service *Service) CreateEnrollmentToken(ctx context.Context, actor auth.Identity, nodeID string, request CreateEnrollmentTokenRequest, metadata Metadata) (EnrollmentTokenResponse, error) {
	if err := authorize(actor); err != nil {
		return EnrollmentTokenResponse{}, err
	}
	if !ValidUUID(nodeID) {
		return EnrollmentTokenResponse{}, invalidField("node_id", "must be a canonical lowercase UUID")
	}
	if request.ExpiresInSeconds == 0 {
		request.ExpiresInSeconds = 900
	}
	if request.ExpiresInSeconds < 60 || request.ExpiresInSeconds > 86400 {
		return EnrollmentTokenResponse{}, invalidField("expires_in_seconds", "must be between 60 and 86400")
	}
	tokenID, plaintext, tokenHash, err := newEnrollmentToken()
	if err != nil {
		return EnrollmentTokenResponse{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EnrollmentTokenResponse{}, fmt.Errorf("begin enrollment token creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return EnrollmentTokenResponse{}, err
	}
	if err := lockNode(ctx, tx, nodeID); err != nil {
		return EnrollmentTokenResponse{}, err
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM nodes WHERE id = $1::uuid`, nodeID).Scan(&enabled); err != nil {
		return EnrollmentTokenResponse{}, classifyWriteError(err)
	}
	if !enabled {
		return EnrollmentTokenResponse{}, ErrConflict
	}
	replacedTokens, err := tx.Exec(ctx, `
		UPDATE enrollment_tokens
		SET used_at = CURRENT_TIMESTAMP,
		    used_source_ip = NULL
		WHERE node_id = $1::uuid AND used_at IS NULL
	`, nodeID)
	if err != nil {
		return EnrollmentTokenResponse{}, fmt.Errorf("invalidate previous enrollment tokens: %w", err)
	}
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO enrollment_tokens (id, node_id, token_hash, created_by, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, CURRENT_TIMESTAMP + make_interval(secs => $5))
		RETURNING expires_at
	`, tokenID, nodeID, tokenHash, actor.User.ID, request.ExpiresInSeconds).Scan(&expiresAt)
	if err != nil {
		return EnrollmentTokenResponse{}, classifyWriteError(err)
	}
	response := EnrollmentTokenResponse{NodeID: nodeID, EnrollmentToken: plaintext, ExpiresAt: expiresAt.UTC()}
	after := map[string]any{
		"node_id": nodeID, "expires_at": response.ExpiresAt,
		"invalidated_previous_tokens": replacedTokens.RowsAffected(),
	}
	if err := auditlog.Write(ctx, tx, actor, metadata, "enrollment_token.create", "node", nodeID, nil, after); err != nil {
		return EnrollmentTokenResponse{}, mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return EnrollmentTokenResponse{}, fmt.Errorf("commit enrollment token creation: %w", err)
	}
	return response, nil
}

func (service *Service) RotateAgentToken(ctx context.Context, actor auth.Identity, nodeID string, metadata Metadata) (AgentTokenResponse, error) {
	if err := authorize(actor); err != nil {
		return AgentTokenResponse{}, err
	}
	if !ValidUUID(nodeID) {
		return AgentTokenResponse{}, invalidField("node_id", "must be a canonical lowercase UUID")
	}
	tokenID, plaintext, tokenHash, err := agent.NewAgentToken()
	if err != nil {
		return AgentTokenResponse{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentTokenResponse{}, fmt.Errorf("begin Agent token rotation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return AgentTokenResponse{}, err
	}
	if err := lockNode(ctx, tx, nodeID); err != nil {
		return AgentTokenResponse{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE node_id = $1::uuid AND revoked_at IS NULL`, nodeID)
	if err != nil {
		return AgentTokenResponse{}, fmt.Errorf("revoke previous Agent tokens: %w", err)
	}
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO agent_tokens (id, node_id, token_hash, label, created_by)
		VALUES ($1::uuid, $2::uuid, $3, 'rotation', $4::uuid)
		RETURNING created_at
	`, tokenID, nodeID, tokenHash, actor.User.ID).Scan(&createdAt)
	if err != nil {
		return AgentTokenResponse{}, classifyWriteError(err)
	}
	response := AgentTokenResponse{NodeID: nodeID, AgentToken: plaintext, CreatedAt: createdAt.UTC()}
	after := map[string]any{"node_id": nodeID, "token_id": tokenID, "revoked_token_count": tag.RowsAffected(), "created_at": response.CreatedAt}
	if err := auditlog.Write(ctx, tx, actor, metadata, "agent_token.rotate", "node", nodeID, nil, after); err != nil {
		return AgentTokenResponse{}, mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentTokenResponse{}, fmt.Errorf("commit Agent token rotation: %w", err)
	}
	return response, nil
}

func (service *Service) RevokeAgentTokens(ctx context.Context, actor auth.Identity, nodeID string, metadata Metadata) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if !ValidUUID(nodeID) {
		return invalidField("node_id", "must be a canonical lowercase UUID")
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin Agent token revocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return err
	}
	if err := lockNode(ctx, tx, nodeID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE node_id = $1::uuid AND revoked_at IS NULL`, nodeID)
	if err != nil {
		return fmt.Errorf("revoke Agent tokens: %w", err)
	}
	after := map[string]any{"node_id": nodeID, "revoked_token_count": tag.RowsAffected()}
	if err := auditlog.Write(ctx, tx, actor, metadata, "agent_token.revoke", "node", nodeID, nil, after); err != nil {
		return mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Agent token revocation: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func (service *Service) loadNode(ctx context.Context, tx pgx.Tx, nodeID string, forUpdate bool) (Node, error) {
	query := `
		SELECT n.id::text, n.display_name, n.hostname, n.enabled, n.config_version,
		       n.agent_version, n.operating_system, n.architecture,
		       n.country_code, n.region_key, n.location,
		       n.enrolled_at, n.last_received_at, n.clock_status, n.clock_skew_seconds,
		       n.created_at, n.updated_at,
		       s.collect_interval_seconds, s.report_interval_seconds, s.mountpoints,
		       s.include_virtual_interfaces, s.config_refresh_interval_seconds,
		       s.max_memory_queue_seconds, s.max_batch_samples,
		       m.sampled_at, m.effective_at, m.received_at,
		       m.cpu_percent::double precision, m.load_1::double precision,
		       m.load_5::double precision, m.load_15::double precision,
		       m.uptime_seconds::double precision, m.memory_total_bytes,
		       m.memory_used_bytes, m.memory_available_bytes,
		       m.swap_total_bytes, m.swap_used_bytes,
		       m.network_rx_bps::double precision, m.network_tx_bps::double precision,
		       m.network_rx_bytes, m.network_tx_bytes,
		       d.sampled_at, d.effective_at, d.received_at, d.mountpoint,
		       d.total_bytes, d.used_bytes, d.available_bytes,
		       CURRENT_TIMESTAMP
		FROM nodes AS n
		JOIN node_agent_settings AS s ON s.node_id = n.id
		LEFT JOIN node_metric_current AS m ON m.node_id = n.id
		LEFT JOIN node_disk_current AS d ON d.node_id = n.id AND d.mountpoint = '/'
		WHERE n.id = $1::uuid`
	if forUpdate {
		query += ` FOR UPDATE OF n, s`
	}
	node, err := service.scanNode(tx.QueryRow(ctx, query, nodeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("load managed node: %w", err)
	}
	return node, nil
}

func (service *Service) scanNode(row rowScanner) (Node, error) {
	var node Node
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
	var asOf time.Time
	err := row.Scan(&node.NodeID, &node.DisplayName, &hostname, &node.Enabled, &node.ConfigVersion,
		&agentVersion, &operatingSystem, &architecture, &countryCode, &regionKey, &location,
		&enrolledAt, &lastReceivedAt, &node.ClockStatus, &clockSkew,
		&node.CreatedAt, &node.UpdatedAt,
		&node.AgentSettings.Metrics.CollectIntervalSeconds,
		&node.AgentSettings.Metrics.ReportIntervalSeconds,
		&node.AgentSettings.Metrics.Mountpoints,
		&node.AgentSettings.Metrics.IncludeVirtualInterfaces,
		&node.AgentSettings.Agent.ConfigRefreshIntervalSeconds,
		&node.AgentSettings.Agent.MaxMemoryQueueSeconds,
		&node.AgentSettings.Limits.MaxBatchSamples,
		&currentSampled, &currentEffective, &currentReceived,
		&currentCPU, &currentLoad1, &currentLoad5, &currentLoad15, &currentUptime,
		&currentMemoryTotal, &currentMemoryUsed, &currentMemoryAvailable,
		&currentSwapTotal, &currentSwapUsed, &currentRXBPS, &currentTXBPS,
		&currentRXBytes, &currentTXBytes,
		&rootSampled, &rootEffective, &rootReceived, &rootMountpoint,
		&rootTotal, &rootUsed, &rootAvailable,
		&asOf)
	if err != nil {
		return Node{}, err
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
	if clockSkew.Valid {
		value := clockSkew.Int64
		node.ClockSkewSeconds = &value
	}
	node.Status = panel.StatusAt(node.Enabled, node.EnrolledAt, node.LastReceivedAt, node.ClockStatus, asOf.UTC(), service.offlineAfter)
	node.CreatedAt = node.CreatedAt.UTC()
	node.UpdatedAt = node.UpdatedAt.UTC()
	if currentSampled.Valid {
		if !currentEffective.Valid || !currentReceived.Valid || !currentCPU.Valid ||
			!currentLoad1.Valid || !currentLoad5.Valid || !currentLoad15.Valid || !currentUptime.Valid ||
			!currentMemoryTotal.Valid || !currentMemoryUsed.Valid || !currentMemoryAvailable.Valid ||
			!currentSwapTotal.Valid || !currentSwapUsed.Valid || !currentRXBPS.Valid || !currentTXBPS.Valid ||
			!currentRXBytes.Valid || !currentTXBytes.Valid {
			return Node{}, errors.New("managed node current metrics are incomplete")
		}
		node.CurrentMetrics = &panel.MetricPoint{
			SampledAt: currentSampled.Time.UTC(), EffectiveAt: currentEffective.Time.UTC(), ReceivedAt: currentReceived.Time.UTC(),
			CPUPercent: currentCPU.Float64, Load1: currentLoad1.Float64, Load5: currentLoad5.Float64,
			Load15: currentLoad15.Float64, UptimeSeconds: currentUptime.Float64,
			MemoryTotalBytes: currentMemoryTotal.Int64, MemoryUsedBytes: currentMemoryUsed.Int64,
			MemoryAvailableBytes: currentMemoryAvailable.Int64, SwapTotalBytes: currentSwapTotal.Int64,
			SwapUsedBytes: currentSwapUsed.Int64, NetworkRXBPS: currentRXBPS.Float64,
			NetworkTXBPS: currentTXBPS.Float64, NetworkRXBytes: currentRXBytes.Int64,
			NetworkTXBytes:    currentTXBytes.Int64,
			TotalTrafficBytes: uint64(currentRXBytes.Int64) + uint64(currentTXBytes.Int64),
		}
	}
	if rootSampled.Valid {
		if !rootEffective.Valid || !rootReceived.Valid || !rootMountpoint.Valid ||
			!rootTotal.Valid || !rootUsed.Valid || !rootAvailable.Valid || rootMountpoint.String != "/" {
			return Node{}, errors.New("managed node root disk snapshot is incomplete")
		}
		node.RootDisk = &panel.DiskPoint{
			SampledAt: rootSampled.Time.UTC(), EffectiveAt: rootEffective.Time.UTC(), ReceivedAt: rootReceived.Time.UTC(),
			Mountpoint: rootMountpoint.String, TotalBytes: rootTotal.Int64,
			UsedBytes: rootUsed.Int64, AvailableBytes: rootAvailable.Int64,
		}
	}
	return node, nil
}

func insertSettings(ctx context.Context, tx pgx.Tx, nodeID string, settings AgentSettings) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO node_agent_settings (
			node_id, collect_interval_seconds, report_interval_seconds, mountpoints,
			include_virtual_interfaces, config_refresh_interval_seconds,
			max_memory_queue_seconds, max_batch_samples
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
	`, nodeID, settings.Metrics.CollectIntervalSeconds, settings.Metrics.ReportIntervalSeconds,
		settings.Metrics.Mountpoints, settings.Metrics.IncludeVirtualInterfaces,
		settings.Agent.ConfigRefreshIntervalSeconds, settings.Agent.MaxMemoryQueueSeconds,
		settings.Limits.MaxBatchSamples)
	if err != nil {
		return fmt.Errorf("insert node Agent settings: %w", err)
	}
	return nil
}

func updateSettings(ctx context.Context, tx pgx.Tx, nodeID string, settings AgentSettings) error {
	_, err := tx.Exec(ctx, `
		UPDATE node_agent_settings
		SET collect_interval_seconds = $2,
		    report_interval_seconds = $3,
		    mountpoints = $4,
		    include_virtual_interfaces = $5,
		    config_refresh_interval_seconds = $6,
		    max_memory_queue_seconds = $7,
		    max_batch_samples = $8,
		    updated_at = CURRENT_TIMESTAMP
		WHERE node_id = $1::uuid
	`, nodeID, settings.Metrics.CollectIntervalSeconds, settings.Metrics.ReportIntervalSeconds,
		settings.Metrics.Mountpoints, settings.Metrics.IncludeVirtualInterfaces,
		settings.Agent.ConfigRefreshIntervalSeconds, settings.Agent.MaxMemoryQueueSeconds,
		settings.Limits.MaxBatchSamples)
	if err != nil {
		return fmt.Errorf("update node Agent settings: %w", err)
	}
	return nil
}

func lockNode(ctx context.Context, tx pgx.Tx, nodeID string) error {
	var ignored string
	err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE id = $1::uuid FOR UPDATE`, nodeID).Scan(&ignored)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock node: %w", err)
	}
	return nil
}

func authorize(actor auth.Identity) error {
	if !actor.User.Enabled || !actor.IsAdmin() || !ValidUUID(actor.User.ID) || actor.User.Username == "" {
		return ErrForbidden
	}
	return nil
}

func assertAdmin(ctx context.Context, tx pgx.Tx, actor auth.Identity) error {
	if err := auditlog.AssertCurrentAdmin(ctx, tx, actor); err != nil {
		return mapAuditError(err)
	}
	return nil
}

func mapAuditError(err error) error {
	if errors.Is(err, auditlog.ErrForbidden) {
		return ErrForbidden
	}
	var fieldError *auditlog.FieldError
	if errors.As(err, &fieldError) {
		return &FieldError{Code: fieldError.Code, Field: fieldError.Field, Message: fieldError.Message}
	}
	return err
}

func classifyWriteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return ErrConflict
		case "23503", "23514", "22P02":
			return ErrInvalidRequest
		}
	}
	return err
}

func newEnrollmentToken() (string, string, string, error) {
	tokenID, err := newUUID()
	if err != nil {
		return "", "", "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", "", errors.New("generate enrollment token")
	}
	defer clear(secret)
	plaintext := "enroll.v1." + base64.RawURLEncoding.EncodeToString(secret)
	return tokenID, plaintext, agent.HashOpaqueToken(plaintext), nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate node management UUID")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func cloneSettings(settings AgentSettings) AgentSettings {
	settings.Metrics.Mountpoints = append([]string(nil), settings.Metrics.Mountpoints...)
	return settings
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
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

func nodeAudit(node Node) map[string]any {
	return map[string]any{
		"node_id": node.NodeID, "display_name": node.DisplayName, "enabled": node.Enabled,
		"country_code": node.CountryCode, "region_key": node.RegionKey, "location": node.Location,
		"config_version": node.ConfigVersion, "agent_settings": node.AgentSettings,
	}
}
