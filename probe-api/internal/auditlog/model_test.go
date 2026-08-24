package auditlog

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestCursorRoundTripAndStrictShape(t *testing.T) {
	want := Cursor{OccurredAt: time.Date(2026, 8, 22, 4, 5, 6, 7, time.UTC), AuditID: 42}
	encoded, err := EncodeCursor(want)
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	got, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if !got.OccurredAt.Equal(want.OccurredAt) || got.AuditID != want.AuditID {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	unknown := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"occurred_at":"2026-08-22T04:05:06Z","audit_id":42,"extra":true}`))
	if _, err := DecodeCursor(unknown); err == nil {
		t.Fatal("cursor with unknown field was accepted")
	}
}
