package nodemanagement

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"probe-api/internal/agent"
	"probe-api/internal/auditlog"
	"probe-api/internal/panel"
)

var (
	ErrForbidden      = errors.New("administrator role is required")
	ErrNotFound       = errors.New("node was not found")
	ErrConflict       = errors.New("node state conflicts with the operation")
	ErrInvalidRequest = errors.New("node request is invalid")

	canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type Metadata = auditlog.Metadata
type Node = panel.NodeSummary
type AgentSettings = panel.AgentSettings

type FieldError struct {
	Code    string
	Field   string
	Message string
}

func (err *FieldError) Error() string {
	if err.Field == "" {
		return err.Message
	}
	return err.Field + ": " + err.Message
}

type NullableString struct {
	Set   bool
	Value *string
}

type CreateRequest struct {
	DisplayName   string
	Enabled       *bool
	CountryCode   *string
	RegionKey     *string
	Location      *string
	AgentSettings *AgentSettings
}

type UpdateRequest struct {
	DisplayName   *string
	Enabled       *bool
	CountryCode   NullableString
	RegionKey     NullableString
	Location      NullableString
	AgentSettings *AgentSettings
}

type CreateEnrollmentTokenRequest struct {
	ExpiresInSeconds int
}

type EnrollmentTokenResponse struct {
	NodeID          string    `json:"node_id"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
	InstallCommand  string    `json:"install_command"`
}

type AgentTokenResponse struct {
	NodeID     string    `json:"node_id"`
	AgentToken string    `json:"agent_token"`
	CreatedAt  time.Time `json:"created_at"`
}

func DefaultAgentSettings() AgentSettings {
	return AgentSettings{
		Metrics: panel.AgentMetricsSettings{
			CollectIntervalSeconds: 5, ReportIntervalSeconds: 10,
			Mountpoints: []string{"/"}, IncludeVirtualInterfaces: false,
		},
		Agent:  panel.AgentRuntimeSettings{ConfigRefreshIntervalSeconds: 60, MaxMemoryQueueSeconds: 300},
		Limits: panel.AgentLimitSettings{MaxBatchSamples: 120},
	}
}

func ValidUUID(value string) bool {
	return canonicalUUIDPattern.MatchString(value)
}

func DecodeCreate(body []byte) (CreateRequest, error) {
	object, err := decodeObject(body)
	if err != nil {
		return CreateRequest{}, err
	}
	allowed := map[string]bool{"display_name": true, "enabled": true, "country_code": true, "region_key": true, "location": true, "agent_settings": true}
	if err := rejectUnknown(object, allowed); err != nil {
		return CreateRequest{}, err
	}
	raw, ok := object["display_name"]
	if !ok {
		return CreateRequest{}, invalidField("display_name", "is required")
	}
	var request CreateRequest
	if err := decodeRequired(raw, &request.DisplayName); err != nil {
		return CreateRequest{}, invalidField("display_name", "must be a string")
	}
	if raw, ok := object["enabled"]; ok {
		var value bool
		if err := decodeRequired(raw, &value); err != nil {
			return CreateRequest{}, invalidField("enabled", "must be a boolean")
		}
		request.Enabled = &value
	}
	if request.CountryCode, err = decodeOptionalNullableString(object, "country_code"); err != nil {
		return CreateRequest{}, err
	}
	if request.RegionKey, err = decodeOptionalNullableString(object, "region_key"); err != nil {
		return CreateRequest{}, err
	}
	if request.Location, err = decodeOptionalNullableString(object, "location"); err != nil {
		return CreateRequest{}, err
	}
	if raw, ok := object["agent_settings"]; ok {
		settings, err := decodeSettings(raw)
		if err != nil {
			return CreateRequest{}, err
		}
		request.AgentSettings = &settings
	}
	if err := validateNodeValues(request.DisplayName, request.CountryCode, request.RegionKey, request.Location); err != nil {
		return CreateRequest{}, err
	}
	if request.AgentSettings != nil {
		if err := validateSettings(*request.AgentSettings); err != nil {
			return CreateRequest{}, err
		}
	}
	return request, nil
}

func DecodeUpdate(body []byte) (UpdateRequest, error) {
	object, err := decodeObject(body)
	if err != nil {
		return UpdateRequest{}, err
	}
	if len(object) == 0 {
		return UpdateRequest{}, invalidField("request", "at least one field is required")
	}
	allowed := map[string]bool{"display_name": true, "enabled": true, "country_code": true, "region_key": true, "location": true, "agent_settings": true}
	if err := rejectUnknown(object, allowed); err != nil {
		return UpdateRequest{}, err
	}
	var request UpdateRequest
	if raw, ok := object["display_name"]; ok {
		var value string
		if err := decodeRequired(raw, &value); err != nil {
			return UpdateRequest{}, invalidField("display_name", "must be a string")
		}
		request.DisplayName = &value
	}
	if raw, ok := object["enabled"]; ok {
		var value bool
		if err := decodeRequired(raw, &value); err != nil {
			return UpdateRequest{}, invalidField("enabled", "must be a boolean")
		}
		request.Enabled = &value
	}
	if request.CountryCode, err = decodeNullableUpdate(object, "country_code"); err != nil {
		return UpdateRequest{}, err
	}
	if request.RegionKey, err = decodeNullableUpdate(object, "region_key"); err != nil {
		return UpdateRequest{}, err
	}
	if request.Location, err = decodeNullableUpdate(object, "location"); err != nil {
		return UpdateRequest{}, err
	}
	if raw, ok := object["agent_settings"]; ok {
		settings, err := decodeSettings(raw)
		if err != nil {
			return UpdateRequest{}, err
		}
		request.AgentSettings = &settings
	}
	return request, nil
}

func DecodeEnrollmentTokenRequest(body []byte) (CreateEnrollmentTokenRequest, error) {
	object, err := decodeObject(body)
	if err != nil {
		return CreateEnrollmentTokenRequest{}, err
	}
	if err := rejectUnknown(object, map[string]bool{"expires_in_seconds": true}); err != nil {
		return CreateEnrollmentTokenRequest{}, err
	}
	request := CreateEnrollmentTokenRequest{ExpiresInSeconds: 900}
	if raw, ok := object["expires_in_seconds"]; ok {
		if err := decodeRequired(raw, &request.ExpiresInSeconds); err != nil {
			return CreateEnrollmentTokenRequest{}, invalidField("expires_in_seconds", "must be an integer")
		}
	}
	if request.ExpiresInSeconds < 60 || request.ExpiresInSeconds > 86400 {
		return CreateEnrollmentTokenRequest{}, invalidField("expires_in_seconds", "must be between 60 and 86400")
	}
	return request, nil
}

func validateNodeValues(displayName string, countryCode, regionKey, location *string) error {
	if !validText(displayName, 128) {
		return invalidField("display_name", "must contain 1 to 128 valid characters")
	}
	if countryCode != nil && !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(*countryCode) {
		return invalidField("country_code", "must contain two uppercase ASCII letters")
	}
	if regionKey != nil && !validText(*regionKey, 64) {
		return invalidField("region_key", "must contain 1 to 64 valid characters")
	}
	if location != nil && !validText(*location, 128) {
		return invalidField("location", "must contain 1 to 128 valid characters")
	}
	return nil
}

func validateSettings(settings AgentSettings) error {
	configuration := agent.Config{
		ConfigVersion: 1,
		Metrics: agent.MetricsConfig{
			CollectIntervalSeconds:   int(settings.Metrics.CollectIntervalSeconds),
			ReportIntervalSeconds:    int(settings.Metrics.ReportIntervalSeconds),
			Mountpoints:              settings.Metrics.Mountpoints,
			IncludeVirtualInterfaces: settings.Metrics.IncludeVirtualInterfaces,
		},
		Agent: agent.RuntimeConfig{
			ConfigRefreshIntervalSeconds: int(settings.Agent.ConfigRefreshIntervalSeconds),
			MaxMemoryQueueSeconds:        int(settings.Agent.MaxMemoryQueueSeconds),
		},
		Limits: agent.LimitsConfig{MaxBatchSamples: int(settings.Limits.MaxBatchSamples)},
	}
	if err := configuration.ValidateSettings(); err != nil {
		return invalidField("agent_settings", err.Error())
	}
	return nil
}

func decodeSettings(raw json.RawMessage) (AgentSettings, error) {
	object, err := decodeRawObject(raw, "agent_settings")
	if err != nil || !hasExactKeys(object, "metrics", "agent", "limits") {
		return AgentSettings{}, invalidField("agent_settings", "must contain exactly metrics, agent, and limits")
	}
	metrics, err := decodeRawObject(object["metrics"], "agent_settings.metrics")
	if err != nil || !hasExactKeys(metrics, "collect_interval_seconds", "report_interval_seconds", "mountpoints", "include_virtual_interfaces") {
		return AgentSettings{}, invalidField("agent_settings.metrics", "must be a complete metrics object")
	}
	runtime, err := decodeRawObject(object["agent"], "agent_settings.agent")
	if err != nil || !hasExactKeys(runtime, "config_refresh_interval_seconds", "max_memory_queue_seconds") {
		return AgentSettings{}, invalidField("agent_settings.agent", "must be a complete Agent runtime object")
	}
	limits, err := decodeRawObject(object["limits"], "agent_settings.limits")
	if err != nil || !hasExactKeys(limits, "max_batch_samples") {
		return AgentSettings{}, invalidField("agent_settings.limits", "must be a complete limits object")
	}
	var settings AgentSettings
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return AgentSettings{}, invalidField("agent_settings", "must be a complete settings object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AgentSettings{}, invalidField("agent_settings", "must contain one JSON object")
	}
	if err := validateSettings(settings); err != nil {
		return AgentSettings{}, err
	}
	return settings, nil
}

func decodeRawObject(raw json.RawMessage, field string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, invalidField(field, "must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, invalidField(field, "must be one object")
	}
	return object, nil
}

func hasExactKeys(object map[string]json.RawMessage, expected ...string) bool {
	if len(object) != len(expected) {
		return false
	}
	for _, name := range expected {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, invalidField("request", "must be one JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, invalidField("request", "must be one JSON object")
	}
	return object, nil
}

func rejectUnknown(object map[string]json.RawMessage, allowed map[string]bool) error {
	for name := range object {
		if !allowed[name] {
			return invalidField(name, "unknown field")
		}
	}
	return nil
}

func decodeRequired(raw json.RawMessage, destination any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func decodeOptionalNullableString(object map[string]json.RawMessage, name string) (*string, error) {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := decodeRequired(raw, &value); err != nil {
		return nil, invalidField(name, "must be a string or null")
	}
	return &value, nil
}

func decodeNullableUpdate(object map[string]json.RawMessage, name string) (NullableString, error) {
	raw, ok := object[name]
	if !ok {
		return NullableString{}, nil
	}
	result := NullableString{Set: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return result, nil
	}
	var value string
	if err := decodeRequired(raw, &value); err != nil {
		return NullableString{}, invalidField(name, "must be a string or null")
	}
	result.Value = &value
	return result, nil
}

func validText(value string, maxRunes int) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func invalidField(field, message string) error {
	return &FieldError{Code: "invalid_request", Field: field, Message: message}
}
