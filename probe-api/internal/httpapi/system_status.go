package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"probe-api/internal/auth"
	"probe-api/internal/httpapi/respond"
)

const systemAPIVersion = "v1"

type systemComponentStatus struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

type systemSecurityBoundary struct {
	ManagementIPAllowlistEnforced bool `json:"management_ip_allowlist_enforced"`
	AdministratorSessionRequired  bool `json:"administrator_session_required"`
	AdminWriteCSRFRequired        bool `json:"admin_write_csrf_required"`
	RemoteOperationsEnabled       bool `json:"remote_operations_enabled"`
}

type systemStatusResponse struct {
	Status           string                 `json:"status"`
	CheckedAt        time.Time              `json:"checked_at"`
	API              systemComponentStatus  `json:"api"`
	Database         systemComponentStatus  `json:"database"`
	Agent            systemComponentStatus  `json:"agent"`
	SecurityBoundary systemSecurityBoundary `json:"security_boundary"`
}

type systemStatusHandler struct {
	database            Database
	pingTimeout         time.Duration
	agentRuntimeEnabled bool
	now                 func() time.Time
}

func registerSystemStatusRoute(
	mux *http.ServeMux,
	logger *slog.Logger,
	database Database,
	authService AuthService,
	pingTimeout time.Duration,
	agentRuntimeEnabled bool,
) {
	if mux == nil || logger == nil || authService == nil {
		return
	}
	if pingTimeout <= 0 {
		pingTimeout = 5 * time.Second
	}
	handler := systemStatusHandler{
		database: database, pingTimeout: pingTimeout,
		agentRuntimeEnabled: agentRuntimeEnabled, now: time.Now,
	}
	secured := sessionAuthMiddleware(logger, authService,
		requireRoleMiddleware(auth.RoleAdmin, http.HandlerFunc(handler.serve)))
	secured = authNoStoreMiddleware(secured)
	mux.HandleFunc("/api/v1/admin/system/status", exactMethod(http.MethodGet, secured.ServeHTTP))
}

func (handler systemStatusHandler) serve(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
		return
	}
	if !validFetchSite(request) {
		writeCSRFError(writer, request)
		return
	}
	databaseStatus := "unavailable"
	if handler.database != nil {
		ctx, cancel := context.WithTimeout(request.Context(), handler.pingTimeout)
		defer cancel()
		if handler.database.Ready(ctx) == nil {
			databaseStatus = "ready"
		}
	}
	overallStatus := "ready"
	if databaseStatus != "ready" {
		overallStatus = "degraded"
	}
	respond.JSON(writer, http.StatusOK, systemStatusResponse{
		Status:    overallStatus,
		CheckedAt: handler.now().UTC(),
		API:       systemComponentStatus{Status: "ready", Version: systemAPIVersion},
		Database:  systemComponentStatus{Status: databaseStatus},
		Agent:     systemComponentStatus{Status: agentRuntimeStatus(handler.agentRuntimeEnabled)},
		SecurityBoundary: systemSecurityBoundary{
			ManagementIPAllowlistEnforced: true,
			AdministratorSessionRequired:  true,
			AdminWriteCSRFRequired:        true,
			RemoteOperationsEnabled:       false,
		},
	})
}

func agentRuntimeStatus(enabled bool) string {
	if enabled {
		return "configured"
	}
	return "not_configured"
}
