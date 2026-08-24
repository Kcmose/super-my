package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"probe-api/internal/config"
	"probe-api/internal/httpapi/respond"
)

type fixedWindowEntry struct {
	started time.Time
	count   int
}

type fixedWindowLimiter struct {
	mu         sync.Mutex
	entries    map[string]fixedWindowEntry
	limit      int
	window     time.Duration
	maxEntries int
	now        func() time.Time
	lastSweep  time.Time
}

func newFixedWindowLimiter(limit int, window time.Duration, maxEntries int) *fixedWindowLimiter {
	return &fixedWindowLimiter{
		entries: make(map[string]fixedWindowEntry), limit: limit, window: window,
		maxEntries: maxEntries, now: time.Now,
	}
}

func (limiter *fixedWindowLimiter) Allow(key string) (bool, time.Duration) {
	now := limiter.now().UTC()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	entry, exists := limiter.entries[key]
	if !exists && len(limiter.entries) >= limiter.maxEntries {
		sweepInterval := limiter.window / 4
		if sweepInterval < time.Second {
			sweepInterval = time.Second
		}
		if limiter.lastSweep.IsZero() || now.Sub(limiter.lastSweep) >= sweepInterval {
			limiter.removeExpired(now)
			limiter.lastSweep = now
		}
		if len(limiter.entries) >= limiter.maxEntries {
			return false, limiter.window
		}
	}
	if !exists || now.Before(entry.started) || now.Sub(entry.started) >= limiter.window {
		limiter.entries[key] = fixedWindowEntry{started: now, count: 1}
		return true, 0
	}
	if entry.count >= limiter.limit {
		limiter.entries[key] = entry
		retry := entry.started.Add(limiter.window).Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	entry.count++
	limiter.entries[key] = entry
	return true, 0
}

func (limiter *fixedWindowLimiter) removeExpired(now time.Time) {
	for key, entry := range limiter.entries {
		if now.Before(entry.started) || now.Sub(entry.started) >= limiter.window {
			delete(limiter.entries, key)
		}
	}
}

type agentRateLimiters struct {
	enrollIP   *fixedWindowLimiter
	configIP   *fixedWindowLimiter
	reportIP   *fixedWindowLimiter
	configNode *fixedWindowLimiter
	reportNode *fixedWindowLimiter
}

func newAgentRateLimiters(cfg config.Config) *agentRateLimiters {
	maxKeys := int(cfg.RateLimitMaxKeysPerBucket)
	window := cfg.AgentRateWindow
	runtimeIP := newFixedWindowLimiter(int(cfg.AgentRuntimeIPLimit), window, maxKeys)
	runtimeNode := newFixedWindowLimiter(int(cfg.AgentNodeLimit), window, maxKeys)
	return &agentRateLimiters{
		enrollIP:   newFixedWindowLimiter(int(cfg.AgentEnrollIPLimit), window, maxKeys),
		configIP:   runtimeIP,
		reportIP:   runtimeIP,
		configNode: runtimeNode,
		reportNode: runtimeNode,
	}
}

func writeRateLimited(writer http.ResponseWriter, request *http.Request, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	writer.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writer.Header().Set("Cache-Control", "no-store")
	respond.Error(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later", requestIDFromContext(request.Context()))
}
