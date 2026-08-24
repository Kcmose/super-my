import test from 'node:test'
import assert from 'node:assert/strict'
import { authApi } from '../src/api/auth.js'
import { panelApi } from '../src/api/panel.js'

test('authentication client uses the administrator session contract', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
  })

  const seen = []
  globalThis.fetch = async (url, options) => {
    seen.push({ url, options })
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }

  await authApi.getAccessStatus()
  await authApi.login('admin', 'correct horse battery staple')
  await authApi.getCurrentUser()
  await authApi.logout()

  assert.deepEqual(seen.map(({ url }) => url), [
    '/api/v1/auth/access',
    '/api/v1/auth/login',
    '/api/v1/auth/me',
    '/api/v1/auth/logout',
  ])
  assert.equal(seen[0].options.method, 'GET')
  assert.equal(seen[1].options.method, 'POST')
  assert.deepEqual(JSON.parse(seen[1].options.body), {
    username: 'admin',
    password: 'correct horse battery staple',
  })
  assert.equal(seen[2].options.method, 'GET')
  assert.equal(seen[3].options.method, 'POST')
  for (const { options } of seen) {
    assert.equal(options.credentials, 'include')
    assert.equal(options.headers.has('X-Forwarded-For'), false)
    assert.equal(options.headers.has('Forwarded'), false)
    assert.equal(options.headers.has('X-Probe-Client-IP'), false)
  }
})

test('panel client reads same-origin data needed by management pages', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
  })

  const seen = []
  globalThis.fetch = async (url, options) => {
    seen.push({ url, options })
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }

  await panelApi.getNodes({ limit: 50, status: 'online' })

  assert.equal(seen[0].url, '/api/v1/panel/nodes?limit=50&status=online')
  for (const { options } of seen) assert.equal(options.method, 'GET')
})
