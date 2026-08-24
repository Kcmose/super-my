import { userRoleLabel } from './roles.js'

export const MAX_ADMIN_PAGES = 100
export const MIN_PASSWORD_BYTES = 12
export const MAX_PASSWORD_BYTES = 1024

const DEFAULT_AGENT_SETTINGS = Object.freeze({
  metrics: Object.freeze({
    collect_interval_seconds: 5,
    report_interval_seconds: 10,
    mountpoints: Object.freeze(['/']),
    include_virtual_interfaces: false,
  }),
  agent: Object.freeze({
    config_refresh_interval_seconds: 60,
    max_memory_queue_seconds: 300,
  }),
  limits: Object.freeze({
    max_batch_samples: 120,
  }),
})

const CONTROL_CHARACTER_PATTERN = /[\u0000-\u001f\u007f]/

function trimmed(value) {
  return String(value ?? '').trim()
}

function nullableText(value) {
  const normalized = trimmed(value)
  return normalized || null
}

function integerValue(value) {
  if (typeof value === 'number') return value
  if (typeof value !== 'string' || !value.trim()) return Number.NaN
  return Number(value)
}

function normalizedMountpoints(value) {
  if (!Array.isArray(value)) return []
  return value
    .map((mountpoint) => String(mountpoint ?? '').replace(/^ +| +$/g, ''))
    .filter(Boolean)
}

export function cloneAgentSettings(settings = DEFAULT_AGENT_SETTINGS) {
  const metrics = settings?.metrics || DEFAULT_AGENT_SETTINGS.metrics
  const agent = settings?.agent || DEFAULT_AGENT_SETTINGS.agent
  const limits = settings?.limits || DEFAULT_AGENT_SETTINGS.limits
  return {
    metrics: {
      collect_interval_seconds: metrics.collect_interval_seconds ?? DEFAULT_AGENT_SETTINGS.metrics.collect_interval_seconds,
      report_interval_seconds: metrics.report_interval_seconds ?? DEFAULT_AGENT_SETTINGS.metrics.report_interval_seconds,
      mountpoints: Array.isArray(metrics.mountpoints)
        ? metrics.mountpoints.map((mountpoint) => String(mountpoint ?? ''))
        : [...DEFAULT_AGENT_SETTINGS.metrics.mountpoints],
      include_virtual_interfaces: metrics.include_virtual_interfaces ?? DEFAULT_AGENT_SETTINGS.metrics.include_virtual_interfaces,
    },
    agent: {
      config_refresh_interval_seconds: agent.config_refresh_interval_seconds ?? DEFAULT_AGENT_SETTINGS.agent.config_refresh_interval_seconds,
      max_memory_queue_seconds: agent.max_memory_queue_seconds ?? DEFAULT_AGENT_SETTINGS.agent.max_memory_queue_seconds,
    },
    limits: {
      max_batch_samples: limits.max_batch_samples ?? DEFAULT_AGENT_SETTINGS.limits.max_batch_samples,
    },
  }
}

export function createDefaultAgentSettings() {
  return cloneAgentSettings(DEFAULT_AGENT_SETTINGS)
}

function normalizeAgentSettings(settings) {
  const normalized = cloneAgentSettings(settings)
  return {
    metrics: {
      collect_interval_seconds: integerValue(normalized.metrics.collect_interval_seconds),
      report_interval_seconds: integerValue(normalized.metrics.report_interval_seconds),
      mountpoints: normalizedMountpoints(normalized.metrics.mountpoints),
      include_virtual_interfaces: Boolean(normalized.metrics.include_virtual_interfaces),
    },
    agent: {
      config_refresh_interval_seconds: integerValue(normalized.agent.config_refresh_interval_seconds),
      max_memory_queue_seconds: integerValue(normalized.agent.max_memory_queue_seconds),
    },
    limits: {
      max_batch_samples: integerValue(normalized.limits.max_batch_samples),
    },
  }
}

export async function collectCursorPages(fetchPage, itemsKey, maxPages = MAX_ADMIN_PAGES) {
  const items = []
  const seenCursors = new Set()
  let cursor

  for (let page = 0; page < maxPages; page += 1) {
    const response = await fetchPage(cursor)
    if (Array.isArray(response?.[itemsKey])) items.push(...response[itemsKey])

    const nextCursor = response?.next_cursor || null
    if (!nextCursor) return items
    if (seenCursors.has(nextCursor)) throw new Error('服务端返回了重复分页游标')
    seenCursors.add(nextCursor)
    cursor = nextCursor
  }

  throw new Error(`分页超过安全上限 ${maxPages}`)
}

export function normalizeNodePayload(form) {
  const countryCode = trimmed(form?.country_code).toUpperCase()
  return {
    display_name: trimmed(form?.display_name),
    enabled: Boolean(form?.enabled),
    country_code: countryCode || null,
    region_key: nullableText(form?.region_key),
    location: nullableText(form?.location),
    agent_settings: normalizeAgentSettings(form?.agent_settings),
  }
}

