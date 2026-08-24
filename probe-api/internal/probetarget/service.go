package probetarget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/auth"
)

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func (service *Service) List(ctx context.Context, actor auth.Identity, request ListRequest) (ListResponse, error) {
	if err := authorize(actor); err != nil {
		return ListResponse{}, err
	}
	if request.Limit == 0 {
		request.Limit = DefaultListLimit
	}
	if request.Limit < 1 || request.Limit > MaxListLimit {
		return ListResponse{}, invalidField("limit", "must be between 1 and 200")
	}
	nodeID := ""
	if request.NodeID != nil {
		if !ValidUUID(*request.NodeID) {
			return ListResponse{}, invalidField("node_id", "must be a canonical lowercase UUID")
		}
		nodeID = *request.NodeID
	}
	var cursorTime any
	var cursorID any
	if request.Cursor != nil {
		if request.Cursor.CreatedAt.IsZero() || !ValidUUID(request.Cursor.TargetID) {
			return ListResponse{}, ErrInvalidCursor
		}
		cursorTime = request.Cursor.CreatedAt.UTC()
		cursorID = request.Cursor.TargetID
	}
	rows, err := service.pool.Query(ctx, `
		SELECT id::text, node_id::text, name, probe_type, host, port, path,
		       interval_seconds, timeout_seconds, retention_seconds, enabled,
		       config_version, created_at, updated_at
		FROM probe_targets
		WHERE ($1::text = '' OR node_id = NULLIF($1, '')::uuid)
		  AND probe_type IN ('tcp', 'http', 'https')
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2::timestamptz, $3::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, nodeID, cursorTime, cursorID, request.Limit+1)
	if err != nil {
		return ListResponse{}, fmt.Errorf("query probe targets: %w", err)
	}
	defer rows.Close()
	response := ListResponse{Targets: make([]Target, 0, request.Limit)}
	for rows.Next() {
		target, err := scanTarget(rows)
		if err != nil {
			return ListResponse{}, fmt.Errorf("scan probe target: %w", err)
		}
		response.Targets = append(response.Targets, target)
	}
	if err := rows.Err(); err != nil {
		return ListResponse{}, fmt.Errorf("iterate probe targets: %w", err)
	}
	if len(response.Targets) > request.Limit {
		response.Targets = response.Targets[:request.Limit]
		last := response.Targets[len(response.Targets)-1]
		cursor, err := EncodeCursor(Cursor{CreatedAt: last.CreatedAt, TargetID: last.TargetID})
		if err != nil {
			return ListResponse{}, fmt.Errorf("encode probe target cursor: %w", err)
		}
		response.NextCursor = &cursor
	}
	return response, nil
}

func (service *Service) Create(ctx context.Context, actor auth.Identity, request CreateRequest, metadata Metadata) (Target, error) {
	if err := authorize(actor); err != nil {
		return Target{}, err
	}
	metadata, err := normalizeMetadata(metadata)
	if err != nil {
		return Target{}, err
	}
	values, err := request.normalized()
	if err != nil {
		return Target{}, err
	}
	targetID, err := newUUID()
	if err != nil {
		return Target{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Target{}, fmt.Errorf("begin probe target creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	nodeVersion, err := lockNodeVersion(ctx, tx, request.NodeID)
	if err != nil {
		return Target{}, err
	}
	if nodeVersion == math.MaxInt64 {
		return Target{}, ErrConflict
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM probe_targets WHERE node_id = $1::uuid`, request.NodeID).Scan(&count); err != nil {
		return Target{}, fmt.Errorf("count node probe targets: %w", err)
	}
	if count >= MaxTargetsPerNode {
		return Target{}, ErrLimitExceeded
	}

	target, err := insertTarget(ctx, tx, targetID, request.NodeID, actor.User.ID, values)
	if err != nil {
		return Target{}, classifyWriteError(err)
	}
	if err := advanceNodeVersion(ctx, tx, request.NodeID); err != nil {
		return Target{}, err
	}
	after, err := target.auditJSON()
	if err != nil {
		return Target{}, err
	}
	if err := writeAudit(ctx, tx, actor, metadata, "probe_target.create", target.TargetID, nil, after); err != nil {
		return Target{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Target{}, fmt.Errorf("commit probe target creation: %w", err)
	}
	return target, nil
}

func (service *Service) Update(ctx context.Context, actor auth.Identity, targetID string, request UpdateRequest, metadata Metadata) (Target, error) {
	if err := authorize(actor); err != nil {
		return Target{}, err
	}
	if !ValidUUID(targetID) {
		return Target{}, invalidField("target_id", "must be a canonical lowercase UUID")
	}
	metadata, err := normalizeMetadata(metadata)
	if err != nil {
		return Target{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Target{}, fmt.Errorf("begin probe target update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	nodeID, err := findTargetNode(ctx, tx, targetID)
	if err != nil {
		return Target{}, err
	}
	nodeVersion, err := lockNodeVersion(ctx, tx, nodeID)
	if err != nil {
		return Target{}, err
	}
	current, err := lockTarget(ctx, tx, targetID)
	if err != nil {
		return Target{}, err
	}
	if current.ConfigVersion == math.MaxInt64 || nodeVersion == math.MaxInt64 {
		return Target{}, ErrConflict
	}
	values, err := mergeUpdate(current, request)
	if err != nil {
		return Target{}, err
	}
	before, err := current.auditJSON()
	if err != nil {
		return Target{}, err
	}
	updated, err := updateTarget(ctx, tx, targetID, values)
	if err != nil {
		return Target{}, classifyWriteError(err)
	}
	if err := advanceNodeVersion(ctx, tx, nodeID); err != nil {
		return Target{}, err
	}
	after, err := updated.auditJSON()
	if err != nil {
		return Target{}, err
	}
	if err := writeAudit(ctx, tx, actor, metadata, "probe_target.update", targetID, before, after); err != nil {
		return Target{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Target{}, fmt.Errorf("commit probe target update: %w", err)
	}
	return updated, nil
}

func (service *Service) Delete(ctx context.Context, actor auth.Identity, targetID string, metadata Metadata) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if !ValidUUID(targetID) {
		return invalidField("target_id", "must be a canonical lowercase UUID")
	}
	metadata, err := normalizeMetadata(metadata)
	if err != nil {
		return err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin probe target deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	nodeID, err := findTargetNode(ctx, tx, targetID)
	if err != nil {
		return err
	}
	nodeVersion, err := lockNodeVersion(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	current, err := lockTarget(ctx, tx, targetID)
	if err != nil {
		return err
	}
	if current.ConfigVersion == math.MaxInt64 || nodeVersion == math.MaxInt64 {
		return ErrConflict
	}
	before, err := current.auditJSON()
	if err != nil {
		return err
	}
	tombstone, err := bumpTargetVersion(ctx, tx, targetID)
	if err != nil {
		return classifyWriteError(err)
	}
	if err := advanceNodeVersion(ctx, tx, nodeID); err != nil {
		return err
	}
	after, err := deletedAuditJSON(tombstone)
	if err != nil {
		return err
	}
	// The immutable audit entry is inserted before the hard delete in this same
	// transaction. Cascades then remove all raw and aggregate target history.
	if err := writeAudit(ctx, tx, actor, metadata, "probe_target.delete", targetID, before, after); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM probe_targets WHERE id = $1::uuid`, targetID)
	if err != nil {
		return fmt.Errorf("hard-delete probe target: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit probe target deletion: %w", err)
	}
	return nil
}

func insertTarget(ctx context.Context, tx pgx.Tx, targetID, nodeID, actorID string, values targetValues) (Target, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO probe_targets (
			id, node_id, name, probe_type, host, port, path,
			interval_seconds, timeout_seconds, retention_seconds, enabled, created_by
		) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::uuid)
		RETURNING id::text, node_id::text, name, probe_type, host, port, path,
		          interval_seconds, timeout_seconds, retention_seconds, enabled,
		          config_version, created_at, updated_at
	`, targetID, nodeID, values.Name, string(values.Type), values.Host, values.Port, values.Path,
		values.IntervalSeconds, values.TimeoutSeconds, values.RetentionSeconds, values.Enabled, actorID)
	return scanTarget(row)
}

func updateTarget(ctx context.Context, tx pgx.Tx, targetID string, values targetValues) (Target, error) {
	row := tx.QueryRow(ctx, `
		UPDATE probe_targets
		SET name = $2, probe_type = $3, host = $4, port = $5, path = $6,
		    interval_seconds = $7, timeout_seconds = $8, retention_seconds = $9,
		    enabled = $10, config_version = config_version + 1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
		RETURNING id::text, node_id::text, name, probe_type, host, port, path,
		          interval_seconds, timeout_seconds, retention_seconds, enabled,
		          config_version, created_at, updated_at
	`, targetID, values.Name, string(values.Type), values.Host, values.Port, values.Path,
		values.IntervalSeconds, values.TimeoutSeconds, values.RetentionSeconds, values.Enabled)
	return scanTarget(row)
}

func bumpTargetVersion(ctx context.Context, tx pgx.Tx, targetID string) (Target, error) {
	row := tx.QueryRow(ctx, `
		UPDATE probe_targets
		SET config_version = config_version + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
		RETURNING id::text, node_id::text, name, probe_type, host, port, path,
		          interval_seconds, timeout_seconds, retention_seconds, enabled,
		          config_version, created_at, updated_at
	`, targetID)
	return scanTarget(row)
}

func findTargetNode(ctx context.Context, tx pgx.Tx, targetID string) (string, error) {
	var nodeID string
	err := tx.QueryRow(ctx, `SELECT node_id::text FROM probe_targets WHERE id = $1::uuid`, targetID).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("locate probe target node: %w", err)
	}
	return nodeID, nil
}

func lockNodeVersion(ctx context.Context, tx pgx.Tx, nodeID string) (int64, error) {
	var version int64
	err := tx.QueryRow(ctx, `SELECT config_version FROM nodes WHERE id = $1::uuid FOR UPDATE`, nodeID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock probe target node: %w", err)
	}
	return version, nil
}

func lockTarget(ctx context.Context, tx pgx.Tx, targetID string) (Target, error) {
	target, err := scanTarget(tx.QueryRow(ctx, `
		SELECT id::text, node_id::text, name, probe_type, host, port, path,
		       interval_seconds, timeout_seconds, retention_seconds, enabled,
		       config_version, created_at, updated_at
		FROM probe_targets
		WHERE id = $1::uuid
		FOR UPDATE
	`, targetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, ErrNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("lock probe target: %w", err)
	}
	return target, nil
}

func advanceNodeVersion(ctx context.Context, tx pgx.Tx, nodeID string) error {
	command, err := tx.Exec(ctx, `
		UPDATE nodes
		SET config_version = config_version + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
	`, nodeID)
	if err != nil {
		return fmt.Errorf("advance node configuration version: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTarget(scanner rowScanner) (Target, error) {
	var target Target
	var probeType string
	var port pgtype.Int4
	var targetPath pgtype.Text
	if err := scanner.Scan(
		&target.TargetID, &target.NodeID, &target.Name, &probeType, &target.Host,
		&port, &targetPath, &target.IntervalSeconds, &target.TimeoutSeconds,
		&target.RetentionSeconds, &target.Enabled, &target.ConfigVersion,
		&target.CreatedAt, &target.UpdatedAt,
	); err != nil {
		return Target{}, err
	}
	target.Type = Type(probeType)
	if port.Valid {
		value := port.Int32
		target.Port = &value
	}
	if targetPath.Valid {
		value := targetPath.String
		target.Path = &value
	}
	target.CreatedAt = target.CreatedAt.UTC()
	target.UpdatedAt = target.UpdatedAt.UTC()
	return target, nil
}

func writeAudit(ctx context.Context, tx pgx.Tx, actor auth.Identity, metadata Metadata, action, targetID string, before, after []byte) error {
	var beforeValue any
	var afterValue any
	if before != nil {
		beforeValue = string(before)
	}
	if after != nil {
		afterValue = string(after)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			actor_user_id, actor_username, action, target_type, target_id,
			source_ip, request_id, before_summary, after_summary, result
		) VALUES (
			$1::uuid, $2, $3, 'probe_target', $4,
			NULLIF($5, '')::inet, $6, $7::jsonb, $8::jsonb, 'success'
		)
	`, actor.User.ID, actor.User.Username, action, targetID,
		metadata.SourceIP, metadata.RequestID, beforeValue, afterValue)
	if err != nil {
		return fmt.Errorf("insert probe target audit: %w", err)
	}
	return nil
}

func deletedAuditJSON(target Target) ([]byte, error) {
	body, err := target.auditJSON()
	if err != nil {
		return nil, err
	}
	var summary map[string]any
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, fmt.Errorf("decode deletion audit summary: %w", err)
	}
	summary["deleted"] = true
	body, err = json.Marshal(summary)
	if err != nil {
		return nil, fmt.Errorf("encode deletion audit summary: %w", err)
	}
	return body, nil
}

func authorize(actor auth.Identity) error {
	if !actor.User.Enabled || actor.User.Role != auth.RoleAdmin || !ValidUUID(actor.User.ID) || actor.User.Username == "" {
		return ErrForbidden
	}
	return nil
}

func normalizeMetadata(metadata Metadata) (Metadata, error) {
	metadata.SourceIP = strings.TrimSpace(metadata.SourceIP)
	if metadata.SourceIP != "" {
		address, err := netip.ParseAddr(metadata.SourceIP)
		if err != nil {
			return Metadata{}, invalidField("source_ip", "is invalid")
		}
		metadata.SourceIP = address.Unmap().String()
	}
	if metadata.RequestID == "" || len(metadata.RequestID) > 128 || !utf8.ValidString(metadata.RequestID) {
		return Metadata{}, invalidField("request_id", "is invalid")
	}
	for _, character := range metadata.RequestID {
		if unicode.IsControl(character) {
			return Metadata{}, invalidField("request_id", "is invalid")
		}
	}
	return metadata, nil
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
		case "23514", "23503":
			return ErrInvalidRequest
		}
	}
	return err
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate probe target UUID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
