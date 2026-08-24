package auditlog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

var (
	ErrForbidden      = errors.New("administrator role is required")
	ErrInvalidCursor  = errors.New("audit log cursor is invalid")
	ErrInvalidRequest = errors.New("audit log request is invalid")
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

type Metadata struct {
	SourceIP  string
	RequestID string
}

type Entry struct {
	AuditID       int64           `json:"audit_id"`
	ActorUserID   *string         `json:"actor_user_id"`
	ActorUsername *string         `json:"actor_username"`
	Action        string          `json:"action"`
	TargetType    *string         `json:"target_type"`
	TargetID      *string         `json:"target_id"`
	SourceIP      *string         `json:"source_ip"`
	RequestID     string          `json:"request_id"`
	BeforeSummary json.RawMessage `json:"before_summary"`
	AfterSummary  json.RawMessage `json:"after_summary"`
	Result        string          `json:"result"`
	ErrorCode     *string         `json:"error_code"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

type Cursor struct {
	OccurredAt time.Time
	AuditID    int64
}

type ListRequest struct {
	Limit  int
	Cursor *Cursor
	Action *string
	From   *time.Time
	To     *time.Time
}

type ListResponse struct {
	Logs       []Entry `json:"logs"`
	NextCursor *string `json:"next_cursor"`
}

type cursorWire struct {
	Version    int       `json:"v"`
	OccurredAt time.Time `json:"occurred_at"`
	AuditID    int64     `json:"audit_id"`
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.OccurredAt.IsZero() || cursor.AuditID < 1 {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(cursorWire{Version: 1, OccurredAt: cursor.OccurredAt.UTC(), AuditID: cursor.AuditID})
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
	if wire.Version != 1 || wire.OccurredAt.IsZero() || wire.AuditID < 1 {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{OccurredAt: wire.OccurredAt.UTC(), AuditID: wire.AuditID}, nil
}

func validateText(value string, maxRunes int) bool {
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
