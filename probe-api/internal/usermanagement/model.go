package usermanagement

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"probe-api/internal/auditlog"
	"probe-api/internal/auth"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

var (
	ErrForbidden       = errors.New("administrator role is required")
	ErrNotFound        = errors.New("user was not found")
	ErrConflict        = errors.New("user state conflicts with the operation")
	ErrLastUsableAdmin = errors.New("the last usable administrator must be preserved")
	ErrInvalidCursor   = errors.New("user cursor is invalid")
	ErrInvalidRequest  = errors.New("user request is invalid")

	canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type Metadata = auditlog.Metadata
type User = auth.User

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

type Cursor struct {
	CreatedAt time.Time
	UserID    string
}

type ListRequest struct {
	Limit  int
	Cursor *Cursor
}

type ListResponse struct {
	Users      []User  `json:"users"`
	NextCursor *string `json:"next_cursor"`
}

type CreateRequest struct {
	Username string
	Password string
	Role     auth.Role
	Enabled  bool
}

type UpdateRequest struct {
	Username *string
	Password *string
	Role     *auth.Role
	Enabled  *bool
}

func (request *CreateRequest) ClearPassword() {
	request.Password = ""
}

func (request *UpdateRequest) ClearPassword() {
	request.Password = nil
}

type cursorWire struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
	UserID    string    `json:"user_id"`
}

func ValidUUID(value string) bool {
	return canonicalUUIDPattern.MatchString(value)
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.CreatedAt.IsZero() || !ValidUUID(cursor.UserID) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(cursorWire{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), UserID: cursor.UserID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func DecodeCursor(value string) (Cursor, error) {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return Cursor{}, ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(body) == 0 || len(body) > 384 {
		return Cursor{}, ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var wire cursorWire
	if err := decoder.Decode(&wire); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Cursor{}, ErrInvalidCursor
	}
	if wire.Version != 1 || wire.CreatedAt.IsZero() || !ValidUUID(wire.UserID) {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{CreatedAt: wire.CreatedAt.UTC(), UserID: wire.UserID}, nil
}

func DecodeCreate(body []byte) (CreateRequest, error) {
	object, err := decodeObject(body)
	if err != nil {
		return CreateRequest{}, err
	}
	required := []string{"username", "password", "role", "enabled"}
	if len(object) != len(required) {
		return CreateRequest{}, invalidField("request", "must contain exactly username, password, role, and enabled")
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return CreateRequest{}, invalidField(name, "is required")
		}
	}
	var request CreateRequest
	if err := decodeRequired(object["username"], &request.Username); err != nil {
		return CreateRequest{}, invalidField("username", "must be a string")
	}
	if err := decodeRequired(object["password"], &request.Password); err != nil {
		return CreateRequest{}, invalidField("password", "must be a string")
	}
	if err := decodeRequired(object["role"], &request.Role); err != nil {
		return CreateRequest{}, invalidField("role", "must be admin")
	}
	if err := decodeRequired(object["enabled"], &request.Enabled); err != nil {
		return CreateRequest{}, invalidField("enabled", "must be a boolean")
	}
	if err := validateValues(request.Username, request.Password, request.Role); err != nil {
		return CreateRequest{}, err
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
	allowed := map[string]bool{"username": true, "password": true, "role": true, "enabled": true}
	var request UpdateRequest
	for name, raw := range object {
		if !allowed[name] {
			return UpdateRequest{}, invalidField(name, "unknown field")
		}
		switch name {
		case "username":
			var value string
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, invalidField(name, "must be a string")
			}
			request.Username = &value
		case "password":
			var value string
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, invalidField(name, "must be a string")
			}
			request.Password = &value
		case "role":
			var value auth.Role
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, invalidField(name, "must be admin")
			}
			request.Role = &value
		case "enabled":
			var value bool
			if err := decodeRequired(raw, &value); err != nil {
				return UpdateRequest{}, invalidField(name, "must be a boolean")
			}
			request.Enabled = &value
		}
	}
	if request.Username != nil && !validUsername(*request.Username) {
		return UpdateRequest{}, invalidField("username", "must contain 1 to 128 valid characters")
	}
	if request.Password != nil && !validPassword(*request.Password) {
		return UpdateRequest{}, invalidField("password", "must contain 12 to 1024 valid UTF-8 bytes")
	}
	if request.Role != nil && !validRole(*request.Role) {
		return UpdateRequest{}, invalidField("role", "must be admin")
	}
	return request, nil
}

func validateValues(username, password string, role auth.Role) error {
	if !validUsername(username) {
		return invalidField("username", "must contain 1 to 128 valid characters")
	}
	if !validPassword(password) {
		return invalidField("password", "must contain 12 to 1024 valid UTF-8 bytes")
	}
	if !validRole(role) {
		return invalidField("role", "must be admin")
	}
	return nil
}

func validUsername(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validPassword(value string) bool {
	return len(value) >= 12 && len(value) <= 1024 && utf8.ValidString(value)
}

func validRole(role auth.Role) bool {
	return role == auth.RoleAdmin
}

func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
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

func decodeRequired(raw json.RawMessage, destination any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalidRequest
	}
	return json.Unmarshal(raw, destination)
}

func invalidField(field, message string) error {
	return &FieldError{Code: "invalid_request", Field: field, Message: message}
}
