package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultSocketPath    = "/run/probe-panel-setup/setup.sock"
	DefaultBrowserHost   = "127.0.0.1:18080"
	DefaultBrowserOrigin = "http://127.0.0.1:18080"
	DefaultMaxBodyBytes  = int64(64 * 1024)
)

var ErrServerClosed = http.ErrServerClosed

type ServerConfig struct {
	SocketPath             string
	AdminRoot              string
	Defaults               *SetupDefaults
	MaxBodyBytes           int64
	SessionLimit           int
	SessionWindow          time.Duration
	FinalizeTimeout        time.Duration
	InstalledShutdownDelay time.Duration
	privateCALoader        privateCALoader
	installedAccessLoader  installedAccessLoader
}

type SetupDefaults struct {
	ServerIP string `json:"server_ip"`
	PanelURL string `json:"panel_url"`
	AgentURL string `json:"agent_url"`
	AdminURL string `json:"admin_url"`
}

type Server struct {
	httpServer             *http.Server
	handler                http.Handler
	manager                *Manager
	finalizer              Finalizer
	logger                 *slog.Logger
	limiter                *fixedWindowLimiter
	ctx                    context.Context
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
	installedMu            sync.RWMutex
	installedAdminURL      string
	installedPrivateCA     *setupPrivateCAMetadata
	installedHandoffReady  bool
	installedHandoffFailed bool
	terminalShutdownOnce   sync.Once
	privateCALoader        privateCALoader
	installedAccessLoader  installedAccessLoader
	installedShutdownDelay time.Duration
	socketPath             string
	defaults               *SetupDefaults
}

func NewServer(config ServerConfig, logger *slog.Logger, manager *Manager, finalizer Finalizer) (*Server, error) {
	if config.SocketPath == "" {
		config.SocketPath = DefaultSocketPath
	}
	if config.SocketPath != DefaultSocketPath {
		return nil, errors.New("setup server must use the fixed private Unix socket")
	}
	if config.Defaults == nil || strings.TrimSpace(config.Defaults.ServerIP) == "" || strings.TrimSpace(config.Defaults.PanelURL) == "" || strings.TrimSpace(config.Defaults.AgentURL) == "" || strings.TrimSpace(config.Defaults.AdminURL) == "" {
		return nil, errors.New("setup network defaults are required")
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if config.MaxBodyBytes < 1 || config.MaxBodyBytes > DefaultMaxBodyBytes {
		return nil, errors.New("setup request body limit must be between 1 byte and 64 KiB")
	}
	if config.SessionLimit == 0 {
		config.SessionLimit = 5
	}
	if config.SessionWindow == 0 {
		config.SessionWindow = time.Minute
	}
	if config.FinalizeTimeout == 0 {
		config.FinalizeTimeout = 35 * time.Minute
	}
	if config.InstalledShutdownDelay == 0 {
		config.InstalledShutdownDelay = 25 * time.Second
	}
	if config.SessionLimit < 1 || config.SessionLimit > 100 || config.SessionWindow < time.Second || config.SessionWindow > time.Hour || config.FinalizeTimeout < time.Minute || config.FinalizeTimeout > time.Hour || config.InstalledShutdownDelay < 25*time.Second || config.InstalledShutdownDelay > time.Minute {
		return nil, errors.New("setup server limits are invalid")
	}
	if manager == nil || finalizer == nil {
		return nil, errors.New("setup manager and finalizer are required")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if config.privateCALoader == nil {
		config.privateCALoader = loadPrivateCA
	}
	if config.installedAccessLoader == nil {
		config.installedAccessLoader = loadInstalledAccess
	}
	root, err := validateStaticRoot(config.AdminRoot)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		manager: manager, finalizer: finalizer, logger: logger,
		limiter: newFixedWindowLimiter(config.SessionLimit, config.SessionWindow, manager.now),
		ctx:     ctx, cancel: cancel, installedShutdownDelay: config.InstalledShutdownDelay,
		socketPath: config.SocketPath, defaults: cloneSetupDefaults(config.Defaults),
		privateCALoader:       config.privateCALoader,
		installedAccessLoader: config.installedAccessLoader,
	}
	server.handler = server.securityMiddleware(server.routes(root, config.MaxBodyBytes, config.FinalizeTimeout))
	server.httpServer = &http.Server{
		Handler:           server.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnContext: func(ctx context.Context, connection net.Conn) context.Context {
			if authorizedRootUnixConnection(connection, config.SocketPath) {
				return context.WithValue(ctx, setupTransportContextKey{}, true)
			}
			return ctx
		},
	}
	return server, nil
}

func (server *Server) Handler() http.Handler { return server.handler }

func (server *Server) Serve(listener net.Listener) error {
	if err := validateActivatedUnixListener(listener, server.socketPath); err != nil {
		return err
	}
	server.wg.Add(1)
	go server.watchTerminalState()
	return server.httpServer.Serve(listener)
}

func (server *Server) Shutdown(ctx context.Context) error {
	server.cancel()
	err := server.httpServer.Shutdown(ctx)
	done := make(chan struct{})
	go func() { server.wg.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		if err != nil {
			return err
		}
		return ctx.Err()
	}
}

func (server *Server) routes(staticRoot string, maxBody int64, finalizeTimeout time.Duration) http.Handler {
	fileServer := http.FileServer(http.Dir(staticRoot))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/setup/status":
			if request.Method != http.MethodGet {
				methodNotAllowed(writer, http.MethodGet)
				return
			}
			server.status(writer)
		case "/api/v1/setup/session":
			if request.Method != http.MethodPost {
				methodNotAllowed(writer, http.MethodPost)
				return
			}
			server.session(writer, request, maxBody)
		case "/api/v1/setup/complete":
			if request.Method != http.MethodPost {
				methodNotAllowed(writer, http.MethodPost)
				return
			}
			server.complete(writer, request, maxBody, finalizeTimeout)
		case "/", "/install", "/install/":
			if request.Method != http.MethodGet && request.Method != http.MethodHead {
				methodNotAllowed(writer, http.MethodGet, http.MethodHead)
				return
			}
			if request.URL.Path == "/" {
				http.Redirect(writer, request, "/install", http.StatusTemporaryRedirect)
				return
			}
			serveStaticFile(writer, request, filepath.Join(staticRoot, "index.html"), "text/html; charset=utf-8")
		default:
			if (request.Method == http.MethodGet || request.Method == http.MethodHead) && strings.HasPrefix(request.URL.Path, "/assets/") && request.URL.RawPath == "" && filepath.ToSlash(filepath.Clean(request.URL.Path)) == request.URL.Path {
				fileServer.ServeHTTP(writer, request)
				return
			}
			httpError(writer, http.StatusNotFound, "not_found", "route not found")
		}
	})
}

