package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"probe-api/internal/agentbootstrap"
	"probe-api/internal/auditlog"
	"probe-api/internal/auth"
	"probe-api/internal/httpapi/respond"
	"probe-api/internal/nodemanagement"
	"probe-api/internal/usermanagement"
)

type NodeManagementService interface {
	Create(context.Context, auth.Identity, nodemanagement.CreateRequest, nodemanagement.Metadata) (nodemanagement.Node, error)
	Update(context.Context, auth.Identity, string, nodemanagement.UpdateRequest, nodemanagement.Metadata) (nodemanagement.Node, error)
	Delete(context.Context, auth.Identity, string, nodemanagement.Metadata) error
	CreateEnrollmentToken(context.Context, auth.Identity, string, nodemanagement.CreateEnrollmentTokenRequest, nodemanagement.Metadata) (nodemanagement.EnrollmentTokenResponse, error)
	RotateAgentToken(context.Context, auth.Identity, string, nodemanagement.Metadata) (nodemanagement.AgentTokenResponse, error)
	RevokeAgentTokens(context.Context, auth.Identity, string, nodemanagement.Metadata) error
}

type UserManagementService interface {
	List(context.Context, auth.Identity, usermanagement.ListRequest) (usermanagement.ListResponse, error)
	Create(context.Context, auth.Identity, usermanagement.CreateRequest, usermanagement.Metadata) (usermanagement.User, error)
	Update(context.Context, auth.Identity, string, usermanagement.UpdateRequest, usermanagement.Metadata) (usermanagement.User, error)
	Delete(context.Context, auth.Identity, string, usermanagement.Metadata) error
}

type AuditLogService interface {
	List(context.Context, auth.Identity, auditlog.ListRequest) (auditlog.ListResponse, error)
}

type adminManagementHandler struct {
	logger              *slog.Logger
	nodes               NodeManagementService
	users               UserManagementService
	audit               AuditLogService
	installer           agentbootstrap.Generator
	agentRuntimeEnabled bool
	maxBodyBytes        int64
}

// RegisterAdminManagementRoutes registers the frozen Stage 5 node, user, and
// audit administration surface. The caller remains responsible for placing
// the returned mux behind the management CIDR middleware.
func RegisterAdminManagementRoutes(
	mux *http.ServeMux,
	logger *slog.Logger,
	nodes NodeManagementService,
	users UserManagementService,
	audit AuditLogService,
	authService AuthService,
	maxBodyBytes int64,
	adminOrigin string,
	installer agentbootstrap.Generator,
	agentRuntimeEnabled bool,
) {
	if mux == nil || logger == nil || authService == nil {
		return
	}
	handler := adminManagementHandler{
		logger: logger, nodes: nodes, users: users, audit: audit, installer: installer,
		agentRuntimeEnabled: agentRuntimeEnabled, maxBodyBytes: maxBodyBytes,
	}
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
		secured := sessionAuthMiddleware(logger, authService, requireRoleMiddleware(auth.RoleAdmin, dispatch))
		return func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Cache-Control", "no-store")
			secured.ServeHTTP(writer, request)
		}
	}
	if nodes != nil {
		mux.HandleFunc("/api/v1/admin/nodes", protect(handler.nodeCollection))
		mux.HandleFunc("/api/v1/admin/nodes/", protect(handler.nodeItem))
	}
	if users != nil {
		mux.HandleFunc("/api/v1/admin/users", protect(handler.userCollection))
		mux.HandleFunc("/api/v1/admin/users/", protect(handler.userItem))
	}
	if audit != nil {
		mux.HandleFunc("/api/v1/admin/audit-logs", protect(handler.auditCollection))
	}
}

func (handler adminManagementHandler) nodeCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeManagementMethodNotAllowed(writer, request)
		return
	}
	if request.URL.RawQuery != "" {
		writeManagementParameterError(writer, request, "query", "query parameters are not allowed")
		return
	}
	body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if bodyError != nil {
		respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
		return
	}
	createRequest, err := nodemanagement.DecodeCreate(body)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	node, err := handler.nodes.Create(request.Context(), actor, createRequest, metadata)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusCreated, node)
}