export function validateNodePayload(payload) {
  if (!payload.display_name || payload.display_name.length > 128) return '节点名称长度必须为 1 - 128 个字符'
  if (payload.country_code !== null && !/^[A-Z]{2}$/.test(payload.country_code)) return '国家/地区代码必须是两个大写英文字母'
  if (payload.region_key !== null && payload.region_key.length > 64) return '地区标识最长 64 个字符'
  if (payload.location !== null && payload.location.length > 128) return '位置说明最长 128 个字符'

  const collectInterval = payload.agent_settings?.metrics?.collect_interval_seconds
  const reportInterval = payload.agent_settings?.metrics?.report_interval_seconds
  const mountpoints = payload.agent_settings?.metrics?.mountpoints
  const includeVirtualInterfaces = payload.agent_settings?.metrics?.include_virtual_interfaces
  const configRefreshInterval = payload.agent_settings?.agent?.config_refresh_interval_seconds
  const maxMemoryQueue = payload.agent_settings?.agent?.max_memory_queue_seconds
  const maxBatchSamples = payload.agent_settings?.limits?.max_batch_samples

  if (!Number.isInteger(collectInterval) || collectInterval < 5 || collectInterval > 300) return '采集周期必须是 5 - 300 秒的整数'
  if (!Number.isInteger(reportInterval) || reportInterval < 5 || reportInterval > 300) return '上报周期必须是 5 - 300 秒的整数'
  if (reportInterval < collectInterval) return '上报周期不能短于采集周期'
  if (!Array.isArray(mountpoints) || mountpoints.length < 1 || mountpoints.length > 32) return '挂载点数量必须为 1 - 32 个'
  if (mountpoints.some((mountpoint) => typeof mountpoint !== 'string' || !mountpoint || mountpoint.length > 4096)) return '每个挂载点长度必须为 1 - 4096 个字符'
  if (mountpoints.some((mountpoint) => CONTROL_CHARACTER_PATTERN.test(mountpoint))) return '挂载点不能包含控制字符'
  if (mountpoints.some((mountpoint) => !mountpoint.startsWith('/'))) return '挂载点必须使用以 / 开头的绝对路径'
  if (!mountpoints.includes('/')) return '挂载点必须包含根目录 /'
  if (new Set(mountpoints).size !== mountpoints.length) return '挂载点不能重复'
  if (typeof includeVirtualInterfaces !== 'boolean') return '虚拟网卡采集设置必须是布尔值'
  if (!Number.isInteger(configRefreshInterval) || configRefreshInterval < 10 || configRefreshInterval > 86400) return '配置刷新周期必须是 10 - 86400 秒的整数'
  if (!Number.isInteger(maxMemoryQueue) || maxMemoryQueue < 1 || maxMemoryQueue > 300) return '内存队列时长必须是 1 - 300 秒的整数'
  if (maxMemoryQueue < reportInterval) return '内存队列时长不能短于上报周期'
  if (!Number.isInteger(maxBatchSamples) || maxBatchSamples < 1 || maxBatchSamples > 120) return '单批样本上限必须是 1 - 120 的整数'
  return ''
}

export function utf8ByteLength(value) {
  return new TextEncoder().encode(String(value ?? '')).length
}

export function normalizeUserPayload(form, editing = false) {
  const payload = {
    username: trimmed(form?.username),
    role: 'admin',
    enabled: Boolean(form?.enabled),
  }
  const password = String(form?.password ?? '')
  if (!editing || password) payload.password = password
  return payload
}

export function validateUserPayload(payload, editing = false) {
  if (!payload.username || payload.username.length > 128) return '用户名长度必须为 1 - 128 个字符'
  if (payload.role !== 'admin') return '只能创建或修改管理员账号'
  if (!editing && !Object.prototype.hasOwnProperty.call(payload, 'password')) return '新用户必须设置密码'
  if (Object.prototype.hasOwnProperty.call(payload, 'password')) {
    const passwordBytes = utf8ByteLength(payload.password)
    if (passwordBytes < MIN_PASSWORD_BYTES) return `密码至少需要 ${MIN_PASSWORD_BYTES} 个 UTF-8 字节`
    if (passwordBytes > MAX_PASSWORD_BYTES) return `密码最长 ${MAX_PASSWORD_BYTES} 个 UTF-8 字节`
  }
  return ''
}

export function localDateTimeToISO(value) {
  if (!value) return undefined
  const parsed = new Date(value)
  if (!Number.isFinite(parsed.getTime())) return null
  return parsed.toISOString()
}

export function validateAuditWindow(from, to) {
  const fromISO = localDateTimeToISO(from)
  const toISO = localDateTimeToISO(to)
  if (from && fromISO === null) return { error: '开始时间无效' }
  if (to && toISO === null) return { error: '结束时间无效' }
  if (fromISO && toISO && Date.parse(fromISO) >= Date.parse(toISO)) return { error: '开始时间必须早于结束时间' }
  return { from: fromISO, to: toISO, error: '' }
}

export function formatUTCDateTime(value) {
  if (!value) return '—'
  const parsed = new Date(value)
  if (!Number.isFinite(parsed.getTime())) return '—'
  return `${parsed.toLocaleString('zh-CN', {
    timeZone: 'UTC',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })} UTC`
}

export function auditSummaryText(value) {
  if (value === null || value === undefined) return '—'
  try {
    return JSON.stringify(value, (key, entry) => key === 'role' ? userRoleLabel(entry) : entry, 2)
  } catch {
    return '摘要无法显示'
  }
}
