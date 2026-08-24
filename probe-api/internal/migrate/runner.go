package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/migrations"
)

const advisoryLockKey int64 = 5787771289713006919

type Migration struct {
	Version  int64
	Name     string
	Checksum string
	SQL      string
	Applied  bool
}

type appliedMigration struct {
	Name     string
	Checksum string
}

func Up(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return 0, fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey) }()

	if err := ensureTable(ctx, conn); err != nil {
		return 0, err
	}
	items, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return 0, err
	}

	expected := make(map[int64]struct{}, len(items))
	for _, item := range items {
		expected[item.Version] = struct{}{}
	}
	for version := range applied {
		if _, ok := expected[version]; !ok {
			return 0, fmt.Errorf("database migration %d is unknown to this binary", version)
		}
	}

	count := 0
	for _, item := range items {
		if actual, ok := applied[item.Version]; ok {
			if actual.Name != item.Name || actual.Checksum != item.Checksum {
				return count, fmt.Errorf("migration %d metadata mismatch", item.Version)
			}
			continue
		}

		tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return count, fmt.Errorf("begin migration %d: %w", item.Version, err)
		}
		if _, err := tx.Exec(ctx, item.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("execute migration %d: %w", item.Version, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
			item.Version, item.Name, item.Checksum,
		); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("record migration %d: %w", item.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return count, fmt.Errorf("commit migration %d: %w", item.Version, err)
		}
		count++
	}
	return count, nil
}

func Status(ctx context.Context, pool *pgxpool.Pool) ([]Migration, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if err := ensureTable(ctx, conn); err != nil {
		return nil, err
	}
	items, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}
	expected := make(map[int64]struct{}, len(items))
	for index := range items {
		expected[items[index].Version] = struct{}{}
		actual, ok := applied[items[index].Version]
		if ok && (actual.Name != items[index].Name || actual.Checksum != items[index].Checksum) {
			return nil, fmt.Errorf("migration %d metadata mismatch", items[index].Version)
		}
		items[index].Applied = ok
	}
	for version := range applied {
		if _, ok := expected[version]; !ok {
			return nil, fmt.Errorf("database migration %d is unknown to this binary", version)
		}
	}
	return items, nil
}

// RequireCurrent performs a read-only fail-closed check that every migration
// embedded in this binary is applied with the expected name and checksum and
// that the database does not contain a version unknown to this binary.
func RequireCurrent(ctx context.Context, pool *pgxpool.Pool) error {
	items, err := loadMigrations()
	if err != nil {
		return err
	}
	var tableExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&tableExists); err != nil {
		return fmt.Errorf("check schema_migrations table: %w", err)
	}
	if !tableExists {
		return fmt.Errorf("database schema is not migrated")
	}

	applied := make(map[int64]appliedMigration)
	rows, err := pool.Query(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("query migration readiness: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("scan migration readiness: %w", err)
		}
		applied[version] = appliedMigration{Name: name, Checksum: checksum}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migration readiness: %w", err)
	}

	expected := make(map[int64]Migration, len(items))
	for _, item := range items {
		expected[item.Version] = item
		actual, ok := applied[item.Version]
		if !ok {
			return fmt.Errorf("migration %d is pending", item.Version)
		}
		if actual.Name != item.Name || actual.Checksum != item.Checksum {
			return fmt.Errorf("migration %d metadata mismatch", item.Version)
		}
	}
	for version := range applied {
		if _, ok := expected[version]; !ok {
			return fmt.Errorf("database migration %d is unknown to this binary", version)
		}
	}
	return nil
}

func ensureTable(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CONSTRAINT schema_migrations_name_not_blank CHECK (btrim(name) <> ''),
			CONSTRAINT schema_migrations_checksum_not_blank CHECK (btrim(checksum) <> '')
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

func appliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[int64]appliedMigration, error) {
	rows, err := conn.Query(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]appliedMigration)
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return nil, fmt.Errorf("scan migration: %w", err)
		}
		applied[version] = appliedMigration{Name: name, Checksum: checksum}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migrations: %w", err)
	}
	return applied, nil
}

func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	items := make([]Migration, 0)
	seen := make(map[int64]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if _, exists := seen[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %d", version)
		}
		seen[version] = struct{}{}
		body, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(body)
		items = append(items, Migration{
			Version:  version,
			Name:     strings.TrimSuffix(parts[1], ".up.sql"),
			Checksum: hex.EncodeToString(sum[:]),
			SQL:      string(body),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
	return items, nil
}
