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

	"probe-api/internal/access"
	"probe-api/internal/auth"
	"probe-api/internal/config"
	"probe-api/internal/nodemanagement"
)

const adminManagementTestNodeID = "11111111-1111-4111-8111-111111111111"

type fakeNodeManagementService struct {
	createResponse     nodemanagement.Node
	enrollmentResponse nodemanagement.EnrollmentTokenResponse
	rotateResponse     nodemanagement.AgentTokenResponse
	createCalls        int
	enrollmentCalls    int
	rotateCalls        int
	lastActor          auth.Identity
	lastCreate         nodemanagement.CreateRequest
	lastNodeID         string
	lastMetadata       nodemanagement.Metadata
}

func (service *fakeNodeManagementService) Create(
	_ context.Context,
	actor auth.Identity,
	request nodemanagement.CreateRequest,
	metadata nodemanagement.Metadata,
) (nodemanagement.Node, error) {
	service.createCalls++
	service.lastActor = actor
	service.lastCreate = request
	service.lastMetadata = metadata
	return service.createResponse, nil
}

func (service *fakeNodeManagementService) Update(
	context.Context,
	auth.Identity,
	string,
	nodemanagement.UpdateRequest,
	nodemanagement.Metadata,
) (nodemanagement.Node, error) {
	return nodemanagement.Node{}, nil
}

func (service *fakeNodeManagementService) Delete(
	context.Context,
	auth.Identity,
	string,
	nodemanagement.Metadata,
) error {
	return nil
}

func (service *fakeNodeManagementService) CreateEnrollmentToken(
	_ context.Context,
	actor auth.Identity,
	nodeID string,
	_ nodemanagement.CreateEnrollmentTokenRequest,
	metadata nodemanagement.Metadata,
) (nodemanagement.EnrollmentTokenResponse, error) {
	service.enrollmentCalls++
	service.lastActor = actor
	service.lastNodeID = nodeID
	service.lastMetadata = metadata
	return service.enrollmentResponse, nil
}

func (service *fakeNodeManagementService) RotateAgentToken(
	_ context.Context,
	actor auth.Identity,
	nodeID string,
	metadata nodemanagement.Metadata,
) (nodemanagement.AgentTokenResponse, error) {
	service.rotateCalls++
	service.lastActor = actor
	service.lastNodeID = nodeID
	service.lastMetadata = metadata
	return service.rotateResponse, nil
}

func (service *fakeNodeManagementService) RevokeAgentTokens(
	context.Context,
	auth.Identity,
	string,
	nodemanagement.Metadata,
) error {
	return nil
}

func TestAdminManagementViewerCannotReachService(t *testing.T) {
	viewer := testUser()
	viewer.Role = auth.RoleViewer
	authService := &fakeAuthService{identity: auth.Identity{User: viewer}}
	nodeService := &fakeNodeManagementService{}
	request := authenticatedAdminManagementRequest(t, http.MethodPost, "/api/v1/admin/nodes", `{"display_name":"edge-viewer"}`)
	addAdminManagementMutationHeaders(request, true)

	response := serveAdminManagementRequest(t, adminManagementTestConfig(t), authService, nodeService, request)

	if response.Code != http.StatusUnauthorized || nodeService.createCalls != 0 || authService.verifyCalls != 0 {
		t.Fatalf("status=%d create_calls=%d csrf_calls=%d body=%s",
			response.Code, nodeService.createCalls, authService.verifyCalls, response.Body.String())
	}
}

func TestAdminManagementWritesRequireOriginAndCSRF(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"missing-origin": func(request *http.Request) {
			request.Header.Del("Origin")
		},
		"missing-csrf": func(request *http.Request) {
			request.Header.Del("X-CSRF-Token")
		},
	} {
		t.Run(name, func(t *testing.T) {
			authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
			nodeService := &fakeNodeManagementService{}
			request := authenticatedAdminManagementRequest(t, http.MethodPost, "/api/v1/admin/nodes", `{"display_name":"edge-secure"}`)
			addAdminManagementMutationHeaders(request, true)
			mutate(request)

			response := serveAdminManagementRequest(t, adminManagementTestConfig(t), authService, nodeService, request)

			if response.Code != http.StatusForbidden || nodeService.createCalls != 0 || authService.verifyCalls != 0 {
				t.Fatalf("status=%d create_calls=%d csrf_calls=%d body=%s",
					response.Code, nodeService.createCalls, authService.verifyCalls, response.Body.String())
			}
		})
	}
}

