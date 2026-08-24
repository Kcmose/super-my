package usermanagement

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/agent"
	"probe-api/internal/auth"
	"probe-api/internal/nodemanagement"
)

func TestServiceIntegrationConcurrencyAndAuditAtomicity(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("PROBE_API_INTEGRATION_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("PROBE_API_INTEGRATION_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() error = %v", err)
	}
	if poolConfig.MaxConns < 6 {
		poolConfig.MaxConns = 6
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig() error = %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("database ping: %v", err)
	}

	prefix := "um-int-" + strings.ReplaceAll(mustIntegrationUUID(t), "-", "")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		// Request IDs and object names are unique to this run. Cleanup does not
		// assume that the integration database contains no unrelated fixtures.
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM audit_logs WHERE request_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM nodes WHERE display_name LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE username LIKE $1`, prefix+"%")
	})

	service := NewService(pool)

	t.Run("concurrent changes preserve a usable administrator", func(t *testing.T) {
		first := seedIntegrationUser(t, ctx, pool, prefix+"-last-admin-a", auth.RoleAdmin, true)
		second := seedIntegrationUser(t, ctx, pool, prefix+"-last-admin-b", auth.RoleAdmin, true)

		type result struct {
			err error
		}
		results := make(chan result, 2)
		start := make(chan struct{})
		disable := false
		for index, identity := range []auth.Identity{first, second} {
			index := index
			identity := identity
			go func() {
				<-start
				_, updateErr := service.Update(ctx, identity, identity.User.ID, UpdateRequest{Enabled: &disable}, Metadata{
					SourceIP: "192.0.2.40", RequestID: prefix + "-last-admin-" + string(rune('a'+index)),
				})
				results <- result{err: updateErr}
			}()
		}
		close(start)

		successes := 0
		lastAdminRejections := 0
		for range 2 {
			operation := <-results
			switch {
			case operation.err == nil:
				successes++
			case errors.Is(operation.err, ErrLastUsableAdmin):
				lastAdminRejections++
			default:
				t.Fatalf("concurrent administrator update error = %v", operation.err)
			}
		}

		var administratorsAfter, fixtureAdministratorsAfter int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND enabled`).Scan(&administratorsAfter); err != nil {
			t.Fatalf("count administrators after concurrent updates: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM users
			WHERE role = 'admin' AND enabled AND id IN ($1::uuid, $2::uuid)
		`, first.User.ID, second.User.ID).Scan(&fixtureAdministratorsAfter); err != nil {
			t.Fatalf("count fixture administrators after concurrent updates: %v", err)
		}
		if administratorsAfter < 1 {
			t.Fatal("concurrent updates removed every enabled administrator")
		}
		// When no unrelated enabled administrator remains, this pair is the
		// actual last-admin boundary and exactly one operation must be rejected.
		// If the shared integration database already has another administrator,
		// disabling both fixtures is valid and the global invariant still holds.
		if administratorsAfter == fixtureAdministratorsAfter && (successes != 1 || lastAdminRejections != 1 || fixtureAdministratorsAfter != 1) {
			t.Fatalf("isolated last-admin results successes/rejections = %d/%d, want 1/1", successes, lastAdminRejections)
		}
	})

	t.Run("management lock does not deadlock a login-style row update", func(t *testing.T) {
		administrator := seedIntegrationUser(t, ctx, pool, prefix+"-deadlock-admin", auth.RoleAdmin, true)
		targetAdministrator := seedIntegrationUser(t, ctx, pool, prefix+"-deadlock-target-admin", auth.RoleAdmin, true)

		loginTx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin login-style transaction: %v", err)
		}
		defer func() { _ = loginTx.Rollback(context.Background()) }()
		var lockedID string
		if err := loginTx.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1::uuid FOR UPDATE`, administrator.User.ID).Scan(&lockedID); err != nil {
			t.Fatalf("lock login user row: %v", err)
		}

		operationCtx, operationCancel := context.WithTimeout(ctx, 15*time.Second)
		defer operationCancel()
		operationResult := make(chan error, 1)
		updatedUsername := prefix + "-deadlock-target-admin-updated"
		go func() {
			_, updateErr := service.Update(operationCtx, administrator, targetAdministrator.User.ID, UpdateRequest{Username: &updatedUsername}, Metadata{
				SourceIP: "192.0.2.41", RequestID: prefix + "-deadlock-update",
			})
			operationResult <- updateErr
		}()

		waitForIntegrationAdvisoryLock(t, operationCtx, pool, userAdministrationLock)
		if _, err := loginTx.Exec(operationCtx, `UPDATE users SET last_login_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, administrator.User.ID); err != nil {
			if hasPostgresCode(err, "40P01") {
				t.Fatalf("login-style user update deadlocked with user management: %v", err)
			}
			t.Fatalf("update login timestamp: %v", err)
		}
		if err := loginTx.Commit(operationCtx); err != nil {
			if hasPostgresCode(err, "40P01") {
				t.Fatalf("login-style transaction deadlocked with user management: %v", err)
			}
			t.Fatalf("commit login-style transaction: %v", err)
		}

		select {
		case err := <-operationResult:
			if hasPostgresCode(err, "40P01") {
				t.Fatalf("user management deadlocked with login-style update: %v", err)
			}
			if err != nil {
				t.Fatalf("user management update error = %v", err)
			}
		case <-operationCtx.Done():
			t.Fatalf("user management did not finish after login-style transaction released the row: %v", operationCtx.Err())
		}
	})

	t.Run("deleted actor identity remains an immutable audit snapshot", func(t *testing.T) {
		_ = seedIntegrationUser(t, ctx, pool, prefix+"-snapshot-guard", auth.RoleAdmin, true)
		actor := seedIntegrationUser(t, ctx, pool, prefix+"-snapshot-actor", auth.RoleAdmin, true)
		requestID := prefix + "-snapshot-delete"
		if err := service.Delete(ctx, actor, actor.User.ID, Metadata{SourceIP: "2001:db8::42", RequestID: requestID}); err != nil {
			t.Fatalf("Delete(self) error = %v", err)
		}

		var actorID, actorUsername string
		if err := pool.QueryRow(ctx, `
			SELECT actor_user_id::text, actor_username
			FROM audit_logs
			WHERE request_id = $1 AND action = 'user.delete'
		`, requestID).Scan(&actorID, &actorUsername); err != nil {
			t.Fatalf("read deletion audit snapshot: %v", err)
		}
		if actorID != actor.User.ID || actorUsername != actor.User.Username {
			t.Fatalf("deletion audit actor snapshot = %q/%q, want %q/%q", actorID, actorUsername, actor.User.ID, actor.User.Username)
		}
		var actorStillExists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1::uuid)`, actor.User.ID).Scan(&actorStillExists); err != nil {
			t.Fatalf("check deleted actor: %v", err)
		}
		if actorStillExists {
			t.Fatal("self-deleted audit actor still exists")
		}
	})

	t.Run("user writes and password-safe audits commit or roll back together", func(t *testing.T) {
		actor := seedIntegrationUser(t, ctx, pool, prefix+"-atomic-admin", auth.RoleAdmin, true)
		plaintext := prefix + "-primary-password!"
		createRequestID := prefix + "-atomic-create"
		created, err := service.Create(ctx, actor, CreateRequest{
			Username: prefix + "-atomic-admin-created", Password: plaintext, Role: auth.RoleAdmin, Enabled: true,
		}, Metadata{SourceIP: "192.0.2.42", RequestID: createRequestID})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		storedHash := readIntegrationPasswordHash(t, ctx, pool, created.ID)
		if storedHash == plaintext {
			t.Fatal("created user password was stored in plaintext")
		}
		if matched, err := auth.VerifyPassword(storedHash, plaintext); err != nil || !matched {
			t.Fatalf("VerifyPassword(created user) = %v, %v", matched, err)
		}
		createAudit := readIntegrationAuditSummary(t, ctx, pool, createRequestID)
		assertAuditOmitsSecrets(t, createAudit, plaintext, storedHash)

		updatedPlaintext := prefix + "-updated-password!"
		updateRequestID := prefix + "-atomic-update"
		if _, err := service.Update(ctx, actor, created.ID, UpdateRequest{Password: &updatedPlaintext}, Metadata{
			SourceIP: "192.0.2.42", RequestID: updateRequestID,
		}); err != nil {
			t.Fatalf("Update(password) error = %v", err)
		}
		updatedHash := readIntegrationPasswordHash(t, ctx, pool, created.ID)
		if updatedHash == storedHash || updatedHash == updatedPlaintext {
			t.Fatal("password update did not replace the stored one-way hash")
		}
		if matched, err := auth.VerifyPassword(updatedHash, updatedPlaintext); err != nil || !matched {
			t.Fatalf("VerifyPassword(updated user) = %v, %v", matched, err)
		}
		updateAudit := readIntegrationAuditSummary(t, ctx, pool, updateRequestID)
		assertAuditOmitsSecrets(t, updateAudit, plaintext, updatedPlaintext, storedHash, updatedHash)

		rollbackUsername := prefix + "-audit-failure-rollback"
		rollbackRequestID := prefix + "-atomic-rollback"
		_, err = service.Create(ctx, actor, CreateRequest{
			Username: rollbackUsername, Password: prefix + "-rollback-password!", Role: auth.RoleAdmin, Enabled: true,
		}, Metadata{SourceIP: "not-an-ip", RequestID: rollbackRequestID})
		if err == nil {
			t.Fatal("Create() with invalid audit metadata unexpectedly succeeded")
		}
		var userCount, auditCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username = $1`, rollbackUsername).Scan(&userCount); err != nil {
			t.Fatalf("count rolled-back user: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE request_id = $1`, rollbackRequestID).Scan(&auditCount); err != nil {
			t.Fatalf("count rolled-back audit: %v", err)
		}
		if userCount != 0 || auditCount != 0 {
			t.Fatalf("failed audited write left user/audit rows = %d/%d", userCount, auditCount)
		}
	})

	t.Run("one-time tokens never enter audits and rotate atomically", func(t *testing.T) {
		actor := seedIntegrationUser(t, ctx, pool, prefix+"-token-admin", auth.RoleAdmin, true)
		nodeService, err := nodemanagement.NewService(pool, 45*time.Second)
		if err != nil {
			t.Fatalf("nodemanagement.NewService() error = %v", err)
		}
		node, err := nodeService.Create(ctx, actor, nodemanagement.CreateRequest{DisplayName: prefix + "-token-node"}, nodemanagement.Metadata{
			SourceIP: "192.0.2.43", RequestID: prefix + "-token-node-create",
		})
		if err != nil {
			t.Fatalf("Create(node) error = %v", err)
		}

		enrollmentRequestID := prefix + "-enrollment-token-create"
		enrollment, err := nodeService.CreateEnrollmentToken(ctx, actor, node.NodeID, nodemanagement.CreateEnrollmentTokenRequest{ExpiresInSeconds: 120}, nodemanagement.Metadata{
			SourceIP: "192.0.2.43", RequestID: enrollmentRequestID,
		})
		if err != nil {
			t.Fatalf("CreateEnrollmentToken() error = %v", err)
		}
		var enrollmentHash string
		if err := pool.QueryRow(ctx, `SELECT token_hash FROM enrollment_tokens WHERE node_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, node.NodeID).Scan(&enrollmentHash); err != nil {
			t.Fatalf("read enrollment token hash: %v", err)
		}
		if enrollmentHash != agent.HashOpaqueToken(enrollment.EnrollmentToken) {
			t.Fatal("enrollment token was not stored as the expected one-way hash")
		}
		assertAuditOmitsSecrets(t, readIntegrationAuditSummary(t, ctx, pool, enrollmentRequestID), enrollment.EnrollmentToken, enrollmentHash)

		replacementRequestID := prefix + "-enrollment-token-replace"
		replacement, err := nodeService.CreateEnrollmentToken(ctx, actor, node.NodeID, nodemanagement.CreateEnrollmentTokenRequest{ExpiresInSeconds: 120}, nodemanagement.Metadata{
			SourceIP: "192.0.2.43", RequestID: replacementRequestID,
		})
		if err != nil {
			t.Fatalf("replacement CreateEnrollmentToken() error = %v", err)
		}
		var activeEnrollmentTokens int
		var firstTokenInvalidated bool
		var replacementHash string
		if err := pool.QueryRow(ctx, `
			SELECT
				count(*) FILTER (WHERE used_at IS NULL AND expires_at > CURRENT_TIMESTAMP),
				bool_or(token_hash = $2 AND used_at IS NOT NULL),
				COALESCE(max(token_hash) FILTER (WHERE used_at IS NULL), '')
			FROM enrollment_tokens
			WHERE node_id = $1::uuid
		`, node.NodeID, enrollmentHash).Scan(&activeEnrollmentTokens, &firstTokenInvalidated, &replacementHash); err != nil {
			t.Fatalf("read replaced enrollment token state: %v", err)
		}
		if activeEnrollmentTokens != 1 || !firstTokenInvalidated || replacementHash != agent.HashOpaqueToken(replacement.EnrollmentToken) {
			t.Fatalf("replacement token state active=%d first_invalidated=%t replacement_hash=%t",
				activeEnrollmentTokens, firstTokenInvalidated, replacementHash == agent.HashOpaqueToken(replacement.EnrollmentToken))
		}
		assertAuditOmitsSecrets(t, readIntegrationAuditSummary(t, ctx, pool, replacementRequestID),
			enrollment.EnrollmentToken, enrollmentHash, replacement.EnrollmentToken, replacementHash)

		rotateRequestID := prefix + "-agent-token-rotate"
		rotated, err := nodeService.RotateAgentToken(ctx, actor, node.NodeID, nodemanagement.Metadata{
			SourceIP: "192.0.2.43", RequestID: rotateRequestID,
		})
		if err != nil {
			t.Fatalf("RotateAgentToken() error = %v", err)
		}
		var agentHash string
		if err := pool.QueryRow(ctx, `SELECT token_hash FROM agent_tokens WHERE node_id = $1::uuid AND revoked_at IS NULL`, node.NodeID).Scan(&agentHash); err != nil {
			t.Fatalf("read active Agent token hash: %v", err)
		}
		if agentHash != agent.HashOpaqueToken(rotated.AgentToken) {
			t.Fatal("Agent token was not stored as the expected one-way hash")
		}
		assertAuditOmitsSecrets(t, readIntegrationAuditSummary(t, ctx, pool, rotateRequestID), rotated.AgentToken, agentHash)

		rollbackRequestID := prefix + "-agent-token-rollback"
		if _, err := nodeService.RotateAgentToken(ctx, actor, node.NodeID, nodemanagement.Metadata{
			SourceIP: "invalid-source", RequestID: rollbackRequestID,
		}); err == nil {
			t.Fatal("RotateAgentToken() with invalid audit metadata unexpectedly succeeded")
		}
		var activeTokens, rollbackAudits int
		var survivingHash string
		if err := pool.QueryRow(ctx, `
			SELECT count(*), COALESCE(max(token_hash), '')
			FROM agent_tokens
			WHERE node_id = $1::uuid AND revoked_at IS NULL
		`, node.NodeID).Scan(&activeTokens, &survivingHash); err != nil {
			t.Fatalf("read active Agent tokens after rollback: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE request_id = $1`, rollbackRequestID).Scan(&rollbackAudits); err != nil {
			t.Fatalf("count rolled-back token audits: %v", err)
		}
		if activeTokens != 1 || survivingHash != agentHash || rollbackAudits != 0 {
			t.Fatalf("failed token rotation left active/hash/audit = %d/%t/%d", activeTokens, survivingHash == agentHash, rollbackAudits)
		}

		if _, err := pool.Exec(ctx, `UPDATE nodes SET enabled = FALSE WHERE id = $1::uuid`, node.NodeID); err != nil {
			t.Fatalf("disable token test node: %v", err)
		}
		disabledRequestID := prefix + "-disabled-enrollment-token"
		if _, err := nodeService.CreateEnrollmentToken(ctx, actor, node.NodeID,
			nodemanagement.CreateEnrollmentTokenRequest{ExpiresInSeconds: 120},
			nodemanagement.Metadata{SourceIP: "192.0.2.43", RequestID: disabledRequestID}); !errors.Is(err, nodemanagement.ErrConflict) {
			t.Fatalf("CreateEnrollmentToken(disabled node) error = %v, want ErrConflict", err)
		}
		var disabledAudits int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE request_id = $1`, disabledRequestID).Scan(&disabledAudits); err != nil {
			t.Fatalf("count disabled enrollment audits: %v", err)
		}
		if disabledAudits != 0 {
			t.Fatalf("disabled enrollment token request wrote %d audits", disabledAudits)
		}
	})
}

func seedIntegrationUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username string, role auth.Role, enabled bool) auth.Identity {
	t.Helper()
	userID := mustIntegrationUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role, enabled)
		VALUES ($1::uuid, $2, $3, $4, $5)
	`, userID, username, auth.DummyPasswordHash(), role, enabled); err != nil {
		t.Fatalf("seed integration user %q: %v", username, err)
	}
	return auth.Identity{User: auth.User{ID: userID, Username: username, Role: role, Enabled: enabled}}
}

