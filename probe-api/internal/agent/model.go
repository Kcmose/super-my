package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/netip"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxBodyBytes       = 256 * 1024
	MaxBatchSamples    = 120
	MaxProbeTargets    = 32
	MaxMountpoints     = 32
	MaxInt64           = int64(^uint64(0) >> 1)
	MaxProbeCount      = int64(1<<32 - 1)
	ClockSkewThreshold = 120 * time.Second
	SamplePastWindow   = 300 * time.Second
	SampleFutureWindow = 30 * time.Second
)

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var (
	ErrUnauthorized         = errors.New("agent credential is invalid")
	ErrEnrollmentTokenUsed  = errors.New("enrollment token already used")
	ErrConfigVersionAhead   = errors.New("agent config version is ahead")
	ErrIdempotencyKeyReused = errors.New("idempotency key reused")
	ErrStaleSequence        = errors.New("report sequence is stale")
)

type FieldError struct {
	Code    string
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

type Identity struct {
	NodeID  string
	TokenID string
}

type EnrollRequest struct {
	EnrollmentToken string `json:"enrollment_token"`
	Hostname        string `json:"hostname"`
	AgentVersion    string `json:"agent_version"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
}

func (r *EnrollRequest) Validate() error {
	if len(r.EnrollmentToken) < 32 || len(r.EnrollmentToken) > 1024 || strings.TrimSpace(r.EnrollmentToken) != r.EnrollmentToken || containsControl(r.EnrollmentToken) {
		return fieldError("invalid_enrollment", "enrollment_token", "must be a valid opaque token")
	}
	r.Hostname = strings.TrimSpace(r.Hostname)
	if runeLength(r.Hostname) < 1 || runeLength(r.Hostname) > 253 || containsControl(r.Hostname) {
		return fieldError("invalid_enrollment", "hostname", "must contain 1 to 253 printable characters")
	}
	r.AgentVersion = strings.TrimSpace(r.AgentVersion)
	if runeLength(r.AgentVersion) < 1 || runeLength(r.AgentVersion) > 64 || containsControl(r.AgentVersion) {
		return fieldError("invalid_enrollment", "agent_version", "must contain 1 to 64 printable characters")
	}
	if r.OS != "linux" {
		return fieldError("invalid_enrollment", "os", "must be linux")
	}
	if r.Arch != "amd64" && r.Arch != "arm64" {
		return fieldError("invalid_enrollment", "arch", "must be amd64 or arm64")
	}
	return nil
}

type EnrollResponse struct {
	NodeID        string `json:"node_id"`
	AgentToken    string `json:"agent_token"`
	ConfigVersion int64  `json:"config_version"`
}

type MetricsConfig struct {
	CollectIntervalSeconds   int      `json:"collect_interval_seconds"`
	ReportIntervalSeconds    int      `json:"report_interval_seconds"`
	Mountpoints              []string `json:"mountpoints"`
	IncludeVirtualInterfaces bool     `json:"include_virtual_interfaces"`
}

type RuntimeConfig struct {
	ConfigRefreshIntervalSeconds int `json:"config_refresh_interval_seconds"`
	MaxMemoryQueueSeconds        int `json:"max_memory_queue_seconds"`
}

type LimitsConfig struct {
	MaxBatchSamples int `json:"max_batch_samples"`
}

type ProbeTarget struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Type             string  `json:"type"`
	Host             string  `json:"host"`
	Port             *int32  `json:"port"`
	Path             *string `json:"path"`
	IntervalSeconds  int     `json:"interval_seconds"`
	TimeoutSeconds   int     `json:"timeout_seconds"`
	RetentionSeconds int     `json:"retention_seconds"`
	Enabled          bool    `json:"enabled"`
	ConfigVersion    int64   `json:"config_version"`
}

type Config struct {
	ConfigVersion int64         `json:"config_version"`
	IssuedAt      time.Time     `json:"issued_at"`
	Metrics       MetricsConfig `json:"metrics"`
	Agent         RuntimeConfig `json:"agent"`
	Limits        LimitsConfig  `json:"limits"`
	ProbeTargets  []ProbeTarget `json:"probe_targets"`
}

func (c Config) ValidateSettings() error {
	if c.ConfigVersion < 1 {
		return errors.New("persistent config version must be positive")
	}
	if c.Metrics.CollectIntervalSeconds < 5 || c.Metrics.CollectIntervalSeconds > 300 || c.Metrics.ReportIntervalSeconds < c.Metrics.CollectIntervalSeconds || c.Metrics.ReportIntervalSeconds > 300 {
		return errors.New("persistent metric intervals violate the frozen range")
	}
	if len(c.Metrics.Mountpoints) < 1 || len(c.Metrics.Mountpoints) > MaxMountpoints {
		return errors.New("persistent mountpoint count violates the frozen range")
	}
	seen := make(map[string]struct{}, len(c.Metrics.Mountpoints))
	hasRoot := false
	for _, mountpoint := range c.Metrics.Mountpoints {
		if !validMountpoint(mountpoint) {
			return errors.New("persistent mountpoint is invalid")
		}
		if _, exists := seen[mountpoint]; exists {
			return errors.New("persistent mountpoints contain a duplicate")
		}
		seen[mountpoint] = struct{}{}
		hasRoot = hasRoot || mountpoint == "/"
	}
	if !hasRoot {
		return errors.New("persistent mountpoints do not include root")
	}
	if c.Agent.ConfigRefreshIntervalSeconds < 10 || c.Agent.ConfigRefreshIntervalSeconds > 86400 || c.Agent.MaxMemoryQueueSeconds < 1 || c.Agent.MaxMemoryQueueSeconds > 300 {
		return errors.New("persistent Agent runtime settings violate the frozen range")
	}
	if c.Agent.MaxMemoryQueueSeconds < c.Metrics.ReportIntervalSeconds {
		return errors.New("persistent Agent memory queue is shorter than the report interval")
	}
	if c.Limits.MaxBatchSamples < 1 || c.Limits.MaxBatchSamples > MaxBatchSamples {
		return errors.New("persistent batch limit violates the frozen range")
	}
	return nil
}

func (c Config) Validate() error {
	if err := c.ValidateSettings(); err != nil {
		return err
	}
	if len(c.ProbeTargets) > MaxProbeTargets {
		return errors.New("persistent probe target count exceeds the frozen limit")
	}
	for _, target := range c.ProbeTargets {
		if !validUUID(target.ID) || runeLength(target.Name) < 1 || runeLength(target.Name) > 128 || strings.TrimSpace(target.Name) != target.Name || containsControl(target.Name) {
			return errors.New("persistent probe target identity or name is invalid")
		}
		if !validProbeHost(target.Host) {
			return errors.New("persistent probe target host is invalid")
		}
		if target.IntervalSeconds < 10 || target.IntervalSeconds > 86400 || target.TimeoutSeconds < 1 || target.TimeoutSeconds > 60 || target.TimeoutSeconds > target.IntervalSeconds {
			return errors.New("persistent probe target intervals violate the frozen range")
		}
		if target.RetentionSeconds < 1 || target.RetentionSeconds > 7776000 || target.ConfigVersion < 1 {
			return errors.New("persistent probe target retention or version is invalid")
		}
		switch target.Type {
		case "tcp":
			if target.Port == nil || *target.Port < 1 || *target.Port > 65535 || target.Path != nil {
				return errors.New("persistent TCP target has an invalid port or path")
			}
		case "http", "https":
			if target.Port != nil && (*target.Port < 1 || *target.Port > 65535) {
				return errors.New("persistent HTTP target has an invalid port")
			}
			if target.Path != nil && (runeLength(*target.Path) > 2048 || !strings.HasPrefix(*target.Path, "/") || strings.ContainsAny(*target.Path, "?#") || containsControl(*target.Path)) {
				return errors.New("persistent HTTP target has an invalid path")
			}
		default:
			return errors.New("persistent probe target type is not enabled; Stage 4 supports tcp, http, and https")
		}
	}
	return nil
}

func validProbeHost(value string) bool {
	if runeLength(value) < 1 || runeLength(value) > 253 || strings.TrimSpace(value) != value || containsControl(value) {
		return false
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Zone() == ""
	}
	if strings.HasSuffix(value, ".") {
		value = strings.TrimSuffix(value, ".")
	}
	if value == "" || strings.ContainsAny(value, "/@?#\\:;'\"`$|&<>(){}[]%") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-') {
				return false
			}
		}
	}
	return true
}

