package usermanagement

import (
	"testing"
	"time"

	"probe-api/internal/auth"
)

func TestDecodeUserRequestsAreStrict(t *testing.T) {
	request, err := DecodeCreate([]byte(`{"username":"operator","password":"correct horse battery","role":"admin","enabled":true}`))
	if err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
	if request.Role != auth.RoleAdmin {
		t.Fatalf("unexpected role: %q", request.Role)
	}
	if _, err := DecodeCreate([]byte(`{"username":"operator","password":"correct horse battery","role":"admin","enabled":true,"unexpected":"value"}`)); err == nil {
		t.Fatal("unknown create field was accepted")
	}
	if _, err := DecodeCreate([]byte(`{"username":"operator","password":"correct horse battery","role":"viewer","enabled":true}`)); err == nil {
		t.Fatal("legacy viewer role was accepted as an account")
	}
	if _, err := DecodeCreate([]byte(`{"username":"operator","password":"correct horse battery","role":"guest","enabled":true}`)); err == nil {
		t.Fatal("anonymous guest label was accepted as an account role")
	}
	viewer := auth.RoleViewer
	if _, err := DecodeUpdate([]byte(`{"role":"viewer"}`)); err == nil || validRole(viewer) {
		t.Fatal("legacy viewer role was accepted by user update validation")
	}
	if _, err := DecodeUpdate([]byte(`{}`)); err == nil {
		t.Fatal("empty update was accepted")
	}
}

func TestUserCursorRoundTrip(t *testing.T) {
	want := Cursor{CreatedAt: time.Date(2026, 8, 22, 1, 2, 3, 4, time.UTC), UserID: "11111111-1111-4111-8111-111111111111"}
	encoded, err := EncodeCursor(want)
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	got, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if got.UserID != want.UserID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}
