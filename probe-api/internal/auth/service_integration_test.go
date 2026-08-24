package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceBootstrapAndSessionLifecycle(t *testing.T) {
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
	var existingUsers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&existingUsers); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if existingUsers != 0 {
		t.Skipf("administrator bootstrap integration requires an isolated database; found %d users", existingUsers)
	}

	config := DefaultServiceConfig()
	config.MaxSessions = 2
	config.RevokedRetention = time.Hour
	service, err := NewService(pool, config)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	prefix, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	prefix = "auth-int-" + prefix
	var cleanupUserID string
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM audit_logs WHERE request_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM login_rate_limits`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE username LIKE $1`, prefix+"%")
		if cleanupUserID != "" {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1::uuid`, cleanupUserID)
		}
	})

	const bootstrapPassword = "stage3-bootstrap-password"
	type bootstrapResult struct {
		user User
		err  error
	}
	results := make(chan bootstrapResult, 2)
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			password := []byte(bootstrapPassword)
			user, err := service.BootstrapAdmin(ctx, "bootstrap-admin-"+string(rune('a'+index)), password, prefix+"-bootstrap-"+string(rune('a'+index)))
			for offset, value := range password {
				if value != 0 {
					results <- bootstrapResult{err: errors.New("BootstrapAdmin did not clear password buffer at byte " + string(rune(offset)))}
					return
				}
			}
			results <- bootstrapResult{user: user, err: err}
		}(index)
	}
	wait.Wait()
	close(results)
	var administrator User
	successes := 0
	conflicts := 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			administrator = result.user
		case errors.Is(result.err, ErrBootstrapUnavailable):
			conflicts++
		default:
			t.Fatalf("BootstrapAdmin() error = %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 || administrator.Role != RoleAdmin || !administrator.Enabled {
		t.Fatalf("bootstrap successes/conflicts/user = %d/%d/%#v", successes, conflicts, administrator)
	}
	cleanupUserID = administrator.ID
	legacyViewerID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, $3, 'viewer', TRUE)
	`, legacyViewerID, prefix+"-legacy-viewer", DummyPasswordHash())
	var roleConstraintError *pgconn.PgError
	if !errors.As(err, &roleConstraintError) || roleConstraintError.Code != "23514" {
		t.Fatalf("database accepted legacy viewer account: %v", err)
	}
	var storedPasswordHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1::uuid`, administrator.ID).Scan(&storedPasswordHash); err != nil {
		t.Fatalf("read bootstrap password hash: %v", err)
	}
	if storedPasswordHash == bootstrapPassword {
		t.Fatal("database stored the bootstrap password in plaintext")
	}
	if matched, err := VerifyPassword(storedPasswordHash, bootstrapPassword); err != nil || !matched {
		t.Fatalf("VerifyPassword(bootstrap) = %v, %v", matched, err)
	}
	var bootstrapActor string
	if err := pool.QueryRow(ctx, `SELECT actor_username FROM audit_logs WHERE action = 'user.bootstrap' AND target_id = $1`, administrator.ID).Scan(&bootstrapActor); err != nil || bootstrapActor != "local-bootstrap" {
		t.Fatalf("bootstrap audit actor = %q, %v", bootstrapActor, err)
	}

	login := func(suffix string) LoginResult {
		t.Helper()
		result, err := service.Login(ctx, LoginInput{
			LoginRequest: LoginRequest{Username: administrator.Username, Password: bootstrapPassword},
			Metadata:     RequestMetadata{SourceIP: "192.0.2.10", UserAgent: "integration-test", RequestID: prefix + "-login-" + suffix},
		})
		if err != nil {
			t.Fatalf("Login(%s) error = %v", suffix, err)
		}
		return result
	}
	first := login("one")
	time.Sleep(2 * time.Millisecond)
	second := login("two")
	firstID, parsed := ParseSessionToken(first.SessionToken)
	if !parsed {
		t.Fatal("first session token did not parse")
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET last_seen_at = CURRENT_TIMESTAMP + INTERVAL '1 minute' WHERE id = $1::uuid`, firstID); err != nil {
		t.Fatalf("make oldest session most recently seen: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	third := login("three")
	if _, err := service.Authenticate(ctx, first.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("oldest Authenticate() error = %v, want ErrUnauthorized", err)
	}
	secondIdentity, err := service.Authenticate(ctx, second.SessionToken)
	if err != nil {
		t.Fatalf("second Authenticate() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, third.SessionToken); err != nil {
		t.Fatalf("third Authenticate() error = %v", err)
	}
	var activeSessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1::uuid AND revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP`, administrator.ID).Scan(&activeSessions); err != nil || activeSessions != 2 {
		t.Fatalf("active session count = %d, %v", activeSessions, err)
	}
	var storedSessionHash, storedCSRFHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash, csrf_token_hash FROM sessions WHERE id = $1::uuid`, secondIdentity.SessionID).Scan(&storedSessionHash, &storedCSRFHash); err != nil {
		t.Fatalf("read stored session hashes: %v", err)
	}
	if storedSessionHash == second.SessionToken || storedCSRFHash == second.CSRFToken || !ConstantTimeHashEqual(storedSessionHash, second.SessionToken) || !ConstantTimeHashEqual(storedCSRFHash, second.CSRFToken) {
		t.Fatal("database did not retain only matching session/CSRF hashes")
	}

	if err := service.VerifyCSRF(ctx, secondIdentity, second.CSRFToken); err != nil {
		t.Fatalf("VerifyCSRF(original) error = %v", err)
	}
	tabA, err := service.CurrentAuth(ctx, second.SessionToken)
	if err != nil {
		t.Fatalf("CurrentAuth(tab A) error = %v", err)
	}
	tabB, err := service.CurrentAuth(ctx, second.SessionToken)
	if err != nil {
		t.Fatalf("CurrentAuth(tab B) error = %v", err)
	}
	if tabA.CSRFToken != second.CSRFToken || tabB.CSRFToken != second.CSRFToken || tabA.User.ID != administrator.ID || tabB.User.ID != administrator.ID {
		t.Fatal("CurrentAuth() did not return the stable token bound to the current session")
	}
	var storedCSRFHashAfterCurrentAuth string
	if err := pool.QueryRow(ctx, `SELECT csrf_token_hash FROM sessions WHERE id = $1::uuid`, secondIdentity.SessionID).Scan(&storedCSRFHashAfterCurrentAuth); err != nil {
		t.Fatalf("read CSRF hash after CurrentAuth(): %v", err)
	}
	if storedCSRFHashAfterCurrentAuth != storedCSRFHash {
		t.Fatal("CurrentAuth() changed the stored CSRF hash")
	}
	if err := service.VerifyCSRF(ctx, secondIdentity, second.CSRFToken); err != nil {
		t.Fatalf("VerifyCSRF(old tab after two CurrentAuth calls) error = %v", err)
	}
	logoutMetadata := RequestMetadata{SourceIP: "192.0.2.10", UserAgent: "integration-test", RequestID: prefix + "-logout"}
	if err := service.Logout(ctx, second.SessionToken, second.CSRFToken, logoutMetadata); err != nil {
		t.Fatalf("Logout(old tab after two CurrentAuth calls) error = %v", err)
	}
	if _, err := service.Authenticate(ctx, second.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(logged out) error = %v", err)
	}

	wrongMetadata := RequestMetadata{SourceIP: "192.0.2.11", RequestID: prefix + "-wrong"}
	if _, err := service.Login(ctx, LoginInput{LoginRequest: LoginRequest{Username: administrator.Username, Password: "wrong"}, Metadata: wrongMetadata}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(wrong) error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET enabled = FALSE WHERE id = $1::uuid`, administrator.ID); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, err := service.Login(ctx, LoginInput{LoginRequest: LoginRequest{Username: administrator.Username, Password: bootstrapPassword}, Metadata: RequestMetadata{SourceIP: "192.0.2.11", RequestID: prefix + "-disabled"}}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(disabled) error = %v", err)
	}
	if _, err := service.Authenticate(ctx, third.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate(disabled user) error = %v", err)
	}
	var failureAudits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE request_id IN ($1, $2) AND result = 'failure' AND error_code = 'invalid_credentials'`, prefix+"-wrong", prefix+"-disabled").Scan(&failureAudits); err != nil || failureAudits != 2 {
		t.Fatalf("login failure audit count = %d, %v", failureAudits, err)
	}

	rateConfig := DefaultServiceConfig()
	rateConfig.LoginIPLimit = 1
	rateConfig.LoginUsernameLimit = 100
	rateService, err := NewService(pool, rateConfig)
	if err != nil {
		t.Fatalf("NewService(rate limit) error = %v", err)
	}
	rateIP := "198.51.100.240"
	firstUnknown := LoginInput{
		LoginRequest: LoginRequest{Username: "unknown-rate-a", Password: "wrong"},
		Metadata:     RequestMetadata{SourceIP: rateIP, RequestID: prefix + "-rate-first"},
	}
	if _, err := rateService.Login(ctx, firstUnknown); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first limited-source Login() error = %v", err)
	}
	secondUsername := "unknown-rate-b"
	_, rateErr := rateService.Login(ctx, LoginInput{
		LoginRequest: LoginRequest{Username: secondUsername, Password: "wrong"},
		Metadata:     RequestMetadata{SourceIP: rateIP, RequestID: prefix + "-rate-second"},
	})
	var limited *RateLimitError
	if !errors.As(rateErr, &limited) {
		t.Fatalf("second limited-source Login() error = %v, want RateLimitError", rateErr)
	}
	secondKey := loginRateKey("username", secondUsername)
	var secondUsernameKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM login_rate_limits WHERE scope = 'username' AND key_hash = $1`, secondKey[:]).Scan(&secondUsernameKeys); err != nil || secondUsernameKeys != 0 {
		t.Fatalf("blocked IP created username key: count=%d error=%v", secondUsernameKeys, err)
	}

	concurrentConfig := DefaultServiceConfig()
	concurrentConfig.LoginIPLimit = 100
	concurrentConfig.LoginUsernameLimit = 100
	concurrentService, err := NewService(pool, concurrentConfig)
	if err != nil {
		t.Fatalf("NewService(concurrent rate limit) error = %v", err)
	}
	const concurrentAttempts = 16
	concurrentIP := "198.51.100.241"
	concurrentUsername := "unknown-rate-concurrent"
	startConcurrent := make(chan struct{})
	concurrentErrors := make(chan error, concurrentAttempts)
	var concurrentWait sync.WaitGroup
	for range concurrentAttempts {
		concurrentWait.Add(1)
		go func() {
			defer concurrentWait.Done()
			<-startConcurrent
			concurrentErrors <- concurrentService.consumeLoginRateLimits(ctx, concurrentUsername, RequestMetadata{
				SourceIP: concurrentIP, RequestID: prefix + "-rate-concurrent",
			})
		}()
	}
	close(startConcurrent)
	concurrentWait.Wait()
	close(concurrentErrors)
	for err := range concurrentErrors {
		if err != nil {
			t.Fatalf("concurrent login rate-limit consume error = %v", err)
		}
	}
	for scope, value := range map[string]string{"source_ip": concurrentIP, "username": concurrentUsername} {
		key := loginRateKey(scope, value)
		var attempts int
		var monotonic bool
		if err := pool.QueryRow(ctx, `
			SELECT attempt_count, updated_at >= window_started_at
			FROM login_rate_limits
			WHERE scope = $1 AND key_hash = $2
		`, scope, key[:]).Scan(&attempts, &monotonic); err != nil {
			t.Fatalf("read concurrent %s rate limit: %v", scope, err)
		}
		if attempts != concurrentAttempts || !monotonic {
			t.Fatalf("concurrent %s rate limit attempts/monotonic = %d/%v, want %d/true", scope, attempts, monotonic, concurrentAttempts)
		}
	}

}

func TestServiceConfigFrozenBounds(t *testing.T) {
	for _, config := range []ServiceConfig{
		{SessionTTL: 7*24*time.Hour + time.Second, MaxSessions: 5, RevokedRetention: time.Hour},
		{SessionTTL: time.Hour, MaxSessions: 21, RevokedRetention: time.Hour},
		{SessionTTL: time.Hour, MaxSessions: 5, RevokedRetention: time.Minute},
	} {
		if err := config.Validate(); err == nil {
			t.Fatalf("ServiceConfig.Validate() accepted %#v", config)
		}
	}
}