func (handler adminManagementHandler) nodeItem(writer http.ResponseWriter, request *http.Request) {
	nodeID, action, ok := parseManagementPath(request, "/api/v1/admin/nodes/")
	if !ok {
		writeManagementNotFound(writer, request)
		return
	}
	if !nodemanagement.ValidUUID(nodeID) {
		writeManagementParameterError(writer, request, "node_id", "node_id must be a canonical lowercase UUID")
		return
	}
	if request.URL.RawQuery != "" {
		writeManagementParameterError(writer, request, "query", "query parameters are not allowed")
		return
	}
	if action == "" {
		switch request.Method {
		case http.MethodPatch:
			handler.updateNode(writer, request, nodeID)
		case http.MethodDelete:
			handler.deleteNode(writer, request, nodeID)
		default:
			writer.Header().Set("Allow", http.MethodPatch+", "+http.MethodDelete)
			writeManagementMethodNotAllowed(writer, request)
		}
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeManagementMethodNotAllowed(writer, request)
		return
	}
	switch action {
	case "enrollment-token":
		handler.createEnrollmentToken(writer, request, nodeID)
	case "rotate-token":
		handler.rotateAgentToken(writer, request, nodeID)
	case "revoke-token":
		handler.revokeAgentToken(writer, request, nodeID)
	default:
		writeManagementNotFound(writer, request)
	}
}

func (handler adminManagementHandler) updateNode(writer http.ResponseWriter, request *http.Request, nodeID string) {
	body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if bodyError != nil {
		respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
		return
	}
	updateRequest, err := nodemanagement.DecodeUpdate(body)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	node, err := handler.nodes.Update(request.Context(), actor, nodeID, updateRequest, metadata)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, node)
}

func (handler adminManagementHandler) deleteNode(writer http.ResponseWriter, request *http.Request, nodeID string) {
	if !managementBodyAbsent(request) {
		writeManagementBodyNotAllowed(writer, request)
		return
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	if err := handler.nodes.Delete(request.Context(), actor, nodeID, metadata); err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler adminManagementHandler) createEnrollmentToken(writer http.ResponseWriter, request *http.Request, nodeID string) {
	if !handler.agentRuntimeEnabled {
		respond.Error(writer, http.StatusConflict, "agent_not_configured",
			"Agent integration is not configured", requestIDFromContext(request.Context()))
		return
	}
	createRequest := nodemanagement.CreateEnrollmentTokenRequest{ExpiresInSeconds: 900}
	if !managementBodyAbsent(request) {
		body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
		if bodyError != nil {
			respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
			return
		}
		var err error
		createRequest, err = nodemanagement.DecodeEnrollmentTokenRequest(body)
		if err != nil {
			handler.writeError(writer, request, err)
			return
		}
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	response, err := handler.nodes.CreateEnrollmentToken(request.Context(), actor, nodeID, createRequest, metadata)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	response.InstallCommand = handler.installer.Build(response.EnrollmentToken)
	respond.JSON(writer, http.StatusCreated, response)
}

func (handler adminManagementHandler) rotateAgentToken(writer http.ResponseWriter, request *http.Request, nodeID string) {
	if !handler.agentRuntimeEnabled {
		respond.Error(writer, http.StatusConflict, "agent_not_configured",
			"Agent integration is not configured", requestIDFromContext(request.Context()))
		return
	}
	if !managementBodyAbsent(request) {
		writeManagementBodyNotAllowed(writer, request)
		return
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	response, err := handler.nodes.RotateAgentToken(request.Context(), actor, nodeID, metadata)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, response)
}

func (handler adminManagementHandler) revokeAgentToken(writer http.ResponseWriter, request *http.Request, nodeID string) {
	if !managementBodyAbsent(request) {
		writeManagementBodyNotAllowed(writer, request)
		return
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	if err := handler.nodes.RevokeAgentTokens(request.Context(), actor, nodeID, metadata); err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler adminManagementHandler) userCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.listUsers(writer, request)
	case http.MethodPost:
		handler.createUser(writer, request)
	default:
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeManagementMethodNotAllowed(writer, request)
	}
}

func (handler adminManagementHandler) listUsers(writer http.ResponseWriter, request *http.Request) {
	query, ok := strictManagementQuery(writer, request, "limit", "cursor")
	if !ok {
		return
	}
	listRequest := usermanagement.ListRequest{Limit: usermanagement.DefaultListLimit}
	if values, exists := query["limit"]; exists {
		limit, valid := parseManagementLimit(values[0])
		if !valid {
			writeManagementParameterError(writer, request, "limit", "limit must be an integer between 1 and 200")
			return
		}
		listRequest.Limit = limit
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := usermanagement.DecodeCursor(values[0])
		if err != nil {
			writeManagementParameterError(writer, request, "cursor", "cursor is invalid")
			return
		}
		listRequest.Cursor = &cursor
	}
	actor, ok := CurrentUserFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return
	}
	response, err := handler.users.List(request.Context(), actor, listRequest)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, response)
}

