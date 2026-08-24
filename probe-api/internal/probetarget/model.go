package probetarget

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultListLimit    = 50
	MaxListLimit        = 200
	MaxTargetsPerNode   = 32
	MaxRetentionSeconds = 7776000
)

type Type string

const (
	TypeTCP   Type = "tcp"
	TypeHTTP  Type = "http"
	TypeHTTPS Type = "https"
)

var (
	ErrForbidden      = errors.New("administrator role is required")
	ErrNotFound       = errors.New("probe target was not found")
	ErrConflict       = errors.New("probe target state conflicts with the operation")
	ErrLimitExceeded  = errors.New("probe target limit exceeded")
	ErrInvalidCursor  = errors.New("probe target cursor is invalid")
	ErrInvalidRequest = errors.New("probe target request is invalid")

	canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

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

type Target struct {
	TargetID         string    `json:"target_id"`
	NodeID           string    `json:"node_id"`
	Name             string    `json:"name"`
	Type             Type      `json:"type"`
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

type ListRequest struct {
	NodeID *string
	Limit  int
	Cursor *Cursor
}

type ListResponse struct {
	Targets    []Target `json:"targets"`
	NextCursor *string  `json:"next_cursor"`
}

type CreateRequest struct {
	NodeID           string
	Name             string
	Type             Type
	Host             string
	Port             *int32
	Path             *string
	IntervalSeconds  int32
	TimeoutSeconds   int32
	RetentionSeconds int32
	Enabled          bool
}

type NullableInt32 struct {
	Set   bool
	Value *int32
}

type NullableString struct {
	Set   bool
	Value *string
}

type UpdateRequest struct {
	Name             *string
	Type             *Type
	Host             *string
	Port             NullableInt32
	Path             NullableString
	IntervalSeconds  *int32
	TimeoutSeconds   *int32
	RetentionSeconds *int32
	Enabled          *bool
}

type Metadata struct {
	SourceIP  string
	RequestID string
}

type targetValues struct {
	Name             string
	Type             Type
	Host             string
	Port             *int32
	Path             *string
	IntervalSeconds  int32
	TimeoutSeconds   int32
	RetentionSeconds int32
	Enabled          bool
}

func DecodeCreate(body []byte) (CreateRequest, error) {
	object, err := decodeStrictObject(body)
	if err != nil {
		return CreateRequest{}, err
	}
	required := []string{"node_id", "name", "type", "host", "port", "path", "interval_seconds", "timeout_seconds", "retention_seconds", "enabled"}
	if err := requireExactFields(object, required); err != nil {
		return CreateRequest{}, err
	}
	var request CreateRequest
	if err := decodeRequired(object["node_id"], &request.NodeID); err != nil {
		return CreateRequest{}, fieldDecodeError("node_id")
	}
	if err := decodeRequired(object["name"], &request.Name); err != nil {
		return CreateRequest{}, fieldDecodeError("name")
	}
	if err := decodeRequired(object["type"], &request.Type); err != nil {
		return CreateRequest{}, fieldDecodeError("type")
	}
	if err := decodeRequired(object["host"], &request.Host); err != nil {
		return CreateRequest{}, fieldDecodeError("host")
	}
	if request.Port, err = decodeNullableInt32(object["port"]); err != nil {
		return CreateRequest{}, fieldDecodeError("port")
	}
	if request.Path, err = decodeNullableString(object["path"]); err != nil {
		return CreateRequest{}, fieldDecodeError("path")
	}
	if err := decodeRequired(object["interval_seconds"], &request.IntervalSeconds); err != nil {
		return CreateRequest{}, fieldDecodeError("interval_seconds")
	}
	if err := decodeRequired(object["timeout_seconds"], &request.TimeoutSeconds); err != nil {
		return CreateRequest{}, fieldDecodeError("timeout_seconds")
	}
	if err := decodeRequired(object["retention_seconds"], &request.RetentionSeconds); err != nil {
		return CreateRequest{}, fieldDecodeError("retention_seconds")
	}
	if err := decodeRequired(object["enabled"], &request.Enabled); err != nil {
		return CreateRequest{}, fieldDecodeError("enabled")
	}
	if _, err := request.normalized(); err != nil {
		return CreateRequest{}, err
	}
	return request, nil
}

func DecodeUpdate(body []byte) (UpdateRequest, error) {
	object, err := decodeStrictObject(body)
	if err != nil {
		return UpdateRequest{}, err
	}
	allowed := map[string]struct{}{
		"name": {}, "type": {}, "host": {}, "port": {}, "path": {},
		"interval_seconds": {}, "timeout_seconds": {}, "retention_seconds": {}, "enabled": {},
	}
	if len(object) == 0 {
		return UpdateRequest{}, &FieldError{Code: "invalid_request", Field: "request", Message: "at least one field is required"}
	}
	for name := range object {
		if _, ok := allowed[name]; !ok {
			return UpdateRequest{}, &FieldError{Code: "invalid_request", Field: name, Message: "unknown field"}
		}
	}
	var request UpdateRequest
	for name, raw := range object {
		switch name {
		case "name":
			var value string
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.Name = &value
		case "type":
			var value Type
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.Type = &value
		case "host":
			var value string
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.Host = &value
		case "port":
			value, err := decodeNullableInt32(raw)
			if err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.Port = NullableInt32{Set: true, Value: value}
		case "path":
			value, err := decodeNullableString(raw)
			if err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.Path = NullableString{Set: true, Value: value}
		case "interval_seconds":
			var value int32
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.IntervalSeconds = &value
		case "timeout_seconds":
			var value int32
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.TimeoutSeconds = &value
		case "retention_seconds":
			var value int32
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.RetentionSeconds = &value
		case "enabled":
			var value bool
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, fieldDecodeError(name)
			}
			request.Enabled = &value
		}
	}
	return request, nil
}

func (request CreateRequest) normalized() (targetValues, error) {
	if !ValidUUID(request.NodeID) {
		return targetValues{}, invalidField("node_id", "must be a canonical lowercase UUID")
	}
	return normalizeValues(targetValues{
		Name: request.Name, Type: request.Type, Host: request.Host,
		Port: cloneInt32(request.Port), Path: cloneString(request.Path),
		IntervalSeconds: request.IntervalSeconds, TimeoutSeconds: request.TimeoutSeconds,
		RetentionSeconds: request.RetentionSeconds, Enabled: request.Enabled,
	})
}

func mergeUpdate(current Target, request UpdateRequest) (targetValues, error) {
	values := targetValues{
		Name: current.Name, Type: current.Type, Host: current.Host,
		Port: cloneInt32(current.Port), Path: cloneString(current.Path),
		IntervalSeconds: current.IntervalSeconds, TimeoutSeconds: current.TimeoutSeconds,
		RetentionSeconds: current.RetentionSeconds, Enabled: current.Enabled,
	}
	if request.Name != nil {
		values.Name = *request.Name
	}
	if request.Type != nil {
		values.Type = *request.Type
	}
	if request.Host != nil {
		values.Host = *request.Host
	}
	if request.Port.Set {
		values.Port = cloneInt32(request.Port.Value)
	}
	if request.Path.Set {
		values.Path = cloneString(request.Path.Value)
	}
	if request.IntervalSeconds != nil {
		values.IntervalSeconds = *request.IntervalSeconds
	}
	if request.TimeoutSeconds != nil {
		values.TimeoutSeconds = *request.TimeoutSeconds
	}
	if request.RetentionSeconds != nil {
		values.RetentionSeconds = *request.RetentionSeconds
	}
	if request.Enabled != nil {
		values.Enabled = *request.Enabled
	}
	return normalizeValues(values)
}

func normalizeValues(values targetValues) (targetValues, error) {
	if !validDisplayName(values.Name) {
		return targetValues{}, invalidField("name", "must contain 1 to 128 printable characters without surrounding whitespace")
	}
	if values.Type != TypeTCP && values.Type != TypeHTTP && values.Type != TypeHTTPS {
		return targetValues{}, invalidField("type", "must be tcp, http, or https; ICMP is not enabled")
	}
	if !validHost(values.Host) {
		return targetValues{}, invalidField("host", "must be a DNS name or IP address without a scheme, path, or credentials")
	}
	if values.IntervalSeconds < 10 || values.IntervalSeconds > 86400 {
		return targetValues{}, invalidField("interval_seconds", "must be between 10 and 86400")
	}
	if values.TimeoutSeconds < 1 || values.TimeoutSeconds > 60 || values.TimeoutSeconds > values.IntervalSeconds {
		return targetValues{}, invalidField("timeout_seconds", "must be between 1 and 60 and no greater than interval_seconds")
	}
	if values.RetentionSeconds < 1 {
		return targetValues{}, invalidField("retention_seconds", "must be positive")
	}
	if values.RetentionSeconds > MaxRetentionSeconds {
		return targetValues{}, &FieldError{Code: "retention_exceeds_limit", Field: "retention_seconds", Message: "must not exceed 7776000"}
	}
	switch values.Type {
	case TypeTCP:
		if values.Port == nil || *values.Port < 1 || *values.Port > 65535 {
			return targetValues{}, invalidField("port", "TCP targets require a port between 1 and 65535")
		}
		if values.Path != nil {
			return targetValues{}, invalidField("path", "TCP targets require a null path")
		}
	case TypeHTTP, TypeHTTPS:
		if values.Port != nil && (*values.Port < 1 || *values.Port > 65535) {
			return targetValues{}, invalidField("port", "must be null or between 1 and 65535")
		}
		if values.Path == nil {
			defaultPath := "/"
			values.Path = &defaultPath
		}
		if !validHTTPPath(*values.Path) {
			return targetValues{}, invalidField("path", "must start with /, exclude query or fragment characters, and contain at most 2048 printable characters")
		}
	}
	return values, nil
}

func ValidUUID(value string) bool {
	return canonicalUUIDPattern.MatchString(value)
}

func validDisplayName(value string) bool {
	return validPrintable(value, 128) && strings.TrimSpace(value) == value
}

func validHost(value string) bool {
	if !validPrintable(value, 253) || strings.TrimSpace(value) != value {
		return false
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Zone() == ""
	}
	host := strings.TrimSuffix(value, ".")
	if host == "" || strings.ContainsAny(host, "/@?#\\:;'\"`$|&<>(){}[]%") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
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

func validHTTPPath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "?#") && validPrintable(value, 2048)
}

