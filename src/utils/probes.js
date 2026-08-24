export const MAX_PROBE_RETENTION_SECONDS = 90 * 24 * 60 * 60

const RANGE_SECONDS = {
  '6h': 6 * 60 * 60,
  '24h': 24 * 60 * 60,
  '7d': 7 * 24 * 60 * 60,
  '30d': 30 * 24 * 60 * 60,
  '90d': MAX_PROBE_RETENTION_SECONDS,
}

const TYPE_LABELS = {
  tcp: 'TCP',
  http: 'HTTP',
  https: 'HTTPS',
}

const RESOLUTION_LABELS = {
  raw: '原始结果',
  '5m': '5 分钟聚合',
  '1h': '1 小时聚合',
}

function finiteNumber(value) {
  if (value == null || value === '') return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}

export function probeTypeLabel(type) {
  return TYPE_LABELS[type] || String(type || '未知').toUpperCase()
}

export function resolutionLabel(resolution) {
  return RESOLUTION_LABELS[resolution] || '自动选择'
}

export function formatTargetAddress(target) {
  if (!target?.host) return '—'
  const port = finiteNumber(target.port)
  const host = String(target.host)
  const authorityHost = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
  const authority = port == null ? authorityHost : `${authorityHost}:${port}`
  if (target.type === 'http' || target.type === 'https') {
    return `${target.type}://${authority}${target.path || '/'}`
  }
  return authority
}

export function retentionDays(retentionSeconds) {
  const seconds = finiteNumber(retentionSeconds)
  return seconds == null || seconds < 0 ? null : seconds / 86400
}

export function probeTimeWindow(range, retentionSeconds, now = Date.now()) {
  const requestedSeconds = RANGE_SECONDS[range] || RANGE_SECONDS['24h']
  const configuredRetention = finiteNumber(retentionSeconds)
  const boundedRetention = configuredRetention == null
    ? MAX_PROBE_RETENTION_SECONDS
    : Math.max(1, Math.min(MAX_PROBE_RETENTION_SECONDS, configuredRetention))
  const effectiveSeconds = Math.min(requestedSeconds, boundedRetention)
  const toMilliseconds = finiteNumber(now)
  const to = new Date(toMilliseconds == null ? Date.now() : toMilliseconds)
  const from = new Date(to.getTime() - effectiveSeconds * 1000)

  return {
    from: from.toISOString(),
    to: to.toISOString(),
    requestedSeconds,
    effectiveSeconds,
    clipped: effectiveSeconds < requestedSeconds,
  }
}

export function summarizeProbePoints(points) {
  let totalSent = 0
  let totalReceived = 0
  let totalHttpErrors = 0
  let latencySumUs = 0
  let latestHTTPStatus = null

  for (const point of Array.isArray(points) ? points : []) {
    const sent = Math.max(0, finiteNumber(point?.sent_count) || 0)
    const received = Math.max(0, Math.min(sent, finiteNumber(point?.received_count) || 0))
    const httpErrors = Math.max(0, Math.min(received, finiteNumber(point?.http_error_count) || 0))
    const httpStatus = finiteNumber(point?.http_status_code)
    totalSent += sent
    totalReceived += received
    totalHttpErrors += httpErrors
    latencySumUs += Math.max(0, finiteNumber(point?.latency_sum_us) || 0)
    if (Number.isInteger(httpStatus) && httpStatus >= 100 && httpStatus <= 599) latestHTTPStatus = httpStatus
  }

  const totalFailed = Math.min(totalSent, totalSent - totalReceived + totalHttpErrors)
  return {
    totalSent,
    totalReceived,
    totalSuccessful: totalSent - totalFailed,
    totalHttpErrors,
    latestHTTPStatus,
    averageLatencyMs: totalReceived > 0 ? latencySumUs / totalReceived / 1000 : null,
    lossRate: totalSent > 0 ? Math.max(0, Math.min(1, (totalSent - totalReceived) / totalSent)) : null,
    failureRate: totalSent > 0 ? Math.max(0, Math.min(1, totalFailed / totalSent)) : null,
  }
}

export function microsecondsToMilliseconds(value) {
  const number = finiteNumber(value)
  return number == null ? null : number / 1000
}
