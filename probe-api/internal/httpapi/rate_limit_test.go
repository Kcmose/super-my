package httpapi

import (
	"testing"
	"time"

	"probe-api/internal/config"
)

func TestFixedWindowLimiterBoundaryRecoveryAndBoundedKeys(t *testing.T) {
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	limiter := newFixedWindowLimiter(2, time.Minute, 2)
	limiter.now = func() time.Time { return now }
	if allowed, _ := limiter.Allow("one"); !allowed {
		t.Fatal("first request was denied")
	}
	if allowed, _ := limiter.Allow("one"); !allowed {
		t.Fatal("second request was denied")
	}
	if allowed, retry := limiter.Allow("one"); allowed || retry != time.Minute {
		t.Fatalf("third request = allowed %v, retry %s", allowed, retry)
	}
	limiter.Allow("two")
	if allowed, retry := limiter.Allow("three"); allowed || retry != time.Minute {
		t.Fatalf("new key at capacity = allowed %v, retry %s", allowed, retry)
	}
	if len(limiter.entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(limiter.entries))
	}
	now = now.Add(time.Minute)
	if allowed, retry := limiter.Allow("three"); !allowed || retry != 0 {
		t.Fatalf("request after reset = allowed %v, retry %s", allowed, retry)
	}
}

func TestAgentConfigAndReportShareRuntimeBuckets(t *testing.T) {
	cfg := config.Config{
		AgentEnrollIPLimit: 10, AgentRuntimeIPLimit: 2, AgentNodeLimit: 2,
		AgentRateWindow: time.Minute, RateLimitMaxKeysPerBucket: 100,
	}
	limits := newAgentRateLimiters(cfg)
	if limits.configIP != limits.reportIP || limits.configNode != limits.reportNode {
		t.Fatal("config and report do not share their runtime buckets")
	}
}
