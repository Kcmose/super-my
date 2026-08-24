package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"probe-api/internal/httpapi/respond"
	"probe-api/internal/probequery"
)

type ProbeQueryService interface {
	ListTargets(context.Context, string) (probequery.PanelProbeTargetListResponse, error)
	Probes(context.Context, probequery.ProbeSeriesRequest) (probequery.ProbeSeriesResponse, error)
}

type panelProbeHandler struct {
	logger  *slog.Logger
	service ProbeQueryService
}

// registerPanelProbeRoutes adds the anonymous, allowlisted panel query routes.
// The exact patterns are more specific than the existing node-prefix handler.
func registerPanelProbeRoutes(mux *http.ServeMux, logger *slog.Logger, service ProbeQueryService) {
	if service == nil {
		return
	}
	handler := panelProbeHandler{logger: logger, service: service}
	mux.HandleFunc("/api/v1/panel/nodes/{node_id}/probes", panelNoStore(panelReadMethod(handler.probes)))
	mux.HandleFunc("/api/v1/panel/nodes/{node_id}/probe-targets", panelNoStore(panelReadMethod(handler.targets)))
}

func (h panelProbeHandler) targets(w http.ResponseWriter, request *http.Request) {
	if !validPanelProbePath(w, request) {
		return
	}
	if request.URL.RawQuery != "" {
		writePanelParameterError(w, request, "query", "query parameters are not allowed")
		return
	}
	nodeID := request.PathValue("node_id")
	if !probequery.ValidUUID(nodeID) {
		writePanelParameterError(w, request, "node_id", "node_id must be a canonical lowercase UUID")
		return
	}
	response, err := h.service.ListTargets(request.Context(), nodeID)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	respond.JSON(w, http.StatusOK, response)
}

func (h panelProbeHandler) probes(w http.ResponseWriter, request *http.Request) {
	if !validPanelProbePath(w, request) {
		return
	}
	nodeID := request.PathValue("node_id")
	if !probequery.ValidUUID(nodeID) {
		writePanelParameterError(w, request, "node_id", "node_id must be a canonical lowercase UUID")
		return
	}
	query, ok := strictPanelQuery(w, request, "target_id", "from", "to", "resolution")
	if !ok {
		return
	}
	for _, required := range []string{"target_id", "from", "to"} {
		if _, exists := query[required]; !exists {
			writePanelParameterError(w, request, required, required+" is required")
			return
		}
	}
	targetID := query.Get("target_id")
	if !probequery.ValidUUID(targetID) {
		writePanelParameterError(w, request, "target_id", "target_id must be a canonical lowercase UUID")
		return
	}
	from, valid := parseRequiredProbeTime(w, request, "from", query.Get("from"))
	if !valid {
		return
	}
	to, valid := parseRequiredProbeTime(w, request, "to", query.Get("to"))
	if !valid {
		return
	}
	if !from.Before(to) {
		writePanelParameterError(w, request, "from", "from must be earlier than to")
		return
	}
	resolution := probequery.ResolutionAuto
	if value := query.Get("resolution"); value != "" {
		resolution = probequery.Resolution(value)
		if !probequery.ValidResolution(resolution) {
			writePanelParameterError(w, request, "resolution", "resolution must be auto, raw, 5m, or 1h")
			return
		}
	}
	response, err := h.service.Probes(request.Context(), probequery.ProbeSeriesRequest{
		NodeID: nodeID, TargetID: targetID, From: from, To: to, Resolution: resolution,
	})
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	respond.JSON(w, http.StatusOK, response)
}

func validPanelProbePath(w http.ResponseWriter, request *http.Request) bool {
	if request.URL.EscapedPath() != request.URL.Path {
		respond.Error(w, http.StatusNotFound, "not_found", "route not found", requestIDFromContext(request.Context()))
		return false
	}
	return true
}

func parseRequiredProbeTime(w http.ResponseWriter, request *http.Request, field, value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
		writePanelParameterError(w, request, field, field+" must be an RFC3339 timestamp")
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func (h panelProbeHandler) writeServiceError(w http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, probequery.ErrNotFound):
		respond.Error(w, http.StatusNotFound, "not_found", "node or probe target not found", requestIDFromContext(request.Context()))
	case errors.Is(err, probequery.ErrInvalidArgument):
		respond.Error(w, http.StatusBadRequest, "invalid_request", "request parameters are invalid", requestIDFromContext(request.Context()))
	case errors.Is(err, probequery.ErrResolutionUnavailable):
		respond.Error(w, http.StatusUnprocessableEntity, "resolution_unavailable", "requested resolution is unavailable", requestIDFromContext(request.Context()))
	default:
		h.logger.Error("Panel probe API operation failed", "request_id", requestIDFromContext(request.Context()),
			"path", request.URL.Path, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "internal server error", requestIDFromContext(request.Context()))
	}
}
