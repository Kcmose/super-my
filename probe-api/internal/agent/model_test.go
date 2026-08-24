package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDecodeStrictRequiresExactKeysAndPresence(t *testing.T) {
	validEnroll := `{"enrollment_token":"12345678901234567890123456789012","hostname":"node","agent_version":"1.0.0","os":"linux","arch":"amd64"}`
	var enrollment EnrollRequest
	if err := DecodeStrict([]byte(validEnroll), &enrollment); err != nil {
		t.Fatalf("DecodeStrict(valid enrollment) error = %v", err)
	}

	for name, body := range map[string]string{
		"case mismatch":  `{"enrollment_token":"12345678901234567890123456789012","Hostname":"node","agent_version":"1.0.0","os":"linux","arch":"amd64"}`,
		"duplicate key":  `{"enrollment_token":"12345678901234567890123456789012","hostname":"one","hostname":"two","agent_version":"1.0.0","os":"linux","arch":"amd64"}`,
		"missing key":    `{"enrollment_token":"12345678901234567890123456789012","hostname":"node","os":"linux","arch":"amd64"}`,
		"trailing value": validEnroll + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			var request EnrollRequest
			if err := DecodeStrict([]byte(body), &request); err == nil {
				t.Fatal("DecodeStrict() accepted an invalid request")
			}
		})
	}
}

func TestDecodeStrictRejectsMissingNestedMetricField(t *testing.T) {
	body := `{
		"batch_id":"0191f724-4cf8-7d71-917a-6468f58cb17d",
		"sequence":1,
		"agent_time":"2026-08-20T09:31:10Z",
		"agent_version":"1.0.0",
		"config_version":1,
		"metrics":[{
			"sampled_at":"2026-08-20T09:31:10Z",
			"load_1":0,"load_5":0,"load_15":0,"uptime_seconds":0,
			"memory_total_bytes":0,"memory_used_bytes":0,"memory_available_bytes":0,
			"swap_total_bytes":0,"swap_used_bytes":0,
			"network_rx_bps":0,"network_tx_bps":0,"network_rx_bytes":0,"network_tx_bytes":0
		}],
		"disks":[],"probe_results":[]
	}`
	var report ReportRequest
	err := DecodeStrict([]byte(body), &report)
	if err == nil || !strings.Contains(err.Error(), "cpu_percent") {
		t.Fatalf("DecodeStrict() error = %v, want missing cpu_percent", err)
	}
}

func TestReportValidationAndClockBoundaries(t *testing.T) {
	agentTime := time.Date(2026, 8, 20, 9, 31, 10, 0, time.UTC)
	report := validMetricReport(agentTime)
	report.Metrics[0].SampledAt = agentTime.Add(-SamplePastWindow)
	if err := report.Validate(); err != nil {
		t.Fatalf("Validate() lower inclusive boundary error = %v", err)
	}
	report.Metrics[0].SampledAt = agentTime.Add(SampleFutureWindow)
	if err := report.Validate(); err != nil {
		t.Fatalf("Validate() upper inclusive boundary error = %v", err)
	}
	report.Metrics[0].SampledAt = agentTime.Add(SampleFutureWindow + time.Nanosecond)
	var fieldError *FieldError
	if err := report.Validate(); !errors.As(err, &fieldError) || fieldError.Code != "sample_time_out_of_window" {
		t.Fatalf("Validate() error = %v, want sample_time_out_of_window", err)
	}

	receivedAt := agentTime
	for _, offset := range []time.Duration{-ClockSkewThreshold, ClockSkewThreshold} {
		status, _ := ClockStatus(receivedAt.Add(offset), receivedAt)
		if status != "ok" {
			t.Fatalf("ClockStatus(%s) = %q, want ok", offset, status)
		}
	}
	status, _ := ClockStatus(receivedAt.Add(ClockSkewThreshold+time.Nanosecond), receivedAt)
	if status != "skewed" {
		t.Fatalf("ClockStatus(over threshold) = %q, want skewed", status)
	}
	sampledAt := receivedAt.Add(-10 * time.Second)
	effectiveAt := EffectiveTime(sampledAt.Add(10*time.Hour), receivedAt.Add(10*time.Hour), receivedAt, "skewed")
	if !effectiveAt.Equal(sampledAt) {
		t.Fatalf("EffectiveTime() = %s, want %s", effectiveAt, sampledAt)
	}
}

func TestCanonicalChecksumIgnoresJSONWhitespaceAndKeyOrder(t *testing.T) {
	report := validMetricReport(time.Date(2026, 8, 20, 9, 31, 10, 0, time.UTC))
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ReportRequest
	if err := DecodeStrict(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	first, err := report.CanonicalChecksum()
	if err != nil {
		t.Fatal(err)
	}
	second, err := decoded.CanonicalChecksum()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("checksums differ: %s != %s", first, second)
	}
}

func TestRingSlotUsesFloorForNegativeUnixTime(t *testing.T) {
	for _, testCase := range []struct {
		seconds int64
		want    int16
	}{
		{seconds: -1, want: 59},
		{seconds: -5, want: 59},
		{seconds: -6, want: 58},
		{seconds: 0, want: 0},
		{seconds: 4, want: 0},
		{seconds: 5, want: 1},
		{seconds: 299, want: 59},
		{seconds: 300, want: 0},
	} {
		if got := ringSlot(time.Unix(testCase.seconds, 0)); got != testCase.want {
			t.Errorf("ringSlot(Unix(%d)) = %d, want %d", testCase.seconds, got, testCase.want)
		}
	}
}