type MetricSample struct {
	SampledAt            time.Time `json:"sampled_at"`
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
}

type DiskSample struct {
	SampledAt      time.Time `json:"sampled_at"`
	Mountpoint     string    `json:"mountpoint"`
	TotalBytes     int64     `json:"total_bytes"`
	UsedBytes      int64     `json:"used_bytes"`
	AvailableBytes int64     `json:"available_bytes"`
}

type NullableInt64 struct {
	Set   bool
	Value *int64
}

func (v *NullableInt64) UnmarshalJSON(data []byte) error {
	v.Set = true
	if bytes.Equal(data, []byte("null")) {
		v.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

func (v NullableInt64) MarshalJSON() ([]byte, error) {
	if v.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*v.Value)
}

type NullableInt struct {
	Set   bool
	Value *int
}

func (v *NullableInt) UnmarshalJSON(data []byte) error {
	v.Set = true
	if bytes.Equal(data, []byte("null")) {
		v.Value = nil
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

func (v NullableInt) MarshalJSON() ([]byte, error) {
	if v.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*v.Value)
}

type NullableString struct {
	Set   bool
	Value *string
}

func (v *NullableString) UnmarshalJSON(data []byte) error {
	v.Set = true
	if bytes.Equal(data, []byte("null")) {
		v.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

func (v NullableString) MarshalJSON() ([]byte, error) {
	if v.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*v.Value)
}

type ProbeResult struct {
	TargetID       string         `json:"target_id"`
	SampledAt      time.Time      `json:"sampled_at"`
	SentCount      int64          `json:"sent_count"`
	ReceivedCount  int64          `json:"received_count"`
	LatencySumUS   int64          `json:"latency_sum_us"`
	LatencyMinUS   NullableInt64  `json:"latency_min_us"`
	LatencyMaxUS   NullableInt64  `json:"latency_max_us"`
	HTTPStatusCode NullableInt    `json:"http_status_code"`
	ErrorCode      NullableString `json:"error_code"`
}

type ReportRequest struct {
	BatchID       string         `json:"batch_id"`
	Sequence      int64          `json:"sequence"`
	AgentTime     time.Time      `json:"agent_time"`
	AgentVersion  string         `json:"agent_version"`
	ConfigVersion int64          `json:"config_version"`
	Metrics       []MetricSample `json:"metrics"`
	Disks         []DiskSample   `json:"disks"`
	ProbeResults  []ProbeResult  `json:"probe_results"`
}

type ReportResponse struct {
	BatchID              string    `json:"batch_id"`
	Status               string    `json:"status"`
	ReceivedAt           time.Time `json:"received_at"`
	ClockStatus          string    `json:"clock_status"`
	CurrentConfigVersion int64     `json:"current_config_version"`
}

func DecodeStrict(data []byte, target any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON body must be valid UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	if err := validateExactShape(data, target); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func validateExactShape(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return errors.New("request body must be a JSON object")
	}
	switch target.(type) {
	case *EnrollRequest:
		return requireExactObject(root, "request", []string{"enrollment_token", "hostname", "agent_version", "os", "arch"}, nil)
	case *ReportRequest:
		if err := requireExactObject(root, "request", []string{"batch_id", "sequence", "agent_time", "agent_version", "config_version", "metrics", "disks", "probe_results"}, nil); err != nil {
			return err
		}
		metrics, err := requiredArray(root, "metrics")
		if err != nil {
			return err
		}
		for index, item := range metrics {
			object, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("metrics[%d] must be an object", index)
			}
			keys := []string{"sampled_at", "cpu_percent", "load_1", "load_5", "load_15", "uptime_seconds", "memory_total_bytes", "memory_used_bytes", "memory_available_bytes", "swap_total_bytes", "swap_used_bytes", "network_rx_bps", "network_tx_bps", "network_rx_bytes", "network_tx_bytes"}
			if err := requireExactObject(object, fmt.Sprintf("metrics[%d]", index), keys, nil); err != nil {
				return err
			}
		}
		disks, err := requiredArray(root, "disks")
		if err != nil {
			return err
		}
		for index, item := range disks {
			object, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("disks[%d] must be an object", index)
			}
			keys := []string{"sampled_at", "mountpoint", "total_bytes", "used_bytes", "available_bytes"}
			if err := requireExactObject(object, fmt.Sprintf("disks[%d]", index), keys, nil); err != nil {
				return err
			}
		}
		probes, err := requiredArray(root, "probe_results")
		if err != nil {
			return err
		}
		for index, item := range probes {
			object, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("probe_results[%d] must be an object", index)
			}
			keys := []string{"target_id", "sampled_at", "sent_count", "received_count", "latency_sum_us", "latency_min_us", "latency_max_us", "http_status_code", "error_code"}
			nullable := map[string]struct{}{"latency_min_us": {}, "latency_max_us": {}, "http_status_code": {}, "error_code": {}}
			if err := requireExactObject(object, fmt.Sprintf("probe_results[%d]", index), keys, nullable); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func requireExactObject(object map[string]any, field string, keys []string, nullable map[string]struct{}) error {
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
		value, exists := object[key]
		if !exists {
			return fmt.Errorf("%s.%s is required", field, key)
		}
		if value == nil {
			if _, acceptsNull := nullable[key]; !acceptsNull {
				return fmt.Errorf("%s.%s must not be null", field, key)
			}
		}
	}
	for key := range object {
		if _, exists := allowed[key]; !exists {
			return fmt.Errorf("unknown field %s.%s", field, key)
		}
	}
	return nil
}

func requiredArray(object map[string]any, key string) ([]any, error) {
	value, exists := object[key]
	if !exists || value == nil {
		return nil, fmt.Errorf("request.%s is required and must be an array", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("request.%s must be an array", key)
	}
	return items, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request body must contain exactly one JSON value")
	}
	return err
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func (r *ReportRequest) Validate() error {
	if !validUUID(r.BatchID) {
		return fieldError("invalid_report", "batch_id", "must be a canonical lowercase UUID")
	}
	if r.Sequence < 1 {
		return fieldError("invalid_report", "sequence", "must be a positive signed 64-bit integer")
	}
	if !validTimestamp(r.AgentTime) {
		return fieldError("invalid_report", "agent_time", "is required")
	}
	if runeLength(r.AgentVersion) < 1 || runeLength(r.AgentVersion) > 64 || strings.TrimSpace(r.AgentVersion) != r.AgentVersion || containsControl(r.AgentVersion) {
		return fieldError("invalid_report", "agent_version", "must contain 1 to 64 printable characters")
	}
	if r.ConfigVersion < 1 {
		return fieldError("invalid_report", "config_version", "must be positive")
	}
	if r.Metrics == nil || r.Disks == nil || r.ProbeResults == nil {
		return fieldError("invalid_report", "metrics", "metrics, disks, and probe_results are required arrays")
	}
	if len(r.Metrics) > MaxBatchSamples || len(r.Disks) > MaxBatchSamples || len(r.ProbeResults) > MaxBatchSamples {
		return fieldError("invalid_report", "metrics", "each data array is limited to 120 items")
	}
	if len(r.Metrics)+len(r.Disks)+len(r.ProbeResults) == 0 {
		return fieldError("invalid_report", "metrics", "the three data arrays cannot all be empty")
	}

	distinctTimes := make(map[string]struct{})
	distinctMountpoints := make(map[string]struct{})
	for i := range r.Metrics {
		if err := r.validateMetric(i, &distinctTimes); err != nil {
			return err
		}
	}
	for i := range r.Disks {
		if err := r.validateDisk(i, &distinctTimes); err != nil {
			return err
		}
		distinctMountpoints[r.Disks[i].Mountpoint] = struct{}{}
	}
	for i := range r.ProbeResults {
		if err := r.validateProbe(i, &distinctTimes); err != nil {
			return err
		}
	}
	if len(distinctTimes) > MaxBatchSamples {
		return fieldError("invalid_report", "sampled_at", "a batch may contain at most 120 distinct sample times")
	}
	if len(distinctMountpoints) > MaxMountpoints {
		return fieldError("invalid_report", "disks", "a batch may contain at most 32 distinct mountpoints")
	}
	return nil
}

func (r *ReportRequest) validateSampleTime(sampled time.Time, field string, distinct *map[string]struct{}) error {
	if !validTimestamp(sampled) || sampled.Before(r.AgentTime.Add(-SamplePastWindow)) || sampled.After(r.AgentTime.Add(SampleFutureWindow)) {
		return fieldError("sample_time_out_of_window", field, "must be between agent_time-300s and agent_time+30s")
	}
	(*distinct)[sampled.UTC().Format(time.RFC3339Nano)] = struct{}{}
	return nil
}

func (r *ReportRequest) validateMetric(index int, distinct *map[string]struct{}) error {
	v := r.Metrics[index]
	prefix := fmt.Sprintf("metrics[%d]", index)
	if err := r.validateSampleTime(v.SampledAt, prefix+".sampled_at", distinct); err != nil {
		return err
	}
	if !finiteInRange(v.CPUPercent, 0, 100) {
		return fieldError("invalid_report", prefix+".cpu_percent", "must be finite and between 0 and 100")
	}
	for field, value := range map[string]float64{"load_1": v.Load1, "load_5": v.Load5, "load_15": v.Load15, "uptime_seconds": v.UptimeSeconds, "network_rx_bps": v.NetworkRXBPS, "network_tx_bps": v.NetworkTXBPS} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fieldError("invalid_report", prefix+"."+field, "must be a finite non-negative number")
		}
	}
	if v.MemoryTotalBytes < 0 || v.MemoryUsedBytes < 0 || v.MemoryUsedBytes > v.MemoryTotalBytes || v.MemoryAvailableBytes < 0 || v.MemoryAvailableBytes > v.MemoryTotalBytes {
		return fieldError("invalid_report", prefix+".memory_total_bytes", "memory byte values are inconsistent")
	}
	if v.SwapTotalBytes < 0 || v.SwapUsedBytes < 0 || v.SwapUsedBytes > v.SwapTotalBytes {
		return fieldError("invalid_report", prefix+".swap_total_bytes", "swap byte values are inconsistent")
	}
	if v.NetworkRXBytes < 0 || v.NetworkTXBytes < 0 {
		return fieldError("invalid_report", prefix+".network_rx_bytes", "network counters must be non-negative")
	}
	return nil
}

func (r *ReportRequest) validateDisk(index int, distinct *map[string]struct{}) error {
	v := r.Disks[index]
	prefix := fmt.Sprintf("disks[%d]", index)
	if err := r.validateSampleTime(v.SampledAt, prefix+".sampled_at", distinct); err != nil {
		return err
	}
	if !validMountpoint(v.Mountpoint) {
		return fieldError("invalid_report", prefix+".mountpoint", "must be a normalized absolute mountpoint of at most 4096 characters")
	}
	if v.TotalBytes < 0 || v.UsedBytes < 0 || v.UsedBytes > v.TotalBytes || v.AvailableBytes < 0 || v.AvailableBytes > v.TotalBytes {
		return fieldError("invalid_report", prefix+".total_bytes", "disk byte values are inconsistent")
	}
	return nil
}

func (r *ReportRequest) validateProbe(index int, distinct *map[string]struct{}) error {
	v := r.ProbeResults[index]
	prefix := fmt.Sprintf("probe_results[%d]", index)
	if !validUUID(v.TargetID) {
		return fieldError("invalid_report", prefix+".target_id", "must be a canonical lowercase UUID")
	}
	if err := r.validateSampleTime(v.SampledAt, prefix+".sampled_at", distinct); err != nil {
		return err
	}
	if v.SentCount < 1 || v.SentCount > MaxProbeCount || v.ReceivedCount < 0 || v.ReceivedCount > v.SentCount || v.ReceivedCount > MaxProbeCount {
		return fieldError("invalid_report", prefix+".sent_count", "probe counts are inconsistent")
	}
	if v.LatencySumUS < 0 {
		return fieldError("invalid_report", prefix+".latency_sum_us", "must be non-negative")
	}
	if !v.LatencyMinUS.Set || !v.LatencyMaxUS.Set || !v.HTTPStatusCode.Set || !v.ErrorCode.Set {
		return fieldError("invalid_report", prefix, "nullable latency, HTTP status, and error fields are required")
	}
	if v.ReceivedCount == 0 {
		if v.LatencySumUS != 0 || v.LatencyMinUS.Value != nil || v.LatencyMaxUS.Value != nil {
			return fieldError("invalid_report", prefix+".latency_sum_us", "zero received probes require zero sum and null min/max")
		}
	} else {
		if v.LatencyMinUS.Value == nil || v.LatencyMaxUS.Value == nil || *v.LatencyMinUS.Value < 0 || *v.LatencyMaxUS.Value < *v.LatencyMinUS.Value {
			return fieldError("invalid_report", prefix+".latency_min_us", "successful probes require ordered non-negative min/max")
		}
		received := big.NewInt(v.ReceivedCount)
		minimumTotal := new(big.Int).Mul(big.NewInt(*v.LatencyMinUS.Value), received)
		maximumTotal := new(big.Int).Mul(big.NewInt(*v.LatencyMaxUS.Value), received)
		sum := big.NewInt(v.LatencySumUS)
		if sum.Cmp(minimumTotal) < 0 || sum.Cmp(maximumTotal) > 0 {
			return fieldError("invalid_report", prefix+".latency_sum_us", "average latency must be between min and max")
		}
	}
	if v.HTTPStatusCode.Value != nil && (*v.HTTPStatusCode.Value < 100 || *v.HTTPStatusCode.Value > 599) {
		return fieldError("invalid_report", prefix+".http_status_code", "must be null or between 100 and 599")
	}
	if v.ErrorCode.Value != nil {
		value := *v.ErrorCode.Value
		if runeLength(value) < 1 || runeLength(value) > 128 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") || containsControl(value) {
			return fieldError("invalid_report", prefix+".error_code", "must be a non-sensitive stable code without surrounding whitespace")
		}
	}
	return nil
}

