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

	"probe-api/internal/auth"
	"probe-api/internal/config"
	"probe-api/internal/probetarget"
)

type fakeProbeTargetAdminService struct {
	listResponse   probetarget.ListResponse
	createResponse probetarget.Target
	updateResponse probetarget.Target
	err            error
	listCalls      int
	createCalls    int
	updateCalls    int
	deleteCalls    int
	lastActor      auth.Identity
	lastTargetID   string
	lastMetadata   probetarget.Metadata
}

func (service *fakeProbeTargetAdminService) List(_ context.Context, actor auth.Identity, _ probetarget.ListRequest) (probetarget.ListResponse, error) {
	service.listCalls++
	service.lastActor = actor
	return service.listResponse, service.err
}

func (service *fakeProbeTargetAdminService) Create(_ context.Context, actor auth.Identity, _ probetarget.CreateRequest, metadata probetarget.Metadata) (probetarget.Target, error) {
	service.createCalls++
	service.lastActor = actor
	service.lastMetadata = metadata
	return service.createResponse, service.err
}

func (service *fakeProbeTargetAdminService) Update(_ context.Context, actor auth.Identity, targetID string, _ probetarget.UpdateRequest, metadata probetarget.Metadata) (probetarget.Target, error) {
	service.updateCalls++
	service.lastActor = actor
	service.lastTargetID = targetID
	service.lastMetadata = metadata
	return service.updateResponse, service.err
}

func (service *fakeProbeTargetAdminService) Delete(_ context.Context, actor auth.Identity, targetID string, metadata probetarget.Metadata) error {
	service.deleteCalls++
	service.lastActor = actor
	service.lastTargetID = targetID
	service.lastMetadata = metadata
	return service.err
}

func TestProbeTargetAdminRequiresAdminRole(t *testing.T) {
	viewer := testUser()
	viewer.Role = auth.RoleViewer
	authService := &fakeAuthService{identity: auth.Identity{User: viewer}}
	targetService := &fakeProbeTargetAdminService{}
	request := authenticatedProbeTargetRequest(t, http.MethodGet, "/api/v1/admin/probe-targets", "")
	response := serveProbeTargetRequest(t, authService, targetService, request)
	if response.Code != http.StatusUnauthorized || targetService.listCalls != 0 {
		t.Fatalf("status=%d list_calls=%d body=%s", response.Code, targetService.listCalls, response.Body.String())
	}
}

func TestProbeTargetAdminReadAndWriteSecurityBoundaries(t *testing.T) {
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	target := testProbeTarget()
	targetService := &fakeProbeTargetAdminService{
		listResponse:   probetarget.ListResponse{Targets: []probetarget.Target{target}},
		createResponse: target,
	}

	get := authenticatedProbeTargetRequest(t, http.MethodGet, "/api/v1/admin/probe-targets?limit=1", "")
	response := serveProbeTargetRequest(t, authService, targetService, get)
	if response.Code != http.StatusOK || targetService.listCalls != 1 || authService.verifyCalls != 0 {
		t.Fatalf("GET status=%d list=%d csrf=%d body=%s", response.Code, targetService.listCalls, authService.verifyCalls, response.Body.String())
	}

	body := `{"node_id":"11111111-1111-4111-8111-111111111111","name":"TLS","type":"https","host":"example.com","port":443,"path":"/","interval_seconds":30,"timeout_seconds":3,"retention_seconds":7776000,"enabled":true}`
	missingOrigin := authenticatedProbeTargetRequest(t, http.MethodPost, "/api/v1/admin/probe-targets", body)
	missingOrigin.Header.Set("Content-Type", "application/json")
	missingOrigin.Header.Set("X-CSRF-Token", "csrf")
	response = serveProbeTargetRequest(t, authService, targetService, missingOrigin)
	if response.Code != http.StatusForbidden || targetService.createCalls != 0 {
		t.Fatalf("missing Origin status=%d create_calls=%d", response.Code, targetService.createCalls)
	}

	create := authenticatedProbeTargetRequest(t, http.MethodPost, "/api/v1/admin/probe-targets", body)
	addProbeTargetMutationHeaders(create)
	response = serveProbeTargetRequest(t, authService, targetService, create)
	if response.Code != http.StatusCreated || targetService.createCalls != 1 || authService.verifyCalls != 1 {
		t.Fatalf("POST status=%d create=%d csrf=%d body=%s", response.Code, targetService.createCalls, authService.verifyCalls, response.Body.String())
	}
	if targetService.lastActor.User.Role != auth.RoleAdmin || targetService.lastMetadata.SourceIP != "192.0.2.1" || targetService.lastMetadata.RequestID == "" {
		t.Fatalf("mutation context actor=%#v metadata=%#v", targetService.lastActor, targetService.lastMetadata)
	}
}

