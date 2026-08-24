package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"probe-api/internal/auth"
)

const testAdminOrigin = "https://admin.example.com"

type fakeAuthService struct {
	loginResult  auth.LoginResult
	loginErr     error
	identity     auth.Identity
	authErr      error
	current      auth.AuthResponse
	currentErr   error
	verifyErr    error
	logoutErr    error
	loginCalls   int
	authCalls    int
	currentCalls int
	verifyCalls  int
	logoutCalls  int
	lastToken    string
	lastCSRF     string
	lastMetadata auth.RequestMetadata
}

func (service *fakeAuthService) Login(_ context.Context, input auth.LoginInput) (auth.LoginResult, error) {
	service.loginCalls++
	service.lastMetadata = input.Metadata
	return service.loginResult, service.loginErr
}

func (service *fakeAuthService) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	service.authCalls++
	service.lastToken = token
	return service.identity, service.authErr
}

func (service *fakeAuthService) CurrentAuth(_ context.Context, token string) (auth.AuthResponse, error) {
	service.currentCalls++
	service.lastToken = token
	return service.current, service.currentErr
}

func (service *fakeAuthService) VerifyCSRF(_ context.Context, _ auth.Identity, token string) error {
	service.verifyCalls++
	service.lastCSRF = token
	return service.verifyErr
}

func (service *fakeAuthService) Logout(_ context.Context, token, csrf string, metadata auth.RequestMetadata) error {
	service.logoutCalls++
	service.lastToken = token
	service.lastCSRF = csrf
	service.lastMetadata = metadata
	return service.logoutErr
}

func TestAuthLoginSetsStrictHostOnlyCookie(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _, err := auth.DeriveCSRFToken(token)
	if err != nil {
		t.Fatal(err)
	}
	user := testUser()
	service := &fakeAuthService{loginResult: auth.LoginResult{
		AuthResponse: auth.AuthResponse{User: user, CSRFToken: csrf},
		SessionToken: token,
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	}}
	response := serveAuthRequest(t, service, newLoginRequest(`{"username":"admin","password":"secret"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Set-Cookie count = %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != SessionCookieName || cookie.Value != token || cookie.Path != "/" || cookie.Domain != "" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %#v", cookie)
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), token) {
		t.Fatal("login response cache/secret handling is invalid")
	}
	if service.loginCalls != 1 || service.lastMetadata.SourceIP != "192.0.2.1" || service.lastMetadata.RequestID == "" {
		t.Fatalf("login service metadata = %#v", service.lastMetadata)
	}
}

func TestAuthLoginRejectsLegacyViewerAsGenericCredentialFailure(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _, err := auth.DeriveCSRFToken(token)
	if err != nil {
		t.Fatal(err)
	}
	viewer := testUser()
	viewer.Role = auth.RoleViewer
	service := &fakeAuthService{loginResult: auth.LoginResult{
		AuthResponse: auth.AuthResponse{User: viewer, CSRFToken: csrf},
		SessionToken: token,
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	}}
	response := serveAuthRequest(t, service, newLoginRequest(`{"username":"legacy-viewer","password":"secret"}`))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"error":"invalid_credentials"`) {
		t.Fatalf("legacy viewer response = %d / %s", response.Code, response.Body.String())
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("legacy viewer received cookies: %#v", cookies)
	}
}

func TestAuthLoginOriginAndFetchSiteAreExact(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"missing-origin": func(request *http.Request) { request.Header.Del("Origin") },
		"duplicate-origin": func(request *http.Request) {
			request.Header["Origin"] = []string{testAdminOrigin, testAdminOrigin}
		},
		"comma-origin": func(request *http.Request) { request.Header.Set("Origin", testAdminOrigin+", "+testAdminOrigin) },
		"cross-origin": func(request *http.Request) { request.Header.Set("Origin", "https://evil.example") },
		"duplicate-fetch": func(request *http.Request) {
			request.Header["Sec-Fetch-Site"] = []string{"same-origin", "same-origin"}
		},
		"comma-fetch": func(request *http.Request) { request.Header.Set("Sec-Fetch-Site", "same-origin, cross-site") },
		"cross-site":  func(request *http.Request) { request.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAuthService{}
			request := newLoginRequest(`{"username":"admin","password":"secret"}`)
			mutate(request)
			response := serveAuthRequest(t, service, request)
			if response.Code != http.StatusForbidden || service.loginCalls != 0 {
				t.Fatalf("status = %d, login calls = %d", response.Code, service.loginCalls)
			}
		})
	}
}

