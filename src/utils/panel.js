const REGION_NAMES = {
  hk: '中国香港',
  us: '美国',
  jp: '日本',
  sg: '新加坡',
  cn: '中国大陆',
}

export function countryFlag(countryCode) {
  if (!/^[A-Z]{2}$/.test(countryCode || '')) return '🌐'
  return String.fromCodePoint(...countryCode.split('').map((letter) => 127397 + letter.charCodeAt(0)))
}

export function formatBytes(bytes) {
  if (bytes === null || bytes === undefined || !Number.isFinite(Number(bytes)) || Number(bytes) < 0) return '—'
  const value = Number(bytes)
  if (value === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${Number((value / (1024 ** index)).toFixed(1))} ${units[index]}`
}

export function formatSpeed(bytesPerSecond) {
  const value = formatBytes(bytesPerSecond)
  return value === '—' ? value : `${value}/s`
}

export function formatTimeAgo(value, now = Date.now()) {
  if (!value) return '—'
  const timestamp = new Date(value).getTime()
  if (!Number.isFinite(timestamp)) return '—'
  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000))
  if (seconds < 10) return '刚刚'
  if (seconds < 60) return `${seconds} 秒前`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
  return `${Math.floor(seconds / 86400)} 天前`
}

export function formatUptime(seconds) {
  if (seconds === null || seconds === undefined || !Number.isFinite(Number(seconds)) || Number(seconds) < 0) return '—'
  const totalHours = Math.floor(Number(seconds) / 3600)
  const days = Math.floor(totalHours / 24)
  return `${days}天 ${String(totalHours % 24).padStart(2, '0')}h`
}

export function usagePercent(used, total) {
  if (!Number.isFinite(Number(used)) || !Number.isFinite(Number(total)) || Number(total) <= 0) return null
  return Math.max(0, Math.min(100, (Number(used) / Number(total)) * 100))
}

export function totalTraffic(metrics) {
  if (!metrics) return null
  if (Number.isFinite(Number(metrics.total_traffic_bytes))) return Number(metrics.total_traffic_bytes)
  if (Number.isFinite(Number(metrics.network_rx_bytes)) && Number.isFinite(Number(metrics.network_tx_bytes))) {
    return Number(metrics.network_rx_bytes) + Number(metrics.network_tx_bytes)
  }
  return null
}

export function normalizeNode(node, previousDisks = {}) {
  const metrics = node?.current_metrics || null
  const primaryName = node?.hostname || node?.display_name || (node?.node_id ? `节点 ${node.node_id.slice(0, 8)}` : '未命名节点')
  const regionKey = node?.region_key || (node?.country_code ? node.country_code.toLowerCase() : 'other')
  const rootDisk = node?.root_disk
  const projectedDisks = rootDisk ? {
    '/': { ...rootDisk, usage_percent: usagePercent(rootDisk.used_bytes, rootDisk.total_bytes) },
  } : {}
  const currentDisks = { ...(previousDisks || {}) }
  if (Object.prototype.hasOwnProperty.call(node || {}, 'root_disk')) {
    if (rootDisk) currentDisks['/'] = projectedDisks['/']
    else delete currentDisks['/']
  }
  return {
    ...node,
    hostname: primaryName,
    agent_hostname: node?.hostname || null,
    os_name: node?.operating_system || '系统未上报',
    arch: node?.architecture || '架构未上报',
    country_flag: countryFlag(node?.country_code),
    region_key: regionKey,
    region_name: REGION_NAMES[regionKey] || node?.location || node?.country_code || '其他地区',
    location: node?.location || '未设置位置',
    last_seen: node?.last_received_at || null,
    uptime_formatted: formatUptime(metrics?.uptime_seconds),
    current_metrics: metrics,
    current_disks: currentDisks,
  }
}

export function normalizeDisks(response) {
  return Object.fromEntries((response?.disks || []).map((series) => {
    const current = series.current
    return [series.mountpoint, current ? {
      ...current,
      usage_percent: usagePercent(current.used_bytes, current.total_bytes),
    } : null]
  }))
}

export function statusMeta(status) {
  switch (status) {
    case 'online': return { label: '在线', tone: 'online' }
    case 'skewed': return { label: '时钟异常', tone: 'warning' }
    case 'unregistered': return { label: '未注册', tone: 'muted' }
    case 'disabled': return { label: '已禁用', tone: 'offline' }
    default: return { label: '离线', tone: 'offline' }
  }
}

export function isOnline(node) {
  return node?.status === 'online'
}
