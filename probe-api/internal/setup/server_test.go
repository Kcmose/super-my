package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type finalizerSnapshot struct {
	databasePassword string
	adminPassword    string
	request          CompleteRequest
}

type blockingFinalizer struct {
	started chan finalizerSnapshot
	release chan error
}

func (finalizer *blockingFinalizer) Finalize(ctx context.Context, request CompleteRequest) error {
	snapshot := finalizerSnapshot{
		databasePassword: string(request.Database.Password),
		adminPassword:    string(request.Administrator.Password),
		request:          request.Clone(),
	}
	select {
	case finalizer.started <- snapshot:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-finalizer.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSetupServerFullAsyncFlow(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	files := newMemoryFiles()
	manager := newTestManager(files, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x61}, 96)))
	code, _, err := manager.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	finalizer := &blockingFinalizer{started: make(chan finalizerSnapshot, 1), release: make(chan error, 1)}
	server := newTestServer(t, manager, finalizer)

	statusResponse := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if statusResponse.Code != http.StatusOK || strings.TrimSpace(statusResponse.Body.String()) != `{"status":"pending"}` {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	sessionBody := `{"setup_code":"` + code + `"}`
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", sessionBody, "http://127.0.0.1:18080")
	if sessionResponse.Code != http.StatusOK || sessionResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session response = %d %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var credentials SessionCredentials
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	completeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", strings.NewReader(validCompleteJSON))
	completeRequest.Host = "127.0.0.1:18080"
	completeRequest.RemoteAddr = "127.0.0.1:45000"
	completeRequest.Header.Set("Origin", "http://127.0.0.1:18080")
	completeRequest.Header.Set("Content-Type", "application/json")
	completeRequest.Header.Set("X-Probe-Setup-Session", credentials.SessionToken)
	completeRequest.Header.Set("X-CSRF-Token", credentials.CSRFToken)
	completeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(completeResponse, completeRequest)
	if completeResponse.Code != http.StatusAccepted || strings.TrimSpace(completeResponse.Body.String()) != `{"status":"finalizing"}` {
		t.Fatalf("complete response = %d %s", completeResponse.Code, completeResponse.Body.String())
	}
	snapshot := <-finalizer.started
	defer snapshot.request.ClearSecrets()
	if snapshot.databasePassword != "database secret" || snapshot.adminPassword != "administrator secret" {
		t.Fatalf("finalizer secrets were not deep-copied: %#v", snapshot)
	}
	if state, _ := manager.Status(); state != StateFinalizing {
		t.Fatalf("state while blocked = %q", state)
	}
	secondResponse := httptest.NewRecorder()
	secondRequest := completeRequest.Clone(context.Background())
	secondRequest.Body = io.NopCloser(strings.NewReader(validCompleteJSON))
	server.Handler().ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("concurrent complete status = %d", secondResponse.Code)
	}
	finalizer.release <- nil
	waitForState(t, manager, StateInstalled)
	installedResponse := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if installedResponse.Code != http.StatusOK || strings.TrimSpace(installedResponse.Body.String()) != `{"admin_url":"https://admin.monitor.test/login","status":"installed"}` {
		t.Fatalf("installed status response = %d %s", installedResponse.Code, installedResponse.Body.String())
	}
	if _, exists := files.files["/code.json"]; exists {
		t.Fatal("code record remains after install")
	}
}

func TestSetupServerSecurityAndLimitsFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	files := newMemoryFiles()
	manager := newTestManager(files, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x62}, 32)))
	if _, _, err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	request.Host = "evil.example:18080"
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("evil host status = %d", response.Code)
	}

	for name, mutate := range map[string]func(*http.Request){
		"missing origin": func(request *http.Request) { request.Header.Del("Origin") },
		"evil origin":    func(request *http.Request) { request.Header.Set("Origin", "http://evil.example:18080") },
		"remote peer":    func(request *http.Request) { request.RemoteAddr = "192.0.2.1:1234" },
	} {
		t.Run(name, func(t *testing.T) {
			request := validSetupRequest(http.MethodPost, "/api/v1/setup/session", `{"setup_code":"`+strings.Repeat("0", 64)+`"}`)
			mutate(request)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}

	large := `{"setup_code":"` + strings.Repeat("0", 65536) + `"}`
	largeResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", large, "http://127.0.0.1:18080")
	if largeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", largeResponse.Code)
	}
	for attempt := 0; attempt < 5; attempt++ {
		rateResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{"setup_code":"`+strings.Repeat("0", 64)+`"}`, "http://127.0.0.1:18080")
		if attempt < 4 && rateResponse.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d", attempt+1, rateResponse.Code)
		}
		if attempt == 4 && rateResponse.Code != http.StatusTooManyRequests {
			t.Fatalf("rate-limited status = %d", rateResponse.Code)
		}
	}
	loginResponse := performSetupRequest(server, http.MethodGet, "/login", "", "")
	if loginResponse.Code != http.StatusNotFound {
		t.Fatalf("normal admin route status = %d", loginResponse.Code)
	}
	installResponse := performSetupRequest(server, http.MethodGet, "/install", "", "")
	if installResponse.Code != http.StatusOK {
		t.Fatalf("install page status = %d", installResponse.Code)
	}
}

func TestSetupServerFinalizerFailureBecomesRecoveryRequired(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	files := newMemoryFiles()
	manager := newTestManager(files, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x63}, 96)))
	code, _, _ := manager.Initialize()
	server := newTestServer(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error {
		return errors.New("sensitive-looking failure must not be returned")
	}))
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{"setup_code":"`+code+`"}`, "http://localhost:18080")
	var credentials SessionCredentials
	_ = json.Unmarshal(sessionResponse.Body.Bytes(), &credentials)
	request := validSetupRequest(http.MethodPost, "/api/v1/setup/complete", validCompleteJSON)
	request.Host = "localhost:18080"
	request.Header.Set("Origin", "http://localhost:18080")
	request.Header.Set("X-Probe-Setup-Session", credentials.SessionToken)
	request.Header.Set("X-CSRF-Token", credentials.CSRFToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || strings.Contains(response.Body.String(), "sensitive") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	waitForState(t, manager, StateRecoveryRequired)
}

func newTestServer(t *testing.T, manager *Manager, finalizer Finalizer) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>Install</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{AdminRoot: root}, slog.New(slog.NewTextHandler(io.Discard, nil)), manager, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return server
}

func validSetupRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "127.0.0.1:18080"
	request.RemoteAddr = "127.0.0.1:45000"
	request.Header.Set("Origin", "http://127.0.0.1:18080")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func performSetupRequest(server *Server, method, path, body, origin string) *httptest.ResponseRecorder {
	request := validSetupRequest(method, path, body)
	if origin == "" {
		request.Header.Del("Origin")
	} else {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func waitForState(t *testing.T, manager *Manager, wanted State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := manager.Status()
		if err == nil && state == wanted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	state, err := manager.Status()
	t.Fatalf("state = %q, error = %v, want %q", state, err, wanted)
}
