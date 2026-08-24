package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"probe-api/internal/access"
	"probe-api/internal/config"
)

func TestManagementCIDRUsesOnlyResolvedClientIP(t *testing.T) {
	allowlist, _ := access.ParseCIDRList("203.0.113.0/24")
	trusted, _ := access.ParseCIDRList("127.0.0.1/32")
	cfg := testSecurityConfig(t)
	cfg.AdminAllowlist = allowlist
	cfg.TrustedProxies = trusted
	server := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeDatabase{})

	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/panel/nodes", nil)
	allowed.RemoteAddr = "127.0.0.1:1234"
	allowed.Header.Set("X-Probe-Client-IP", "203.0.113.9")
	allowed.Header.Set("X-Forwarded-For", "198.51.100.9")
	allowedResponse := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusNotFound {
		t.Fatalf("allowlisted request status = %d, body = %s", allowedResponse.Code, allowedResponse.Body.String())
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/v1/panel/nodes", nil)
	denied.RemoteAddr = "127.0.0.1:1234"
	denied.Header.Set("X-Probe-Client-IP", "198.51.100.9")
	denied.Header.Set("X-Forwarded-For", "203.0.113.9")
	deniedResponse := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("non-allowlisted request status = %d", deniedResponse.Code)
	}
}

func TestClientIPResolverRejectsUntrustedOrAmbiguousInternalHeader(t *testing.T) {
	cfg := testSecurityConfig(t)
	server := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeDatabase{})
	for name, mutate := range map[string]func(*http.Request){
		"untrusted": func(request *http.Request) {
			request.RemoteAddr = "198.51.100.8:443"
			request.Header.Set("X-Probe-Client-IP", "203.0.113.8")
		},
		"duplicate": func(request *http.Request) {
			request.RemoteAddr = "127.0.0.1:443"
			request.Header.Add("X-Probe-Client-IP", "203.0.113.8")
			request.Header.Add("X-Probe-Client-IP", "203.0.113.9")
		},
		"comma": func(request *http.Request) {
			request.RemoteAddr = "127.0.0.1:443"
			request.Header.Set("X-Probe-Client-IP", "203.0.113.8, 203.0.113.9")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config?version=0", nil)
			mutate(request)
			response := httptest.NewRecorder()
			server.httpServer.Handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestInternalHealthUsesSocketPeerNotForwardedClient(t *testing.T) {
	cfg := testSecurityConfig(t)
	trusted, _ := access.ParseCIDRList("192.0.2.1/32")
	cfg.TrustedProxies = trusted
	server := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeDatabase{})
	request := httptest.NewRequest(http.MethodGet, "/internal/health/live", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("X-Probe-Client-IP", "127.0.0.1")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSecurityHeadersAndNoCORS(t *testing.T) {
	cfg := testSecurityConfig(t)
	server := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeDatabase{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/unknown", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("missing security headers: %#v", response.Header())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unexpected CORS allow header")
	}
}

func testSecurityConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
