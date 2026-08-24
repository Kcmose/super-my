package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"probe-api/internal/auth"
	"probe-api/internal/httpapi/respond"
)

const SessionCookieName = "probe_session"

type AuthService interface {
	Login(context.Context, auth.LoginInput) (auth.LoginResult, error)
	Authenticate(context.Context, string) (auth.Identity, error)
	CurrentAuth(context.Context, string) (auth.AuthResponse, error)
	VerifyCSRF(context.Context, auth.Identity, string) error
	Logout(context.Context, string, string, auth.RequestMetadata) error
}

type authHandler struct {
	logger       *slog.Logger
	service      AuthService
	maxBodyBytes int64
	adminOrigin  string
}

type authenticatedSession struct {
	identity auth.Identity
	token    string
}

type authenticatedSessionKey struct{}

func RegisterAuthRoutes(mux *http.ServeMux, logger *slog.Logger, service AuthService, maxBodyBytes int64, adminOrigin string) {
	handler := authHandler{
		logger: logger, service: service, maxBodyBytes: maxBodyBytes, adminOrigin: adminOrigin,
	}
	login := authNoStoreMiddleware(http.HandlerFunc(handler.login))
	mux.HandleFunc("/api/v1/auth/login", exactMethod(http.MethodPost, login.ServeHTTP))
	logout := sessionAuthMiddleware(logger, service,
		csrfProtectionMiddleware(logger, service, adminOrigin, http.HandlerFunc(handler.logout)))
	logout = authNoStoreMiddleware(logout)
	mux.HandleFunc("/api/v1/auth/logout", exactMethod(http.MethodPost, logout.ServeHTTP))
	me := sessionAuthMiddleware(logger, service, http.HandlerFunc(handler.me))
	me = authNoStoreMiddleware(me)
	mux.HandleFunc("/api/v1/auth/me", exactMethod(http.MethodGet, me.ServeHTTP))
}

func (handler authHandler) login(writer http.ResponseWriter, request *http.Request) {
	setAuthNoStore(writer)
	if request.URL.RawQuery != "" {
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
		return
	}
	if !validRequestOrigin(request, handler.adminOrigin) || !validFetchSite(request) {
		writeCSRFError(writer, request)
		return
	}
	sourceIP, validSource := validatedSourceIP(request)
	if !validSource {
		respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
		return
	}
	body, requestError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if requestError != nil {
		respond.Error(writer, requestError.status, requestError.code, requestError.message, requestIDFromContext(request.Context()))
		return
	}
	loginRequest, err := auth.DecodeLoginRequest(body)
	if err != nil {
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "request validation failed", requestIDFromContext(request.Context()))
		return
	}
	result, err := handler.service.Login(request.Context(), auth.LoginInput{
		LoginRequest: loginRequest,
		Metadata: auth.RequestMetadata{
			SourceIP:  sourceIP,
			UserAgent: request.UserAgent(),
			RequestID: requestIDFromContext(request.Context()),
		},
	})
	loginRequest.Password = ""
	if err != nil {
		handler.writeServiceError(writer, request, err, "login")
		return
	}
	if !result.AuthResponse.User.Enabled || result.AuthResponse.User.Role != auth.RoleAdmin {
		handler.writeServiceError(writer, request, auth.ErrInvalidCredentials, "login")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    result.SessionToken,
		Path:     "/",
		Expires:  result.ExpiresAt.UTC(),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	respond.JSON(writer, http.StatusOK, result.AuthResponse)
}

func (handler authHandler) me(writer http.ResponseWriter, request *http.Request) {
	setAuthNoStore(writer)
	if request.URL.RawQuery != "" {
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
		return
	}
	if !validFetchSite(request) {
		writeCSRFError(writer, request)
		return
	}
	session, ok := authenticatedSessionFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return
	}
	response, err := handler.service.CurrentAuth(request.Context(), session.token)
	if err != nil {
		handler.writeServiceError(writer, request, err, "current_authentication")
		return
	}
	respond.JSON(writer, http.StatusOK, response)
}

