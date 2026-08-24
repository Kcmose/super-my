package nodemanagement

import "testing"

func TestDecodeCreateRejectsIncompleteOrOperationalSettings(t *testing.T) {
	valid := []byte(`{
		"display_name":"edge-1",
		"agent_settings":{
			"metrics":{"collect_interval_seconds":5,"report_interval_seconds":10,"mountpoints":["/"],"include_virtual_interfaces":false},
			"agent":{"config_refresh_interval_seconds":60,"max_memory_queue_seconds":300},
			"limits":{"max_batch_samples":120}
		}
	}`)
	if _, err := DecodeCreate(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	incomplete := []byte(`{"display_name":"edge-1","agent_settings":{"metrics":{"collect_interval_seconds":5,"report_interval_seconds":10,"mountpoints":["/"]},"agent":{"config_refresh_interval_seconds":60,"max_memory_queue_seconds":300},"limits":{"max_batch_samples":120}}}`)
	if _, err := DecodeCreate(incomplete); err == nil {
		t.Fatal("incomplete settings were accepted")
	}
	unknown := []byte(`{"display_name":"edge-1","unexpected":"value"}`)
	if _, err := DecodeCreate(unknown); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestEnrollmentExpiryBounds(t *testing.T) {
	request, err := DecodeEnrollmentTokenRequest([]byte(`{}`))
	if err != nil || request.ExpiresInSeconds != 900 {
		t.Fatalf("default expiry mismatch: %#v, %v", request, err)
	}
	if _, err := DecodeEnrollmentTokenRequest([]byte(`{"expires_in_seconds":59}`)); err == nil {
		t.Fatal("short enrollment expiry was accepted")
	}
}
