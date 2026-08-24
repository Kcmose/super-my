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

func TestSetupServerNoCodeSessionAndAsyncFlow(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	files := newMemoryFiles()
	manager := newTestManager(files, func() time.Time { return now }, sessionTestRandom(8))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	finalizer := &blockingFinalizer{started: make(chan finalizerSnapshot, 1), release: make(chan error, 1)}
	defaults := &SetupDefaults{
		ServerIP: "192.0.2.10", PanelURL: "https://192.0.2.10:18453",
		AgentURL: "https://192.0.2.10:18454", AdminURL: "https://192.0.2.10:18455",
	}
	server := newTestServer(t, manager, finalizer, defaults)
	if server.installedShutdownDelay != 25*time.Second {
		t.Fatalf("installed shutdown delay = %s", server.installedShutdownDelay)
	}

	statusResponse := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"status":"pending"`) || !strings.Contains(statusResponse.Body.String(), `"server_ip":"192.0.2.10"`) || !strings.Contains(statusResponse.Body.String(), `"admin_url":"https://192.0.2.10:18455"`) || strings.Contains(statusResponse.Body.String(), `"network"`) || strings.Contains(statusResponse.Body.String(), `"private_ca"`) {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, DefaultBrowserOrigin)
	if sessionResponse.Code != http.StatusOK || sessionResponse.Header().Get("Cache-Control") != "no-store" || !strings.Contains(sessionResponse.Body.String(), `"defaults"`) {
		t.Fatalf("session response = %d %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var credentials SessionCredentials
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	if state, _ := manager.Status(); state != StateConfiguring {
		t.Fatalf("state after session = %q", state)
	}

	completeBody := privateCACompleteJSON("192.0.2.10")
	completeRequest := validSetupRequest(http.MethodPost, "/api/v1/setup/complete", completeBody)
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
	secondRequest = authorizeSetupTestRequest(secondRequest)
	secondRequest.Body = io.NopCloser(strings.NewReader(completeBody))
	server.Handler().ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("concurrent complete status = %d", secondResponse.Code)
	}
	finalizer.release <- nil
	waitForState(t, manager, StateInstalled)
	installedResponse := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if installedResponse.Code != http.StatusOK || installedResponse.Header().Get("Cache-Control") != "no-store" || !strings.Contains(installedResponse.Body.String(), `"admin_url":"https://192.0.2.10:18455/login"`) || !strings.Contains(installedResponse.Body.String(), `"private_ca":{"available":true,"pem":"PUBLIC CA\n","sha256":"`+strings.Repeat("a", 64)+`"}`) {
		t.Fatalf("installed status = %d %s", installedResponse.Code, installedResponse.Body.String())
	}

	server.installedMu.Lock()
	server.installedAdminURL = ""
	server.installedPrivateCA = nil
	server.installedHandoffReady = false
	server.installedMu.Unlock()
	server.installedAccessLoader = func(path string) (installedAccessMetadata, error) {
		if path != DefaultAPIEnvironmentPath {
			t.Errorf("installed API environment path = %q", path)
		}
		return installedAccessMetadata{Mode: IngressModeIP, AdminURL: "https://192.0.2.10:18455/login"}, nil
	}
	reconstructed := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if reconstructed.Code != http.StatusOK || !strings.Contains(reconstructed.Body.String(), `"private_ca":{"available":true`) || !strings.Contains(reconstructed.Body.String(), `"admin_url":"https://192.0.2.10:18455/login"`) {
		t.Fatalf("reconstructed installed status = %d %s", reconstructed.Code, reconstructed.Body.String())
	}
	server.installedMu.Lock()
	server.installedAdminURL = ""
	server.installedPrivateCA = nil
	server.installedHandoffReady = false
	server.installedHandoffFailed = false
	server.installedMu.Unlock()
	server.installedAccessLoader = func(string) (installedAccessMetadata, error) {
		return installedAccessMetadata{}, errors.New("unsafe formal environment")
	}
	unavailable := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if unavailable.Code != http.StatusOK || !strings.Contains(unavailable.Body.String(), `"handoff_unavailable":true`) || strings.Contains(unavailable.Body.String(), `"private_ca"`) || strings.Contains(unavailable.Body.String(), `"/login"`) {
		t.Fatalf("unavailable installed handoff = %d %s", unavailable.Code, unavailable.Body.String())
	}
}

func TestSetupServerPrivateCAReadFailureKeepsInstalledStateAndReturnsFallbackMetadata(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(4))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithPrivateCALoader(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }), nil, func(path string) (setupPrivateCAMetadata, error) {
		if path != DefaultPrivateCACertificatePath {
			t.Errorf("private CA path = %q", path)
		}
		return setupPrivateCAMetadata{}, errors.New("unavailable")
	})
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, DefaultBrowserOrigin)
	var credentials SessionCredentials
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	request := validSetupRequest(http.MethodPost, "/api/v1/setup/complete", privateCACompleteJSON("192.0.2.10"))
	request.Header.Set("X-Probe-Setup-Session", credentials.SessionToken)
	request.Header.Set("X-CSRF-Token", credentials.CSRFToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("complete status = %d", response.Code)
	}
	waitForState(t, manager, StateInstalled)
	installed := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if installed.Code != http.StatusOK || !strings.Contains(installed.Body.String(), `"private_ca":{"available":false}`) || strings.Contains(installed.Body.String(), `"pem"`) || strings.Contains(installed.Body.String(), `"sha256"`) {
		t.Fatalf("installed fallback status = %d %s", installed.Code, installed.Body.String())
	}
}

func TestSetupServerPersistentInstalledWinsOverLostBrokerResult(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(4))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }), nil)
	server.finalizer = FinalizerFunc(func(context.Context, CompleteRequest) error {
		if err := manager.states.Transition(StateFinalizing, StateInstalled, now); err != nil {
			return err
		}
		return errors.New("simulated lost broker result")
	})
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, DefaultBrowserOrigin)
	var credentials SessionCredentials
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	request := validSetupRequest(http.MethodPost, "/api/v1/setup/complete", privateCACompleteJSON("192.0.2.10"))
	request.Header.Set("X-Probe-Setup-Session", credentials.SessionToken)
	request.Header.Set("X-CSRF-Token", credentials.CSRFToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("complete status = %d", response.Code)
	}
	waitForState(t, manager, StateInstalled)
	installed := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if installed.Code != http.StatusOK || !strings.Contains(installed.Body.String(), `"status":"installed"`) || !strings.Contains(installed.Body.String(), `"admin_url":"https://192.0.2.10:18455/login"`) || !strings.Contains(installed.Body.String(), `"private_ca":{"available":true`) {
		t.Fatalf("installed status after lost broker result = %d %s", installed.Code, installed.Body.String())
	}
}

func TestSetupServerTransportCancellationDoesNotPreemptRunningRootWorker(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(4))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	brokerReturned := make(chan struct{})
	server := newTestServer(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }), nil)
	// Bypass the test helper's simulated root state transition: this models a
	// broker context ending while the separately launched privileged worker is
	// still running and has not committed either terminal state.
	server.finalizer = FinalizerFunc(func(context.Context, CompleteRequest) error {
		close(brokerReturned)
		return context.Canceled
	})
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, DefaultBrowserOrigin)
	var credentials SessionCredentials
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	request := validSetupRequest(http.MethodPost, "/api/v1/setup/complete", privateCACompleteJSON("192.0.2.10"))
	request.Header.Set("X-Probe-Setup-Session", credentials.SessionToken)
	request.Header.Set("X-CSRF-Token", credentials.CSRFToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("complete status = %d", response.Code)
	}
	<-brokerReturned
	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		finalizationActive := manager.finalization
		manager.mu.Unlock()
		if !finalizationActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not finish reconciling the broker cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	if state, _ := manager.Status(); state != StateFinalizing {
		t.Fatalf("broker cancellation preempted root worker with state %q", state)
	}
	if err := manager.states.Transition(StateFinalizing, StateInstalled, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	installed := performSetupRequest(server, http.MethodGet, "/api/v1/setup/status", "", "")
	if installed.Code != http.StatusOK || !strings.Contains(installed.Body.String(), `"status":"installed"`) {
		t.Fatalf("installed status after root commit = %d %s", installed.Code, installed.Body.String())
	}
}

func TestSetupServerSessionBodyOriginAndTransportFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"missing": "", "null": `null`, "array": `[]`, "field": `{"setup_code":"forbidden"}`,
		"trailing": `{} {}`, "duplicate-like nonempty": `{"x":1,"x":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := initializedTestServer(t)
			response := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", body, DefaultBrowserOrigin)
			if body == "" && response.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("empty body status = %d", response.Code)
			}
			if body != "" && response.Code != http.StatusBadRequest {
				t.Fatalf("body %q status = %d", body, response.Code)
			}
		})
	}

	server := initializedTestServer(t)
	for name, mutate := range map[string]func(*http.Request){
		"missing origin": func(request *http.Request) { request.Header.Del("Origin") },
		"evil origin":    func(request *http.Request) { request.Header.Set("Origin", "http://evil.example:18080") },
		"evil host":      func(request *http.Request) { request.Host = "evil.example:18080" },
		"untrusted peer": func(request *http.Request) {
			*request = *request.WithContext(context.Background())
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := validSetupRequest(http.MethodPost, "/api/v1/setup/session", `{}`)
			mutate(request)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}

	large := `{"padding":"` + strings.Repeat("0", 65536) + `"}`
	largeResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", large, DefaultBrowserOrigin)
	if largeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", largeResponse.Code)
	}
	loginResponse := performSetupRequest(server, http.MethodGet, "/login", "", "")
	if loginResponse.Code != http.StatusNotFound {
		t.Fatalf("normal admin route status = %d", loginResponse.Code)
	}
}

