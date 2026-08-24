package httpapi

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"probe-api/internal/agent"
	"probe-api/internal/httpapi/respond"
)

type AgentService interface {
	Authenticate(context.Context, string) (agent.Identity, error)
	Enroll(context.Context, agent.EnrollRequest, string) (agent.EnrollResponse, error)
	LoadConfig(context.Context, agent.Identity, int64) (agent.Config, bool, error)
	Report(context.Context, agent.Identity, agent.ReportRequest, string) (agent.ReportResponse, error)
}

type agentHandler struct {
	logger       *slog.Logger
	service      AgentService
	maxBodyBytes int64
	limits       *agentRateLimiters
}

func registerAgentRoutes(mux *http.ServeMux, logger *slog.Logger, service AgentService, maxBodyBytes int64, limits *agentRateLimiters) {
	handler := agentHandler{logger: logger, service: service, maxBodyBytes: maxBodyBytes, limits: limits}
	mux.HandleFunc("/api/v1/agent/enroll", exactMethod(http.MethodPost, handler.enroll))
	mux.HandleFunc("/api/v1/agent/config", exactMethod(http.MethodGet, handler.config))
	mux.HandleFunc("/api/v1/agent/report", exactMethod(http.MethodPost, handler.report))
}

func (h agentHandler) enroll(w http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		respond.Error(w, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
		return
	}
	sourceIP, validSource := validatedSourceIP(request)
	if !validSource {
		respond.Error(w, http.StatusBadRequest, "invalid_client_ip", "trusted client IP header is invalid for this connection", requestIDFromContext(request.Context()))
		return
	}
	if h.limits != nil {
		if allowed, retry := h.limits.enrollIP.Allow(sourceIP); !allowed {
			writeRateLimited(w, request, retry)
			return
		}
	}
	body, bodyError := readAgentBody(w, request, h.maxBodyBytes, false)
	if bodyError != nil {
		h.writeRequestError(w, request, bodyError)
		return
	}
	var enrollment agent.EnrollRequest
	if err := agent.DecodeStrict(body, &enrollment); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_request", "request validation failed", requestIDFromContext(request.Context()))
		return
	}
	if err := enrollment.Validate(); err != nil {
		writeFieldError(w, request, http.StatusBadRequest, err)
		return
	}
	response, err := h.service.Enroll(request.Context(), enrollment, sourceIP)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond.JSON(w, http.StatusCreated, response)
}

func (h agentHandler) config(w http.ResponseWriter, request *http.Request) {
	sourceIP, validSource := validatedSourceIP(request)
	if !validSource {
		respond.Error(w, http.StatusBadRequest, "invalid_client_ip", "trusted client IP header is invalid for this connection", requestIDFromContext(request.Context()))
		return
	}
	if h.limits != nil {
		if allowed, retry := h.limits.configIP.Allow(sourceIP); !allowed {
			writeRateLimited(w, request, retry)
			return
		}
	}
	identity, ok := h.authenticate(w, request)
	if !ok {
		return
	}
	if h.limits != nil {
		if allowed, retry := h.limits.configNode.Allow(identity.NodeID); !allowed {
			writeRateLimited(w, request, retry)
			return
		}
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) != 1 {
		respond.Error(w, http.StatusBadRequest, "invalid_request", "version is the only supported query parameter", requestIDFromContext(request.Context()))
		return
	}
	versions, exists := query["version"]
	if !exists || len(versions) != 1 || versions[0] == "" {
		respond.Error(w, http.StatusBadRequest, "invalid_request", "version is required", requestIDFromContext(request.Context()))
		return
	}
	version, err := strconv.ParseInt(versions[0], 10, 64)
	if err != nil || version < 0 {
		respond.ErrorWithDetails(w, http.StatusBadRequest, "invalid_request", "version must be a non-negative signed 64-bit integer", requestIDFromContext(request.Context()), map[string]string{"field": "version"})
		return
	}
	configuration, notModified, err := h.service.LoadConfig(request.Context(), identity, version)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if notModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	respond.JSON(w, http.StatusOK, configuration)
}

