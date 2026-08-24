package usermanagement

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/auditlog"
	"probe-api/internal/auth"
)

type Service struct {
	pool *pgxpool.Pool
}

// userAdministrationLock serializes only user-management transactions. A
// table lock here can deadlock with login's row lock followed by last_login_at
// update, while an advisory transaction lock preserves the last-admin invariant
// without blocking ordinary authentication.
const userAdministrationLock int64 = 0x70726f6265757365

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
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ListResponse{}, fmt.Errorf("begin user list transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := auditlog.AssertCurrentAdmin(ctx, tx, actor); err != nil {
		return ListResponse{}, mapAuditError(err)
	}
	var cursorTime any
	var cursorID any
	if request.Cursor != nil {
		if request.Cursor.CreatedAt.IsZero() || !ValidUUID(request.Cursor.UserID) {
			return ListResponse{}, ErrInvalidCursor
		}
		cursorTime = request.Cursor.CreatedAt.UTC()
		cursorID = request.Cursor.UserID
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, username, role, enabled, last_login_at, created_at, updated_at
		FROM users
		WHERE role = 'admin'
		  AND ($1::timestamptz IS NULL OR (created_at, id) < ($1::timestamptz, $2::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $3
	`, cursorTime, cursorID, request.Limit+1)
	if err != nil {
		return ListResponse{}, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()
	response := ListResponse{Users: make([]User, 0, request.Limit)}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return ListResponse{}, fmt.Errorf("scan user: %w", err)
		}
		response.Users = append(response.Users, user)
	}
	if err := rows.Err(); err != nil {
		return ListResponse{}, fmt.Errorf("iterate users: %w", err)
	}
	if len(response.Users) > request.Limit {
		response.Users = response.Users[:request.Limit]
		last := response.Users[len(response.Users)-1]
		cursor, err := EncodeCursor(Cursor{CreatedAt: last.CreatedAt, UserID: last.ID})
		if err != nil {
			return ListResponse{}, fmt.Errorf("encode user cursor: %w", err)
		}
		response.NextCursor = &cursor
	}
	if err := tx.Commit(ctx); err != nil {
		return ListResponse{}, fmt.Errorf("commit user list transaction: %w", err)
	}
	return response, nil
}

func (service *Service) Create(ctx context.Context, actor auth.Identity, request CreateRequest, metadata Metadata) (User, error) {
	if err := authorize(actor); err != nil {
		return User{}, err
	}
	if err := validateValues(request.Username, request.Password, request.Role); err != nil {
		return User{}, err
	}
	passwordBytes := []byte(request.Password)
	defer clear(passwordBytes)
	passwordHash, err := auth.HashPasswordBytes(passwordBytes)
	if err != nil {
		return User{}, invalidField("password", "must contain 12 to 1024 valid UTF-8 bytes")
	}
	userID, err := newUUID()
	if err != nil {
		return User{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin user creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUsers(ctx, tx); err != nil {
		return User{}, err
	}
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return User{}, err
	}
	user, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, $3, 'admin', $4)
		RETURNING id::text, username, role, enabled, last_login_at, created_at, updated_at
	`, userID, request.Username, passwordHash, request.Enabled))
	if err != nil {
		return User{}, classifyWriteError(err)
	}
	if err := auditlog.Write(ctx, tx, actor, metadata, "user.create", "user", userID, nil, userAudit(user, true)); err != nil {
		return User{}, mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit user creation: %w", err)
	}
	return user, nil
}

