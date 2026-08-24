import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import {
  clearSetupSecrets,
  hasSetupSession,
  setupApi,
} from '../src/api/setup.js'
import {
  isSetupInstallStatus,
  normalizeSetupPayload,
  setupStatusValue,
  validateSetupStep,
} from '../src/utils/setup.js'

function validSetupForm() {
  return {
    database: {
      mode: 'external-is-ignored',
      name: ' probe ',
      username: ' probe_user ',
      password: 'database-password',
      password_confirmation: 'database-password',
    },
    domains: {
      panel: ' PANEL.MONITOR.TEST ',
      admin: ' ADMIN.MONITOR.TEST ',
      agent: ' API.MONITOR.TEST ',
    },
    tls: { mode: 'manual-is-ignored', email: ' ADMIN@EXAMPLE.COM ' },
    allowlist: '203.0.113.25/32\n2001:db8:1234::/48',
    administrator: {
      username: ' admin ',
      password: 'administrator-password',
      password_confirmation: 'administrator-password',
    },
  }
}

test('dedicated setup client follows the one-time session and CSRF contract', async (context) => {
  const originalFetch = globalThis.fetch
  const seen = []
  context.after(() => {
    globalThis.fetch = originalFetch
    clearSetupSecrets()
  })

  globalThis.fetch = async (url, options) => {
    seen.push({ url, options })
    if (url.endsWith('/status')) {
      return new Response(JSON.stringify({ status: 'pending' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url.endsWith('/session')) {
      return new Response(JSON.stringify({
        session_token: 'temporary-session',
        csrf_token: 'temporary-csrf',
        expires_at: '2026-08-24T12:30:00Z',
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ status: 'finalizing' }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    })
  }

  const payload = normalizeSetupPayload(validSetupForm())
  assert.deepEqual(await setupApi.getStatus(), { status: 'pending' })
  assert.deepEqual(await setupApi.createSession('one-time-code'), { expires_at: '2026-08-24T12:30:00Z' })
  assert.equal(hasSetupSession(), true)
  assert.deepEqual(await setupApi.complete(payload), { status: 'finalizing' })
  assert.equal(hasSetupSession(), false)

  assert.deepEqual(seen.map(({ url }) => url), [
    '/api/v1/setup/status',
    '/api/v1/setup/session',
    '/api/v1/setup/complete',
  ])
  assert.deepEqual(JSON.parse(seen[1].options.body), { setup_code: 'one-time-code' })
  assert.equal(seen[2].options.headers.get('X-Probe-Setup-Session'), 'temporary-session')
  assert.equal(seen[2].options.headers.get('X-CSRF-Token'), 'temporary-csrf')
  assert.deepEqual(JSON.parse(seen[2].options.body), payload)
  for (const { options } of seen) {
    assert.equal(options.credentials, 'omit')
    assert.equal(options.mode, 'same-origin')
    assert.equal(options.cache, 'no-store')
    assert.equal(options.redirect, 'error')
  }
})

test('setup completion cannot run without its in-memory session', async () => {
  clearSetupSecrets()
  await assert.rejects(
    setupApi.complete(normalizeSetupPayload(validSetupForm())),
    (error) => error.code === 'setup_session_missing',
  )
})

test('setup payload normalization is exact and first release stays local-only with ACME', () => {
  const payload = normalizeSetupPayload(validSetupForm())
  assert.deepEqual(payload, {
    database: {
      mode: 'local',
      name: 'probe',
      username: 'probe_user',
      password: 'database-password',
      password_confirmation: 'database-password',
    },
    domains: {
      panel: 'panel.monitor.test',
      admin: 'admin.monitor.test',
      agent: 'api.monitor.test',
    },
    tls: { mode: 'acme', email: 'admin@example.com' },
    allowlist: ['203.0.113.25/32', '2001:db8:1234::/48'],
    administrator: {
      username: 'admin',
      password: 'administrator-password',
      password_confirmation: 'administrator-password',
    },
  })
  assert.equal(validateSetupStep(5, payload), '')
})

test('setup validation rejects unsafe domains, public allowlists and mismatched secrets', () => {
  const payload = normalizeSetupPayload(validSetupForm())

  assert.match(validateSetupStep(2, {
    ...payload,
    database: { ...payload.database, password_confirmation: 'different-password' },
  }), /数据库密码不一致/)
  assert.match(validateSetupStep(3, {
    ...payload,
    domains: { ...payload.domains, panel: 'https://panel.monitor.test' },
  }), /不含协议和路径/)
  assert.match(validateSetupStep(3, {
    ...payload,
    domains: { ...payload.domains, panel: 'panel.example.com' },
  }), /完整域名/)
  assert.match(validateSetupStep(3, {
    ...payload,
    domains: { panel: 'monitor.test', admin: 'admin.monitor.test', agent: 'api.monitor.test' },
  }), /不能互相包含/)
  assert.match(validateSetupStep(3, {
    ...payload,
    allowlist: ['0.0.0.0/0'],
  }), /禁止允许整个/)
  assert.match(validateSetupStep(4, {
    ...payload,
    administrator: { ...payload.administrator, password_confirmation: 'different-password' },
  }), /管理员密码不一致/)
})

test('setup status routing recognizes only explicit installation states', () => {
  assert.equal(setupStatusValue({ status: 'pending' }), 'pending')
  assert.equal(setupStatusValue({ state: 'FINALIZING' }), 'finalizing')
  for (const status of ['pending', 'configuring', 'finalizing', 'recovery_required']) {
    assert.equal(isSetupInstallStatus(status), true)
  }
  assert.equal(isSetupInstallStatus('installed'), false)
  assert.equal(isSetupInstallStatus('unknown'), false)
})

test('install route and setup namespace remain isolated from the normal management client', async () => {
  const [router, app, install, setupClient, normalClient] = await Promise.all([
    readFile(new URL('../src/router/index.js', import.meta.url), 'utf8'),
    readFile(new URL('../src/App.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/views/Install.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/api/setup.js', import.meta.url), 'utf8'),
    readFile(new URL('../src/api/client.js', import.meta.url), 'utf8'),
  ])

  assert.match(router, /path:\s*'\/install'/)
  assert.match(router, /name:\s*'Install'/)
  assert.match(router, /error\?\.status === 404[\s\S]+return 'installed'/)
  assert.match(router, /setupStatus === 'installed' && to\.name === 'Install'[\s\S]+name: 'Login'/)
  assert.match(app, /route\.name === 'Install'/)
  assert.match(setupClient, /X-Probe-Setup-Session/)
  assert.match(setupClient, /X-CSRF-Token/)
  assert.doesNotMatch(setupClient, /from ['"]\.\/client/)
  assert.doesNotMatch(normalClient, /api\/v1\/setup/)
  assert.match(install, /onUnmounted\(\(\) =>[\s\S]+clearSetupSecrets\(\)[\s\S]+clearFormSecrets\(\)/)
  assert.match(install, /admin_url[\s\S]+protocol !== 'https:'[\s\S]+window\.location\.replace\(target\)/)
  assert.doesNotMatch(install, /localStorage|sessionStorage|console\./)
})
