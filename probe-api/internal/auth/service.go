package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const nilUUID = "00000000-0000-0000-0000-000000000000"

type ServiceConfig struct {
	SessionTTL          time.Duration
	MaxSessions         int
	RevokedRetention    time.Duration
	LoginIPLimit        int
	LoginIPWindow       time.Duration
	LoginUsernameLimit  int
	LoginUsernameWindow time.Duration
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		SessionTTL:          12 * time.Hour,
		MaxSessions:         5,
		RevokedRetention:    24 * time.Hour,
		LoginIPLimit:        10,
		LoginIPWindow:       time.Minute,
		LoginUsernameLimit:  5,
		LoginUsernameWindow: 5 * time.Minute,
	}
}

func (config ServiceConfig) Validate() error {
	if config.SessionTTL < 5*time.Minute || config.SessionTTL > 7*24*time.Hour {
		return errors.New("session TTL must be between 5 minutes and 7 days")
	}
	if config.MaxSessions < 1 || config.MaxSessions > 20 {
		return errors.New("maximum concurrent sessions must be between 1 and 20")
	}
	if config.RevokedRetention < time.Hour || config.RevokedRetention > 30*24*time.Hour {
		return errors.New("revoked session retention must be between 1 hour and 30 days")
	}
	if config.LoginIPLimit < 1 || config.LoginIPLimit > 10000 || config.LoginUsernameLimit < 1 || config.LoginUsernameLimit > 10000 {
		return errors.New("login rate limits must be between 1 and 10000")
	}
	if config.LoginIPWindow < time.Second || config.LoginIPWindow > 24*time.Hour ||
		config.LoginUsernameWindow < time.Second || config.LoginUsernameWindow > 24*time.Hour {
		return errors.New("login rate-limit windows must be between 1 second and 24 hours")
	}
	return nil
}

type Service struct {
	pool   *pgxpool.Pool
	config ServiceConfig
}