func TestSetupServerRotationInvalidatesPriorCredentials(t *testing.T) {
	server := initializedTestServer(t)
	firstResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, DefaultBrowserOrigin)
	secondResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, DefaultBrowserOrigin)
	var first, second SessionCredentials
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	request := validSetupRequest(http.MethodPost, "/api/v1/setup/complete", validCompleteJSON)
	request.Header.Set("X-Probe-Setup-Session", first.SessionToken)
	request.Header.Set("X-CSRF-Token", first.CSRFToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("rotated credential status = %d", response.Code)
	}
	if first.SessionToken == second.SessionToken || first.CSRFToken == second.CSRFToken {
		t.Fatal("server re-sign reused setup credentials")
	}
}

func TestSetupServerFinalizerFailureBecomesRecoveryRequired(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	files := newMemoryFiles()
	manager := newTestManager(files, func() time.Time { return now }, sessionTestRandom(4))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error {
		return errors.New("sensitive-looking failure must not be returned")
	}), nil)
	sessionResponse := performSetupRequest(server, http.MethodPost, "/api/v1/setup/session", `{}`, "http://localhost:18080")
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

func TestSetupServerRejectsRedirectedSocketPathOrMissingDefaults(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(2))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("install"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewServer(ServerConfig{SocketPath: "/tmp/redirected.sock", AdminRoot: root}, nil, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }))
	if err == nil {
		t.Fatal("redirected setup socket path was accepted")
	}
	_, err = NewServer(ServerConfig{AdminRoot: root}, nil, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }))
	if err == nil {
		t.Fatal("missing setup network defaults were accepted")
	}
	defaults := &SetupDefaults{
		ServerIP: "192.0.2.10", PanelURL: "https://192.0.2.10:18453",
		AgentURL: "https://192.0.2.10:18454", AdminURL: "https://192.0.2.10:18455",
	}
	_, err = NewServer(ServerConfig{AdminRoot: root, Defaults: defaults, InstalledShutdownDelay: 20 * time.Second}, nil, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }))
	if err == nil {
		t.Fatal("shutdown delay shorter than the socket finalizer grace period was accepted")
	}
}