func (h agentHandler) report(w http.ResponseWriter, request *http.Request) {
	sourceIP, validSource := validatedSourceIP(request)
	if !validSource {
		respond.Error(w, http.StatusBadRequest, "invalid_client_ip", "trusted client IP header is invalid for this connection", requestIDFromContext(request.Context()))
		return
	}
	if h.limits != nil {
		if allowed, retry := h.limits.reportIP.Allow(sourceIP); !allowed {
			writeRateLimited(w, request, retry)
			return
		}
	}
	identity, ok := h.authenticate(w, request)
	if !ok {
		return
	}
	if h.limits != nil {
		if allowed, retry := h.limits.reportNode.Allow(identity.NodeID); !allowed {
			writeRateLimited(w, request, retry)
			return
		}
	}
	if request.URL.RawQuery != "" {
		respond.Error(w, http.StatusBadRequest, "invalid_request", "query parameters are not allowed", requestIDFromContext(request.Context()))
		return
	}
	body, bodyError := readAgentBody(w, request, h.maxBodyBytes, true)
	if bodyError != nil {
		h.writeRequestError(w, request, bodyError)
		return
	}
	var report agent.ReportRequest
	if err := agent.DecodeStrict(body, &report); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_request", "request validation failed", requestIDFromContext(request.Context()))
		return
	}
	if err := report.Validate(); err != nil {
		writeFieldError(w, request, http.StatusUnprocessableEntity, err)
		return
	}
	response, err := h.service.Report(request.Context(), identity, report, sourceIP)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	status := http.StatusAccepted
	if response.Status == "duplicate" {
		status = http.StatusOK
	}
	respond.JSON(w, status, response)
}

