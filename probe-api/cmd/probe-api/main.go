package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"probe-api/internal/access"
	"probe-api/internal/agent"
	"probe-api/internal/auditlog"
	"probe-api/internal/auth"
	"probe-api/internal/config"
	"probe-api/internal/database"
	"probe-api/internal/httpapi"
	"probe-api/internal/ingresstls"
	"probe-api/internal/maintenance"
	"probe-api/internal/migrate"
	"probe-api/internal/nodemanagement"
	"probe-api/internal/panel"
	"probe-api/internal/probequery"
	"probe-api/internal/probetarget"
	"probe-api/internal/usermanagement"
)

var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "version":
		fmt.Printf("probe-api %s commit=%s built_at=%s\n", version, commit, builtAt)
		return nil
	case "config":
		return runConfigCommand(args)
	case "serve", "migrate", "user":
		// handled below
	default:
		return usageError()
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateDatabase(); err != nil {
		return err
	}
	if args[0] == "serve" {
		if err := cfg.ValidateServe(); err != nil {
			return err
		}
	}

	logger := newLogger(cfg.LogLevel)
	ctx := context.Background()
	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	if args[0] == "migrate" {
		if len(args) != 2 {
			return errors.New("usage: probe-api migrate <up|status>")
		}
		switch args[1] {
		case "up":
			applied, migrateErr := migrate.Up(ctx, pool)
			if migrateErr != nil {
				return fmt.Errorf("apply migrations: %w", migrateErr)
			}
			logger.Info("database migrations complete", "applied", applied)
			return nil
		case "status":
			statuses, statusErr := migrate.Status(ctx, pool)
			if statusErr != nil {
				return fmt.Errorf("read migration status: %w", statusErr)
			}
			for _, item := range statuses {
				state := "pending"
				if item.Applied {
					state = "applied"
				}
				fmt.Printf("%06d %-8s %s\n", item.Version, state, item.Name)
			}
			return nil
		default:
			return errors.New("usage: probe-api migrate <up|status>")
		}
	}

	readiness := database.NewReadiness(pool)
	if err := readiness.Ready(ctx); err != nil {
		return fmt.Errorf("API is not ready: %w", err)
	}
	authService, err := auth.NewService(pool, auth.ServiceConfig{
		SessionTTL:          cfg.SessionTTL,
		MaxSessions:         int(cfg.SessionMaxPerUser),
		RevokedRetention:    cfg.SessionRetention,
		LoginIPLimit:        int(cfg.LoginIPLimit),
		LoginIPWindow:       cfg.LoginIPWindow,
		LoginUsernameLimit:  int(cfg.LoginUsernameLimit),
		LoginUsernameWindow: cfg.LoginUsernameWindow,
	})
	if err != nil {
		return fmt.Errorf("create authentication service: %w", err)
	}
	if args[0] == "user" {
		if len(args) != 3 || args[1] != "bootstrap-admin" {
			return errors.New("usage: probe-api user bootstrap-admin <username>")
		}
		return bootstrapAdministrator(ctx, authService, args[2], os.Stdin, os.Stdout, os.Stderr)
	}
	if len(args) != 1 {
		return usageError()
	}

	panelService, err := panel.NewService(pool, cfg.NodeOfflineAfter)
	if err != nil {
		return fmt.Errorf("create panel service: %w", err)
	}
	retention, err := maintenance.NewBasicMetricRetention(pool, time.Minute)
	if err != nil {
		return fmt.Errorf("create basic metric retention job: %w", err)
	}
	probeQueryService, err := probequery.NewService(pool)
	if err != nil {
		return fmt.Errorf("create probe query service: %w", err)
	}
	probeMaintenance, err := maintenance.NewProbeResultMaintenance(pool, time.Minute)
	if err != nil {
		return fmt.Errorf("create probe result maintenance job: %w", err)
	}
	dailyCleanup, err := maintenance.NewDailyCleanup(pool, maintenance.DailyCleanupConfig{
		Interval:                24 * time.Hour,
		RevokedSessionRetention: cfg.SessionRetention,
		LoginIPWindow:           cfg.LoginIPWindow,
		LoginUsernameWindow:     cfg.LoginUsernameWindow,
	})
	if err != nil {
		return fmt.Errorf("create daily cleanup job: %w", err)
	}
	nodeManagementService, err := nodemanagement.NewService(pool, cfg.NodeOfflineAfter)
	if err != nil {
		return fmt.Errorf("create node management service: %w", err)
	}
	userManagementService := usermanagement.NewService(pool)
	auditLogService := auditlog.NewService(pool)
	return serve(cfg, logger, readiness, agent.NewService(pool), authService, panelService,
		probeQueryService, probetarget.NewService(pool), nodeManagementService,
		userManagementService, auditLogService, retention, probeMaintenance, dailyCleanup)
}

