package probequery

import (
	"testing"
	"time"
)

func TestChooseResolutionHonorsAgeSpanAndPointLimit(t *testing.T) {
	asOf := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	target := ProbeTarget{IntervalSeconds: 60, RetentionSeconds: 7776000}
	tests := []struct {
		name      string
		from      time.Time
		to        time.Time
		requested Resolution
		want      Resolution
		wantError error
	}{
		{name: "recent raw", from: asOf.Add(-time.Hour), to: asOf, requested: ResolutionAuto, want: ResolutionRaw},
		{name: "historical faster interval cannot be underestimated", from: asOf.Add(-10 * time.Hour), to: asOf, requested: ResolutionAuto, want: Resolution5m},
		{name: "raw point cap promotes", from: asOf.Add(-24 * time.Hour), to: asOf, requested: ResolutionAuto, want: Resolution5m},
		{name: "old short range uses five minutes", from: asOf.Add(-72 * time.Hour), to: asOf.Add(-71 * time.Hour), requested: ResolutionAuto, want: Resolution5m},
		{name: "older than seven days uses hourly", from: asOf.Add(-8 * 24 * time.Hour), to: asOf.Add(-7 * 24 * time.Hour), requested: ResolutionAuto, want: Resolution1h},
		{name: "explicit unavailable raw", from: asOf.Add(-48 * time.Hour), to: asOf.Add(-47 * time.Hour), requested: ResolutionRaw, wantError: ErrResolutionUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := chooseResolution(test.requested, test.from, test.to, asOf, target)
			if err != test.wantError || got != test.want {
				t.Fatalf("chooseResolution() = %q, %v; want %q, %v", got, err, test.want, test.wantError)
			}
		})
	}
}

func TestClippedWindowAndExactDerivedFacts(t *testing.T) {
	asOf := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	from, to := clippedWindow(asOf.Add(-48*time.Hour), asOf.Add(time.Hour), asOf, 24*time.Hour)
	if !from.Equal(asOf.Add(-24*time.Hour)) || !to.Equal(asOf) {
		t.Fatalf("clippedWindow() = %v..%v", from, to)
	}
	point := ProbeSeriesPoint{ResultCount: 3, SentCount: 7, ReceivedCount: 3, HTTPErrorCount: 2}
	minimum := int64(1)
	maximum := int64(7000000000000000000)
	point.LatencyMinUS = &minimum
	point.LatencyMaxUS = &maximum
	const exactSum = "18000000000000000000"
	if err := derivePoint(&point, exactSum, Resolution5m); err != nil {
		t.Fatalf("derivePoint() error = %v", err)
	}
	if point.LatencySumUS.String() != exactSum || point.AverageLatencyUS == nil ||
		point.LossRate <= 0 || point.LossRate >= 1 || point.FailureRate <= point.LossRate || point.FailureRate > 1 {
		t.Fatalf("derived point = %#v", point)
	}
}

func TestDerivePointClassifiesHTTPStatusAndRejectsInconsistentFacts(t *testing.T) {
	minimum := int64(10)
	maximum := int64(20)
	status199 := int32(199)
	status200 := int32(200)
	status399 := int32(399)
	status400 := int32(400)
	for _, test := range []struct {
		name       string
		status     *int32
		httpErrors int64
		wantError  bool
	}{
		{name: "no HTTP status", status: nil, httpErrors: 0},
		{name: "informational is failure", status: &status199, httpErrors: 1},
		{name: "success", status: &status200, httpErrors: 0},
		{name: "redirect is success", status: &status399, httpErrors: 0},
		{name: "client error is failure", status: &status400, httpErrors: 1},
		{name: "raw classification mismatch", status: &status400, httpErrors: 0, wantError: true},
		{name: "error count exceeds received", status: &status400, httpErrors: 2, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			point := ProbeSeriesPoint{
				ResultCount: 1, SentCount: 1, ReceivedCount: 1,
				HTTPStatusCode: test.status, HTTPErrorCount: test.httpErrors,
				LatencyMinUS: &minimum, LatencyMaxUS: &maximum,
			}
			err := derivePoint(&point, "15", ResolutionRaw)
			if (err != nil) != test.wantError {
				t.Fatalf("derivePoint() error = %v, want_error=%v", err, test.wantError)
			}
			if err == nil {
				wantFailure := float64(test.httpErrors)
				if point.LossRate != 0 || point.FailureRate != wantFailure {
					t.Fatalf("rates = loss %v failure %v, want 0 and %v", point.LossRate, point.FailureRate, wantFailure)
				}
			}
		})
	}
	aggregatedStatus := int32(500)
	aggregated := ProbeSeriesPoint{
		ResultCount: 1, SentCount: 1, ReceivedCount: 1, HTTPStatusCode: &aggregatedStatus,
		HTTPErrorCount: 1, LatencyMinUS: &minimum, LatencyMaxUS: &maximum,
	}
	if err := derivePoint(&aggregated, "10", Resolution5m); err != ErrInvariant {
		t.Fatalf("derivePoint() aggregate status error = %v, want ErrInvariant", err)
	}
}
