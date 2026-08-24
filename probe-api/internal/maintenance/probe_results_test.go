package maintenance

import (
	"errors"
	"testing"
	"time"
)

func TestAlignedFloorUsesUTCUnixBuckets(t *testing.T) {
	value := time.Date(2026, 8, 22, 12, 7, 59, 999, time.FixedZone("offset", 8*60*60))
	got := alignedFloor(value, 5*time.Minute)
	want := time.Date(2026, 8, 22, 4, 5, 0, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("alignedFloor() = %v, want %v", got, want)
	}
}

func TestCleanupWatermarksFailClosed(t *testing.T) {
	asOf := time.Date(2026, 8, 22, 12, 7, 0, 0, time.UTC)
	five := time.Date(2026, 8, 22, 12, 5, 0, 0, time.UTC)
	hour := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if err := validateCleanupWatermarks(asOf, &five, &hour); err != nil {
		t.Fatalf("valid watermarks error = %v", err)
	}
	if err := validateCleanupWatermarks(asOf, nil, &hour); !errors.Is(err, ErrProbeWatermarkInvariant) {
		t.Fatalf("missing source watermark error = %v", err)
	}
	future := time.Date(2026, 8, 22, 12, 10, 0, 0, time.UTC)
	if err := validateCleanupWatermarks(asOf, &future, nil); !errors.Is(err, ErrProbeWatermarkInvariant) {
		t.Fatalf("future watermark error = %v", err)
	}
	misaligned := five.Add(time.Second)
	if err := validateCleanupWatermarks(asOf, &misaligned, nil); !errors.Is(err, ErrProbeWatermarkInvariant) {
		t.Fatalf("misaligned watermark error = %v", err)
	}
}