func (handler adminManagementHandler) createUser(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeManagementParameterError(writer, request, "query", "query parameters are not allowed")
		return
	}
	body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if bodyError != nil {
		respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
		return
	}
	defer clear(body)
	createRequest, err := usermanagement.DecodeCreate(body)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	defer createRequest.ClearPassword()
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	user, err := handler.users.Create(request.Context(), actor, createRequest, metadata)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusCreated, user)
}

func (handler adminManagementHandler) userItem(writer http.ResponseWriter, request *http.Request) {
	userID, action, ok := parseManagementPath(request, "/api/v1/admin/users/")
	if !ok || action != "" {
		writeManagementNotFound(writer, request)
		return
	}
	if !usermanagement.ValidUUID(userID) {
		writeManagementParameterError(writer, request, "user_id", "user_id must be a canonical lowercase UUID")
		return
	}
	if request.URL.RawQuery != "" {
		writeManagementParameterError(writer, request, "query", "query parameters are not allowed")
		return
	}
	switch request.Method {
	case http.MethodPatch:
		handler.updateUser(writer, request, userID)
	case http.MethodDelete:
		handler.deleteUser(writer, request, userID)
	default:
		writer.Header().Set("Allow", http.MethodPatch+", "+http.MethodDelete)
		writeManagementMethodNotAllowed(writer, request)
	}
}

func (handler adminManagementHandler) updateUser(writer http.ResponseWriter, request *http.Request, userID string) {
	body, bodyError := readAuthJSONBody(writer, request, handler.maxBodyBytes)
	if bodyError != nil {
		respond.Error(writer, bodyError.status, bodyError.code, bodyError.message, requestIDFromContext(request.Context()))
		return
	}
	defer clear(body)
	updateRequest, err := usermanagement.DecodeUpdate(body)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	defer updateRequest.ClearPassword()
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	user, err := handler.users.Update(request.Context(), actor, userID, updateRequest, metadata)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, user)
}

func (handler adminManagementHandler) deleteUser(writer http.ResponseWriter, request *http.Request, userID string) {
	if !managementBodyAbsent(request) {
		writeManagementBodyNotAllowed(writer, request)
		return
	}
	actor, metadata, ok := managementMutationContext(writer, request)
	if !ok {
		return
	}
	if err := handler.users.Delete(request.Context(), actor, userID, metadata); err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler adminManagementHandler) auditCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeManagementMethodNotAllowed(writer, request)
		return
	}
	query, ok := strictManagementQuery(writer, request, "limit", "cursor", "action", "from", "to")
	if !ok {
		return
	}
	listRequest := auditlog.ListRequest{Limit: auditlog.DefaultListLimit}
	if values, exists := query["limit"]; exists {
		limit, valid := parseManagementLimit(values[0])
		if !valid {
			writeManagementParameterError(writer, request, "limit", "limit must be an integer between 1 and 200")
			return
		}
		listRequest.Limit = limit
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := auditlog.DecodeCursor(values[0])
		if err != nil {
			writeManagementParameterError(writer, request, "cursor", "cursor is invalid")
			return
		}
		listRequest.Cursor = &cursor
	}
	if values, exists := query["action"]; exists {
		listRequest.Action = &values[0]
	}
	if values, exists := query["from"]; exists {
		value, err := time.Parse(time.RFC3339, values[0])
		if err != nil {
			writeManagementParameterError(writer, request, "from", "from must be an RFC3339 timestamp")
			return
		}
		listRequest.From = &value
	}
	if values, exists := query["to"]; exists {
		value, err := time.Parse(time.RFC3339, values[0])
		if err != nil {
			writeManagementParameterError(writer, request, "to", "to must be an RFC3339 timestamp")
			return
		}
		listRequest.To = &value
	}
	actor, ok := CurrentUserFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return
	}
	response, err := handler.audit.List(request.Context(), actor, listRequest)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	respond.JSON(writer, http.StatusOK, response)
}

func managementMutationContext(writer http.ResponseWriter, request *http.Request) (auth.Identity, auditlog.Metadata, bool) {
	actor, ok := CurrentUserFromContext(request.Context())
	if !ok {
		writeUnauthorized(writer, request)
		return auth.Identity{}, auditlog.Metadata{}, false
	}
	sourceIP, valid := validatedSourceIP(request)
	if !valid {
		respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestIDFromContext(request.Context()))
		return auth.Identity{}, auditlog.Metadata{}, false
	}
	return actor, auditlog.Metadata{SourceIP: sourceIP, RequestID: requestIDFromContext(request.Context())}, true
}

