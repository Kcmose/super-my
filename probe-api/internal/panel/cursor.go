package panel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type Cursor struct {
	CreatedAt time.Time
	NodeID    string
}

type cursorWire struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
	NodeID    string    `json:"node_id"`
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.CreatedAt.IsZero() || !ValidUUID(cursor.NodeID) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(cursorWire{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), NodeID: cursor.NodeID})
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
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var wire cursorWire
	if err := decoder.Decode(&wire); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	if err := ensureCursorEOF(decoder); err != nil || wire.Version != 1 || wire.CreatedAt.IsZero() || !ValidUUID(wire.NodeID) {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{CreatedAt: wire.CreatedAt.UTC(), NodeID: wire.NodeID}, nil
}

func ensureCursorEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return ErrInvalidCursor
}