func NewService(pool *pgxpool.Pool, config ServiceConfig) (*Service, error) {
	if pool == nil {
		return nil, errors.New("authentication database pool is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Service{pool: pool, config: config}, nil
}

func NewDefaultService(pool *pgxpool.Pool) *Service {
	service, err := NewService(pool, DefaultServiceConfig())
	if err != nil {
		panic(err)
	}
	return service
}

func (service *Service) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	if err := input.LoginRequest.Validate(); err != nil {
		return LoginResult{}, &FieldError{Field: "credentials"}
	}
	metadata, err := normalizeMetadata(input.Metadata)
	if err != nil {
		return LoginResult{}, err
	}
	if err := service.consumeLoginRateLimits(ctx, input.Username, metadata); err != nil {
		return LoginResult{}, err
	}

	candidate, candidateHash, found, err := service.loadUserByUsername(ctx, input.Username)
	if err != nil {
		return LoginResult{}, err
	}
	passwordMatches := false
	if !found {
		_, _ = VerifyPassword(DummyPasswordHash(), input.Password)
	} else {
		passwordMatches, err = VerifyPassword(candidateHash, input.Password)
		if err != nil {
			return LoginResult{}, fmt.Errorf("verify stored password hash: %w", err)
		}
	}

	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LoginResult{}, fmt.Errorf("begin login transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		return LoginResult{}, fmt.Errorf("read login time: %w", err)
	}
	now = now.UTC()

	if !found {
		if err := writeLoginAudit(ctx, tx, nil, input.Username, metadata, "failure", "invalid_credentials"); err != nil {
			return LoginResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return LoginResult{}, fmt.Errorf("commit failed login audit: %w", err)
		}
		return LoginResult{}, ErrInvalidCredentials
	}

	user, currentHash, stillExists, err := lockUserByID(ctx, tx, candidate.ID)
	if err != nil {
		return LoginResult{}, err
	}
	hashUnchanged := stillExists && constantTimeStringEqual(candidateHash, currentHash)
	credentialsValid := passwordMatches && candidate.Enabled && candidate.Role == RoleAdmin &&
		stillExists && user.Enabled && user.Role == RoleAdmin && hashUnchanged && user.Username == input.Username
	if !credentialsValid {
		var auditUserID *string
		auditUsername := candidate.Username
		if stillExists {
			auditUserID = &user.ID
			auditUsername = user.Username
			if !user.Enabled || user.Role != RoleAdmin {
				if _, err := tx.Exec(ctx, `
					UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2)
					WHERE user_id = $1::uuid AND revoked_at IS NULL
				`, user.ID, now); err != nil {
					return LoginResult{}, fmt.Errorf("revoke ineligible user sessions: %w", err)
				}
			}
		}
		if err := writeLoginAudit(ctx, tx, auditUserID, auditUsername, metadata, "failure", "invalid_credentials"); err != nil {
			return LoginResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return LoginResult{}, fmt.Errorf("commit failed login audit: %w", err)
		}
		return LoginResult{}, ErrInvalidCredentials
	}

	sessionID, sessionToken, sessionHash, err := NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrfToken, csrfHash, err := DeriveCSRFToken(sessionToken)
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := now.Add(service.config.SessionTTL)

	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = $2
		WHERE user_id = $1::uuid AND revoked_at IS NULL AND expires_at <= $2
	`, user.ID, now); err != nil {
		return LoginResult{}, fmt.Errorf("revoke expired sessions before login: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text
		FROM sessions
		WHERE user_id = $1::uuid AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at, id
		FOR UPDATE
	`, user.ID, now)
	if err != nil {
		return LoginResult{}, fmt.Errorf("lock active sessions: %w", err)
	}
	activeIDs := make([]string, 0, service.config.MaxSessions)
	for rows.Next() {
		var session string
		if err := rows.Scan(&session); err != nil {
			rows.Close()
			return LoginResult{}, fmt.Errorf("scan active session: %w", err)
		}
		activeIDs = append(activeIDs, session)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return LoginResult{}, fmt.Errorf("iterate active sessions: %w", err)
	}
	rows.Close()
	for index := 0; index <= len(activeIDs)-service.config.MaxSessions; index++ {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id = $1::uuid AND revoked_at IS NULL`, activeIDs[index], now); err != nil {
			return LoginResult{}, fmt.Errorf("revoke oldest concurrent session: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, token_hash, csrf_token_hash, source_ip, user_agent,
			created_at, last_seen_at, expires_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, '')::inet, NULLIF($6, ''), $7, $7, $8)
	`, sessionID, user.ID, sessionHash, csrfHash, metadata.SourceIP, metadata.UserAgent, now, expiresAt); err != nil {
		return LoginResult{}, fmt.Errorf("insert user session: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1::uuid`, user.ID, now); err != nil {
		return LoginResult{}, fmt.Errorf("update last login time: %w", err)
	}
	usernameKey := loginRateKey("username", user.Username)
	if _, err := tx.Exec(ctx, `DELETE FROM login_rate_limits WHERE scope = 'username' AND key_hash = $1`, usernameKey[:]); err != nil {
		return LoginResult{}, fmt.Errorf("reset successful username login rate: %w", err)
	}
	if err := writeLoginAudit(ctx, tx, &user.ID, user.Username, metadata, "success", ""); err != nil {
		return LoginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, fmt.Errorf("commit login transaction: %w", err)
	}
	user.LastLoginAt = timePointer(now)
	return LoginResult{
		AuthResponse: AuthResponse{User: user, CSRFToken: csrfToken},
		SessionToken: sessionToken,
		ExpiresAt:    expiresAt.UTC(),
	}, nil
}

func (service *Service) Authenticate(ctx context.Context, plaintext string) (Identity, error) {
	tokenID, parsed := ParseSessionToken(plaintext)
	lookupID := tokenID
	if !parsed {
		lookupID = nilUUID
	}

	identity, storedHash, now, revoked, found, err := service.loadIdentity(ctx, lookupID)
	if err != nil {
		return Identity{}, err
	}
	matched := ConstantTimeHashEqual(storedHash, plaintext)
	if !parsed || !found || !matched || revoked || !identity.User.Enabled || identity.User.Role != RoleAdmin || !identity.ExpiresAt.After(now) {
		if found && (!identity.User.Enabled || identity.User.Role != RoleAdmin || !identity.ExpiresAt.After(now)) {
			_, _ = service.pool.Exec(ctx, `
				UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2)
				WHERE id = $1::uuid
			`, identity.SessionID, now)
		}
		return Identity{}, ErrUnauthorized
	}
	result, err := service.pool.Exec(ctx, `
		UPDATE sessions AS s
		SET last_seen_at = CASE
			WHEN s.last_seen_at < $2 - INTERVAL '1 minute' THEN $2
			ELSE s.last_seen_at
		END
		FROM users AS u
		WHERE s.id = $1::uuid AND s.user_id = $3::uuid
		  AND u.id = s.user_id AND u.enabled AND u.role = $4
		  AND s.revoked_at IS NULL AND s.expires_at > $2
	`, identity.SessionID, now, identity.User.ID, string(identity.User.Role))
	if err != nil {
		return Identity{}, fmt.Errorf("touch authenticated session: %w", err)
	}
	if result.RowsAffected() != 1 {
		return Identity{}, ErrUnauthorized
	}
	return identity, nil
}

func (service *Service) CurrentAuth(ctx context.Context, plaintext string) (AuthResponse, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthResponse{}, fmt.Errorf("begin current authentication transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	identity, storedSessionHash, storedCSRFHash, now, revoked, found, err := lockSessionIdentity(ctx, tx, plaintext)
	if err != nil {
		return AuthResponse{}, err
	}
	if !found || !ConstantTimeHashEqual(storedSessionHash, plaintext) || revoked || !identity.User.Enabled || identity.User.Role != RoleAdmin || !identity.ExpiresAt.After(now) {
		return AuthResponse{}, ErrUnauthorized
	}
	csrfToken, csrfHash, err := DeriveCSRFToken(plaintext)
	if err != nil {
		return AuthResponse{}, ErrUnauthorized
	}
	if !ConstantTimeDigestEqual(storedCSRFHash, csrfHash) {
		return AuthResponse{}, ErrUnauthorized
	}
	if err := tx.Commit(ctx); err != nil {
		return AuthResponse{}, fmt.Errorf("commit current authentication transaction: %w", err)
	}
	return AuthResponse{User: identity.User, CSRFToken: csrfToken}, nil
}

func (service *Service) VerifyCSRF(ctx context.Context, identity Identity, plaintext string) error {
	validPlaintext := ParseCSRFToken(plaintext)
	var storedHash string
	var now time.Time
	err := service.pool.QueryRow(ctx, `
		SELECT s.csrf_token_hash, CURRENT_TIMESTAMP
		FROM sessions AS s
		JOIN users AS u ON u.id = s.user_id
		WHERE s.id = $1::uuid AND s.user_id = $2::uuid
		  AND s.revoked_at IS NULL AND s.expires_at > CURRENT_TIMESTAMP
		  AND u.enabled AND u.role = 'admin'
	`, identity.SessionID, identity.User.ID).Scan(&storedHash, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = ConstantTimeHashEqual("", plaintext)
		return ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("revalidate CSRF session: %w", err)
	}
	if !identity.ExpiresAt.After(now.UTC()) {
		return ErrUnauthorized
	}
	if !validPlaintext || !ConstantTimeHashEqual(storedHash, plaintext) {
		return ErrForbidden
	}
	return nil
}

func (service *Service) Logout(ctx context.Context, plaintext string, csrfToken string, metadata RequestMetadata) error {
	metadata, err := normalizeMetadata(metadata)
	if err != nil {
		return err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin logout transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	identity, storedSessionHash, storedCSRFHash, now, revoked, found, err := lockSessionIdentity(ctx, tx, plaintext)
	if err != nil {
		return err
	}
	if !found || !ConstantTimeHashEqual(storedSessionHash, plaintext) || revoked || !identity.User.Enabled || identity.User.Role != RoleAdmin || !identity.ExpiresAt.After(now) {
		return ErrUnauthorized
	}
	if !ParseCSRFToken(csrfToken) || !ConstantTimeHashEqual(storedCSRFHash, csrfToken) {
		return ErrForbidden
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id = $1::uuid AND revoked_at IS NULL`, identity.SessionID, now); err != nil {
		return fmt.Errorf("revoke current session: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			actor_user_id, actor_username, action, target_type, target_id,
			source_ip, request_id, result, occurred_at
		) VALUES ($1::uuid, $2, 'auth.logout', 'session', $3, NULLIF($4, '')::inet, $5, 'success', $6)
	`, identity.User.ID, identity.User.Username, identity.SessionID, metadata.SourceIP, metadata.RequestID, now); err != nil {
		return fmt.Errorf("insert logout audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit logout transaction: %w", err)
	}
	return nil
}

func (service *Service) BootstrapAdmin(ctx context.Context, username string, password []byte, requestID string) (User, error) {
	defer clear(password)
	if !validUsername(username) {
		return User{}, &FieldError{Field: "username"}
	}
	if !validNewPasswordBytes(password) {
		return User{}, &FieldError{Field: "password"}
	}
	if err := validateRequestID(requestID); err != nil {
		return User{}, err
	}
	passwordHash, err := HashPasswordBytes(password)
	clear(password)
	if err != nil {
		return User{}, err
	}
	userID, err := newUUID()
	if err != nil {
		return User{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin administrator bootstrap transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7809801984)`); err != nil {
		return User{}, fmt.Errorf("lock administrator bootstrap: %w", err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return User{}, fmt.Errorf("lock users table for administrator bootstrap: %w", err)
	}
	var usersExist bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&usersExist); err != nil {
		return User{}, fmt.Errorf("check existing users: %w", err)
	}
	if usersExist {
		return User{}, ErrBootstrapUnavailable
	}
	var user User
	var role string
	err = tx.QueryRow(ctx, `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, $3, 'admin', TRUE)
		RETURNING id::text, username, role, enabled, created_at, updated_at
	`, userID, username, passwordHash).Scan(
		&user.ID, &user.Username, &role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return User{}, fmt.Errorf("insert bootstrap administrator: %w", err)
	}
	user.Role = Role(role)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			actor_username, action, target_type, target_id,
			request_id, after_summary, result
		) VALUES ('local-bootstrap', 'user.bootstrap', 'user', $1,
			$3, jsonb_build_object('username', $2::text, 'role', 'admin', 'enabled', TRUE), 'success')
	`, user.ID, user.Username, requestID); err != nil {
		return User{}, fmt.Errorf("insert administrator bootstrap audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit administrator bootstrap: %w", err)
	}
	user.CreatedAt = user.CreatedAt.UTC()
	user.UpdatedAt = user.UpdatedAt.UTC()
	return user, nil
}

func (service *Service) consumeLoginRateLimits(ctx context.Context, username string, metadata RequestMetadata) error {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin login rate-limit transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type dimension struct {
		scope  string
		value  string
		limit  int
		window time.Duration
	}
	consume := func(item dimension) (time.Duration, error) {
		key := loginRateKey(item.scope, item.value)
		windowSeconds := int64((item.window + time.Second - 1) / time.Second)
		var count int
		var windowStarted time.Time
		var now time.Time
		err := tx.QueryRow(ctx, `
			INSERT INTO login_rate_limits (
				scope, key_hash, window_started_at, attempt_count, updated_at
			)
			SELECT $1, $2, rate_clock.now, 1, rate_clock.now
			FROM (SELECT clock_timestamp() AS now) AS rate_clock
			ON CONFLICT (scope, key_hash) DO UPDATE SET
				window_started_at = CASE
					WHEN login_rate_limits.window_started_at + ($3 * INTERVAL '1 second') <=
						GREATEST(login_rate_limits.updated_at, EXCLUDED.updated_at)
					THEN GREATEST(login_rate_limits.updated_at, EXCLUDED.updated_at)
					ELSE login_rate_limits.window_started_at END,
				attempt_count = CASE
					WHEN login_rate_limits.window_started_at + ($3 * INTERVAL '1 second') <=
						GREATEST(login_rate_limits.updated_at, EXCLUDED.updated_at)
					THEN 1 ELSE login_rate_limits.attempt_count + 1 END,
				updated_at = GREATEST(login_rate_limits.updated_at, EXCLUDED.updated_at)
			RETURNING attempt_count, window_started_at, updated_at
		`, item.scope, key[:], windowSeconds).Scan(&count, &windowStarted, &now)
		if err != nil {
			return 0, fmt.Errorf("consume %s login rate limit: %w", item.scope, err)
		}
		if count > item.limit {
			retry := windowStarted.UTC().Add(item.window).Sub(now.UTC())
			if retry < time.Second {
				retry = time.Second
			}
			return retry, nil
		}
		return 0, nil
	}

	// Source IP is deliberately consumed first and short-circuits the
	// username dimension. A blocked source therefore cannot create arbitrary
	// username keys or lock unrelated users by rotating candidate names.
	retryAfter, err := consume(dimension{
		scope: "source_ip", value: metadata.SourceIP,
		limit: service.config.LoginIPLimit, window: service.config.LoginIPWindow,
	})
	if err != nil {
		return err
	}
	if retryAfter == 0 {
		retryAfter, err = consume(dimension{
			scope: "username", value: username,
			limit: service.config.LoginUsernameLimit, window: service.config.LoginUsernameWindow,
		})
		if err != nil {
			return err
		}
	}
	if retryAfter > 0 {
		if err := writeLoginAudit(ctx, tx, nil, username, metadata, "failure", "rate_limited"); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit login rate-limit transaction: %w", err)
	}
	if retryAfter > 0 {
		return &RateLimitError{RetryAfter: retryAfter}
	}
	return nil
}

func loginRateKey(scope, value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(scope + "\x00" + value))
}

func (service *Service) loadUserByUsername(ctx context.Context, username string) (User, string, bool, error) {
	var user User
	var role string
	var passwordHash string
	var lastLogin pgtype.Timestamptz
	err := service.pool.QueryRow(ctx, `
		SELECT id::text, username, password_hash, role, enabled, last_login_at,
		       created_at, updated_at
		FROM users
		WHERE username = $1 AND role = 'admin'
	`, username).Scan(
		&user.ID, &user.Username, &passwordHash, &role, &user.Enabled, &lastLogin,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", false, nil
	}
	if err != nil {
		return User{}, "", false, fmt.Errorf("query login user: %w", err)
	}
	user.Role = Role(role)
	if lastLogin.Valid {
		value := lastLogin.Time.UTC()
		user.LastLoginAt = &value
	}
	user.CreatedAt = user.CreatedAt.UTC()
	user.UpdatedAt = user.UpdatedAt.UTC()
	return user, passwordHash, true, nil
}

func lockUserByID(ctx context.Context, tx pgx.Tx, userID string) (User, string, bool, error) {
	var user User
	var role string
	var passwordHash string
	var lastLogin pgtype.Timestamptz
	err := tx.QueryRow(ctx, `
		SELECT id::text, username, password_hash, role, enabled, last_login_at,
		       created_at, updated_at
		FROM users WHERE id = $1::uuid
		FOR UPDATE
	`, userID).Scan(
		&user.ID, &user.Username, &passwordHash, &role, &user.Enabled, &lastLogin,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", false, nil
	}
	if err != nil {
		return User{}, "", false, fmt.Errorf("lock login user: %w", err)
	}
	user.Role = Role(role)
	if lastLogin.Valid {
		value := lastLogin.Time.UTC()
		user.LastLoginAt = &value
	}
	user.CreatedAt = user.CreatedAt.UTC()
	user.UpdatedAt = user.UpdatedAt.UTC()
	return user, passwordHash, true, nil
}

func (service *Service) loadIdentity(ctx context.Context, sessionID string) (Identity, string, time.Time, bool, bool, error) {
	var identity Identity
	var role string
	var storedHash string
	var lastLogin pgtype.Timestamptz
	var revokedAt pgtype.Timestamptz
	var now time.Time
	err := service.pool.QueryRow(ctx, `
		SELECT s.id::text, s.token_hash, s.expires_at, s.revoked_at,
		       u.id::text, u.username, u.role, u.enabled, u.last_login_at,
		       u.created_at, u.updated_at, CURRENT_TIMESTAMP
		FROM sessions AS s
		JOIN users AS u ON u.id = s.user_id
		WHERE s.id = $1::uuid
	`, sessionID).Scan(
		&identity.SessionID, &storedHash, &identity.ExpiresAt, &revokedAt,
		&identity.User.ID, &identity.User.Username, &role, &identity.User.Enabled, &lastLogin,
		&identity.User.CreatedAt, &identity.User.UpdatedAt, &now,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, "", time.Now().UTC(), false, false, nil
	}
	if err != nil {
		return Identity{}, "", time.Time{}, false, false, fmt.Errorf("query user session: %w", err)
	}
	identity.User.Role = Role(role)
	normalizeIdentityTimes(&identity, lastLogin)
	return identity, storedHash, now.UTC(), revokedAt.Valid, true, nil
}

func lockSessionIdentity(ctx context.Context, tx pgx.Tx, plaintext string) (Identity, string, string, time.Time, bool, bool, error) {
	sessionID, parsed := ParseSessionToken(plaintext)
	if !parsed {
		sessionID = nilUUID
	}
	identity, storedHash, csrfHash, now, revoked, found, err := queryLockedIdentity(ctx, tx, sessionID)
	if err != nil {
		return Identity{}, "", "", time.Time{}, false, false, err
	}
	if !parsed {
		found = false
	}
	return identity, storedHash, csrfHash, now, revoked, found, nil
}

func queryLockedIdentity(ctx context.Context, tx pgx.Tx, sessionID string) (Identity, string, string, time.Time, bool, bool, error) {
	var identity Identity
	var role string
	var storedHash string
	var csrfHash string
	var lastLogin pgtype.Timestamptz
	var revokedAt pgtype.Timestamptz
	var now time.Time
	err := tx.QueryRow(ctx, `
		SELECT s.id::text, s.token_hash, s.csrf_token_hash, s.expires_at, s.revoked_at,
		       u.id::text, u.username, u.role, u.enabled, u.last_login_at,
		       u.created_at, u.updated_at, CURRENT_TIMESTAMP
		FROM sessions AS s
		JOIN users AS u ON u.id = s.user_id
		WHERE s.id = $1::uuid
		FOR UPDATE OF s, u
	`, sessionID).Scan(
		&identity.SessionID, &storedHash, &csrfHash, &identity.ExpiresAt, &revokedAt,
		&identity.User.ID, &identity.User.Username, &role, &identity.User.Enabled, &lastLogin,
		&identity.User.CreatedAt, &identity.User.UpdatedAt, &now,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, "", "", time.Now().UTC(), false, false, nil
	}
	if err != nil {
		return Identity{}, "", "", time.Time{}, false, false, fmt.Errorf("lock user session: %w", err)
	}
	identity.User.Role = Role(role)
	normalizeIdentityTimes(&identity, lastLogin)
	return identity, storedHash, csrfHash, now.UTC(), revokedAt.Valid, true, nil
}

func normalizeIdentityTimes(identity *Identity, lastLogin pgtype.Timestamptz) {
	identity.ExpiresAt = identity.ExpiresAt.UTC()
	identity.User.CreatedAt = identity.User.CreatedAt.UTC()
	identity.User.UpdatedAt = identity.User.UpdatedAt.UTC()
	if lastLogin.Valid {
		value := lastLogin.Time.UTC()
		identity.User.LastLoginAt = &value
	}
}

func writeLoginAudit(ctx context.Context, tx pgx.Tx, userID *string, username string, metadata RequestMetadata, result, errorCode string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			actor_user_id, actor_username, action, target_type, target_id,
			source_ip, request_id, result, error_code
		) VALUES (
			NULLIF($1, '')::uuid, $2, 'auth.login', 'user', NULLIF($1, ''),
			NULLIF($3, '')::inet, $4, $5, NULLIF($6, '')
		)
	`, stringValue(userID), username, metadata.SourceIP, metadata.RequestID, result, errorCode)
	if err != nil {
		return fmt.Errorf("insert login audit: %w", err)
	}
	return nil
}

func normalizeMetadata(metadata RequestMetadata) (RequestMetadata, error) {
	metadata.SourceIP = strings.TrimSpace(metadata.SourceIP)
	if metadata.SourceIP != "" {
		address, err := netip.ParseAddr(metadata.SourceIP)
		if err != nil {
			return RequestMetadata{}, errors.New("source IP is invalid")
		}
		metadata.SourceIP = address.Unmap().String()
	}
	if err := validateRequestID(metadata.RequestID); err != nil {
		return RequestMetadata{}, err
	}
	metadata.UserAgent = sanitizeUserAgent(metadata.UserAgent)
	return metadata, nil
}

func validateRequestID(value string) error {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return errors.New("request ID is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("request ID is invalid")
		}
	}
	return nil
}

func sanitizeUserAgent(value string) string {
	if !utf8.ValidString(value) {
		return ""
	}
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > 512 {
			break
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		_ = subtle.ConstantTimeCompare([]byte(left), []byte(left))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
