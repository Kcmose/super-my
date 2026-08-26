package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"probe-api/internal/agent"
	"probe-api/internal/config"
)

type stubAgentService struct {
	authErr      error
	notModified  bool
	reportStatus string
	reportCalls  int
}

func (s *stubAgentService) Authenticate(context.Context, string) (agent.Identity, error) {
	if s.authErr != nil {
		return agent.Identity{}, s.authErr
	}
	return agent.Identity{NodeID: "0191f6d0-35c8-7f31-a165-3f418377e8d8", TokenID: "0191f6d0-35c8-7f31-a165-3f418377e8d9"}, nil
}

func (s *stubAgentService) Enroll(context.Context, agent.EnrollRequest, string) (agent.EnrollResponse, error) {
	return agent.EnrollResponse{NodeID: "0191f6d0-35c8-7f31-a165-3f418377e8d8", AgentToken: "opaque", ConfigVersion: 1}, nil
}

func (s *stubAgentService) LoadConfig(context.Context, agent.Identity, int64) (agent.Config, bool, error) {
	return agent.Config{}, s.notModified, nil
}

func (s *stubAgentService) Report(_ context.Context, _ agent.Identity, request agent.ReportRequest, _ string) (agent.ReportResponse, error) {
	s.reportCalls++
	status := s.reportStatus
	if status == "" {
		status = "accepted"
	}
	return agent.ReportResponse{BatchID: request.BatchID, Status: status, ReceivedAt: time.Now().UTC(), ClockStatus: "ok", CurrentConfigVersion: 1}, nil
}

func TestAgentConfigNotModifiedHasNoBody(t *testing.T) {
	service := &stubAgentService{notModified: true}
	server := newAgentTestServer(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config?version=1", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Body.Len() != 0 {
		t.Fatalf("304 body = %q", response.Body.String())
	}
}

func TestAgentAuthenticationInternalFailureIsNotUnauthorized(t *testing.T) {
	service := &stubAgentService{authErr: errors.New("database unavailable")}
	server := newAgentTestServer(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config?version=0", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAgentRoutesRejectSpoofedTrustedClientHeader(t *testing.T) {
	service := &stubAgentService{notModified: true}
	server := newAgentTestServer(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config?version=0", nil)
	request.RemoteAddr = "192.0.2.10:443"
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("X-Probe-Client-IP", "198.51.100.20")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAgentRuntimeRateLimitReturnsRetryAfterJSON(t *testing.T) {
	t.Setenv("PROBE_AGENT_RUNTIME_IP_LIMIT", "1")
	t.Setenv("PROBE_AGENT_NODE_LIMIT", "100")
	service := &stubAgentService{notModified: true}
	server := newAgentTestServer(t, service)
	for attempt := 1; attempt <= 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config?version=0", nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(response, request)
		if attempt == 1 && response.Code != http.StatusNotModified {
			t.Fatalf("first status = %d, body = %s", response.Code, response.Body.String())
		}
		if attempt == 2 {
			if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" ||
				!strings.Contains(response.Body.String(), `"error":"rate_limited"`) {
				t.Fatalf("limited response = %d, headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
		}
	}
}

func TestReportRejectsMissingRequiredNestedNumber(t *testing.T) {
	service := &stubAgentService{}
	server := newAgentTestServer(t, service)
	body := `{
		"batch_id":"0191f724-4cf8-7d71-917a-6468f58cb17d","sequence":1,
		"agent_time":"2026-08-20T09:31:10Z","agent_version":"1.0.0","config_version":1,
		"metrics":[{"sampled_at":"2026-08-20T09:31:10Z","load_1":0,"load_5":0,"load_15":0,
		"uptime_seconds":0,"memory_total_bytes":0,"memory_used_bytes":0,"memory_available_bytes":0,
		"swap_total_bytes":0,"swap_used_bytes":0,"network_rx_bps":0,"network_tx_bps":0,
		"network_rx_bytes":0,"network_tx_bytes":0}],"disks":[],"probe_results":[]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/report", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.reportCalls != 0 {
		t.Fatal("invalid report reached the service")
	}
}

func TestReadAgentBodyEnforcesGzipLimitsAndSingleMember(t *testing.T) {
	t.Run("decompressed limit", func(t *testing.T) {
		body := gzipBytes(t, bytes.Repeat([]byte("a"), 256))
		request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Encoding", "gzip")
		_, requestError := readAgentBody(httptest.NewRecorder(), request, 64, true)
		if requestError == nil || requestError.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("error = %#v, want 413", requestError)
		}
	})

	t.Run("multiple members", func(t *testing.T) {
		first := gzipBytes(t, []byte(`{}`))
		second := gzipBytes(t, []byte(`{}`))
		body := append(first, second...)
		request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Encoding", "gzip")
		_, requestError := readAgentBody(httptest.NewRecorder(), request, 1024, true)
		if requestError == nil || requestError.code != "invalid_gzip" {
			t.Fatalf("error = %#v, want invalid_gzip", requestError)
		}
	})

	t.Run("compressed limit", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(bytes.Repeat([]byte("x"), 65)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Encoding", "gzip")
		_, requestError := readAgentBody(httptest.NewRecorder(), request, 64, true)
		if requestError == nil || requestError.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("error = %#v, want 413", requestError)
		}
	})
}

func TestManagementProfileDoesNotRegisterAgentRuntimeRoutes(t *testing.T) {
	service := &stubAgentService{}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	cfg.InstallationProfile = "management"
	cfg.AgentPublicURL = ""
	cfg.AgentInstallerURL = ""
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(cfg, logger, fakeDatabase{}, WithAgentService(service))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/report", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound || service.reportCalls != 0 {
		t.Fatalf("status=%d report_calls=%d body=%s", response.Code, service.reportCalls, response.Body.String())
	}
}

func newAgentTestServer(t *testing.T, service AgentService) *Server {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(cfg, logger, fakeDatabase{}, WithAgentService(service))
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
