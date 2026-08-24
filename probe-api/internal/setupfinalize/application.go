package setupfinalize

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"probe-api/internal/auth"
	"probe-api/internal/migrate"
)

type DatabaseConfig struct {
	Name     string
	Username string
	Password []byte
}

type ApplicationBootstrapper interface {
	MigrateAndBootstrap(context.Context, DatabaseConfig, string, []byte) error
}

type PostgresApplicationBootstrapper struct{}

func (PostgresApplicationBootstrapper) MigrateAndBootstrap(ctx context.Context, database DatabaseConfig, administrator string, administratorPassword []byte) error {
	defer clear(database.Password)
	defer clear(administratorPassword)
	poolConfig, err := pgxpool.ParseConfig("postgresql://127.0.0.1:5432/" + database.Name + "?sslmode=disable")
	if err != nil {
		return errors.New("construct local application database configuration")
	}
	poolConfig.ConnConfig.User = database.Username
	poolConfig.ConnConfig.Password = string(database.Password)
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	poolConfig.MaxConns = 4
	poolConfig.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("open local application database")
	}
	defer pool.Close()
	pingContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		return errors.New("local application database ping failed")
	}
	if _, err := migrate.Up(ctx, pool); err != nil {
		return fmt.Errorf("apply application migrations: %w", err)
	}
	service, err := auth.NewService(pool, auth.DefaultServiceConfig())
	if err != nil {
		return errors.New("initialize administrator service")
	}
	requestID, err := newRequestID(time.Now())
	if err != nil {
		return err
	}
	password := append([]byte(nil), administratorPassword...)
	defer clear(password)
	if _, err := service.BootstrapAdmin(ctx, administrator, password, requestID); err != nil {
		if errors.Is(err, auth.ErrBootstrapUnavailable) {
			return errors.New("administrator bootstrap is unavailable because the database is not empty")
		}
		return errors.New("create first administrator")
	}
	return nil
}
