package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"probe-api/internal/panel"
)

const panelTestNodeID = "01234567-89ab-cdef-8123-456789abcdef"

type fakePanelService struct {
	calls int
	err   error
}

func (f *fakePanelService) ListNodes(context.Context, panel.ListNodesRequest) (panel.NodeListResponse, error) {
	f.calls++
	return panel.NodeListResponse{Nodes: []panel.NodeSummary{}, Summary: panel.PanelSummary{}}, f.err
}

func (f *fakePanelService) GetNode(context.Context, string) (panel.NodeSummary, error) {
	f.calls++
	return panel.NodeSummary{NodeID: panelTestNodeID}, f.err
}

func (f *fakePanelService) Metrics(context.Context, string, panel.TimeRange) (panel.MetricSeriesResponse, error) {
	f.calls++
	now := time.Now().UTC()
	return panel.MetricSeriesResponse{NodeID: panelTestNodeID, AsOf: now, From: now.Add(-5 * time.Minute), To: now, Points: []panel.MetricPoint{}}, f.err
}

func (f *fakePanelService) Disks(context.Context, string, panel.TimeRange) (panel.DiskSeriesResponse, error) {
	f.calls++
	now := time.Now().UTC()
	return panel.DiskSeriesResponse{NodeID: panelTestNodeID, AsOf: now, From: now.Add(-5 * time.Minute), To: now, Disks: []panel.DiskSeries{}}, f.err
}

func TestPanelRoutesAllowAnonymousGuestReads(t *testing.T) {
	service := &fakePanelService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelRoutes(mux, logger, service)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/panel/nodes", nil))
	if response.Code != http.StatusOK || service.calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d service calls=%d cache=%q", response.Code, service.calls, response.Header().Get("Cache-Control"))
	}
}

func TestPanelSuccessfulResponsesAreNeverCached(t *testing.T) {
	service := &fakePanelService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelRoutes(mux, logger, service)
	paths := []string{
		"/api/v1/panel/nodes",
		"/api/v1/panel/nodes/" + panelTestNodeID,
		"/api/v1/panel/nodes/" + panelTestNodeID + "/metrics",
		"/api/v1/panel/nodes/" + panelTestNodeID + "/disks",
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range paths {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s status=%d body=%s", method, path, response.Code, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("%s %s Cache-Control=%q", method, path, got)
			}
			if method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatalf("HEAD %s returned a response body: %q", path, response.Body.String())
			}
		}
	}
}

func TestPanelRejectsMalformedPathsAndQueries(t *testing.T) {
	service := &fakePanelService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelRoutes(mux, logger, service)
	tests := []struct {
		path string
		want int
	}{
		{path: "/api/v1/panel/nodes?limit=01", want: http.StatusBadRequest},
		{path: "/api/v1/panel/nodes?limit=1&limit=2", want: http.StatusBadRequest},
		{path: "/api/v1/panel/nodes?unknown=1", want: http.StatusBadRequest},
		{path: "/api/v1/panel/nodes/NOT-A-UUID", want: http.StatusBadRequest},
		{path: "/api/v1/panel/nodes/" + panelTestNodeID + "/metrics?from=bad", want: http.StatusBadRequest},
		{path: "/api/v1/panel/nodes/" + panelTestNodeID + "/metrics?from=2026-08-21T12%3A00%3A00Z&to=2026-08-21T11%3A00%3A00Z", want: http.StatusBadRequest},
		{path: "/api/v1/panel/nodes/" + panelTestNodeID + "/probes", want: http.StatusNotFound},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s status=%d want=%d body=%s", test.path, response.Code, test.want, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control=%q", test.path, got)
		}
	}
	if service.calls != 0 {
		t.Fatalf("invalid requests reached service %d times", service.calls)
	}
}

func TestPanelKnownRouteRejectsWrongMethod(t *testing.T) {
	service := &fakePanelService{}
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerPanelRoutes(mux, logger, service)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/panel/nodes/"+panelTestNodeID+"/metrics", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" || service.calls != 0 {
		t.Fatalf("status=%d allow=%q calls=%d", response.Code, response.Header().Get("Allow"), service.calls)
	}
}