func TestAdminManagementCreateInvokesServiceWithTrustedContext(t *testing.T) {
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	nodeService := &fakeNodeManagementService{createResponse: nodemanagement.Node{
		NodeID: adminManagementTestNodeID, DisplayName: "edge-admin", Enabled: true,
	}}
	request := authenticatedAdminManagementRequest(t, http.MethodPost, "/api/v1/admin/nodes", `{"display_name":"edge-admin"}`)
	addAdminManagementMutationHeaders(request, true)

	response := serveAdminManagementRequest(t, adminManagementTestConfig(t), authService, nodeService, request)

	if response.Code != http.StatusCreated || nodeService.createCalls != 1 || authService.verifyCalls != 1 {
		t.Fatalf("status=%d create_calls=%d csrf_calls=%d body=%s",
			response.Code, nodeService.createCalls, authService.verifyCalls, response.Body.String())
	}
	if nodeService.lastActor.User.Role != auth.RoleAdmin || nodeService.lastCreate.DisplayName != "edge-admin" ||
		nodeService.lastMetadata.SourceIP != "192.0.2.1" || nodeService.lastMetadata.RequestID == "" {
		t.Fatalf("actor=%#v create=%#v metadata=%#v",
			nodeService.lastActor, nodeService.lastCreate, nodeService.lastMetadata)
	}
	if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), adminManagementTestNodeID) {
		t.Fatalf("cache=%q body=%s", response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestAdminManagementCIDRRejectsBeforeAuthentication(t *testing.T) {
	cfg := adminManagementTestConfig(t)
	allowlist, err := access.ParseCIDRList("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminAllowlist = allowlist
	authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
	nodeService := &fakeNodeManagementService{}
	request := authenticatedAdminManagementRequest(t, http.MethodPost, "/api/v1/admin/nodes", `{"display_name":"edge-denied"}`)
	addAdminManagementMutationHeaders(request, true)

	response := serveAdminManagementRequest(t, cfg, authService, nodeService, request)

	if response.Code != http.StatusForbidden || authService.authCalls != 0 || authService.verifyCalls != 0 || nodeService.createCalls != 0 {
		t.Fatalf("status=%d auth_calls=%d csrf_calls=%d create_calls=%d body=%s",
			response.Code, authService.authCalls, authService.verifyCalls, nodeService.createCalls, response.Body.String())
	}
}

func TestAdminManagementTokenResponsesAreOneTimeAndNeverCached(t *testing.T) {
	for _, test := range []struct {
		name        string
		path        string
		secret      string
		secretCount int
		wantStatus  int
		calls       func(*fakeNodeManagementService) int
		configure   func(*fakeNodeManagementService)
	}{
		{
			name:        "enrollment",
			path:        "/api/v1/admin/nodes/" + adminManagementTestNodeID + "/enrollment-token",
			secret:      "enroll.v1.enrollment-secret-once",
			secretCount: 2,
			wantStatus:  http.StatusCreated,
			calls:       func(service *fakeNodeManagementService) int { return service.enrollmentCalls },
			configure: func(service *fakeNodeManagementService) {
				service.enrollmentResponse = nodemanagement.EnrollmentTokenResponse{
					NodeID: adminManagementTestNodeID, EnrollmentToken: "enroll.v1.enrollment-secret-once", ExpiresAt: time.Unix(10, 0).UTC(),
				}
			},
		},
		{
			name:        "agent-rotation",
			path:        "/api/v1/admin/nodes/" + adminManagementTestNodeID + "/rotate-token",
			secret:      "agent.v1.rotation-secret-once",
			secretCount: 1,
			wantStatus:  http.StatusOK,
			calls:       func(service *fakeNodeManagementService) int { return service.rotateCalls },
			configure: func(service *fakeNodeManagementService) {
				service.rotateResponse = nodemanagement.AgentTokenResponse{
					NodeID: adminManagementTestNodeID, AgentToken: "agent.v1.rotation-secret-once", CreatedAt: time.Unix(10, 0).UTC(),
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authService := &fakeAuthService{identity: auth.Identity{User: testUser()}}
			nodeService := &fakeNodeManagementService{}
			test.configure(nodeService)
			request := authenticatedAdminManagementRequest(t, http.MethodPost, test.path, "")
			addAdminManagementMutationHeaders(request, false)

			response := serveAdminManagementRequest(t, adminManagementTestConfig(t), authService, nodeService, request)

			if response.Code != test.wantStatus || test.calls(nodeService) != 1 || authService.verifyCalls != 1 {
				t.Fatalf("status=%d service_calls=%d csrf_calls=%d body=%s",
					response.Code, test.calls(nodeService), authService.verifyCalls, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || strings.Count(response.Body.String(), test.secret) != test.secretCount {
				t.Fatalf("cache=%q secret_count=%d body=%s",
					response.Header().Get("Cache-Control"), strings.Count(response.Body.String(), test.secret), response.Body.String())
			}
			if test.name == "enrollment" {
				body := response.Body.String()
				for _, expected := range []string{
					"install_command",
					"https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.1/deploy/install.sh",
					"sudo bash -s --", "-e", "https://api.example.com", "-t",
				} {
					if !strings.Contains(body, expected) {
						t.Fatalf("enrollment response is missing %q: %s", expected, body)
					}
				}
				if strings.Contains(body, "--insecure") || strings.Contains(body, "curl -k") || strings.Contains(body, "base64") {
					t.Fatalf("enrollment response weakens TLS: %s", body)
				}
			}
			for name, values := range response.Header() {
				for _, value := range values {
					if strings.Contains(value, test.secret) {
						t.Fatalf("secret leaked through response header %s", name)
					}
				}
			}
			if nodeService.lastNodeID != adminManagementTestNodeID || nodeService.lastMetadata.SourceIP != "192.0.2.1" {
				t.Fatalf("node_id=%q metadata=%#v", nodeService.lastNodeID, nodeService.lastMetadata)
			}
		})
	}
}

func adminManagementTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminOrigin = testAdminOrigin
	allowTestManagement(t, &cfg)
	return cfg
}

func serveAdminManagementRequest(
	t *testing.T,
	cfg config.Config,
	authService AuthService,
	nodeService NodeManagementService,
	request *http.Request,
) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(cfg, logger, fakeDatabase{}, WithAuthService(authService), WithNodeManagementService(nodeService))
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func authenticatedAdminManagementRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	return request
}

func addAdminManagementMutationHeaders(request *http.Request, hasJSONBody bool) {
	request.Header.Set("Origin", testAdminOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", "csrf")
	if hasJSONBody {
		request.Header.Set("Content-Type", "application/json")
	}
}
