package panel

import (
	"errors"
	"regexp"
	"time"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
	MaxHistoryPoints = 60
)

var (
	ErrNotFound        = errors.New("panel resource not found")
	ErrInvalidCursor   = errors.New("invalid panel cursor")
	ErrInvalidArgument = errors.New("invalid panel argument")
	ErrInvariant       = errors.New("panel data invariant violated")

	canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type Status string

const (
	StatusOnline       Status = "online"
	StatusOffline      Status = "offline"
	StatusUnregistered Status = "unregistered"
	StatusDisabled     Status = "disabled"
	StatusSkewed       Status = "skewed"
)

func ValidUUID(value string) bool {
	return canonicalUUIDPattern.MatchString(value)
}

func ValidStatus(value Status) bool {
	switch value {
	case StatusOnline, StatusOffline, StatusUnregistered, StatusDisabled, StatusSkewed:
		return true
	default:
		return false
	}
}

func StatusAt(enabled bool, enrolledAt, lastReceivedAt *time.Time, clockStatus string, asOf time.Time, offlineAfter time.Duration) Status {
	switch {
	case !enabled:
		return StatusDisabled
	case enrolledAt == nil:
		return StatusUnregistered
	case lastReceivedAt == nil || !lastReceivedAt.After(asOf.Add(-offlineAfter)):
		return StatusOffline
	case clockStatus == "skewed":
		return StatusSkewed
	default:
		return StatusOnline
	}
}

type AgentMetricsSettings struct {
	CollectIntervalSeconds   int32    `json:"collect_interval_seconds"`
	ReportIntervalSeconds    int32    `json:"report_interval_seconds"`
	Mountpoints              []string `json:"mountpoints"`
	IncludeVirtualInterfaces bool     `json:"include_virtual_interfaces"`
}

type AgentRuntimeSettings struct {
	ConfigRefreshIntervalSeconds int32 `json:"config_refresh_interval_seconds"`
	MaxMemoryQueueSeconds        int32 `json:"max_memory_queue_seconds"`
}

type AgentLimitSettings struct {
	MaxBatchSamples int32 `json:"max_batch_samples"`
}

type AgentSettings struct {
	Metrics AgentMetricsSettings `json:"metrics"`
	Agent   AgentRuntimeSettings `json:"agent"`
	Limits  AgentLimitSettings   `json:"limits"`
}

type MetricPoint struct {
	SampledAt            time.Time `json:"sampled_at"`
	EffectiveAt          time.Time `json:"effective_at"`
	ReceivedAt           time.Time `json:"received_at"`
	CPUPercent           float64   `json:"cpu_percent"`
	Load1                float64   `json:"load_1"`
	Load5                float64   `json:"load_5"`
	Load15               float64   `json:"load_15"`
	UptimeSeconds        float64   `json:"uptime_seconds"`
	MemoryTotalBytes     int64     `json:"memory_total_bytes"`
	MemoryUsedBytes      int64     `json:"memory_used_bytes"`
	MemoryAvailableBytes int64     `json:"memory_available_bytes"`
	SwapTotalBytes       int64     `json:"swap_total_bytes"`
	SwapUsedBytes        int64     `json:"swap_used_bytes"`
	NetworkRXBPS         float64   `json:"network_rx_bps"`
	NetworkTXBPS         float64   `json:"network_tx_bps"`
	NetworkRXBytes       int64     `json:"network_rx_bytes"`
	NetworkTXBytes       int64     `json:"network_tx_bytes"`
	TotalTrafficBytes    uint64    `json:"total_traffic_bytes"`
}

type NodeSummary struct {
	NodeID           string        `json:"node_id"`
	DisplayName      string        `json:"display_name"`
	Hostname         *string       `json:"hostname"`
	Enabled          bool          `json:"enabled"`
	Status           Status        `json:"status"`
	ConfigVersion    int64         `json:"config_version"`
	AgentSettings    AgentSettings `json:"agent_settings"`
	AgentVersion     *string       `json:"agent_version"`
	OperatingSystem  *string       `json:"operating_system"`
	Architecture     *string       `json:"architecture"`
	CountryCode      *string       `json:"country_code"`
	RegionKey        *string       `json:"region_key"`
	Location         *string       `json:"location"`
	EnrolledAt       *time.Time    `json:"enrolled_at"`
	LastReceivedAt   *time.Time    `json:"last_received_at"`
	ClockStatus      string        `json:"clock_status"`
	ClockSkewSeconds *int64        `json:"clock_skew_seconds"`
	CurrentMetrics   *MetricPoint  `json:"current_metrics"`
	RootDisk         *DiskPoint    `json:"root_disk"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type PanelSummary struct {
	Total        int64   `json:"total"`
	Online       int64   `json:"online"`
	Offline      int64   `json:"offline"`
	Unregistered int64   `json:"unregistered"`
	Disabled     int64   `json:"disabled"`
	Skewed       int64   `json:"skewed"`
	NetworkRXBPS float64 `json:"network_rx_bps"`
	NetworkTXBPS float64 `json:"network_tx_bps"`
}

type NodeListResponse struct {
	Nodes      []NodeSummary `json:"nodes"`
	NextCursor *string       `json:"next_cursor"`
	Summary    PanelSummary  `json:"summary"`
}

type ListNodesRequest struct {
	Limit  int
	Cursor *Cursor
	Status *Status
}

type TimeRange struct {
	From *time.Time
	To   *time.Time
}

type MetricSeriesResponse struct {
	NodeID string        `json:"node_id"`
	AsOf   time.Time     `json:"as_of"`
	From   time.Time     `json:"from"`
	To     time.Time     `json:"to"`
	Points []MetricPoint `json:"points"`
}

type DiskPoint struct {
	SampledAt      time.Time `json:"sampled_at"`
	EffectiveAt    time.Time `json:"effective_at"`
	ReceivedAt     time.Time `json:"received_at"`
	Mountpoint     string    `json:"mountpoint"`
	TotalBytes     int64     `json:"total_bytes"`
	UsedBytes      int64     `json:"used_bytes"`
	AvailableBytes int64     `json:"available_bytes"`
}

type DiskSeries struct {
	Mountpoint string      `json:"mountpoint"`
	Current    *DiskPoint  `json:"current"`
	Points     []DiskPoint `json:"points"`
}

type DiskSeriesResponse struct {
	NodeID string       `json:"node_id"`
	AsOf   time.Time    `json:"as_of"`
	From   time.Time    `json:"from"`
	To     time.Time    `json:"to"`
	Disks  []DiskSeries `json:"disks"`
}
