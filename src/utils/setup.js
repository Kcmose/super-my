import { utf8ByteLength } from './admin.js'

const SETUP_INSTALL_STATUSES = new Set(['pending', 'configuring', 'finalizing', 'recovery_required'])
const POSTGRES_IDENTIFIER = /^[a-z_][a-z0-9_]{0,62}$/
const HOST_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const RESERVED_DATABASE_NAMES = new Set(['postgres', 'template0', 'template1'])

export function setupStatusValue(response) {
  const value = response?.status ?? response?.state ?? response?.setup_state
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}

export function isSetupInstallStatus(status) {
  return SETUP_INSTALL_STATUSES.has(String(status ?? '').trim().toLowerCase())
}

function validHostname(value) {
  if (typeof value !== 'string' || value.length < 1 || value.length > 253) return false
  if (value.includes('://') || value.includes('/') || value.endsWith('.')) return false
  const labels = value.split('.')
  return labels.length >= 2
    && labels.every((label) => HOST_LABEL.test(label))
    && value !== 'example.com'
    && !value.endsWith('.example.com')
}

function validIPv4(value) {
  const octets = value.split('.')
  return octets.length === 4 && octets.every((octet) => (
    /^\d{1,3}$/.test(octet)
    && String(Number(octet)) === octet
    && Number(octet) <= 255
  ))
}

function validAllowlistEntry(value) {
  const [address, prefix, ...extra] = value.split('/')
  if (extra.length || !address) return false
  if (address.includes(':')) {
    if (!/^[0-9a-f:.]+$/i.test(address) || !address.includes(':')) return false
    if (prefix === undefined) return true
    return /^\d{1,3}$/.test(prefix) && Number(prefix) <= 128
  }
  if (!validIPv4(address)) return false
  if (prefix === undefined) return true
  return /^\d{1,2}$/.test(prefix) && Number(prefix) <= 32
}

export function parseSetupAllowlist(value) {
  return String(value ?? '')
    .split(/[\r\n,]+/)
    .map((entry) => entry.trim())
    .filter(Boolean)
}

export function normalizeSetupPayload(form) {
  return {
    database: {
      mode: 'local',
      name: String(form?.database?.name ?? '').trim(),
      username: String(form?.database?.username ?? '').trim(),
      password: String(form?.database?.password ?? ''),
      password_confirmation: String(form?.database?.password_confirmation ?? ''),
    },
    domains: {
      panel: String(form?.domains?.panel ?? '').trim().toLowerCase(),
      admin: String(form?.domains?.admin ?? '').trim().toLowerCase(),
      agent: String(form?.domains?.agent ?? '').trim().toLowerCase(),
    },
    tls: {
      mode: 'acme',
      email: String(form?.tls?.email ?? '').trim().toLowerCase(),
    },
    allowlist: Array.isArray(form?.allowlist)
      ? form.allowlist.map((entry) => String(entry).trim()).filter(Boolean)
      : parseSetupAllowlist(form?.allowlist),
    administrator: {
      username: String(form?.administrator?.username ?? '').trim(),
      password: String(form?.administrator?.password ?? ''),
      password_confirmation: String(form?.administrator?.password_confirmation ?? ''),
    },
  }
}

export function validateSetupStep(step, payload) {
  if (step === 2) {
    const database = payload?.database || {}
    if (database.mode !== 'local') return '首版安装仅支持服务器本机 PostgreSQL'
    if (!POSTGRES_IDENTIFIER.test(database.name || '')) return '数据库名称需以小写字母或下划线开头，仅包含小写字母、数字和下划线，最长 63 个字符'
    if (!POSTGRES_IDENTIFIER.test(database.username || '')) return '数据库用户名需以小写字母或下划线开头，仅包含小写字母、数字和下划线，最长 63 个字符'
    if (RESERVED_DATABASE_NAMES.has(database.name) || database.username === 'postgres') return '数据库名称或用户名使用了 PostgreSQL 保留名称'
    const passwordBytes = utf8ByteLength(database.password)
    if (passwordBytes < 12 || passwordBytes > 1024) return '数据库密码必须为 12 - 1024 个 UTF-8 字节'
    if (/[\u0000-\u001f\u007f-\u009f]/u.test(database.password)) return '数据库密码不能包含控制字符'
    if (database.password !== database.password_confirmation) return '两次输入的数据库密码不一致'
  }

  if (step === 3) {
    const domains = payload?.domains || {}
    for (const [key, label] of [['panel', '游客面板'], ['admin', '管理面板'], ['agent', 'Agent API']]) {
      if (!validHostname(domains[key])) return `${label}必须填写不含协议和路径的完整域名`
    }
    if (new Set([domains.panel, domains.admin, domains.agent]).size !== 3) return '游客面板、管理面板和 Agent API 必须使用三个不同域名'
    const domainValues = [domains.panel, domains.admin, domains.agent]
    if (domainValues.some((value, index) => domainValues.some((other, otherIndex) => index !== otherIndex && value.includes(other)))) {
      return '三个域名不能互相包含，请使用彼此独立的完整域名'
    }
    if (payload?.tls?.mode !== 'acme') return '首版安装仅支持 ACME 公信证书'
    if (!EMAIL_PATTERN.test(payload?.tls?.email || '') || payload.tls.email.length > 254) return '请输入有效的证书通知邮箱'
    if (!Array.isArray(payload?.allowlist) || payload.allowlist.length < 1) return '至少填写一个允许访问面板的 IP 或 CIDR'
    if (payload.allowlist.length > 128) return '访问白名单最多填写 128 项'
    if (payload.allowlist.some((entry) => !validAllowlistEntry(entry))) return '访问白名单中存在无效的 IP 或 CIDR'
    if (payload.allowlist.some((entry) => entry === '0.0.0.0/0' || entry === '::/0')) return '访问白名单禁止允许整个 IPv4 或 IPv6 互联网'
    if (new Set(payload.allowlist).size !== payload.allowlist.length) return '访问白名单不能包含重复项'
  }

  if (step === 4) {
    const administrator = payload?.administrator || {}
    if (!administrator.username || administrator.username.length > 128) return '管理员用户名长度必须为 1 - 128 个字符'
    const passwordBytes = utf8ByteLength(administrator.password)
    if (passwordBytes < 12 || passwordBytes > 1024) return '管理员密码必须为 12 - 1024 个 UTF-8 字节'
    if (administrator.password !== administrator.password_confirmation) return '两次输入的管理员密码不一致'
  }

  if (step === 5) {
    for (const requiredStep of [2, 3, 4]) {
      const error = validateSetupStep(requiredStep, payload)
      if (error) return error
    }
  }
  return ''
}