func TestAuthLoginRejectsInvalidTransportAndUniformCredentials(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*http.Request)
		status int
	}{
		"non-json": {func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") }, http.StatusBadRequest},
		"encoded":  {func(request *http.Request) { request.Header.Set("Content-Encoding", "gzip") }, http.StatusBadRequest},
		"unknown-field": {func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader(`{"username":"admin","password":"secret","extra":1}`))
		}, http.StatusBadRequest},
		"forged-client-ip": {func(request *http.Request) { request.Header.Set("X-Probe-Client-IP", "203.0.113.8") }, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAuthService{}
			request := newLoginRequest(`{"username":"admin","password":"secret"}`)
			test.mutate(request)
			response := serveAuthRequest(t, service, request)
			if response.Code != test.status || service.loginCalls != 0 {
				t.Fatalf("status = %d, login calls = %d", response.Code, service.loginCalls)
			}
		})
	}

	for _, failure := range []error{auth.ErrInvalidCredentials} {
		service := &fakeAuthService{loginErr: failure}
		response := serveAuthRequest(t, service, newLoginRequest(`{"username":"admin","password":"wrong"}`))
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"error":"invalid_credentials"`) {
			t.Fatalf("credential failure status/body = %d / %s", response.Code, response.Body.String())
		}
	}

	service := &fakeAuthService{loginErr: &auth.RateLimitError{RetryAfter: 1500 * time.Millisecond}}
	response := serveAuthRequest(t, service, newLoginRequest(`{"username":"admin","password":"wrong"}`))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "2" ||
		!strings.Contains(response.Body.String(), `"error":"rate_limited"`) {
		t.Fatalf("rate limit response = %d / %v / %s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSessionCookieMustAppearExactlyOnce(t *testing.T) {
	_, validToken, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	for name, cookieHeader := range map[string]string{
		"missing":   "",
		"empty":     SessionCookieName + "=",
		"malformed": SessionCookieName + "=not-a-token",
		"duplicate": SessionCookieName + "=" + validToken + "; " + SessionCookieName + "=" + validToken,
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAuthService{}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			if cookieHeader != "" {
				request.Header.Set("Cookie", cookieHeader)
			}
			response := serveAuthRequest(t, service, request)
			if response.Code != http.StatusUnauthorized || service.authCalls != 0 || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status/auth/cache = %d/%d/%q", response.Code, service.authCalls, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAuthMeReturnsCurrentSessionCSRF(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _, err := auth.DeriveCSRFToken(token)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeAuthService{identity: auth.Identity{SessionID: "11111111-1111-4111-8111-111111111111", User: testUser()}, current: auth.AuthResponse{User: testUser(), CSRFToken: csrf}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	response := serveAuthRequest(t, service, request)
	if response.Code != http.StatusOK || service.currentCalls != 1 || service.lastToken != token || !strings.Contains(response.Body.String(), csrf) {
		t.Fatalf("me status/body = %d / %s", response.Code, response.Body.String())
	}
}

func TestAuthMeRejectsSameSiteSiblingFetch(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeAuthService{identity: auth.Identity{
		SessionID: "11111111-1111-4111-8111-111111111111", User: testUser(),
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.Header.Set("Sec-Fetch-Site", "same-site")
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	response := serveAuthRequest(t, service, request)
	if response.Code != http.StatusForbidden || service.authCalls != 1 || service.currentCalls != 0 || service.lastToken != token {
		t.Fatalf("me status/auth/current/token = %d/%d/%d/%q", response.Code, service.authCalls, service.currentCalls, service.lastToken)
	}
}

func TestAuthLogoutRequiresExactOriginAndCSRF(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _, err := auth.DeriveCSRFToken(token)
	if err != nil {
		t.Fatal(err)
	}
	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		request.Header.Set("Origin", testAdminOrigin)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
		return request
	}
	identity := auth.Identity{SessionID: "11111111-1111-4111-8111-111111111111", User: testUser(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	for name, mutate := range map[string]func(*http.Request){
		"duplicate-origin": func(request *http.Request) { request.Header["Origin"] = []string{testAdminOrigin, testAdminOrigin} },
		"missing-csrf":     func(request *http.Request) { request.Header.Del("X-CSRF-Token") },
		"duplicate-csrf": func(request *http.Request) {
			request.Header[http.CanonicalHeaderKey("X-CSRF-Token")] = []string{csrf, csrf}
		},
		"whitespace-csrf": func(request *http.Request) { request.Header.Set("X-CSRF-Token", " "+csrf) },
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAuthService{identity: identity}
			request := newRequest()
			mutate(request)
			response := serveAuthRequest(t, service, request)
			if response.Code != http.StatusForbidden || service.logoutCalls != 0 {
				t.Fatalf("status/logout calls = %d/%d", response.Code, service.logoutCalls)
			}
		})
	}

	service := &fakeAuthService{identity: identity}
	response := serveAuthRequest(t, service, newRequest())
	if response.Code != http.StatusNoContent || service.verifyCalls != 1 || service.logoutCalls != 1 {
		t.Fatalf("logout status/verify/logout = %d/%d/%d", response.Code, service.verifyCalls, service.logoutCalls)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName || cookies[0].MaxAge != -1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("expired cookie = %#v", cookies)
	}
}

func TestRoleMiddlewareAllowsOnlyAdministratorRequirement(t *testing.T) {
	for _, test := range []struct {
		role     auth.Role
		required auth.Role
		status   int
	}{
		{auth.RoleViewer, auth.RoleViewer, http.StatusForbidden},
		{auth.RoleAdmin, auth.RoleViewer, http.StatusForbidden},
		{auth.RoleViewer, auth.RoleAdmin, http.StatusForbidden},
		{auth.RoleAdmin, auth.RoleAdmin, http.StatusNoContent},
	} {
		identity := auth.Identity{User: auth.User{Role: test.role}}
		ctx := context.WithValue(context.Background(), authenticatedSessionKey{}, authenticatedSession{identity: identity})
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		handler := requireRoleMiddleware(test.required, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("role=%s required=%s status=%d, want %d", test.role, test.required, response.Code, test.status)
		}
	}
}

func TestSessionMiddlewareMapsFailures(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		err    error
		status int
	}{
		{auth.ErrUnauthorized, http.StatusUnauthorized},
		{errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		service := &fakeAuthService{authErr: test.err}
		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
		response := serveAuthRequest(t, service, request)
		if response.Code != test.status {
			t.Fatalf("error=%v status=%d, want %d", test.err, response.Code, test.status)
		}
	}
}

func TestSessionMiddlewareRejectsLegacyViewerIdentity(t *testing.T) {
	_, token, _, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	viewer := testUser()
	viewer.Role = auth.RoleViewer
	service := &fakeAuthService{identity: auth.Identity{User: viewer}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	response := serveAuthRequest(t, service, request)
	if response.Code != http.StatusUnauthorized || service.currentCalls != 0 {
		t.Fatalf("legacy viewer session status/current calls = %d/%d", response.Code, service.currentCalls)
	}
}

func serveAuthRequest(t *testing.T, service AuthService, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	RegisterAuthRoutes(mux, logger, service, 64*1024, testAdminOrigin)
	response := httptest.NewRecorder()
	requestIDMiddleware(mux).ServeHTTP(response, request)
	return response
}

func newLoginRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testAdminOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func testUser() auth.User {
	return auth.User{
		ID: "22222222-2222-4222-8222-222222222222", Username: "admin", Role: auth.RoleAdmin, Enabled: true,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
}