func (server *Server) status(writer http.ResponseWriter) {
	status, err := server.manager.Status()
	if err != nil {
		httpError(writer, http.StatusServiceUnavailable, "setup_state_unavailable", "setup state is unavailable")
		return
	}
	response := map[string]any{"status": status, "defaults": server.defaults}
	if status == StateInstalled {
		server.ensureInstalledHandoff()
		server.scheduleTerminalShutdown()
	} else if status == StateRecoveryRequired {
		server.scheduleTerminalShutdown()
	}
	server.installedMu.RLock()
	if status == StateInstalled && server.installedAdminURL != "" {
		response["admin_url"] = server.installedAdminURL
	}
	if status == StateInstalled && server.installedPrivateCA != nil {
		response["private_ca"] = *server.installedPrivateCA
	}
	if status == StateInstalled && server.installedHandoffFailed {
		response["handoff_unavailable"] = true
	}
	server.installedMu.RUnlock()
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) session(writer http.ResponseWriter, request *http.Request, maxBody int64) {
	allowed, retryAfter := server.limiter.Allow()
	if !allowed {
		writer.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		httpError(writer, http.StatusTooManyRequests, "rate_limited", "too many setup session requests")
		return
	}
	body, status, err := readJSONBody(writer, request, maxBody)
	if err != nil {
		httpError(writer, status, "invalid_request", err.Error())
		return
	}
	err = decodeEmptyObject(body)
	clear(body)
	if err != nil {
		httpError(writer, http.StatusBadRequest, "invalid_request", "setup session request is invalid")
		return
	}
	credentials, err := server.manager.CreateSession()
	if err != nil {
		if errors.Is(err, ErrStateConflict) {
			httpError(writer, http.StatusConflict, "setup_state_conflict", "setup session cannot be created in the current state")
			return
		}
		httpError(writer, http.StatusServiceUnavailable, "setup_session_unavailable", "setup session is unavailable")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	response := map[string]any{
		"session_token": credentials.SessionToken,
		"csrf_token":    credentials.CSRFToken,
		"expires_at":    credentials.ExpiresAt,
		"defaults":      server.defaults,
	}
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) complete(writer http.ResponseWriter, request *http.Request, maxBody int64, finalizeTimeout time.Duration) {
	sessionToken, ok := setupSessionToken(request.Header.Get("X-Probe-Setup-Session"))
	if !ok {
		httpError(writer, http.StatusUnauthorized, "unauthorized", "valid setup session required")
		return
	}
	csrfToken := request.Header.Get("X-CSRF-Token")
	if csrfToken == "" {
		httpError(writer, http.StatusForbidden, "csrf_failed", "valid setup CSRF token required")
		return
	}
	body, status, err := readJSONBody(writer, request, maxBody)
	if err != nil {
		httpError(writer, status, "invalid_request", err.Error())
		return
	}
	decoded, err := DecodeCompleteRequest(body)
	clear(body)
	if err != nil {
		httpError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	defer decoded.ClearSecrets()
	if err := server.manager.BeginFinalization(sessionToken, csrfToken); err != nil {
		if errors.Is(err, ErrInvalidCSRF) {
			httpError(writer, http.StatusForbidden, "csrf_failed", "valid setup CSRF token required")
			return
		}
		if errors.Is(err, ErrInvalidSession) {
			httpError(writer, http.StatusUnauthorized, "unauthorized", "valid setup session required")
			return
		}
		httpError(writer, http.StatusConflict, "setup_state_conflict", "setup finalization is already active or unavailable")
		return
	}
	finalizerInput := decoded.Clone()
	server.wg.Add(1)
	go server.runFinalizer(finalizerInput, finalizeTimeout)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusAccepted, map[string]State{"status": StateFinalizing})
}

func (server *Server) runFinalizer(request CompleteRequest, timeout time.Duration) {
	defer server.wg.Done()
	defer request.ClearSecrets()
	ctx, cancel := context.WithTimeout(server.ctx, timeout)
	defer cancel()
	err := server.finalizer.Finalize(ctx, request)
	if finishErr := server.manager.FinishFinalization(err == nil); finishErr != nil {
		if errors.Is(finishErr, ErrFinalizationPending) {
			server.logger.Warn("privileged setup finalization outcome is still pending")
		} else {
			server.logger.Error("setup finalization state update failed")
		}
	}
	state, stateErr := server.manager.Status()
	if stateErr != nil {
		server.logger.Error("setup terminal state is unavailable")
	} else if state == StateInstalled {
		// The root-owned persistent commit is authoritative even if the IPC
		// success result was lost, malformed, or could not be cleaned up.
		server.ensureInstalledHandoff()
		server.logger.Info("setup finalization completed")
		server.scheduleTerminalShutdown()
	} else if state == StateRecoveryRequired {
		server.logger.Error("setup finalization failed")
		server.scheduleTerminalShutdown()
	} else {
		server.logger.Warn("setup finalization is awaiting the privileged worker outcome")
	}
}

func (server *Server) watchTerminalState() {
	defer server.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := server.manager.Status()
		if err == nil && (state == StateInstalled || state == StateRecoveryRequired) {
			if state == StateInstalled {
				server.ensureInstalledHandoff()
			}
			server.scheduleTerminalShutdown()
			return
		}
		select {
		case <-server.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (server *Server) ensureInstalledHandoff() {
	server.installedMu.RLock()
	ready := server.installedHandoffReady
	server.installedMu.RUnlock()
	if ready {
		return
	}

	access, err := server.installedAccessLoader(DefaultAPIEnvironmentPath)
	var privateCA *setupPrivateCAMetadata
	if err == nil && access.Mode == IngressModeIP {
		metadata, loadErr := server.privateCALoader(DefaultPrivateCACertificatePath)
		if loadErr != nil || !validPrivateCAMetadata(metadata) {
			metadata = setupPrivateCAMetadata{Available: false}
			server.logger.Warn("private CA browser handoff is unavailable")
		}
		privateCA = &metadata
	}

	server.installedMu.Lock()
	defer server.installedMu.Unlock()
	if server.installedHandoffReady {
		return
	}
	server.installedHandoffReady = true
	if err != nil {
		server.installedHandoffFailed = true
		server.logger.Warn("installed setup handoff metadata is unavailable")
		return
	}
	server.installedAdminURL = access.AdminURL
	server.installedPrivateCA = privateCA
}

func (server *Server) scheduleTerminalShutdown() {
	server.terminalShutdownOnce.Do(func() {
		go func() {
			timer := time.NewTimer(server.installedShutdownDelay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-server.ctx.Done():
				return
			}
			server.cancel()
			shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.httpServer.Shutdown(shutdownContext)
		}()
	})
}

func (server *Server) securityMiddleware(next http.Handler) http.Handler {
	allowedHosts := map[string]struct{}{DefaultBrowserHost: {}, "localhost:18080": {}}
	allowedOrigins := map[string]struct{}{DefaultBrowserOrigin: {}, "http://localhost:18080": {}}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers := writer.Header()
		headers.Set("Cache-Control", "no-store")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if _, ok := allowedHosts[request.Host]; !ok {
			httpError(writer, http.StatusForbidden, "forbidden_host", "request host is forbidden")
			return
		}
		if authorized, _ := request.Context().Value(setupTransportContextKey{}).(bool); !authorized {
			httpError(writer, http.StatusForbidden, "forbidden_peer", "request peer is forbidden")
			return
		}
		origin := request.Header.Get("Origin")
		if origin != "" {
			if _, ok := allowedOrigins[origin]; !ok {
				httpError(writer, http.StatusForbidden, "forbidden_origin", "request origin is forbidden")
				return
			}
		} else if request.Method != http.MethodGet && request.Method != http.MethodHead {
			httpError(writer, http.StatusForbidden, "forbidden_origin", "request origin is required")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func validateStaticRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("setup admin root must be an absolute canonical path")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("setup admin root must be a directory, not a symlink")
	}
	index, err := os.Lstat(filepath.Join(root, "index.html"))
	if err != nil || !index.Mode().IsRegular() || index.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("setup admin root must contain a regular index.html")
	}
	return root, nil
}

func serveStaticFile(writer http.ResponseWriter, request *http.Request, filename, contentType string) {
	file, err := os.Open(filename)
	if err != nil {
		httpError(writer, http.StatusNotFound, "not_found", "route not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		httpError(writer, http.StatusNotFound, "not_found", "route not found")
		return
	}
	writer.Header().Set("Content-Type", contentType)
	http.ServeContent(writer, request, filepath.Base(filename), info.ModTime(), file)
}

func readJSONBody(writer http.ResponseWriter, request *http.Request, limit int64) ([]byte, int, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, http.StatusRequestEntityTooLarge, errors.New("request body exceeds 64 KiB")
		}
		return nil, http.StatusBadRequest, errors.New("request body cannot be read")
	}
	return body, http.StatusOK, nil
}

func setupSessionToken(header string) (string, bool) {
	token := header
	return token, len(token) == 64 && strings.Trim(token, "0123456789abcdef") == ""
}

type setupTransportContextKey struct{}

func authorizeSetupTestRequest(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), setupTransportContextKey{}, true))
}

func decodeEmptyObject(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') || decoder.More() {
		return errors.New("request body must be an empty JSON object")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("request body must be an empty JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func cloneSetupDefaults(defaults *SetupDefaults) *SetupDefaults {
	if defaults == nil {
		return nil
	}
	clone := *defaults
	return &clone
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func httpError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"error": code, "message": message})
}

func methodNotAllowed(writer http.ResponseWriter, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	httpError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

type fixedWindowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	started time.Time
	count   int
}

func newFixedWindowLimiter(limit int, window time.Duration, now func() time.Time) *fixedWindowLimiter {
	return &fixedWindowLimiter{limit: limit, window: window, now: now}
}

func (limiter *fixedWindowLimiter) Allow() (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	if limiter.started.IsZero() || !now.Before(limiter.started.Add(limiter.window)) {
		limiter.started = now
		limiter.count = 0
	}
	if limiter.count >= limiter.limit {
		return false, limiter.started.Add(limiter.window).Sub(now)
	}
	limiter.count++
	return true, 0
}