func validPrintable(value string, maximum int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func decodeStrictObject(body []byte) (map[string]json.RawMessage, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return nil, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrInvalidRequest
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalidRequest
		}
		name, ok := token.(string)
		if !ok {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := object[name]; duplicate {
			return nil, &FieldError{Code: "invalid_request", Field: name, Message: "duplicate field"}
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, ErrInvalidRequest
		}
		object[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, ErrInvalidRequest
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidRequest
	}
	return object, nil
}

func requireExactFields(object map[string]json.RawMessage, names []string) error {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
		if _, exists := object[name]; !exists {
			return &FieldError{Code: "invalid_request", Field: name, Message: "required field is missing"}
		}
	}
	for name := range object {
		if _, exists := allowed[name]; !exists {
			return &FieldError{Code: "invalid_request", Field: name, Message: "unknown field"}
		}
	}
	return nil
}

func decodeRequired(raw json.RawMessage, target any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null is not allowed")
	}
	return json.Unmarshal(raw, target)
}

func decodeNullableInt32(raw json.RawMessage) (*int32, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value int32
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func decodeNullableString(raw json.RawMessage) (*string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func fieldDecodeError(field string) error {
	return &FieldError{Code: "invalid_request", Field: field, Message: "field has an invalid JSON type"}
}

func invalidField(field, message string) error {
	return &FieldError{Code: "invalid_request", Field: field, Message: message}
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (target Target) auditJSON() ([]byte, error) {
	encoded, err := json.Marshal(target)
	if err != nil {
		return nil, fmt.Errorf("encode probe target audit summary: %w", err)
	}
	return encoded, nil
}
