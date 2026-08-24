package probetarget

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type Cursor struct {
	CreatedAt time.Time
	TargetID  string
}

type cursorWire struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
	TargetID  string    `json:"target_id"`
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.CreatedAt.IsZero() || !ValidUUID(cursor.TargetID) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(cursorWire{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), TargetID: cursor.TargetID})
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
	object, err := decodeStrictObject(body)
	if err != nil || requireExactFields(object, []string{"v", "created_at", "target_id"}) != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var wire cursorWire
	if decodeRequired(object["v"], &wire.Version) != nil ||
		decodeRequired(object["created_at"], &wire.CreatedAt) != nil ||
		decodeRequired(object["target_id"], &wire.TargetID) != nil {
		return Cursor{}, ErrInvalidCursor
	}
	if wire.Version != 1 || wire.CreatedAt.IsZero() || !ValidUUID(wire.TargetID) {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{CreatedAt: wire.CreatedAt.UTC(), TargetID: wire.TargetID}, nil
}
