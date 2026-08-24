package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"probe-api/internal/auth"
	"probe-api/internal/httpapi/respond"
	"probe-api/internal/probetarget"
)

type ProbeTargetAdminService interface {
	List(context.Context, auth.Identity, probetarget.ListRequest) (probetarget.ListResponse, error)
	Create(context.Context, auth.Identity, probetarget.CreateRequest, probetarget.Metadata) (probetarget.Target, error)
	Update(context.Context, auth.Identity, string, probetarget.UpdateRequest, probetarget.Metadata) (probetarget.Target, error)
	Delete(context.Context, auth.Identity, string, probetarget.Metadata) error
}

type probeTargetAdminHandler struct {
	logger       *slog.Logger
	service      ProbeTargetAdminService
	maxBodyBytes int64
}

func registerProbeTargetAdminRoutes(
	mux *http.ServeMux,
	logger *slog.Logger,
	service ProbeTargetAdminService,
	authService AuthService,
	maxBodyBytes int64,
	adminOrigin string,
) {
	if service == nil || authService == nil {
		return
	}
	handler := probeTargetAdminHandler{logger: logger, service: service, maxBodyBytes: maxBodyBytes}
	protect := func(next http.HandlerFunc) http.HandlerFunc {
		csrfProtected := csrfProtectionMiddleware(logger, authService, adminOrigin, http.HandlerFunc(next))
		dispatch := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			switch request.Method {
			case http.MethodPost, http.MethodPatch, http.MethodDelete:
				csrfProtected.ServeHTTP(writer, request)
			default:
				next(writer, request)
			}
		})
		protected := sessionAuthMiddleware(logger, authService,
			requireRoleMiddleware(auth.RoleAdmin, dispatch))
		return func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Cache-Control", "no-store")
			protected.ServeHTTP(writer, request)
		}
	}
	mux.HandleFunc("/api/v1/admin/probe-targets", protect(handler.collection))
	mux.HandleFunc("/api/v1/admin/probe-targets/", protect(handler.item))
}

func (handler probeTargetAdminHandler) collection(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	switch request.Method {
	case http.MethodGet:
		handler.list(writer, request)
	case http.MethodPost:
		handler.create(writer, request)
	default:
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		respond.Error(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", requestIDFromContext(request.Context()))
	}
}

func (handler probeTargetAdminHandler) item(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	targetID, ok := parseProbeTargetPath(request)
	if !ok {
		respond.Error(writer, http.StatusNotFound, "not_found", "route not found", requestIDFromContext(request.Context()))
		return
	}
	if !probetarget.ValidUUID(targetID) {
		writeProbeTargetParameterError(writer, request, "target_id", "target_id must be a canonical lowercase UUID")
		return
	}
	if request.URL.RawQuery != "" {
		writeProbeTargetParameterError(writer, request, "query", "query parameters are not allowed")
		return
	}
	switch request.Method {
	case http.MethodPatch:
		handler.update(writer, request, targetID)
	case http.MethodDelete:
		handler.delete(writer, request, targetID)
	default:
		writer.Header().Set("Allow", http.MethodPatch+", "+http.MethodDelete)
		respond.Error(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", requestIDFromContext(request.Context()))
	}
}

func (handler probeTargetAdminHandler) list(writer http.ResponseWriter, request *http.Request) {
	query, ok := strictProbeTargetQuery(writer, request, "node_id", "limit", "cursor")
	if !ok {
		return
	}
	listRequest := probetarget.ListRequest{Limit: probetarget.DefaultListLimit}
	if values, exists := query["node_id"]; exists {
		if !probetarget.ValidUUID(values[0]) {
			writeProbeTargetParameterError(writer, request, "node_id", "node_id must be a canonical lowercase UUID")
			return
		}
		listRequest.NodeID = &values[0]
	}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > probetarget.MaxListLimit || strconv.Itoa(limit) != values[0] {
			writeProbeTargetParameterError(writer, request, "limit", "limit must be an integer between 1 and 200")
			return
		}
		listRequest.Limit = limit
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := probetarget.DecodeCursor(values[0])
		if err != nil {
			writeProbeTargetParameterError(writer, request, "cursor", "cursor is invalid")
			return
		}
		listRequest.Cursor = &cursor
	}
	actor, ok := CurrentUserFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return
	}
	response, err := handler.service.List(request.Context(), actor, listRequest)
	if err != nil {
		handler.writeServiceError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, response)
}

func (handler probeTargetAdminHandler) create(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProbeTargetParameterError(writer, request, "query", "query parameters are not allowed")
		return
	}
	body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if bodyError != nil {
		respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
		return
	}
	createRequest, err := probetarget.DecodeCreate(body)
	if err != nil {
		handler.writeServiceError(writer, request, err)
		return
	}
	actor, metadata, ok := probeTargetMutationContext(writer, request)
	if !ok {
		return
	}
	target, err := handler.service.Create(request.Context(), actor, createRequest, metadata)
	if err != nil {
		handler.writeServiceError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusCreated, target)
}

