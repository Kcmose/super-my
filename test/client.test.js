import test from 'node:test'
import assert from 'node:assert/strict'
import { clearCsrfToken, parseRetryAfter, request, setCsrfToken } from '../src/api/client.js'

test('write requests carry the in-memory CSRF token while reads do not', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
    clearCsrfToken()
  })

  const seen = []
  globalThis.fetch = async (url, options) => {
    seen.push({ url, options })
    return new Response(null, { status: 204 })
  }

  setCsrfToken('csrf-session-token')
  await request('/api/v1/auth/logout', { method: 'POST' })
  await request('/api/v1/panel/nodes')

  assert.equal(seen[0].options.headers.get('X-CSRF-Token'), 'csrf-session-token')
  assert.equal(seen[1].options.headers.has('X-CSRF-Token'), false)
  assert.equal(seen[0].options.credentials, 'include')
  assert.equal(seen[1].options.credentials, 'include')
  assert.equal(seen[0].options.mode, 'same-origin')
  assert.equal(seen[0].options.redirect, 'error')
})

test('management client rejects cross-origin and undeclared API namespaces', async () => {
  await assert.rejects(
    request('https://api.example.com/api/v1/admin/users'),
    (error) => error.code === 'admin_cross_origin_forbidden',
  )
  await assert.rejects(
    request('//evil.example/api/v1/admin/users'),
    (error) => error.code === 'admin_cross_origin_forbidden',
  )
  await assert.rejects(
    request('/api/v1/agent/config'),
    (error) => error.code === 'admin_endpoint_forbidden',
  )
  await assert.rejects(
    request('/api/v1/admin/../agent/config'),
    (error) => error.code === 'admin_endpoint_forbidden',
  )
})

test('rate-limit errors use Retry-After without exposing a proxy response body', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
  })

  globalThis.fetch = async () => new Response('<html>rate limited</html>', {
    status: 429,
    headers: { 'Retry-After': '17', 'Content-Type': 'text/html' },
  })

  await assert.rejects(
    request('/api/v1/auth/login', { method: 'POST', body: '{}' }),
    (error) => error.status === 429 && error.retryAfterSeconds === 17 && error.message === '请求过于频繁，请在 17 秒后重试',
  )
})

test('Retry-After accepts seconds and HTTP dates', () => {
  const now = Date.UTC(2026, 7, 22, 0, 0, 0)
  assert.equal(parseRetryAfter('9', now), 9)
  assert.equal(parseRetryAfter('Sat, 22 Aug 2026 00:00:12 GMT', now), 12)
  assert.equal(parseRetryAfter('invalid', now), null)
})

test('only expired administrator requests trigger a login redirect event', async (context) => {
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalCustomEvent = globalThis.CustomEvent
  const dispatched = []
  context.after(() => {
    globalThis.fetch = originalFetch
    if (originalWindow === undefined) delete globalThis.window
    else globalThis.window = originalWindow
    if (originalCustomEvent === undefined) delete globalThis.CustomEvent
    else globalThis.CustomEvent = originalCustomEvent
  })

  globalThis.window = {
    location: { origin: 'https://admin.example.test' },
    dispatchEvent: (event) => dispatched.push(event.type),
  }
  globalThis.CustomEvent = class CustomEvent {
    constructor(type) {
      this.type = type
    }
  }
  globalThis.fetch = async () => new Response(null, { status: 401 })

  await assert.rejects(request('/api/v1/panel/nodes'), (error) => error.message === '数据访问未获授权')
  assert.deepEqual(dispatched, [])

  await assert.rejects(request('/api/v1/admin/users'), (error) => error.message === '管理员会话已过期，请重新登录')
  assert.deepEqual(dispatched, ['probe:unauthorized'])
})
