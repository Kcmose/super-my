package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"probe-api/internal/access"
	"probe-api/internal/auth"
	"probe-api/internal/config"
)

type countedStatusDatabase struct {
	err   error
	calls int
}

func (database *countedStatusDatabase) Ready(context.Context) error {
	database.calls++
	return database.err
}

func TestManagementAccessStatusUsesServerResolvedSourceOnly(t *testing.T) {
	cfg := statusTestConfig(t)
	trusted, err := access.ParseCIDRList("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := access.ParseCIDRList("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TrustedProxies = trusted
	cfg.AdminAllowlist = allowlist
	server := NewServer(cfg, statusTestLogger(), &countedStatusDatabase{})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/access", nil)
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("X-Probe-Client-IP", "203.0.113.9")
	request.Header.Set("X-Forwarded-For", "198.51.100.77")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status/cache/body = %d/%q/%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var payload managementAccessStatus
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceIP != "203.0.113.9" || !payload.Allowed || strings.Contains(response.Body.String(), "198.51.100.77") {
		t.Fatalf("access status = %#v / %s", payload, response.Body.String())
	}
}

func TestManagementAccessStatusIgnoresClientForwardingHeaders(t *testing.T) {
	cfg := statusTestConfig(t)
	server := NewServer(cfg, statusTestLogger(), &countedStatusDatabase{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/access", nil)
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("Forwarded", "for=203.0.113.9")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source_ip":"192.0.2.1"`) ||
		strings.Contains(response.Body.String(), "203.0.113.9") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestManagementAccessStatusFailsClosedOutsideAllowlist(t *testing.T) {
	cfg := statusTestConfig(t)
	allowlist, err := access.ParseCIDRList("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminAllowlist = allowlist
	server := NewServer(cfg, statusTestLogger(), &countedStatusDatabase{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/access", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "192.0.2.1") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestSystemStatusRequiresAdministratorSessionAndReturnsSafeReadiness(t *testing.T) {
	database := &countedStatusDatabase{}
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	server := NewServer(statusTestConfig(t), statusTestLogger(), database, WithAuthService(authService))
	request := authenticatedStatusRequest(t, http.MethodGet, "/api/v1/admin/system/status")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || authService.authCalls != 1 || database.calls != 1 {
		t.Fatalf("status/cache/auth/db/body = %d/%q/%d/%d/%s", response.Code, response.Header().Get("Cache-Control"), authService.authCalls, database.calls, response.Body.String())
	}
	var payload systemStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "ready" || payload.API.Status != "ready" || payload.API.Version != "v1" ||
		payload.Database.Status != "ready" || payload.Agent.Status != "configured" || payload.CheckedAt.IsZero() {
		t.Fatalf("system status = %#v", payload)
	}
	boundary := payload.SecurityBoundary
	if !boundary.ManagementIPAllowlistEnforced || !boundary.AdministratorSessionRequired || !boundary.AdminWriteCSRFRequired ||
		boundary.RemoteOperationsEnabled {
		t.Fatalf("security boundary = %#v", boundary)
	}
	for _, forbidden := range []string{"password", "secret", "database_url", "environment", "hostname", "agent_api_separated", "public_api_enabled"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("status response exposes %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestSystemStatusReportsUnconfiguredAgentWithoutDegradingManagement(t *testing.T) {
	database := &countedStatusDatabase{}
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	cfg := statusTestConfig(t)
	cfg.InstallationProfile = "management"
	cfg.AgentPublicURL = ""
	cfg.AgentInstallerURL = ""
	server := NewServer(cfg, statusTestLogger(), database, WithAuthService(authService))
	request := authenticatedStatusRequest(t, http.MethodGet, "/api/v1/admin/system/status")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	var payload systemStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "ready" || payload.Agent.Status != "not_configured" || database.calls != 1 {
		t.Fatalf("system status = %#v, database_calls=%d", payload, database.calls)
	}
}

func TestSystemStatusDoesNotLeakReadinessErrors(t *testing.T) {
	database := &countedStatusDatabase{err: errors.New("super-secret database address")}
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	server := NewServer(statusTestConfig(t), statusTestLogger(), database, WithAuthService(authService))
	request := authenticatedStatusRequest(t, http.MethodGet, "/api/v1/admin/system/status")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"degraded"`) ||
		!strings.Contains(response.Body.String(), `"database":{"status":"unavailable"}`) ||
		strings.Contains(response.Body.String(), "super-secret") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestSystemStatusRejectsMissingSessionCrossSiteAndParameters(t *testing.T) {
	for name, configure := range map[string]func(*http.Request){
		"missing-session": func(request *http.Request) {
			request.Header.Del("Cookie")
		},
		"cross-site": func(request *http.Request) {
			request.Header.Set("Sec-Fetch-Site", "cross-site")
		},
		"query": func(request *http.Request) {
			request.URL.RawQuery = "details=true"
		},
	} {
		t.Run(name, func(t *testing.T) {
			database := &countedStatusDatabase{}
			authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
			server := NewServer(statusTestConfig(t), statusTestLogger(), database, WithAuthService(authService))
			request := authenticatedStatusRequest(t, http.MethodGet, "/api/v1/admin/system/status")
			configure(request)
			response := httptest.NewRecorder()
			server.httpServer.Handler.ServeHTTP(response, request)

			wantStatus := http.StatusBadRequest
			if name == "missing-session" {
				wantStatus = http.StatusUnauthorized
			} else if name == "cross-site" {
				wantStatus = http.StatusForbidden
			}
			if response.Code != wantStatus || database.calls != 0 {
				t.Fatalf("status/db/body = %d/%d/%s", response.Code, database.calls, response.Body.String())
			}
		})
	}
}

func authenticatedStatusRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	return request
}

func statusTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	allowTestManagement(t, &cfg)
	return cfg
}

func statusTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
