package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/migrate"
)

type Readiness struct {
	pool *pgxpool.Pool
}

func NewReadiness(pool *pgxpool.Pool) Readiness {
	return Readiness{pool: pool}
}

func (r Readiness) Ready(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err := migrate.RequireCurrent(ctx, r.pool); err != nil {
		return fmt.Errorf("database migration state: %w", err)
	}
	return nil
}
