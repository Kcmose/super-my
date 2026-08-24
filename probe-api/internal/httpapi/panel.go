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

	"probe-api/internal/httpapi/respond"
	"probe-api/internal/panel"
)

type PanelService interface {
	ListNodes(context.Context, panel.ListNodesRequest) (panel.NodeListResponse, error)
	GetNode(context.Context, string) (panel.NodeSummary, error)
	Metrics(context.Context, string, panel.TimeRange) (panel.MetricSeriesResponse, error)
	Disks(context.Context, string, panel.TimeRange) (panel.DiskSeriesResponse, error)
}

type panelHandler struct {
	logger  *slog.Logger
	service PanelService
}

func registerPanelRoutes(mux *http.ServeMux, logger *slog.Logger, service PanelService) {
	if service == nil {
		return
	}
	handler := panelHandler{logger: logger, service: service}
	mux.HandleFunc("/api/v1/panel/nodes", panelNoStore(panelReadMethod(handler.nodes)))
	mux.HandleFunc("/api/v1/panel/nodes/", panelNoStore(panelReadMethod(handler.nodeRoute)))
}

func panelNoStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next(w, request)
	}
}

// panelReadMethod exposes the allowlisted panel query surface to anonymous
// guests. HEAD executes the same validation and service lookup as GET while
// suppressing the response body.
func panelReadMethod(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			next(w, request)
		case http.MethodHead:
			next(headResponseWriter{ResponseWriter: w}, request)
		default:
			writePanelMethodNotAllowed(w, request)
		}
	}
}

type headResponseWriter struct {
	http.ResponseWriter
}

func (writer headResponseWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func (h panelHandler) nodes(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query, ok := strictPanelQuery(w, request, "limit", "cursor", "status")
	if !ok {
		return
	}
	listRequest := panel.ListNodesRequest{Limit: panel.DefaultListLimit}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > panel.MaxListLimit || strconv.Itoa(limit) != values[0] {
			writePanelParameterError(w, request, "limit", "limit must be an integer between 1 and 200")
			return
		}
		listRequest.Limit = limit
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := panel.DecodeCursor(values[0])
		if err != nil {
			writePanelParameterError(w, request, "cursor", "cursor is invalid")
			return
		}
		listRequest.Cursor = &cursor
	}
	if values, exists := query["status"]; exists {
		status := panel.Status(values[0])
		if !panel.ValidStatus(status) {
			writePanelParameterError(w, request, "status", "status is invalid")
			return
		}
		listRequest.Status = &status
	}
	response, err := h.service.ListNodes(request.Context(), listRequest)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	respond.JSON(w, http.StatusOK, response)
}

func (h panelHandler) nodeRoute(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	nodeID, endpoint, ok := parsePanelNodePath(request)
	if !ok {
		respond.Error(w, http.StatusNotFound, "not_found", "route not found", requestIDFromContext(request.Context()))
		return
	}
	if !panel.ValidUUID(nodeID) {
		writePanelParameterError(w, request, "node_id", "node_id must be a canonical lowercase UUID")
		return
	}
	switch endpoint {
	case "detail":
		if request.URL.RawQuery != "" {
			writePanelParameterError(w, request, "query", "query parameters are not allowed")
			return
		}
		response, err := h.service.GetNode(request.Context(), nodeID)
		if err != nil {
			h.writeServiceError(w, request, err)
			return
		}
		respond.JSON(w, http.StatusOK, response)
	case "metrics", "disks":
		query, valid := strictPanelQuery(w, request, "from", "to")
		if !valid {
			return
		}
		timeRange, valid := parsePanelTimeRange(w, request, query)
		if !valid {
			return
		}
		if endpoint == "metrics" {
			response, err := h.service.Metrics(request.Context(), nodeID, timeRange)
			if err != nil {
				h.writeServiceError(w, request, err)
				return
			}
			respond.JSON(w, http.StatusOK, response)
			return
		}
		response, err := h.service.Disks(request.Context(), nodeID, timeRange)
		if err != nil {
			h.writeServiceError(w, request, err)
			return
		}
		respond.JSON(w, http.StatusOK, response)
	}
}

func parsePanelNodePath(request *http.Request) (string, string, bool) {
	const prefix = "/api/v1/panel/nodes/"
	if request.URL.EscapedPath() != request.URL.Path || !strings.HasPrefix(request.URL.Path, prefix) {
		return "", "", false
	}
	suffix := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(suffix, "/")
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], "detail", true
	}
	if len(parts) == 2 && parts[0] != "" {
		switch parts[1] {
		case "metrics", "disks":
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func strictPanelQuery(w http.ResponseWriter, request *http.Request, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writePanelParameterError(w, request, "query", "query parameters are invalid")
		return nil, false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name, values := range query {
		if _, exists := allowedSet[name]; !exists || len(values) != 1 || values[0] == "" {
			writePanelParameterError(w, request, name, "query parameter is unknown, duplicated, or empty")
			return nil, false
		}
	}
	return query, true
}

func parsePanelTimeRange(w http.ResponseWriter, request *http.Request, query url.Values) (panel.TimeRange, bool) {
	var result panel.TimeRange
	if values, exists := query["from"]; exists {
		parsed, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
			writePanelParameterError(w, request, "from", "from must be an RFC3339 timestamp")
			return panel.TimeRange{}, false
		}
		parsed = parsed.UTC()
		result.From = &parsed
	}
	if values, exists := query["to"]; exists {
		parsed, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
			writePanelParameterError(w, request, "to", "to must be an RFC3339 timestamp")
			return panel.TimeRange{}, false
		}
		parsed = parsed.UTC()
		result.To = &parsed
	}
	if result.From != nil && result.To != nil && !result.From.Before(*result.To) {
		writePanelParameterError(w, request, "from", "from must be earlier than to")
		return panel.TimeRange{}, false
	}
	return result, true
}

func writePanelMethodNotAllowed(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
	respond.Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", requestIDFromContext(request.Context()))
}

func writePanelParameterError(w http.ResponseWriter, request *http.Request, field, message string) {
	respond.ErrorWithDetails(w, http.StatusBadRequest, "invalid_request", message,
		requestIDFromContext(request.Context()), map[string]string{"field": field})
}

func (h panelHandler) writeServiceError(w http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, panel.ErrNotFound):
		respond.Error(w, http.StatusNotFound, "not_found", "node not found", requestIDFromContext(request.Context()))
	case errors.Is(err, panel.ErrInvalidArgument), errors.Is(err, panel.ErrInvalidCursor):
		respond.Error(w, http.StatusBadRequest, "invalid_request", "request parameters are invalid", requestIDFromContext(request.Context()))
	default:
		h.logger.Error("Panel API operation failed", "request_id", requestIDFromContext(request.Context()),
			"path", request.URL.Path, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "internal server error", requestIDFromContext(request.Context()))
	}
}
