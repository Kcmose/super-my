import test from 'node:test'
import assert from 'node:assert/strict'
import { adminApi } from '../src/api/admin.js'
import { clearCsrfToken, setCsrfToken } from '../src/api/client.js'

test('admin API uses frozen same-origin routes, encoded identifiers, JSON and CSRF', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
    clearCsrfToken()
  })

  const seen = []
  globalThis.fetch = async (url, options) => {
    seen.push({ url, options })
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  setCsrfToken('csrf-admin')

  await adminApi.createNode({ display_name: 'edge' })
  await adminApi.updateNode('node /?', { enabled: false })
  await adminApi.createEnrollmentToken('node /?')
  await adminApi.rotateToken('node /?')
  await adminApi.revokeToken('node /?')
  await adminApi.deleteNode('node /?')
  await adminApi.getUsers({ limit: 25, cursor: 'next+page' })
  await adminApi.createUser({ username: 'admin-two', password: 'long-password', role: 'admin', enabled: true })
  await adminApi.updateUser('user /?', { role: 'admin' })
  await adminApi.deleteUser('user /?')
  await adminApi.getAuditLogs({ limit: 10, action: 'user.update', from: '2026-08-21T00:00:00.000Z' })
  await adminApi.getSystemStatus()

  assert.equal(seen[0].url, '/api/v1/admin/nodes')
  assert.deepEqual(JSON.parse(seen[0].options.body), { display_name: 'edge' })
  assert.equal(seen[1].url, '/api/v1/admin/nodes/node%20%2F%3F')
  assert.equal(seen[2].url, '/api/v1/admin/nodes/node%20%2F%3F/enrollment-token')
  assert.deepEqual(JSON.parse(seen[2].options.body), { expires_in_seconds: 900 })
  assert.equal(seen[3].url, '/api/v1/admin/nodes/node%20%2F%3F/rotate-token')
  assert.equal(seen[4].url, '/api/v1/admin/nodes/node%20%2F%3F/revoke-token')
  assert.equal(seen[5].url, '/api/v1/admin/nodes/node%20%2F%3F')
  assert.equal(seen[6].url, '/api/v1/admin/users?limit=25&cursor=next%2Bpage')
  assert.equal(seen[8].url, '/api/v1/admin/users/user%20%2F%3F')
  assert.equal(seen[9].url, '/api/v1/admin/users/user%20%2F%3F')
  assert.match(seen[10].url, /^\/api\/v1\/admin\/audit-logs\?limit=10&action=user\.update&from=/)
  assert.equal(seen[11].url, '/api/v1/admin/system/status')
  for (const entry of seen.filter(({ options }) => !['GET', 'HEAD', 'OPTIONS'].includes(options.method))) {
    assert.equal(entry.options.headers.get('X-CSRF-Token'), 'csrf-admin')
  }
})