func (service *Service) Update(ctx context.Context, actor auth.Identity, userID string, request UpdateRequest, metadata Metadata) (User, error) {
	if err := authorize(actor); err != nil {
		return User{}, err
	}
	if !ValidUUID(userID) {
		return User{}, invalidField("user_id", "must be a canonical lowercase UUID")
	}
	if request.Username == nil && request.Password == nil && request.Role == nil && request.Enabled == nil {
		return User{}, invalidField("request", "at least one field is required")
	}
	if request.Username != nil && !validUsername(*request.Username) {
		return User{}, invalidField("username", "must contain 1 to 128 valid characters")
	}
	if request.Password != nil && !validPassword(*request.Password) {
		return User{}, invalidField("password", "must contain 12 to 1024 valid UTF-8 bytes")
	}
	if request.Role != nil && !validRole(*request.Role) {
		return User{}, invalidField("role", "must be admin")
	}
	var passwordHash any
	if request.Password != nil {
		passwordBytes := []byte(*request.Password)
		defer clear(passwordBytes)
		hash, err := auth.HashPasswordBytes(passwordBytes)
		if err != nil {
			return User{}, invalidField("password", "must contain 12 to 1024 valid UTF-8 bytes")
		}
		passwordHash = hash
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin user update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUsers(ctx, tx); err != nil {
		return User{}, err
	}
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return User{}, err
	}
	current, err := loadUserForUpdate(ctx, tx, userID)
	if err != nil {
		return User{}, err
	}
	username := current.Username
	role := current.Role
	enabled := current.Enabled
	if request.Username != nil {
		username = *request.Username
	}
	if request.Role != nil {
		role = *request.Role
	}
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	if current.Role == auth.RoleAdmin && current.Enabled && (role != auth.RoleAdmin || !enabled) {
		if err := requireAnotherAdmin(ctx, tx); err != nil {
			return User{}, err
		}
	}
	updated, err := scanUser(tx.QueryRow(ctx, `
		UPDATE users
		SET username = $2,
		    password_hash = COALESCE($3::text, password_hash),
		    role = $4,
		    enabled = $5,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1::uuid
		RETURNING id::text, username, role, enabled, last_login_at, created_at, updated_at
	`, userID, username, passwordHash, role, enabled))
	if err != nil {
		return User{}, classifyWriteError(err)
	}
	sensitive := request.Password != nil || role != current.Role || enabled != current.Enabled
	if sensitive {
		if _, err := tx.Exec(ctx, `
			UPDATE sessions
			SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
			WHERE user_id = $1::uuid AND revoked_at IS NULL
		`, userID); err != nil {
			return User{}, fmt.Errorf("revoke changed user's sessions: %w", err)
		}
	}
	if err := auditlog.Write(ctx, tx, actor, metadata, "user.update", "user", userID,
		userAudit(current, false), userAudit(updated, request.Password != nil)); err != nil {
		return User{}, mapAuditError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit user update: %w", err)
	}
	return updated, nil
}

func (service *Service) Delete(ctx context.Context, actor auth.Identity, userID string, metadata Metadata) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if !ValidUUID(userID) {
		return invalidField("user_id", "must be a canonical lowercase UUID")
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin user deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUsers(ctx, tx); err != nil {
		return err
	}
	if err := assertAdmin(ctx, tx, actor); err != nil {
		return err
	}
	current, err := loadUserForUpdate(ctx, tx, userID)
	if err != nil {
		return err
	}
	if current.Role == auth.RoleAdmin && current.Enabled {
		if err := requireAnotherAdmin(ctx, tx); err != nil {
			return err
		}
	}
	after := map[string]any{"user_id": userID, "deleted": true}
	if err := auditlog.Write(ctx, tx, actor, metadata, "user.delete", "user", userID, userAudit(current, false), after); err != nil {
		return mapAuditError(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID); err != nil {
		return classifyWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit user deletion: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanUser(row rowScanner) (User, error) {
	var user User
	var lastLogin pgtype.Timestamptz
	if err := row.Scan(&user.ID, &user.Username, &user.Role, &user.Enabled, &lastLogin, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return User{}, err
	}
	if lastLogin.Valid {
		value := lastLogin.Time.UTC()
		user.LastLoginAt = &value
	}
	user.CreatedAt = user.CreatedAt.UTC()
	user.UpdatedAt = user.UpdatedAt.UTC()
	return user, nil
}

func loadUserForUpdate(ctx context.Context, tx pgx.Tx, userID string) (User, error) {
	user, err := scanUser(tx.QueryRow(ctx, `
		SELECT id::text, username, role, enabled, last_login_at, created_at, updated_at
		FROM users
		WHERE id = $1::uuid AND role = 'admin'
		FOR UPDATE
	`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("lock user: %w", err)
	}
	return user, nil
}

func lockUsers(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, userAdministrationLock); err != nil {
		return fmt.Errorf("serialize user administration: %w", err)
	}
	return nil
}

func requireAnotherAdmin(ctx context.Context, tx pgx.Tx) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND enabled`).Scan(&count); err != nil {
		return fmt.Errorf("count usable administrators: %w", err)
	}
	if count <= 1 {
		return ErrLastUsableAdmin
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

func userAudit(user User, passwordChanged bool) map[string]any {
	return map[string]any{
		"user_id": user.ID, "username": user.Username, "role": user.Role,
		"enabled": user.Enabled, "password_changed": passwordChanged,
	}
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate user UUID")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