func (h agentHandler) authenticate(w http.ResponseWriter, request *http.Request) (agent.Identity, bool) {
	token, ok := bearerToken(request.Header.Values("Authorization"))
	if !ok {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "valid Agent credentials are required", requestIDFromContext(request.Context()))
		return agent.Identity{}, false
	}
	identity, err := h.service.Authenticate(request.Context(), token)
	if err != nil {
		if errors.Is(err, agent.ErrUnauthorized) {
			respond.Error(w, http.StatusUnauthorized, "unauthorized", "valid Agent credentials are required", requestIDFromContext(request.Context()))
			return agent.Identity{}, false
		}
		h.logger.Error("Agent authentication failed", "request_id", requestIDFromContext(request.Context()), "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "internal server error", requestIDFromContext(request.Context()))
		return agent.Identity{}, false
	}
	return identity, true
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	separator := strings.IndexByte(value, ' ')
	if separator < 1 || !strings.EqualFold(value[:separator], "Bearer") {
		return "", false
	}
	token := value[separator+1:]
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

type requestBodyError struct {
	status  int
	code    string
	message string
}

func readAgentBody(w http.ResponseWriter, request *http.Request, limit int64, allowGzip bool) ([]byte, *requestBodyError) {
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
	encodings := request.Header.Values("Content-Encoding")
	encoding := ""
	if len(encodings) > 1 {
		return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_content_encoding", message: "unsupported Content-Encoding"}
	}
	if len(encodings) == 1 {
		encoding = strings.TrimSpace(encodings[0])
	}
	if encoding != "" && (!allowGzip || !strings.EqualFold(encoding, "gzip")) {
		return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_content_encoding", message: "unsupported Content-Encoding"}
	}
	if request.ContentLength > limit {
		return nil, &requestBodyError{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", message: "request body exceeds 256 KiB"}
	}

	limited := http.MaxBytesReader(w, request.Body, limit)
	defer limited.Close()
	if encoding == "" {
		body, err := io.ReadAll(limited)
		if err != nil {
			return nil, classifyBodyReadError(err)
		}
		return body, nil
	}

	buffered := bufio.NewReader(limited)
	compressed, err := gzip.NewReader(buffered)
	if err != nil {
		return nil, classifyGzipError(err)
	}
	compressed.Multistream(false)
	body, err := io.ReadAll(io.LimitReader(compressed, limit+1))
	if err != nil {
		_ = compressed.Close()
		return nil, classifyGzipError(err)
	}
	if int64(len(body)) > limit {
		_ = compressed.Close()
		return nil, &requestBodyError{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", message: "decompressed request body exceeds 256 KiB"}
	}
	if err := compressed.Close(); err != nil {
		return nil, classifyGzipError(err)
	}
	if _, err := buffered.Peek(1); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, classifyGzipError(err)
		}
		return nil, &requestBodyError{status: http.StatusBadRequest, code: "invalid_gzip", message: "multiple gzip members or trailing data are not allowed"}
	}
	return body, nil
}

func classifyBodyReadError(err error) *requestBodyError {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &requestBodyError{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", message: "request body exceeds 256 KiB"}
	}
	return &requestBodyError{status: http.StatusBadRequest, code: "invalid_request", message: "request body could not be read"}
}

func classifyGzipError(err error) *requestBodyError {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &requestBodyError{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", message: "compressed request body exceeds 256 KiB"}
	}
	return &requestBodyError{status: http.StatusBadRequest, code: "invalid_gzip", message: "gzip request body is invalid"}
}

func (h agentHandler) writeRequestError(w http.ResponseWriter, request *http.Request, requestError *requestBodyError) {
	respond.Error(w, requestError.status, requestError.code, requestError.message, requestIDFromContext(request.Context()))
}

func (h agentHandler) writeServiceError(w http.ResponseWriter, request *http.Request, err error) {
	requestID := requestIDFromContext(request.Context())
	switch {
	case errors.Is(err, agent.ErrUnauthorized):
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "valid Agent credentials are required", requestID)
	case errors.Is(err, agent.ErrEnrollmentTokenUsed):
		respond.Error(w, http.StatusConflict, "enrollment_token_used", "enrollment token has already been used", requestID)
	case errors.Is(err, agent.ErrConfigVersionAhead):
		respond.Error(w, http.StatusConflict, "config_version_ahead", "Agent config version is ahead of the server", requestID)
	case errors.Is(err, agent.ErrIdempotencyKeyReused):
		h.logger.Warn("Agent report idempotency key reused", "request_id", requestID)
		respond.Error(w, http.StatusConflict, "idempotency_key_reused", "batch_id was reused with a different payload", requestID)
	case errors.Is(err, agent.ErrStaleSequence):
		respond.Error(w, http.StatusConflict, "stale_sequence", "report sequence does not advance the node high-water mark", requestID)
	default:
		var fieldError *agent.FieldError
		if errors.As(err, &fieldError) {
			writeFieldError(w, request, http.StatusUnprocessableEntity, fieldError)
			return
		}
		h.logger.Error("Agent API operation failed", "request_id", requestID, "path", request.URL.Path, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "internal server error", requestID)
	}
}

func writeFieldError(w http.ResponseWriter, request *http.Request, status int, err error) {
	var fieldError *agent.FieldError
	if errors.As(err, &fieldError) {
		respond.ErrorWithDetails(w, status, fieldError.Code, "request validation failed", requestIDFromContext(request.Context()), map[string]string{"field": fieldError.Field})
		return
	}
	respond.Error(w, status, "invalid_request", "request validation failed", requestIDFromContext(request.Context()))
}

func validatedSourceIP(request *http.Request) (string, bool) {
	if address, ok := clientIPFromContext(request.Context()); ok {
		return address.String(), true
	}
	// Directly registered handlers do not have a trusted-proxy policy. In that
	// case, fail closed instead of accepting a caller-supplied internal header.
	if len(request.Header.Values("X-Probe-Client-IP")) != 0 {
		return "", false
	}
	peer := parseRemoteIP(request.RemoteAddr)
	return peer.String(), peer.IsValid()
}
