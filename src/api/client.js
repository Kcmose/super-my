let csrfToken = ''

const allowedAPIPrefixes = [
  '/api/v1/auth',
  '/api/v1/admin',
  '/api/v1/panel',
]

export function setCsrfToken(token) {
  csrfToken = typeof token === 'string' ? token : ''
}

export function clearCsrfToken() {
  csrfToken = ''
}

async function readResponseBody(response) {
  if (response.status === 204 || response.status === 205) return null

  const text = await response.text()
  if (!text) return null

  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) return text

  try {
    return JSON.parse(text)
  } catch {
    const error = new Error('服务器返回了无效的 JSON 响应')
    error.status = response.status
    error.code = 'invalid_json_response'
    throw error
  }
}

function getErrorMessage(body, fallback) {
  if (typeof body === 'string' && body.trim()) return body
  return body?.message || body?.error || fallback
}

export function parseRetryAfter(value, now = Date.now()) {
  if (typeof value !== 'string' || !value.trim()) return null
  const normalized = value.trim()
  if (/^\d+$/.test(normalized)) {
    const seconds = Number(normalized)
    return Number.isSafeInteger(seconds) ? seconds : null
  }

  const retryAt = Date.parse(normalized)
  if (!Number.isFinite(retryAt)) return null
  return Math.max(0, Math.ceil((retryAt - now) / 1000))
}

function requestPathFor(url) {
  if (typeof url !== 'string' || !url.startsWith('/') || url.startsWith('//')) {
    const error = new Error('管理面板只允许访问同源 API')
    error.code = 'admin_cross_origin_forbidden'
    throw error
  }

  const requestPath = new URL(url, 'https://admin.invalid').pathname
  const allowed = allowedAPIPrefixes.some(
    (prefix) => requestPath === prefix || requestPath.startsWith(`${prefix}/`),
  )
  if (!allowed) {
    const error = new Error('管理面板请求不属于允许的 API 命名空间')
    error.code = 'admin_endpoint_forbidden'
    throw error
  }
  return requestPath
}

export async function request(url, options = {}) {
  const requestPath = requestPathFor(url)
  const headers = new Headers(options.headers || {})
  headers.set('Accept', 'application/json')

  if (options.body != null && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const method = (options.method || 'GET').toUpperCase()
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }

  const response = await fetch(url, {
    ...options,
    method,
    credentials: 'include',
    mode: 'same-origin',
    redirect: 'error',
    headers,
  })
  const body = await readResponseBody(response)

  if (!response.ok) {
    const loginRequest = requestPath === '/api/v1/auth/login'
    const adminRequest = requestPath === '/api/v1/admin' || requestPath.startsWith('/api/v1/admin/')
    const retryAfterSeconds = parseRetryAfter(response.headers.get('retry-after'))
    const fallback = response.status === 401
      ? (loginRequest
          ? '管理员账号或密码错误'
          : adminRequest
            ? '管理员会话已过期，请重新登录'
            : '数据访问未获授权')
      : response.status === 403
        ? '访问被拒绝：来源地址或角色权限不满足要求'
        : response.status === 429
          ? (retryAfterSeconds === null ? '请求过于频繁，请稍后重试' : `请求过于频繁，请在 ${retryAfterSeconds} 秒后重试`)
        : `请求失败: ${response.status}`
    const error = new Error(response.status === 429 ? fallback : getErrorMessage(body, fallback))
    error.status = response.status
    error.code = body?.error || body?.code || ''
    error.requestId = body?.request_id || ''
    error.retryAfterSeconds = retryAfterSeconds

    if (response.status === 401 && adminRequest && typeof window !== 'undefined') {
      window.dispatchEvent(new CustomEvent('probe:unauthorized'))
    }
    throw error
  }

  return body
}