func mustIntegrationUUID(t *testing.T) string {
	t.Helper()
	value, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID() error = %v", err)
	}
	return value
}

func waitForIntegrationAdvisoryLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, lockID int64) {
	t.Helper()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire advisory-lock observer: %v", err)
	}
	defer connection.Release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var acquired bool
		if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, lockID).Scan(&acquired); err != nil {
			t.Fatalf("observe user administration lock: %v", err)
		}
		if !acquired {
			return
		}
		var unlocked bool
		if err := connection.QueryRow(ctx, `SELECT pg_advisory_unlock($1::bigint)`, lockID).Scan(&unlocked); err != nil || !unlocked {
			t.Fatalf("release advisory-lock observer: %v, unlocked=%v", err, unlocked)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("user management did not acquire its transaction advisory lock")
}

func hasPostgresCode(err error, code string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == code
}

func readIntegrationPasswordHash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var passwordHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1::uuid`, userID).Scan(&passwordHash); err != nil {
		t.Fatalf("read password hash: %v", err)
	}
	return passwordHash
}

func readIntegrationAuditSummary(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID string) string {
	t.Helper()
	var summary string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(before_summary::text, '') || COALESCE(after_summary::text, '')
		FROM audit_logs
		WHERE request_id = $1
	`, requestID).Scan(&summary); err != nil {
		t.Fatalf("read audit summary for %q: %v", requestID, err)
	}
	return summary
}

func assertAuditOmitsSecrets(t *testing.T, summary string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(summary, secret) {
			t.Fatalf("audit summary contains a password, token, or stored secret: %q", summary)
		}
	}
}