func parseManagementPath(request *http.Request, prefix string) (string, string, bool) {
	if request.URL.EscapedPath() != request.URL.Path || !strings.HasPrefix(request.URL.Path, prefix) {
		return "", "", false
	}
	suffix := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(suffix, "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return "", "", false
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	return parts[0], action, true
}

func strictManagementQuery(writer http.ResponseWriter, request *http.Request, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeManagementParameterError(writer, request, "query", "query parameters are invalid")
		return nil, false
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	for name, values := range query {
		if !allowedSet[name] || len(values) != 1 || values[0] == "" {
			writeManagementParameterError(writer, request, name, "query parameter is unknown, duplicated, or empty")
			return nil, false
		}
	}
	return query, true
}

func parseManagementLimit(value string) (int, bool) {
	limit, err := strconv.Atoi(value)
	return limit, err == nil && limit >= 1 && limit <= 200 && strconv.Itoa(limit) == value
}

func managementBodyAbsent(request *http.Request) bool {
	return request.ContentLength == 0 && len(request.TransferEncoding) == 0 &&
		len(request.Header.Values("Content-Type")) == 0 && len(request.Header.Values("Content-Encoding")) == 0
}

func writeManagementParameterError(writer http.ResponseWriter, request *http.Request, field, message string) {
	respond.ErrorWithDetails(writer, http.StatusBadRequest, "invalid_request", message,
		requestIDFromContext(request.Context()), map[string]string{"field": field})
}

func writeManagementBodyNotAllowed(writer http.ResponseWriter, request *http.Request) {
	respond.Error(writer, http.StatusBadRequest, "invalid_request", "request body is not allowed", requestIDFromContext(request.Context()))
}

func writeManagementMethodNotAllowed(writer http.ResponseWriter, request *http.Request) {
	respond.Error(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", requestIDFromContext(request.Context()))
}

func writeManagementNotFound(writer http.ResponseWriter, request *http.Request) {
	respond.Error(writer, http.StatusNotFound, "not_found", "route not found", requestIDFromContext(request.Context()))
}

func (handler adminManagementHandler) writeError(writer http.ResponseWriter, request *http.Request, err error) {
	requestID := requestIDFromContext(request.Context())
	switch {
	case errors.Is(err, nodemanagement.ErrForbidden), errors.Is(err, usermanagement.ErrForbidden), errors.Is(err, auditlog.ErrForbidden):
		respond.Error(writer, http.StatusForbidden, "forbidden", "request is forbidden", requestID)
	case errors.Is(err, nodemanagement.ErrNotFound):
		respond.Error(writer, http.StatusNotFound, "not_found", "node not found", requestID)
	case errors.Is(err, usermanagement.ErrNotFound):
		respond.Error(writer, http.StatusNotFound, "not_found", "user not found", requestID)
	case errors.Is(err, usermanagement.ErrLastUsableAdmin):
		respond.Error(writer, http.StatusConflict, "last_admin_required", "the last enabled administrator cannot be removed, disabled, or demoted", requestID)
	case errors.Is(err, nodemanagement.ErrConflict):
		respond.Error(writer, http.StatusConflict, "node_conflict", "node state conflicts with the operation", requestID)
	case errors.Is(err, usermanagement.ErrConflict):
		respond.Error(writer, http.StatusConflict, "username_conflict", "username already exists", requestID)
	case errors.Is(err, nodemanagement.ErrInvalidRequest), errors.Is(err, usermanagement.ErrInvalidRequest),
		errors.Is(err, auditlog.ErrInvalidRequest), errors.Is(err, usermanagement.ErrInvalidCursor), errors.Is(err, auditlog.ErrInvalidCursor):
		respond.Error(writer, http.StatusBadRequest, "invalid_request", "request validation failed", requestID)
	default:
		if field, ok := managementFieldError(err); ok {
			respond.ErrorWithDetails(writer, http.StatusBadRequest, field.Code, "request validation failed", requestID,
				map[string]string{"field": field.Field})
			return
		}
		handler.logger.Error("administration operation failed", "request_id", requestID, "path", request.URL.Path, "error", err)
		respond.Error(writer, http.StatusInternalServerError, "internal_error", "internal server error", requestID)
	}
}

type managementField struct {
	Code  string
	Field string
}

func managementFieldError(err error) (managementField, bool) {
	var nodeField *nodemanagement.FieldError
	if errors.As(err, &nodeField) {
		return managementField{Code: nodeField.Code, Field: nodeField.Field}, true
	}
	var userField *usermanagement.FieldError
	if errors.As(err, &userField) {
		return managementField{Code: userField.Code, Field: userField.Field}, true
	}
	var auditField *auditlog.FieldError
	if errors.As(err, &auditField) {
		return managementField{Code: auditField.Code, Field: auditField.Field}, true
	}
	return managementField{}, false
}
