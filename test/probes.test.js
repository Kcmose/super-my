import test from 'node:test'
import assert from 'node:assert/strict'
import {
  MAX_PROBE_RETENTION_SECONDS,
  formatTargetAddress,
  probeTimeWindow,
  summarizeProbePoints,
} from '../src/utils/probes.js'

test('probe windows are clipped to each target retention and never exceed 90 days', () => {
  const now = Date.UTC(2026, 7, 22, 0, 0, 0)
  const rawWindow = probeTimeWindow('6h', MAX_PROBE_RETENTION_SECONDS, now)
  assert.equal(rawWindow.effectiveSeconds, 6 * 60 * 60)
  assert.equal(rawWindow.clipped, false)

  const short = probeTimeWindow('30d', 7 * 86400, now)
  assert.equal(short.effectiveSeconds, 7 * 86400)
  assert.equal(short.clipped, true)
  assert.equal(Date.parse(short.to) - Date.parse(short.from), 7 * 86400 * 1000)

  const bounded = probeTimeWindow('90d', MAX_PROBE_RETENTION_SECONDS * 2, now)
  assert.equal(bounded.effectiveSeconds, MAX_PROBE_RETENTION_SECONDS)
})

test('probe summaries keep transport loss and HTTP status failures separate', () => {
  const summary = summarizeProbePoints([
    { sent_count: 4, received_count: 3, latency_sum_us: 6000 },
    { sent_count: 6, received_count: 5, latency_sum_us: 14000, http_error_count: 1, http_status_code: 500 },
  ])
  assert.deepEqual(summary, {
    totalSent: 10,
    totalReceived: 8,
    totalSuccessful: 7,
    totalHttpErrors: 1,
    latestHTTPStatus: 500,
    averageLatencyMs: 2.5,
    lossRate: 0.2,
    failureRate: 0.3,
  })

  assert.deepEqual(summarizeProbePoints([]), {
    totalSent: 0,
    totalReceived: 0,
    totalSuccessful: 0,
    totalHttpErrors: 0,
    latestHTTPStatus: null,
    averageLatencyMs: null,
    lossRate: null,
    failureRate: null,
  })
})

test('probe summaries clamp malformed HTTP error counters to received attempts', () => {
  const summary = summarizeProbePoints([
    { sent_count: 2, received_count: 1, latency_sum_us: 1000, http_error_count: 99, http_status_code: 700 },
  ])
  assert.equal(summary.totalHttpErrors, 1)
  assert.equal(summary.totalSuccessful, 0)
  assert.equal(summary.failureRate, 1)
  assert.equal(summary.latestHTTPStatus, null)
})

test('target addresses stay type aware', () => {
  assert.equal(formatTargetAddress({ type: 'tcp', host: 'db.internal', port: 5432 }), 'db.internal:5432')
  assert.equal(formatTargetAddress({ type: 'https', host: 'example.com', port: null, path: '/health' }), 'https://example.com/health')
  assert.equal(formatTargetAddress({ type: 'tcp', host: '2001:db8::1', port: 443 }), '[2001:db8::1]:443')
})