func TestAgentConfigRejectsDeferredICMPTarget(t *testing.T) {
	configuration := Config{
		ConfigVersion: 1,
		Metrics:       MetricsConfig{CollectIntervalSeconds: 5, ReportIntervalSeconds: 10, Mountpoints: []string{"/"}},
		Agent:         RuntimeConfig{ConfigRefreshIntervalSeconds: 60, MaxMemoryQueueSeconds: 300},
		Limits:        LimitsConfig{MaxBatchSamples: 120},
		ProbeTargets: []ProbeTarget{{
			ID: "22222222-2222-4222-8222-222222222222", Name: "deferred", Type: "icmp",
			Host: "example.com", IntervalSeconds: 30, TimeoutSeconds: 3,
			RetentionSeconds: 86400, Enabled: true, ConfigVersion: 1,
		}},
	}
	if err := configuration.Validate(); err == nil {
		t.Fatal("Config.Validate() accepted a deferred ICMP target")
	}
}

func TestAgentConfigRejectsQueueShorterThanReportInterval(t *testing.T) {
	configuration := Config{
		ConfigVersion: 1,
		Metrics:       MetricsConfig{CollectIntervalSeconds: 5, ReportIntervalSeconds: 10, Mountpoints: []string{"/"}},
		Agent:         RuntimeConfig{ConfigRefreshIntervalSeconds: 60, MaxMemoryQueueSeconds: 9},
		Limits:        LimitsConfig{MaxBatchSamples: 120},
		ProbeTargets:  []ProbeTarget{},
	}
	if err := configuration.ValidateSettings(); err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("Config.ValidateSettings() accepted queue shorter than report interval: %v", err)
	}
}

func TestAgentConfigRejectsQueryOrFragmentInHTTPPath(t *testing.T) {
	for _, targetPath := range []string{"/health?token=secret", "/health#fragment"} {
		configuration := Config{
			ConfigVersion: 1,
			Metrics:       MetricsConfig{CollectIntervalSeconds: 5, ReportIntervalSeconds: 10, Mountpoints: []string{"/"}},
			Agent:         RuntimeConfig{ConfigRefreshIntervalSeconds: 60, MaxMemoryQueueSeconds: 300},
			Limits:        LimitsConfig{MaxBatchSamples: 120},
			ProbeTargets: []ProbeTarget{{
				ID: "22222222-2222-4222-8222-222222222222", Name: "HTTP", Type: "https",
				Host: "example.com", Path: &targetPath, IntervalSeconds: 30, TimeoutSeconds: 3,
				RetentionSeconds: 86400, Enabled: true, ConfigVersion: 1,
			}},
		}
		if err := configuration.Validate(); err == nil {
			t.Fatalf("Config.Validate() accepted path %q", targetPath)
		}
	}
}

func TestConfiguredProbeResultClosesSingleAttemptStatusAndErrorSemantics(t *testing.T) {
	statusOK := 200
	statusError := 500
	errorCode := "connection_refused"
	tests := []struct {
		name       string
		targetType string
		result     ProbeResult
		wantCode   string
	}{
		{name: "TCP success", targetType: "tcp", result: configuredProbeResult(1, 1, nil, nil)},
		{name: "TCP network failure", targetType: "tcp", result: configuredProbeResult(1, 0, nil, &errorCode)},
		{name: "HTTP success", targetType: "https", result: configuredProbeResult(1, 1, &statusOK, nil)},
		{name: "HTTP application failure", targetType: "http", result: configuredProbeResult(1, 1, &statusError, nil)},
		{name: "multiple attempts", targetType: "http", result: configuredProbeResult(2, 1, &statusOK, nil), wantCode: "invalid_probe_count"},
		{name: "HTTP response without status", targetType: "http", result: configuredProbeResult(1, 1, nil, nil), wantCode: "invalid_http_status"},
		{name: "HTTP network failure with status", targetType: "http", result: configuredProbeResult(1, 0, &statusError, &errorCode), wantCode: "invalid_http_status"},
		{name: "TCP response with status", targetType: "tcp", result: configuredProbeResult(1, 1, &statusOK, nil), wantCode: "invalid_http_status"},
		{name: "failure without error code", targetType: "tcp", result: configuredProbeResult(1, 0, nil, nil), wantCode: "invalid_probe_error"},
		{name: "response with transport error", targetType: "https", result: configuredProbeResult(1, 1, &statusError, &errorCode), wantCode: "invalid_probe_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateConfiguredProbeResult(test.result, test.targetType, 0)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("validateConfiguredProbeResult() error = %v", err)
				}
				return
			}
			var fieldError *FieldError
			if !errors.As(err, &fieldError) || fieldError.Code != test.wantCode {
				t.Fatalf("validateConfiguredProbeResult() error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func configuredProbeResult(sent, received int64, status *int, errorCode *string) ProbeResult {
	return ProbeResult{
		SentCount: sent, ReceivedCount: received,
		HTTPStatusCode: NullableInt{Set: true, Value: status},
		ErrorCode:      NullableString{Set: true, Value: errorCode},
	}
}

func validMetricReport(agentTime time.Time) ReportRequest {
	return ReportRequest{
		BatchID:       "0191f724-4cf8-7d71-917a-6468f58cb17d",
		Sequence:      1,
		AgentTime:     agentTime,
		AgentVersion:  "1.0.0",
		ConfigVersion: 1,
		Metrics: []MetricSample{{
			SampledAt: agentTime, CPUPercent: 0, Load1: 0, Load5: 0, Load15: 0,
			UptimeSeconds: 0, MemoryTotalBytes: 0, MemoryUsedBytes: 0,
			MemoryAvailableBytes: 0, SwapTotalBytes: 0, SwapUsedBytes: 0,
			NetworkRXBPS: 0, NetworkTXBPS: 0, NetworkRXBytes: 0, NetworkTXBytes: 0,
		}},
		Disks:        []DiskSample{},
		ProbeResults: []ProbeResult{},
	}
}
