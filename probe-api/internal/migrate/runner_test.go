package migrate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	migrationfiles "probe-api/migrations"
)

func TestEmbeddedMigrationsAreOrdered(t *testing.T) {
	items, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one embedded migration")
	}
	for index := 1; index < len(items); index++ {
		if items[index-1].Version >= items[index].Version {
			t.Fatalf("migrations are not strictly ordered")
		}
	}
}

func TestAnonymousGuestMigrationDeletesLegacyAccountsBeforeConstrainingRoles(t *testing.T) {
	upBytes, err := migrationfiles.Files.ReadFile("000005_anonymous_guests_admin_accounts.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	downBytes, err := migrationfiles.Files.ReadFile("000005_anonymous_guests_admin_accounts.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	up := string(upBytes)
	down := string(downBytes)

	sessionDelete := strings.Index(up, "DELETE FROM sessions")
	viewerDelete := strings.Index(up, "DELETE FROM users")
	adminConstraint := strings.Index(up, "CHECK (role = 'admin')")
	if sessionDelete < 0 || viewerDelete <= sessionDelete || adminConstraint <= viewerDelete {
		t.Fatalf("up migration must delete viewer sessions and users before adding the admin-only constraint:\n%s", up)
	}
	if !strings.Contains(up, "role = 'viewer'") || strings.Contains(up, "UPDATE users") {
		t.Fatal("up migration must delete, not promote, legacy viewer accounts")
	}
	if !strings.Contains(down, "CHECK (role IN ('viewer', 'admin'))") ||
		!strings.Contains(down, "not recoverable") {
		t.Fatal("down migration must restore only the old constraint and document irreversible data deletion")
	}
}

func TestAnonymousGuestMigrationUpAndDownIntegration(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration test transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE users (
			id UUID PRIMARY KEY,
			role TEXT NOT NULL,
			CONSTRAINT users_role_valid CHECK (role IN ('viewer', 'admin'))
		) ON COMMIT DROP;
		CREATE TEMP TABLE sessions (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE
		) ON COMMIT DROP;
		INSERT INTO users (id, role) VALUES
			('11111111-1111-4111-8111-111111111111', 'admin'),
			('22222222-2222-4222-8222-222222222222', 'viewer');
		INSERT INTO sessions (id, user_id) VALUES
			('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', '11111111-1111-4111-8111-111111111111'),
			('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', '22222222-2222-4222-8222-222222222222');
	`); err != nil {
		t.Fatalf("seed temporary legacy schema: %v", err)
	}
	up, err := migrationfiles.Files.ReadFile("000005_anonymous_guests_admin_accounts.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(up)); err != nil {
		t.Fatalf("execute up migration: %v", err)
	}
	var users, sessions int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users after up: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions after up: %v", err)
	}
	if users != 1 || sessions != 1 {
		t.Fatalf("up migration retained users/sessions = %d/%d, want 1/1", users, sessions)
	}
	var constraint string
	if err := tx.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = to_regclass('pg_temp.users') AND conname = 'users_role_valid'
	`).Scan(&constraint); err != nil {
		t.Fatalf("read admin-only constraint: %v", err)
	}
	if !strings.Contains(constraint, "role = 'admin'") {
		t.Fatalf("up constraint = %q", constraint)
	}

	down, err := migrationfiles.Files.ReadFile("000005_anonymous_guests_admin_accounts.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("execute down migration: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, role)
		VALUES ('33333333-3333-4333-8333-333333333333', 'viewer')
	`); err != nil {
		t.Fatalf("down migration did not restore the legacy role constraint: %v", err)
	}
	var viewerUsers, viewerSessions int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'viewer'`).Scan(&viewerUsers); err != nil {
		t.Fatalf("count viewer users after down: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM sessions AS s
		JOIN users AS u ON u.id = s.user_id
		WHERE u.role = 'viewer'
	`).Scan(&viewerSessions); err != nil {
		t.Fatalf("count viewer sessions after down: %v", err)
	}
	if viewerUsers != 1 || viewerSessions != 0 {
		t.Fatalf("down migration restored deleted data: viewer users/sessions = %d/%d", viewerUsers, viewerSessions)
	}
}
