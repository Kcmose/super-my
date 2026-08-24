package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"probe-api/internal/probequery"
)

const panelProbeTestTargetID = "fedcba98-7654-4321-8123-456789abcdef"

type fakeProbeQueryService struct {
	calls       int
	lastRequest probequery.ProbeSeriesRequest
	err         error
}

func (f *fakeProbeQueryService) ListTargets(_ context.Context, nodeID string) (probequery.PanelProbeTargetListResponse, error) {
	f.calls++
	return probequery.PanelProbeTargetListResponse{NodeID: nodeID, Targets: []probequery.PanelProbeTargetSummary{
		{TargetID: panelProbeTestTargetID, Name: "HTTPS", Type: "https", Enabled: true, RetentionSeconds: 86400},
	}}, f.err
}

func (f *fakeProbeQueryService) Probes(_ context.Context, request probequery.ProbeSeriesRequest) (probequery.ProbeSeriesResponse, error) {
	f.calls++
	f.lastRequest = request
	return probequery.ProbeSeriesResponse{
		Target:     probequery.ProbeTarget{TargetID: request.TargetID, NodeID: request.NodeID},
		Resolution: request.Resolution, From: request.From, To: request.To, AsOf: request.To,
		Points: []probequery.ProbeSeriesPoint{},
	}, f.err
}

func TestPanelProbeRoutesAllowAnonymousGuestAndParseStrictQuery(t *testing.T) {
	service := &fakeProbeQueryService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelProbeRoutes(mux, logger, service)

	from := "2026-08-22T10%3A00%3A00Z"
	to := "2026-08-22T11%3A00%3A00Z"
	path := "/api/v1/panel/nodes/" + panelTestNodeID + "/probes?target_id=" + panelProbeTestTargetID + "&from=" + from + "&to=" + to + "&resolution=raw"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || service.calls != 1 ||
		service.lastRequest.NodeID != panelTestNodeID || service.lastRequest.TargetID != panelProbeTestTargetID ||
		service.lastRequest.Resolution != probequery.ResolutionRaw {
		t.Fatalf("status=%d calls=%d request=%#v body=%s", response.Code, service.calls, service.lastRequest, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
	}
}

func TestPanelProbeTargetListIsReadOnlyAndNoStore(t *testing.T) {
	service := &fakeProbeQueryService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelProbeRoutes(mux, logger, service)
	path := "/api/v1/panel/nodes/" + panelTestNodeID + "/probe-targets"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || service.calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d calls=%d cache=%q body=%s", response.Code, service.calls, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"host"`) || strings.Contains(response.Body.String(), `"path"`) {
		t.Fatalf("redacted target list exposed destination fields: %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" || service.calls != 1 {
		t.Fatalf("POST status=%d allow=%q calls=%d", response.Code, response.Header().Get("Allow"), service.calls)
	}

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodHead, path, nil))
	if response.Code != http.StatusOK || response.Body.Len() != 0 || service.calls != 2 {
		t.Fatalf("HEAD status=%d body=%q calls=%d", response.Code, response.Body.String(), service.calls)
	}
}

func TestPanelProbeRoutesRejectMalformedAndUnavailableRequests(t *testing.T) {
	service := &fakeProbeQueryService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelProbeRoutes(mux, logger, service)
	base := "/api/v1/panel/nodes/" + panelTestNodeID + "/probes"
	tests := []string{
		base,
		base + "?target_id=" + panelProbeTestTargetID + "&from=bad&to=2026-08-22T11%3A00%3A00Z",
		base + "?target_id=" + panelProbeTestTargetID + "&from=2026-08-22T11%3A00%3A00Z&to=2026-08-22T10%3A00%3A00Z",
		base + "?target_id=" + panelProbeTestTargetID + "&from=2026-08-22T10%3A00%3A00Z&to=2026-08-22T11%3A00%3A00Z&resolution=icmp",
		base + "?target_id=" + panelProbeTestTargetID + "&target_id=" + panelProbeTestTargetID + "&from=2026-08-22T10%3A00%3A00Z&to=2026-08-22T11%3A00%3A00Z",
	}
	for _, path := range tests {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if service.calls != 0 {
		t.Fatalf("invalid requests reached service %d times", service.calls)
	}

	service.err = probequery.ErrResolutionUnavailable
	path := base + "?target_id=" + panelProbeTestTargetID + "&from=2026-08-22T10%3A00%3A00Z&to=2026-08-22T11%3A00%3A00Z&resolution=raw"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusUnprocessableEntity || service.calls != 1 {
		t.Fatalf("unavailable status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
}

func TestParseRequiredProbeTimeAcceptsFractionAndOffset(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	parsed, ok := parseRequiredProbeTime(recorder, request, "from", "2026-08-22T18:00:00.123+08:00")
	if !ok || !parsed.Equal(time.Date(2026, 8, 22, 10, 0, 0, 123000000, time.UTC)) {
		t.Fatalf("parsed=%v ok=%v", parsed, ok)
	}
}
