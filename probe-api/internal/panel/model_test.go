package panel

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestStatusAtPrecedenceAndBoundary(t *testing.T) {
	asOf := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	enrolled := asOf.Add(-time.Hour)
	recent := asOf.Add(-44 * time.Second)
	boundary := asOf.Add(-45 * time.Second)
	tests := []struct {
		name       string
		enabled    bool
		enrolled   *time.Time
		received   *time.Time
		clock      string
		wantStatus Status
	}{
		{name: "disabled wins", enabled: false, enrolled: nil, received: &recent, clock: "skewed", wantStatus: StatusDisabled},
		{name: "unregistered wins", enabled: true, enrolled: nil, received: &recent, clock: "skewed", wantStatus: StatusUnregistered},
		{name: "missing receive is offline", enabled: true, enrolled: &enrolled, clock: "skewed", wantStatus: StatusOffline},
		{name: "boundary is offline", enabled: true, enrolled: &enrolled, received: &boundary, clock: "skewed", wantStatus: StatusOffline},
		{name: "recent skew", enabled: true, enrolled: &enrolled, received: &recent, clock: "skewed", wantStatus: StatusSkewed},
		{name: "recent healthy", enabled: true, enrolled: &enrolled, received: &recent, clock: "ok", wantStatus: StatusOnline},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StatusAt(test.enabled, test.enrolled, test.received, test.clock, asOf, 45*time.Second); got != test.wantStatus {
				t.Fatalf("StatusAt() = %q, want %q", got, test.wantStatus)
			}
		})
	}
}

func TestCursorRoundTripAndStrictRejection(t *testing.T) {
	want := Cursor{CreatedAt: time.Date(2026, 8, 21, 12, 0, 0, 123000000, time.UTC), NodeID: "01234567-89ab-cdef-8123-456789abcdef"}
	encoded, err := EncodeCursor(want)
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}
	got, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}
	if got.NodeID != want.NodeID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("DecodeCursor() = %#v, want %#v", got, want)
	}

	unknown := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"created_at":"2026-08-21T12:00:00Z","node_id":"01234567-89ab-cdef-8123-456789abcdef","extra":true}`))
	for _, value := range []string{"", encoded + "=", unknown, "not-base64", encoded + encoded + encoded + encoded} {
		if _, err := DecodeCursor(value); err == nil {
			t.Fatalf("DecodeCursor(%q) unexpectedly succeeded", value)
		}
	}
}

func TestVisibleWindowNeverExpandsFiveMinutes(t *testing.T) {
	asOf := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	oldFrom := asOf.Add(-time.Hour)
	futureTo := asOf.Add(time.Hour)
	from, to := visibleWindow(asOf, TimeRange{From: &oldFrom, To: &futureTo})
	if !from.Equal(asOf.Add(-5*time.Minute)) || !to.Equal(asOf) {
		t.Fatalf("visibleWindow() = %v..%v", from, to)
	}
	oldTo := asOf.Add(-10 * time.Minute)
	from, to = visibleWindow(asOf, TimeRange{To: &oldTo})
	if !from.Equal(asOf.Add(-5*time.Minute)) || !to.Equal(from) {
		t.Fatalf("old visibleWindow() = %v..%v", from, to)
	}
	futureFrom := asOf.Add(10 * time.Minute)
	from, to = visibleWindow(asOf, TimeRange{From: &futureFrom})
	if !from.Equal(asOf) || !to.Equal(asOf) {
		t.Fatalf("future visibleWindow() = %v..%v", from, to)
	}
}