func (handler authHandler) logout(writer http.ResponseWriter, request *http.Request) {
	setAuthNoStore(writer)
	if request.URL.RawQuery != "" {
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
		return
	}
	session, ok := authenticatedSessionFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return
	}
	csrfToken, ok := singleStrictHeader(request, "X-CSRF-Token")
	if !ok {
		writeCSRFError(writer, request)
		return
	}
	sourceIP, validSource := validatedSourceIP(request)
	if !validSource {
		writeCSRFError(writer, request)
		return
	}
	err := handler.service.Logout(request.Context(), session.token, csrfToken, auth.RequestMetadata{
		SourceIP: sourceIP, UserAgent: request.UserAgent(), RequestID: requestIDFromContext(request.Context()),
	})
	if err != nil {
		handler.writeServiceError(writer, request, err, "logout")
		return
	}
	expireSessionCookie(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func sessionAuthMiddleware(logger *slog.Logger, service AuthService, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token, ok := singleSessionCookie(request)
		if !ok {
			writeUnauthorized(writer, request)
			return
		}
		identity, err := service.Authenticate(request.Context(), token)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) {
				writeUnauthorized(writer, request)
				return
			}
			logger.Error("user session authentication failed", "request_id", requestIDFromContext(request.Context()), "error", err)
			respond.Error(writer, http.StatusInternalServerError, "internal_error", "internal server error", requestIDFromContext(request.Context()))
			return
		}
		if !identity.User.Enabled || !identity.IsAdmin() {
			writeUnauthorized(writer, request)
			return
		}
		ctx := context.WithValue(request.Context(), authenticatedSessionKey{}, authenticatedSession{identity: identity, token: token})
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func requireRoleMiddleware(required auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, ok := currentUserFromContext(request.Context())
		if !ok {
			writeUnauthorized(writer, request)
			return
		}
		allowed := required == auth.RoleAdmin && identity.User.Role == auth.RoleAdmin
		if !allowed {
			respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func csrfProtectionMiddleware(logger *slog.Logger, service AuthService, adminOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !validRequestOrigin(request, adminOrigin) || !validFetchSite(request) {
			writeCSRFError(writer, request)
			return
		}
		csrfToken, ok := singleStrictHeader(request, "X-CSRF-Token")
		if !ok {
			writeCSRFError(writer, request)
			return
		}
		identity, ok := currentUserFromContext(request.Context())
		if !ok {
			writeUnauthorized(writer, request)
			return
		}
		if err := service.VerifyCSRF(request.Context(), identity, csrfToken); err != nil {
			switch {
			case errors.Is(err, auth.ErrUnauthorized):
				writeUnauthorized(writer, request)
			case errors.Is(err, auth.ErrForbidden):
				writeCSRFError(writer, request)
			default:
				logger.Error("CSRF session validation failed", "request_id", requestIDFromContext(request.Context()), "error", err)
				respond.Error(writer, http.StatusInternalServerError, "internal_error", "internal server error", requestIDFromContext(request.Context()))
			}
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func currentUserFromContext(ctx context.Context) (auth.Identity, bool) {
	session, ok := authenticatedSessionFromContext(ctx)
	return session.identity, ok
}

func CurrentUserFromContext(ctx context.Context) (auth.Identity, bool) {
	return currentUserFromContext(ctx)
}

func authenticatedSessionFromContext(ctx context.Context) (authenticatedSession, bool) {
	session, ok := ctx.Value(authenticatedSessionKey{}).(authenticatedSession)
	return session, ok
}

func singleSessionCookie(request *http.Request) (string, bool) {
	cookies := request.Cookies()
	value := ""
	count := 0
	for _, cookie := range cookies {
		if cookie.Name != SessionCookieName {
			continue
		}
		count++
		value = cookie.Value
	}
	if count != 1 || value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	if _, valid := auth.ParseSessionToken(value); !valid {
		return "", false
	}
	return value, true
}

func validRequestOrigin(request *http.Request, expected string) bool {
	if !validAdminOrigin(expected) {
		return false
	}
	value, ok := singleStrictHeader(request, "Origin")
	return ok && value == expected
}

func validFetchSite(request *http.Request) bool {
	values := request.Header.Values("Sec-Fetch-Site")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || strings.Contains(values[0], ",") || strings.TrimSpace(values[0]) != values[0] {
		return false
	}
	return values[0] == "same-origin" || values[0] == "none"
}

func singleStrictHeader(request *http.Request, name string) (string, bool) {
	values := request.Header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || strings.Contains(values[0], ",") {
		return "", false
	}
	return values[0], true
}

func validAdminOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.String() == value
}

func readAuthJSONBody(writer http.ResponseWriter, request *http.Request, limit int64) ([]byte, *requestBodyError) {
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_content_type", message: "exactly one Content-Type is required"}
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_content_type", message: "Content-Type must be application/json"}
	}
	for key, value := range parameters {
		if !strings.EqualFold(key, "charset") || !strings.EqualFold(value, "utf-8") {
			return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_content_type", message: "unsupported Content-Type parameter"}
		}
	}
	if len(request.Header.Values("Content-Encoding")) != 0 {
		return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_content_encoding", message: "Content-Encoding is not supported"}
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	if request.ContentLength > limit {
		return nil, &requestBodyError{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", message: "request body exceeds 64 KiB"}
	}
	limited := http.MaxBytesReader(writer, request.Body, limit)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		classified := classifyBodyReadError(err)
		if classified.status == http.StatusRequestEntityTooLarge {
			classified.message = "request body exceeds 64 KiB"
		}
		return nil, classified
	}
	return body, nil
}

func (handler authHandler) writeServiceError(writer http.ResponseWriter, request *http.Request, err error, operation string) {
	requestID := requestIDFromContext(request.Context())
	var rateError *auth.RateLimitError
	switch {
	case errors.As(err, &rateError):
		if rateError == nil || rateError.RetryAfter <= 0 {
			rateError = &auth.RateLimitError{RetryAfter: time.Second}
		}
		seconds := int64((rateError.RetryAfter + time.Second - 1) / time.Second)
		writer.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		respond.Error(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later", requestID)
	case errors.Is(err, auth.ErrInvalidCredentials):
		respond.Error(writer, http.StatusUnauthorized, "invalid_credentials", "username or password is invalid", requestID)
	case errors.Is(err, auth.ErrUnauthorized):
		writeUnauthorized(writer, request)
	case errors.Is(err, auth.ErrForbidden):
		writeCSRFError(writer, request)
	default:
		var fieldError *auth.FieldError
		if errors.As(err, &fieldError) {
			respond.Error(writer, http.StatusBadRequest, "invalid_request", "request validation failed", requestID)
			return
		}
		handler.logger.Error("authentication operation failed", "request_id", requestID, "operation", operation, "error", err)
		respond.Error(writer, http.StatusInternalServerError, "internal_error", "internal server error", requestID)
	}
}

func setAuthNoStore(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
}

func authNoStoreMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setAuthNoStore(writer)
		next.ServeHTTP(writer, request)
	})
}

func writeUnauthorized(writer http.ResponseWriter, request *http.Request) {
	respond.Error(writer, http.StatusUnauthorized, "unauthorized", "valid user credentials are required", requestIDFromContext(request.Context()))
}

func writeCSRFError(writer http.ResponseWriter, request *http.Request) {
	respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
}

func expireSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}