func serve(
	cfg config.Config,
	logger *slog.Logger,
	db httpapi.Database,
	agentService httpapi.AgentService,
	authService *auth.Service,
	panelService httpapi.PanelService,
	probeQueryService httpapi.ProbeQueryService,
	probeTargetAdminService httpapi.ProbeTargetAdminService,
	nodeManagementService httpapi.NodeManagementService,
	userManagementService httpapi.UserManagementService,
	auditLogService httpapi.AuditLogService,
	retention *maintenance.BasicMetricRetention,
	probeMaintenance *maintenance.ProbeResultMaintenance,
	dailyCleanup *maintenance.DailyCleanup,
) error {
	server := httpapi.NewServer(cfg, logger, db,
		httpapi.WithAgentService(agentService),
		httpapi.WithAuthService(authService),
		httpapi.WithPanelService(panelService),
		httpapi.WithProbeQueryService(probeQueryService),
		httpapi.WithProbeTargetAdminService(probeTargetAdminService),
		httpapi.WithNodeManagementService(nodeManagementService),
		httpapi.WithUserManagementService(userManagementService),
		httpapi.WithAuditLogService(auditLogService),
	)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go retention.Run(ctx, func(err error) {
		logger.Error("basic metric retention failed", "error", err)
	})
	go probeMaintenance.Run(ctx, func(err error) {
		logger.Error("probe result maintenance failed", "error", err)
	})
	go dailyCleanup.Run(ctx, func(err error) {
		logger.Error("daily authentication and idempotency cleanup failed", "error", err)
	})

	errCh := make(chan error, 1)
	go func() {
		logger.Info("probe API listening", "address", cfg.ListenAddress, "version", version)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, httpapi.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return nil
}

func newLogger(levelName string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(levelName) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func usageError() error {
	return errors.New("usage: probe-api <serve|migrate up|migrate status|user bootstrap-admin <username>|config validate-admin-allowlist <path>|config validate-ingress-tls <domain PANEL ADMIN AGENT|ip ADDRESS>|version>")
}

func runConfigCommand(args []string) error {
	if len(args) == 3 && args[1] == "validate-admin-allowlist" {
		allowlist, err := access.LoadNginxGeoAllowlist(args[2])
		if err != nil {
			return fmt.Errorf("validate management allowlist: %w", err)
		}
		fmt.Printf("management allowlist valid: %d entries\n", allowlist.Len())
		return nil
	}
	if len(args) >= 3 && args[1] == "validate-ingress-tls" {
		paths := ingresstls.ProductionPaths()
		switch args[2] {
		case "domain":
			if len(args) != 6 {
				return ingressTLSUsageError()
			}
			if err := ingresstls.ValidateDomain(ingresstls.DomainConfig{
				Paths: paths, PanelHost: args[3], AdminHost: args[4], AgentHost: args[5],
			}); err != nil {
				return fmt.Errorf("validate domain ingress TLS: %w", err)
			}
		case "ip":
			if len(args) != 4 {
				return ingressTLSUsageError()
			}
			if err := ingresstls.ValidateIP(ingresstls.IPConfig{Paths: paths, Address: args[3]}); err != nil {
				return fmt.Errorf("validate IP ingress TLS: %w", err)
			}
		default:
			return ingressTLSUsageError()
		}
		fmt.Printf("ingress TLS valid: %s\n", args[2])
		return nil
	}
	return errors.New("usage: probe-api config <validate-admin-allowlist PATH|validate-ingress-tls domain PANEL ADMIN AGENT|validate-ingress-tls ip ADDRESS>")
}

func ingressTLSUsageError() error {
	return errors.New("usage: probe-api config validate-ingress-tls <domain PANEL ADMIN AGENT|ip ADDRESS>")
}
