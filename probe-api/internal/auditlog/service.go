package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/auth"
)

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func (service *Service) List(ctx context.Context, actor auth.Identity, request ListRequest) (ListResponse, error) {
	if !validActor(actor) {
		return ListResponse{}, ErrForbidden
	}
	if request.Limit == 0 {
		request.Limit = DefaultListLimit
	}
	if request.Limit < 1 || request.Limit > MaxListLimit {
		return ListResponse{}, &FieldError{Code: "invalid_request", Field: "limit", Message: "must be between 1 and 200"}
	}
	action := ""
	if request.Action != nil {
		if !validateText(*request.Action, 128) {
			return ListResponse{}, &FieldError{Code: "invalid_request", Field: "action", Message: "is invalid"}
		}
		action = *request.Action
	}
	if request.From != nil && request.To != nil && !request.From.Before(*request.To) {
		return ListResponse{}, &FieldError{Code: "invalid_request", Field: "from", Message: "must be earlier than to"}
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ListResponse{}, fmt.Errorf("begin audit log list transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := AssertCurrentAdmin(ctx, tx, actor); err != nil {
		return ListResponse{}, err
	}
	var from any
	var to any
	if request.From != nil {
		from = request.From.UTC()
	}
	if request.To != nil {
		to = request.To.UTC()
	}
	var cursorTime any
	var cursorID any
	if request.Cursor != nil {
		if request.Cursor.OccurredAt.IsZero() || request.Cursor.AuditID < 1 {
			return ListResponse{}, ErrInvalidCursor
		}
		cursorTime = request.Cursor.OccurredAt.UTC()
		cursorID = request.Cursor.AuditID
	}
	rows, err := tx.Query(ctx, `
		SELECT id, actor_user_id::text, actor_username, action, target_type, target_id,
		       host(source_ip), request_id, before_summary, after_summary,
		       result, error_code, occurred_at
		FROM audit_logs
		WHERE ($1::text = '' OR action = $1)
		  AND ($2::timestamptz IS NULL OR occurred_at >= $2)
		  AND ($3::timestamptz IS NULL OR occurred_at < $3)
		  AND ($4::timestamptz IS NULL OR (occurred_at, id) < ($4::timestamptz, $5::bigint))
		ORDER BY occurred_at DESC, id DESC
		LIMIT $6
	`, action, from, to, cursorTime, cursorID, request.Limit+1)
	if err != nil {
		return ListResponse{}, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()
	response := ListResponse{Logs: make([]Entry, 0, request.Limit)}
	for rows.Next() {
		var entry Entry
		var actorUserID, actorUsername, targetType, targetID, sourceIP, errorCode pgtype.Text
		var beforeSummary, afterSummary []byte
		if err := rows.Scan(&entry.AuditID, &actorUserID, &actorUsername, &entry.Action,
			&targetType, &targetID, &sourceIP, &entry.RequestID,
			&beforeSummary, &afterSummary, &entry.Result, &errorCode, &entry.OccurredAt); err != nil {
			return ListResponse{}, fmt.Errorf("scan audit log: %w", err)
		}
		entry.BeforeSummary = beforeSummary
		entry.AfterSummary = afterSummary
		entry.ActorUserID = optionalText(actorUserID)
		entry.ActorUsername = optionalText(actorUsername)
		entry.TargetType = optionalText(targetType)
		entry.TargetID = optionalText(targetID)
		entry.SourceIP = optionalText(sourceIP)
		entry.ErrorCode = optionalText(errorCode)
		entry.OccurredAt = entry.OccurredAt.UTC()
		response.Logs = append(response.Logs, entry)
	}
	if err := rows.Err(); err != nil {
		return ListResponse{}, fmt.Errorf("iterate audit logs: %w", err)
	}
	if len(response.Logs) > request.Limit {
		response.Logs = response.Logs[:request.Limit]
		last := response.Logs[len(response.Logs)-1]
		cursor, err := EncodeCursor(Cursor{OccurredAt: last.OccurredAt, AuditID: last.AuditID})
		if err != nil {
			return ListResponse{}, fmt.Errorf("encode audit cursor: %w", err)
		}
		response.NextCursor = &cursor
	}
	if err := tx.Commit(ctx); err != nil {
		return ListResponse{}, fmt.Errorf("commit audit log list transaction: %w", err)
	}
	return response, nil
}

func optionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func Write(ctx context.Context, tx pgx.Tx, actor auth.Identity, metadata Metadata, action, targetType, targetID string, before, after any) error {
	if !validActor(actor) {
		return ErrForbidden
	}
	metadata, err := normalizeMetadata(metadata)
	if err != nil {
		return err
	}
	if !validateText(action, 128) || !validateText(targetType, 128) || !validateText(targetID, 512) {
		return ErrInvalidRequest
	}
	beforeJSON, err := marshalOptional(before)
	if err != nil {
		return fmt.Errorf("encode audit before summary: %w", err)
	}
	afterJSON, err := marshalOptional(after)
	if err != nil {
		return fmt.Errorf("encode audit after summary: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (
			actor_user_id, actor_username, action, target_type, target_id,
			source_ip, request_id, before_summary, after_summary, result
		) VALUES (
			$1::uuid, $2, $3, $4, $5,
			NULLIF($6, '')::inet, $7, $8::jsonb, $9::jsonb, 'success'
		)
	`, actor.User.ID, actor.User.Username, action, targetType, targetID,
		metadata.SourceIP, metadata.RequestID, beforeJSON, afterJSON)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func marshalOptional(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(body), nil
}

func normalizeMetadata(metadata Metadata) (Metadata, error) {
	metadata.SourceIP = strings.TrimSpace(metadata.SourceIP)
	if metadata.SourceIP != "" {
		address, err := netip.ParseAddr(metadata.SourceIP)
		if err != nil {
			return Metadata{}, &FieldError{Code: "invalid_request", Field: "source_ip", Message: "is invalid"}
		}
		metadata.SourceIP = address.Unmap().String()
	}
	if !validateText(metadata.RequestID, 128) {
		return Metadata{}, &FieldError{Code: "invalid_request", Field: "request_id", Message: "is invalid"}
	}
	return metadata, nil
}

func validActor(actor auth.Identity) bool {
	return actor.User.Enabled && actor.IsAdmin() && canonicalUUIDPattern.MatchString(actor.User.ID) && actor.User.Username != ""
}

func AssertCurrentAdmin(ctx context.Context, tx pgx.Tx, actor auth.Identity) error {
	if !validActor(actor) {
		return ErrForbidden
	}
	var allowed bool
	err := tx.QueryRow(ctx, `
		SELECT enabled AND role = 'admin'
		FROM users
		WHERE id = $1::uuid
		FOR SHARE
	`, actor.User.ID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("revalidate administrator: %w", err)
	}
	return nil
}
