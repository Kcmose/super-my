package setup

import (
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
	DefaultListenAddress = "127.0.0.1:18080"
	DefaultMaxBodyBytes  = int64(64 * 1024)
)

var ErrServerClosed = http.ErrServerClosed

type ServerConfig struct {
	ListenAddress          string
	AdminRoot              string
	MaxBodyBytes           int64
	SessionLimit           int
	SessionWindow          time.Duration
	FinalizeTimeout        time.Duration
	InstalledShutdownDelay time.Duration
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
	installedShutdownDelay time.Duration
}

func NewServer(config ServerConfig, logger *slog.Logger, manager *Manager, finalizer Finalizer) (*Server, error) {
	if config.ListenAddress == "" {
		config.ListenAddress = DefaultListenAddress
	}
	if config.ListenAddress != DefaultListenAddress {
		return nil, errors.New("setup server must listen on 127.0.0.1:18080")
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
		config.InstalledShutdownDelay = 15 * time.Second
	}
	if config.SessionLimit < 1 || config.SessionLimit > 100 || config.SessionWindow < time.Second || config.SessionWindow > time.Hour || config.FinalizeTimeout < time.Minute || config.FinalizeTimeout > time.Hour || config.InstalledShutdownDelay < time.Second || config.InstalledShutdownDelay > time.Minute {
		return nil, errors.New("setup server limits are invalid")
	}
	if manager == nil || finalizer == nil {
		return nil, errors.New("setup manager and finalizer are required")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
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
	}
	server.handler = server.securityMiddleware(server.routes(root, config.MaxBodyBytes, config.FinalizeTimeout))
	server.httpServer = &http.Server{
		Addr:              config.ListenAddress,
		Handler:           server.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	return server, nil
}

func (server *Server) Handler() http.Handler { return server.handler }

func (server *Server) ListenAndServe() error { return server.httpServer.ListenAndServe() }

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
	response := map[string]any{"status": status}
	server.installedMu.RLock()
	if status == StateInstalled && server.installedAdminURL != "" {
		response["admin_url"] = server.installedAdminURL
	}
	server.installedMu.RUnlock()
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) session(writer http.ResponseWriter, request *http.Request, maxBody int64) {
	allowed, retryAfter := server.limiter.Allow()
	if !allowed {
		writer.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		httpError(writer, http.StatusTooManyRequests, "rate_limited", "too many setup code attempts")
		return
	}
	body, status, err := readJSONBody(writer, request, maxBody)
	if err != nil {
		httpError(writer, status, "invalid_request", err.Error())
		return
	}
	decoded, err := DecodeSessionRequest(body)
	clear(body)
	if err != nil {
		httpError(writer, http.StatusBadRequest, "invalid_request", "setup session request is invalid")
		return
	}
	credentials, err := server.manager.ExchangeCode(decoded.SetupCode)
	decoded.SetupCode = ""
	if err != nil {
		status := http.StatusUnauthorized
		code := "invalid_setup_code"
		if !errors.Is(err, ErrInvalidCode) {
			status = http.StatusConflict
			code = "setup_state_conflict"
		}
		httpError(writer, status, code, "setup session cannot be created")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, credentials)
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
		server.logger.Error("setup finalization state update failed")
		return
	}
	if err != nil {
		server.logger.Error("setup finalization failed")
		return
	}
	server.installedMu.Lock()
	server.installedAdminURL = "https://" + request.Domains.Admin + "/login"
	server.installedMu.Unlock()
	server.logger.Info("setup finalization completed")
	server.scheduleInstalledShutdown()
}

func (server *Server) scheduleInstalledShutdown() {
	go func() {
		timer := time.NewTimer(server.installedShutdownDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-server.ctx.Done():
			return
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.httpServer.Shutdown(shutdownContext)
	}()
}

func (server *Server) securityMiddleware(next http.Handler) http.Handler {
	allowedHosts := map[string]struct{}{"127.0.0.1:18080": {}, "localhost:18080": {}}
	allowedOrigins := map[string]struct{}{"http://127.0.0.1:18080": {}, "http://localhost:18080": {}}
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
		if !loopbackPeer(request.RemoteAddr) {
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

func loopbackPeer(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
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