func TestProbeTargetAdminStrictRequestAndDeferredICMP(t *testing.T) {
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	targetService := &fakeProbeTargetAdminService{}
	valid := `{"node_id":"11111111-1111-4111-8111-111111111111","name":"TCP","type":"tcp","host":"example.com","port":443,"path":null,"interval_seconds":30,"timeout_seconds":3,"retention_seconds":86400,"enabled":true}`
	for name, body := range map[string]string{
		"icmp":      strings.Replace(valid, `"type":"tcp"`, `"type":"icmp"`, 1),
		"unknown":   strings.TrimSuffix(valid, "}") + `,"script":"bad"}`,
		"duplicate": strings.Replace(valid, `"name":"TCP"`, `"name":"TCP","name":"other"`, 1),
		"retention": strings.Replace(valid, `"retention_seconds":86400`, `"retention_seconds":7776001`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			request := authenticatedProbeTargetRequest(t, http.MethodPost, "/api/v1/admin/probe-targets", body)
			addProbeTargetMutationHeaders(request)
			response := serveProbeTargetRequest(t, authService, targetService, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if name == "retention" && (!strings.Contains(response.Body.String(), `"error":"retention_exceeds_limit"`) || !strings.Contains(response.Body.String(), `"message":"retention_seconds must not exceed 7776000"`) || !strings.Contains(response.Body.String(), `"max_retention_seconds":7776000`)) {
				t.Fatalf("retention response=%s", response.Body.String())
			}
		})
	}
	if targetService.createCalls != 0 {
		t.Fatalf("invalid requests reached service %d times", targetService.createCalls)
	}
}

func TestProbeTargetAdminListQueryIsStrict(t *testing.T) {
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	targetService := &fakeProbeTargetAdminService{}
	for _, query := range []string{
		"?unknown=1",
		"?limit=01",
		"?limit=1&limit=2",
		"?node_id=11111111-1111-4111-8111-111111111111&node_id=11111111-1111-4111-8111-111111111111",
		"?node_id=11111111-1111-4111-8111-11111111111A",
		"?cursor=not-base64",
	} {
		request := authenticatedProbeTargetRequest(t, http.MethodGet, "/api/v1/admin/probe-targets"+query, "")
		response := serveProbeTargetRequest(t, authService, targetService, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query=%s status=%d body=%s", query, response.Code, response.Body.String())
		}
	}
	if targetService.listCalls != 0 {
		t.Fatalf("invalid query reached service %d times", targetService.listCalls)
	}
}

func TestProbeTargetAdminPatchDeleteAndFrozenPaths(t *testing.T) {
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	targetService := &fakeProbeTargetAdminService{updateResponse: testProbeTarget()}
	targetID := "22222222-2222-4222-8222-22222222222a"
	patch := authenticatedProbeTargetRequest(t, http.MethodPatch, "/api/v1/admin/probe-targets/"+targetID, `{"retention_seconds":1}`)
	addProbeTargetMutationHeaders(patch)
	response := serveProbeTargetRequest(t, authService, targetService, patch)
	if response.Code != http.StatusOK || targetService.updateCalls != 1 || targetService.lastTargetID != targetID {
		t.Fatalf("PATCH status=%d calls=%d body=%s", response.Code, targetService.updateCalls, response.Body.String())
	}

	deleteRequest := authenticatedProbeTargetRequest(t, http.MethodDelete, "/api/v1/admin/probe-targets/"+targetID, "")
	deleteRequest.ContentLength = 0
	addProbeTargetMutationHeaders(deleteRequest)
	deleteRequest.Header.Del("Content-Type")
	response = serveProbeTargetRequest(t, authService, targetService, deleteRequest)
	if response.Code != http.StatusNoContent || targetService.deleteCalls != 1 {
		t.Fatalf("DELETE status=%d calls=%d body=%s", response.Code, targetService.deleteCalls, response.Body.String())
	}

	for _, path := range []string{
		"/api/v1/admin/probe-targets/" + strings.ToUpper(targetID),
		"/api/v1/admin/probe-targets/" + targetID + "/extra",
		"/api/v1/admin/probe-targets/" + targetID + "?extra=1",
	} {
		request := authenticatedProbeTargetRequest(t, http.MethodDelete, path, "")
		addProbeTargetMutationHeaders(request)
		request.Header.Del("Content-Type")
		response := serveProbeTargetRequest(t, authService, targetService, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d", path, response.Code)
		}
	}
}

func TestProbeTargetRoutesFailClosedWithoutAuthService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	allowTestManagement(t, &cfg)
	server := NewServer(cfg, logger, fakeDatabase{}, WithProbeTargetAdminService(&fakeProbeTargetAdminService{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/probe-targets", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func serveProbeTargetRequest(t *testing.T, authService AuthService, targetService ProbeTargetAdminService, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminOrigin = testAdminOrigin
	allowTestManagement(t, &cfg)
	server := NewServer(cfg, logger, fakeDatabase{}, WithAuthService(authService), WithProbeTargetAdminService(targetService))
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func authenticatedProbeTargetRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	return request
}

func addProbeTargetMutationHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testAdminOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", "csrf")
}

func testProbeTarget() probetarget.Target {
	port := int32(443)
	path := "/"
	return probetarget.Target{
		TargetID: "22222222-2222-4222-8222-222222222222",
		NodeID:   "11111111-1111-4111-8111-111111111111",
		Name:     "TLS", Type: probetarget.TypeHTTPS, Host: "example.com", Port: &port, Path: &path,
		IntervalSeconds: 30, TimeoutSeconds: 3, RetentionSeconds: 86400,
		Enabled: true, ConfigVersion: 1,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
}