func (handler probeTargetAdminHandler) update(writer http.ResponseWriter, request *http.Request, targetID string) {
	body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if bodyError != nil {
		respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
		return
	}
	updateRequest, err := probetarget.DecodeUpdate(body)
	if err != nil {
		handler.writeServiceError(writer, request, err)
		return
	}
	actor, metadata, ok := probeTargetMutationContext(writer, request)
	if !ok {
		return
	}
	target, err := handler.service.Update(request.Context(), actor, targetID, updateRequest, metadata)
	if err != nil {
		handler.writeServiceError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, target)
}

func (handler probeTargetAdminHandler) delete(writer http.ResponseWriter, request *http.Request, targetID string) {
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 || len(request.Header.Values("Content-Type")) != 0 || len(request.Header.Values("Content-Encoding")) != 0 {
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "DELETE request body is not allowed", requestIDFromContext(request.Context()))
		return
	}
	actor, metadata, ok := probeTargetMutationContext(writer, request)
	if !ok {
		return
	}
	if err := handler.service.Delete(request.Context(), actor, targetID, metadata); err != nil {
		handler.writeServiceError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func probeTargetMutationContext(writer http.ResponseWriter, request *http.Request) (auth.Identity, probetarget.Metadata, bool) {
	actor, ok := CurrentUserFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return auth.Identity{}, probetarget.Metadata{}, false
	}
	sourceIP, valid := validatedSourceIP(request)
	if !valid {
		respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
		return auth.Identity{}, probetarget.Metadata{}, false
	}
	return actor, probetarget.Metadata{SourceIP: sourceIP, RequestID: requestIDFromContext(request.Context())}, true
}

func parseProbeTargetPath(request *http.Request) (string, bool) {
	const prefix = "/api/v1/admin/probe-targets/"
	if request.URL.EscapedPath() != request.URL.Path || !strings.HasPrefix(request.URL.Path, prefix) {
		return "", false
	}
	suffix := strings.TrimPrefix(request.URL.Path, prefix)
	return suffix, suffix != "" && !strings.Contains(suffix, "/")
}

func strictProbeTargetQuery(writer http.ResponseWriter, request *http.Request, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeProbeTargetParameterError(writer, request, "query", "query parameters are invalid")
		return nil, false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name, values := range query {
		if _, exists := allowedSet[name]; !exists || len(values) != 1 || values[0] == "" {
			writeProbeTargetParameterError(writer, request, name, "query parameter is unknown, duplicated, or empty")
			return nil, false
		}
	}
	return query, true
}

func writeProbeTargetParameterError(writer http.ResponseWriter, request *http.Request, field, message string) {
	respond.ErrorWithDetails(writer, http.StatusBadRequest, "invalid_request", message,
		requestIDFromContext(request.Context()), map[string]string{"field": field})
}

func (handler probeTargetAdminHandler) writeServiceError(writer http.ResponseWriter, request *http.Request, err error) {
	requestID := requestIDFromContext(request.Context())
	switch {
	case errors.Is(err, probetarget.ErrForbidden):
		respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestID)
	case errors.Is(err, probetarget.ErrNotFound):
		respond.Error(writer, http.StatusNotFound, "not_found", "probe target or node not found", requestID)
	case errors.Is(err, probetarget.ErrLimitExceeded):
		respond.ErrorWithDetails(writer, http.StatusConflict, "probe_target_limit_exceeded", "a node may have at most 32 probe targets", requestID,
			map[string]int{"max_probe_targets_per_node": probetarget.MaxTargetsPerNode})
	case errors.Is(err, probetarget.ErrConflict):
		respond.Error(writer, http.StatusConflict, "probe_target_conflict", "probe target state conflicts with the operation", requestID)
	case errors.Is(err, probetarget.ErrInvalidCursor), errors.Is(err, probetarget.ErrInvalidRequest):
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "request validation failed", requestID)
	default:
		var fieldError *probetarget.FieldError
		if errors.As(err, &fieldError) {
			var details any = map[string]string{"field": fieldError.Field}
			message := "request validation failed"
			if fieldError.Code == "retention_exceeds_limit" {
				details = map[string]int{"max_retention_seconds": probetarget.MaxRetentionSeconds}
				message = "retention_seconds must not exceed 7776000"
			}
			respond.ErrorWithDetails(writer, http.StatusBadRequest, fieldError.Code, message, requestID, details)
			return
		}
		handler.logger.Error("probe target administration failed", "request_id", requestID,
			"path", request.URL.Path, "error", err)
		respond.Error(writer, http.StatusInternalServerError, "internal_error", "internal server error", requestID)
	}
}
