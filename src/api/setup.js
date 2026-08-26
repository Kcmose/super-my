import { setupDefaultsValue } from '../utils/setup.js'

const setupEndpoints = Object.freeze({
  status: '/api/v1/setup/status',
  session: '/api/v1/setup/session',
  complete: '/api/v1/setup/complete',
})

let setupSessionToken = ''
let setupCsrfToken = ''
let setupSessionPromise = null
const SETUP_TOKEN_PATTERN = /^[0-9a-f]{64}$/
const RFC3339_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/

function setupPathFor(url) {
  if (typeof url !== 'string' || !url.startsWith('/') || url.startsWith('//')) {
    const error = new Error('安装向导只允许访问同源 Setup API')
    error.code = 'setup_cross_origin_forbidden'
    throw error
  }

  const parsed = new URL(url, 'https://setup.invalid')
  if (!Object.values(setupEndpoints).includes(parsed.pathname) || parsed.search || parsed.hash) {
    const error = new Error('安装向导请求不属于允许的 Setup API')
    error.code = 'setup_endpoint_forbidden'
    throw error
  }
  return parsed.pathname
}

async function readSetupResponse(response) {
  if (response.status === 204 || response.status === 205) return null
  const text = await response.text()
  if (!text) return null

  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) return text
  try {
    return JSON.parse(text)
  } catch {
    const error = new Error('安装服务返回了无效的 JSON 响应')
    error.status = response.status
    error.code = 'invalid_setup_json_response'
    throw error
  }
}

function setupErrorMessage(body, status) {
  if (body && typeof body === 'object') return body.message || body.error || `安装请求失败: ${status}`
  return `安装请求失败: ${status}`
}

async function setupRequest(url, options = {}) {
  setupPathFor(url)
  const headers = new Headers(options.headers || {})
  headers.set('Accept', 'application/json')
  if (options.body != null) headers.set('Content-Type', 'application/json')

  const response = await fetch(url, {
    ...options,
    method: (options.method || 'GET').toUpperCase(),
    credentials: 'omit',
    mode: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    headers,
  })
  const body = await readSetupResponse(response)
  if (!response.ok) {
    const error = new Error(setupErrorMessage(body, response.status))
    error.status = response.status
    error.code = body?.error || body?.code || ''
    error.requestId = body?.request_id || ''
    throw error
  }
  return body
}

function requireSetupSession() {
  if (setupSessionToken && setupCsrfToken) return
  const error = new Error('安装会话不存在或已经失效，请重新建立安全会话后重试')
  error.code = 'setup_session_missing'
  throw error
}

function requireManagementSetupPayload(payload) {
  const domainFields = payload?.domains && typeof payload.domains === 'object' && !Array.isArray(payload.domains)
    ? Object.keys(payload.domains)
    : []
  if (
    payload?.profile !== 'management'
    || domainFields.length !== 1
    || domainFields[0] !== 'admin'
  ) {
    const error = new Error('管理端安装向导只允许提交 management 配置')
    error.code = 'setup_profile_forbidden'
    throw error
  }
}

export function clearSetupSecrets() {
  setupSessionToken = ''
  setupCsrfToken = ''
}

export function hasSetupSession() {
  return Boolean(setupSessionToken && setupCsrfToken)
}

export const setupApi = {
  getStatus: ({ signal } = {}) => setupRequest(setupEndpoints.status, { signal }),

  createSession({ signal } = {}) {
    if (setupSessionPromise) return setupSessionPromise

    clearSetupSecrets()
    const request = (async () => {
      const response = await setupRequest(setupEndpoints.session, {
        method: 'POST',
        body: '{}',
        signal,
      })
      const expiresAt = typeof response?.expires_at === 'string' ? response.expires_at : ''
      const expiresAtMilliseconds = RFC3339_PATTERN.test(expiresAt) ? Date.parse(expiresAt) : Number.NaN
      const defaults = setupDefaultsValue(response)
      if (
        !SETUP_TOKEN_PATTERN.test(response?.session_token || '')
        || !SETUP_TOKEN_PATTERN.test(response?.csrf_token || '')
        || !Number.isFinite(expiresAtMilliseconds)
        || expiresAtMilliseconds <= Date.now()
        || !defaults
      ) {
        const error = new Error('安装服务没有返回有效的临时会话')
        error.code = 'invalid_setup_session_response'
        throw error
      }

      setupSessionToken = response.session_token
      setupCsrfToken = response.csrf_token
      return { expires_at: expiresAt, defaults }
    })()
    setupSessionPromise = request
    request.finally(() => {
      if (setupSessionPromise === request) setupSessionPromise = null
    }).catch(() => {})
    return request
  },

  async complete(payload) {
    requireSetupSession()
    requireManagementSetupPayload(payload)
    try {
      const response = await setupRequest(setupEndpoints.complete, {
        method: 'POST',
        headers: {
          'X-Probe-Setup-Session': setupSessionToken,
          'X-CSRF-Token': setupCsrfToken,
        },
        body: JSON.stringify(payload),
      })
      clearSetupSecrets()
      return response
    } catch (error) {
      if (error?.status === 401 || error?.status === 403) clearSetupSecrets()
      throw error
    }
  },
}
