package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"probe-api/internal/agentbootstrap"
	"probe-api/internal/config"
	"probe-api/internal/httpapi/respond"
)

var ErrServerClosed = http.ErrServerClosed

type Database interface {
	Ready(context.Context) error
}

type Server struct {
	httpServer *http.Server
}

type serverOptions struct {
	agentService            AgentService
	authService             AuthService
	panelService            PanelService
	probeQueryService       ProbeQueryService
	probeTargetAdminService ProbeTargetAdminService
	nodeManagementService   NodeManagementService
	userManagementService   UserManagementService
	auditLogService         AuditLogService
}

type Option func(*serverOptions)

func WithAgentService(service AgentService) Option {
	return func(options *serverOptions) {
		options.agentService = service
	}
}

func WithAuthService(service AuthService) Option {
	return func(options *serverOptions) {
		options.authService = service
	}
}

func WithPanelService(service PanelService) Option {
	return func(options *serverOptions) {
		options.panelService = service
	}
}

func WithProbeQueryService(service ProbeQueryService) Option {
	return func(options *serverOptions) {
		options.probeQueryService = service
	}
}

func WithProbeTargetAdminService(service ProbeTargetAdminService) Option {
	return func(options *serverOptions) {
		options.probeTargetAdminService = service
	}
}

func WithNodeManagementService(service NodeManagementService) Option {
	return func(options *serverOptions) {
		options.nodeManagementService = service
	}
}

func WithUserManagementService(service UserManagementService) Option {
	return func(options *serverOptions) {
		options.userManagementService = service
	}
}

func WithAuditLogService(service AuditLogService) Option {
	return func(options *serverOptions) {
		options.auditLogService = service
	}
}

func NewServer(cfg config.Config, logger *slog.Logger, db Database, options ...Option) *Server {
	settings := serverOptions{}
	for _, apply := range options {
		apply(&settings)
	}
	mux := http.NewServeMux()
	health := healthHandler{db: db, pingTimeout: cfg.DatabasePingTimeout}
	mux.HandleFunc("/internal/health/live", exactMethod(http.MethodGet, health.live))
	mux.HandleFunc("/internal/health/ready", exactMethod(http.MethodGet, health.ready))
	registerManagementAccessStatusRoute(mux, cfg.AdminAllowlist)
	if settings.agentService != nil {
		registerAgentRoutes(mux, logger, settings.agentService, cfg.MaxAgentBodyBytes, newAgentRateLimiters(cfg))
	}
	if settings.authService != nil {
		RegisterAuthRoutes(mux, logger, settings.authService, cfg.MaxPanelBodyBytes, cfg.AdminOrigin)
		registerSystemStatusRoute(mux, logger, db, settings.authService, cfg.DatabasePingTimeout)
	}
	if settings.panelService != nil {
		registerPanelRoutes(mux, logger, settings.panelService)
	}
	if settings.probeQueryService != nil {
		registerPanelProbeRoutes(mux, logger, settings.probeQueryService)
	}
	if settings.authService != nil && settings.probeTargetAdminService != nil {
		registerProbeTargetAdminRoutes(mux, logger, settings.probeTargetAdminService,
			settings.authService, cfg.MaxPanelBodyBytes, cfg.AdminOrigin)
	}
	if settings.authService != nil {
		installCommandGenerator := agentbootstrap.New(cfg.AgentPublicURL, cfg.AgentInstallerURL, cfg.AgentInstallCAPEM)
		RegisterAdminManagementRoutes(mux, logger,
			settings.nodeManagementService, settings.userManagementService, settings.auditLogService,
			settings.authService, cfg.MaxPanelBodyBytes, cfg.AdminOrigin, installCommandGenerator)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		respond.Error(w, http.StatusNotFound, "not_found", "route not found", requestIDFromContext(r.Context()))
	})

	var handler http.Handler = mux
	handler = managementCIDRMiddleware(cfg.AdminAllowlist, handler)
	handler = internalPeerMiddleware(handler)
	handler = recoveryMiddleware(logger, handler)
	handler = clientIPMiddleware(cfg.TrustedProxies, handler)
	handler = accessLogMiddleware(logger, handler)
	handler = securityHeadersMiddleware(handler)
	handler = requestIDMiddleware(handler)

	return &Server{httpServer: &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}}
}

func exactMethod(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			respond.Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", requestIDFromContext(r.Context()))
			return
		}
		handler(w, r)
	}
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

type healthHandler struct {
	db          Database
	pingTimeout time.Duration
}

func (h healthHandler) live(w http.ResponseWriter, _ *http.Request) {
	respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h healthHandler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.pingTimeout)
	defer cancel()
	if err := h.db.Ready(ctx); err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "not_ready", "database is not ready", requestIDFromContext(r.Context()))
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
