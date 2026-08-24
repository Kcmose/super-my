import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { requiresResolvedAuth } from '../src/utils/access.js'
import { isAdminUser } from '../src/utils/roles.js'

function routeBlock(source, name) {
  const start = source.indexOf(`name: '${name}'`)
  assert.notEqual(start, -1, `missing route ${name}`)
  const end = source.indexOf('\n  },', start)
  assert.notEqual(end, -1, `unterminated route ${name}`)
  return source.slice(start, end)
}

test('only enabled administrator records are authenticated', () => {
  assert.equal(isAdminUser(null), false)
  assert.equal(isAdminUser({ enabled: true, role: 'viewer' }), false)
  assert.equal(isAdminUser({ enabled: false, role: 'admin' }), false)
  assert.equal(isAdminUser({ enabled: true, role: 'admin' }), true)
})

test('login and protected routes wait for administrator session discovery', () => {
  assert.equal(requiresResolvedAuth({}), false)
  assert.equal(requiresResolvedAuth({ requiresAdmin: true }), true)
  assert.equal(requiresResolvedAuth({ requiresAuth: true }), true)
  assert.equal(requiresResolvedAuth({ guestOnly: true }), true)
})

test('router exposes only setup, login and administrator pages', async () => {
  const source = await readFile(new URL('../src/router/index.js', import.meta.url), 'utf8')
  const paths = [...source.matchAll(/\bpath:\s*'([^']+)'/g)].map((match) => match[1])
  assert.deepEqual(paths, [
    '/',
    '/install',
    '/login',
    '/admin/nodes',
    '/admin/probes',
    '/admin/users',
    '/admin/audit',
    '/admin/system',
  ])
  assert.match(source, /path:\s*'\/'\s*,\s*redirect:\s*'\/admin\/nodes'/)

  for (const routeName of ['NodeAdmin', 'ProbeAdmin', 'UserAdmin', 'AuditLogs', 'SystemStatus']) {
    const block = routeBlock(source, routeName)
    assert.match(block, /requiresAuth:\s*true/)
    assert.match(block, /requiresAdmin:\s*true/)
  }

  assert.match(source, /if \(!authStore\.initialized && requiresResolvedAuth\(to\.meta\)\) await authStore\.checkAuth\(\)/)
  assert.match(source, /to\.matched\.length === 0[\s\S]+authStore\.isAuthenticated \? 'NodeAdmin' : 'Login'/)
  assert.match(source, /to\.meta\.guestOnly && authStore\.isAuthenticated[^\n]+NodeAdmin/)
  assert.match(source, /name:\s*'Login',\s*query:\s*\{ redirect:\s*to\.fullPath \}/)
})

test('management chrome is shown only for setup, login or a signed-in administrator', async () => {
  const [app, header, nav, login, authStore] = await Promise.all([
    readFile(new URL('../src/App.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/PanelHeader.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/AdminNav.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/views/Login.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/stores/auth.js', import.meta.url), 'utf8'),
  ])
  const disallowedAudienceLabel = String.fromCharCode(28216, 23458)

  assert.match(app, /route\.name === 'Login' \|\| route\.name === 'Install' \|\| authStore\.isAdmin/)
  assert.match(header, /<header v-if="authStore\.isAdmin"/)
  assert.match(header, /PROBE ADMIN/)
  assert.match(header, /\{\{ authStore\.currentUsername \}\}/)
  assert.match(header, />管理员<\/span>/)
  assert.match(header, /aria-label="退出登录"/)
  assert.match(nav, />管理面板<\/span>/)
  assert.match(login, /本管理面板仅供管理员登录/)
  assert.match(login, /来源 IP：\{\{ accessStatus\.source_ip \}\}/)
  assert.match(login, /允许（allowed=true）/)
  assert.match(login, /authApi\.getAccessStatus/)
  assert.doesNotMatch(login, /<router-link/)
  assert.match(login, /record\.meta\.requiresAdmin === true/)
  assert.match(authStore, /currentUsername:\s*\(state\) => state\.user\?\.username \|\| ''/)
  for (const source of [app, header, nav, login, authStore]) {
    assert.equal(source.includes(disallowedAudienceLabel), false)
  }
})