func initializedTestServer(t *testing.T) *Server {
	t.Helper()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	files := newMemoryFiles()
	manager := newTestManager(files, func() time.Time { return now }, sessionTestRandom(16))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	return newTestServer(t, manager, FinalizerFunc(func(context.Context, CompleteRequest) error { return nil }), nil)
}

func newTestServer(t *testing.T, manager *Manager, finalizer Finalizer, defaults *SetupDefaults) *Server {
	return newTestServerWithPrivateCALoader(t, manager, finalizer, defaults, func(path string) (setupPrivateCAMetadata, error) {
		if path != DefaultPrivateCACertificatePath {
			t.Errorf("private CA path = %q", path)
		}
		return setupPrivateCAMetadata{Available: true, PEM: "PUBLIC CA\n", SHA256: strings.Repeat("a", 64)}, nil
	})
}

func newTestServerWithPrivateCALoader(t *testing.T, manager *Manager, finalizer Finalizer, defaults *SetupDefaults, loader privateCALoader) *Server {
	t.Helper()
	if defaults == nil {
		defaults = &SetupDefaults{
			ServerIP: "192.0.2.10", PanelURL: "https://192.0.2.10:18453",
			AgentURL: "https://192.0.2.10:18454", AdminURL: "https://192.0.2.10:18455",
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>Install</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	delegate := finalizer
	finalizer = FinalizerFunc(func(ctx context.Context, request CompleteRequest) error {
		if err := delegate.Finalize(ctx, request); err != nil {
			if stateErr := manager.states.MarkRecovery(manager.now().UTC()); stateErr != nil {
				return errors.Join(err, stateErr)
			}
			return err
		}
		return manager.states.Transition(StateFinalizing, StateInstalled, manager.now().UTC())
	})
	server, err := NewServer(ServerConfig{
		AdminRoot: root, Defaults: defaults, privateCALoader: loader,
		installedAccessLoader: func(path string) (installedAccessMetadata, error) {
			if path != DefaultAPIEnvironmentPath {
				t.Errorf("installed API environment path = %q", path)
			}
			return installedAccessMetadata{Mode: IngressModeIP, AdminURL: defaults.AdminURL + "/login"}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), manager, finalizer)
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
	request.Host = DefaultBrowserHost
	request.Header.Set("Origin", DefaultBrowserOrigin)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return authorizeSetupTestRequest(request)
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

func sessionTestRandom(sessionCount int) *bytes.Reader {
	contents := make([]byte, 0, sessionCount*64)
	for index := 0; index < sessionCount*2; index++ {
		contents = append(contents, bytes.Repeat([]byte{byte(index + 1)}, 32)...)
	}
	return bytes.NewReader(contents)
}