func (r ReportRequest) CanonicalChecksum() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func EffectiveTime(sampledAt, agentTime, receivedAt time.Time, clockStatus string) time.Time {
	if clockStatus == "ok" {
		return sampledAt.UTC()
	}
	return receivedAt.UTC().Add(sampledAt.Sub(agentTime))
}

func ClockStatus(agentTime, receivedAt time.Time) (string, int64) {
	status := "ok"
	if agentTime.After(receivedAt.Add(ClockSkewThreshold)) || agentTime.Before(receivedAt.Add(-ClockSkewThreshold)) {
		status = "skewed"
	}
	seconds := agentTime.Unix() - receivedAt.Unix()
	nanoseconds := agentTime.Nanosecond() - receivedAt.Nanosecond()
	if seconds > 0 && nanoseconds < 0 {
		seconds--
	} else if seconds < 0 && nanoseconds > 0 {
		seconds++
	}
	return status, seconds
}

func ValidUUID(value string) bool { return validUUID(value) }

func validUUID(value string) bool { return canonicalUUIDPattern.MatchString(value) }

func validTimestamp(value time.Time) bool {
	year := value.Year()
	return !value.IsZero() && year >= 1 && year <= 9999
}

func validMountpoint(value string) bool {
	return runeLength(value) >= 1 && runeLength(value) <= 4096 && strings.HasPrefix(value, "/") && path.Clean(value) == value && !containsControl(value)
}

func finiteInRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func runeLength(value string) int { return utf8.RuneCountInString(value) }

func fieldError(code, field, message string) error {
	return &FieldError{Code: code, Field: field, Message: message}
}
