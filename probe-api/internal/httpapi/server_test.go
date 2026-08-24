package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"probe-api/internal/access"
	"probe-api/internal/config"
)

type fakeDatabase struct {
	err error
}

func (f fakeDatabase) Ready(context.Context) error { return f.err }

func allowTestManagement(t *testing.T, cfg *config.Config) {
	t.Helper()
	allowlist, err := access.ParseCIDRList("192.0.2.1/32")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminAllowlist = allowlist
}

func TestHealthEndpoints(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	server := NewServer(cfg, logger, fakeDatabase{})
	request := httptest.NewRequest(http.MethodGet, "/internal/health/ready", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func TestReadinessFailsClosed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	server := NewServer(cfg, logger, fakeDatabase{err: errors.New("database down")})
	request := httptest.NewRequest(http.MethodGet, "/internal/health/ready", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHealthRejectsUnlistedMethodsAsJSON(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	server := NewServer(cfg, logger, fakeDatabase{})
	for _, method := range []string{http.MethodHead, http.MethodPost} {
		request := httptest.NewRequest(method, "/internal/health/live", nil)
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d", method, response.Code)
		}
		if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("%s content type = %q", method, response.Header().Get("Content-Type"))
		}
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("%s Allow = %q", method, response.Header().Get("Allow"))
		}
	}
}

func TestPanelRoutesAllowAnonymousGuestWithoutAuthenticationService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	panelService := &fakePanelService{}
	allowTestManagement(t, &cfg)
	server := NewServer(cfg, logger, fakeDatabase{}, WithPanelService(panelService))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/panel/nodes", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || panelService.calls != 1 {
		t.Fatalf("status = %d, panel calls = %d", response.Code, panelService.calls)
	}
}

func TestPanelRoutesAcceptAnonymousHeadWithoutAuthentication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	panelService := &fakePanelService{}
	allowTestManagement(t, &cfg)
	server := NewServer(cfg, logger, fakeDatabase{}, WithPanelService(panelService))
	request := httptest.NewRequest(http.MethodHead, "/api/v1/panel/nodes", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || panelService.calls != 1 || response.Body.Len() != 0 {
		t.Fatalf("status = %d, panel calls = %d, body = %s",
			response.Code, panelService.calls, response.Body.String())
	}
}

func TestProbeQueryRoutesAllowAnonymousGuestAfterServerWiring(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	probeService := &fakeProbeQueryService{}
	allowTestManagement(t, &cfg)
	server := NewServer(cfg, logger, fakeDatabase{}, WithProbeQueryService(probeService))
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/panel/nodes/01234567-89ab-4cde-8f01-23456789abcd/probe-targets", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || probeService.calls != 1 {
		t.Fatalf("status = %d, probe calls = %d, body = %s",
			response.Code, probeService.calls, response.Body.String())
	}
}
