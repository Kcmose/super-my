package probequery

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const (
	MaxPoints  = 2200
	MaxTargets = 32
)

var (
	ErrNotFound              = errors.New("probe query resource not found")
	ErrInvalidArgument       = errors.New("invalid probe query argument")
	ErrResolutionUnavailable = errors.New("probe query resolution unavailable")
	ErrInvariant             = errors.New("probe query data invariant violated")

	canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type Resolution string

const (
	ResolutionAuto Resolution = "auto"
	ResolutionRaw  Resolution = "raw"
	Resolution5m   Resolution = "5m"
	Resolution1h   Resolution = "1h"
)

func ValidUUID(value string) bool {
	return canonicalUUIDPattern.MatchString(value)
}

func ValidResolution(value Resolution) bool {
	switch value {
	case ResolutionAuto, ResolutionRaw, Resolution5m, Resolution1h:
		return true
	default:
		return false
	}
}

type ProbeSeriesRequest struct {
	NodeID     string
	TargetID   string
	From       time.Time
	To         time.Time
	Resolution Resolution
}

type ProbeTarget struct {
	TargetID         string    `json:"target_id"`
	NodeID           string    `json:"node_id"`
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	Host             string    `json:"host"`
	Port             *int32    `json:"port"`
	Path             *string   `json:"path"`
	IntervalSeconds  int32     `json:"interval_seconds"`
	TimeoutSeconds   int32     `json:"timeout_seconds"`
	RetentionSeconds int32     `json:"retention_seconds"`
	Enabled          bool      `json:"enabled"`
	ConfigVersion    int64     `json:"config_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// PanelProbeTargetSummary is the intentionally redacted target representation
// exposed to anonymous allowlisted guests and administrators when selecting a trend. Network
// destinations and paths remain outside this collection response.
type PanelProbeTargetSummary struct {
	TargetID         string `json:"target_id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Enabled          bool   `json:"enabled"`
	RetentionSeconds int32  `json:"retention_seconds"`
}

type PanelProbeTargetListResponse struct {
	NodeID  string                    `json:"node_id"`
	Targets []PanelProbeTargetSummary `json:"targets"`
}

type ProbeSeriesPoint struct {
	Time             time.Time   `json:"time"`
	ResultCount      int64       `json:"result_count"`
	SentCount        int64       `json:"sent_count"`
	ReceivedCount    int64       `json:"received_count"`
	HTTPStatusCode   *int32      `json:"http_status_code"`
	HTTPErrorCount   int64       `json:"http_error_count"`
	LatencySumUS     json.Number `json:"latency_sum_us"`
	LatencyMinUS     *int64      `json:"latency_min_us"`
	LatencyMaxUS     *int64      `json:"latency_max_us"`
	LossRate         float64     `json:"loss_rate"`
	FailureRate      float64     `json:"failure_rate"`
	AverageLatencyUS *float64    `json:"average_latency_us"`
}

type ProbeSeriesResponse struct {
	Target     ProbeTarget        `json:"target"`
	Resolution Resolution         `json:"resolution"`
	From       time.Time          `json:"from"`
	To         time.Time          `json:"to"`
	AsOf       time.Time          `json:"as_of"`
	Points     []ProbeSeriesPoint `json:"points"`
}
