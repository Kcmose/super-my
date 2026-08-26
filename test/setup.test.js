import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import {
  clearSetupSecrets,
  hasSetupSession,
  setupApi,
} from '../src/api/setup.js'
import {
  canonicalIPAddress,
  isSetupInstallStatus,
  normalizeSetupPayload,
  setupDefaultsValue,
  setupInstalledIPAccess,
  setupIPURLs,
  setupPrivateCAValue,
  setupStatusValue,
  validateSetupStep,
} from '../src/utils/setup.js'

const DEFAULTS = Object.freeze({
  profile: 'management',
  server_ip: '198.51.100.20',
  admin_url: 'https://198.51.100.20:18455',
})
const SESSION_TOKEN = 'a'.repeat(64)
const CSRF_TOKEN = 'b'.repeat(64)
const FUTURE_EXPIRY = '2099-08-24T12:30:00Z'

function sessionResponse(overrides = {}) {
  return {
    session_token: SESSION_TOKEN,
    csrf_token: CSRF_TOKEN,
    expires_at: FUTURE_EXPIRY,
    defaults: DEFAULTS,
    ...overrides,
  }
}

function validSetupForm() {
  return {
    database: {
      mode: 'external-is-ignored',
      name: ' probe ',
      username: ' probe_user ',
      password: 'database-password',
      password_confirmation: 'database-password',
    },
    network: { address: '198.51.100.20' },
    domains: {
      admin: ' ADMIN.MONITOR.TEST ',
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

test('dedicated setup client automatically creates an in-memory session with empty JSON', async (context) => {
  const originalFetch = globalThis.fetch
  const seen = []
  context.after(() => {
    globalThis.fetch = originalFetch
    clearSetupSecrets()
  })

  globalThis.fetch = async (url, options) => {
    seen.push({ url, options })
    if (url.endsWith('/status')) {
      return new Response(JSON.stringify({ status: 'pending', defaults: DEFAULTS }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url.endsWith('/session')) {
      return new Response(JSON.stringify(sessionResponse()), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response(JSON.stringify({ status: 'finalizing' }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    })
  }

  const payload = normalizeSetupPayload(validSetupForm())
  assert.deepEqual(await setupApi.getStatus(), { status: 'pending', defaults: DEFAULTS })
  assert.deepEqual(await setupApi.createSession(), { expires_at: FUTURE_EXPIRY, defaults: DEFAULTS })
  assert.equal(hasSetupSession(), true)
  assert.deepEqual(await setupApi.complete(payload), { status: 'finalizing' })
  assert.equal(hasSetupSession(), false)

  assert.deepEqual(seen.map(({ url }) => url), [
    '/api/v1/setup/status',
    '/api/v1/setup/session',
    '/api/v1/setup/complete',
  ])
  assert.equal(seen[1].options.body, '{}')
  assert.equal(seen[2].options.headers.get('X-Probe-Setup-Session'), SESSION_TOKEN)
  assert.equal(seen[2].options.headers.get('X-CSRF-Token'), CSRF_TOKEN)
  assert.deepEqual(JSON.parse(seen[2].options.body), payload)
  for (const { options } of seen) {
    assert.equal(options.credentials, 'omit')
    assert.equal(options.mode, 'same-origin')
    assert.equal(options.cache, 'no-store')
    assert.equal(options.redirect, 'error')
  }
})

test('automatic setup-session creation is single-flight within one page', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
    clearSetupSecrets()
  })

  let resolveFetch
  let requestCount = 0
  globalThis.fetch = () => {
    requestCount += 1
    return new Promise((resolve) => { resolveFetch = resolve })
  }

  const first = setupApi.createSession()
  const second = setupApi.createSession()
  assert.equal(first, second)
  assert.equal(requestCount, 1)
  resolveFetch(new Response(JSON.stringify(sessionResponse()), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  }))
  assert.deepEqual(await first, await second)
})

test('setup client refuses a non-management completion before making the request', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
    clearSetupSecrets()
  })

  let requestCount = 0
  globalThis.fetch = async () => {
    requestCount += 1
    return new Response(JSON.stringify(sessionResponse()), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }
  await setupApi.createSession()
  const payload = normalizeSetupPayload(validSetupForm())
  await assert.rejects(
    setupApi.complete({ ...payload, profile: 'full' }),
    (error) => error.code === 'setup_profile_forbidden',
  )
  await assert.rejects(
    setupApi.complete({ ...payload, domains: { ...payload.domains, agent: '' } }),
    (error) => error.code === 'setup_profile_forbidden',
  )
  assert.equal(requestCount, 1)
})

test('setup client rejects malformed, non-lowercase, or expired session material', async (context) => {
  const originalFetch = globalThis.fetch
  context.after(() => {
    globalThis.fetch = originalFetch
    clearSetupSecrets()
  })

  for (const response of [
    sessionResponse({ session_token: 'A'.repeat(64) }),
    sessionResponse({ csrf_token: 'short' }),
    sessionResponse({ expires_at: '2020-01-01T00:00:00Z' }),
    sessionResponse({ expires_at: 'not-a-date' }),
    sessionResponse({ defaults: { ...DEFAULTS, admin_url: 'https://198.51.100.20:18453' } }),
  ]) {
    globalThis.fetch = async () => new Response(JSON.stringify(response), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
    await assert.rejects(setupApi.createSession(), (error) => error.code === 'invalid_setup_session_response')
    assert.equal(hasSetupSession(), false)
  }
})

test('setup completion cannot run without its in-memory session', async () => {
  clearSetupSecrets()
  await assert.rejects(
    setupApi.complete(normalizeSetupPayload(validSetupForm())),
    (error) => error.code === 'setup_session_missing',
  )
})

test('domain setup payload normalization is exact and forces ACME with an empty network address', () => {
  const payload = normalizeSetupPayload(validSetupForm())
  assert.deepEqual(payload, {
    profile: 'management',
    database: {
      mode: 'local',
      name: 'probe',
      username: 'probe_user',
      password: 'database-password',
      password_confirmation: 'database-password',
    },
    network: { address: '' },
    domains: {
      admin: 'admin.monitor.test',
    },
    tls: { mode: 'acme', email: 'admin@example.com' },
    allowlist: ['203.0.113.25/32', '2001:db8:1234::/48'],
    administrator: {
      username: 'admin',
      password: 'administrator-password',
      password_confirmation: 'administrator-password',
    },
  })
  assert.equal(validateSetupStep(4, payload), '')
})

test('empty domains select private-CA IP mode and normalize an IPv6 NAT override', () => {
  const form = validSetupForm()
  form.network.address = ' 2001:0db8:0:0::20 '
  form.domains = { admin: ' ' }
  const payload = normalizeSetupPayload(form)

  assert.deepEqual(payload.network, { address: '2001:db8::20' })
  assert.deepEqual(payload.domains, { admin: '' })
  assert.deepEqual(payload.tls, { mode: 'private_ca', email: '' })
  assert.equal(validateSetupStep(4, payload), '')
  assert.deepEqual(setupIPURLs(payload.network.address), {
    admin_url: 'https://[2001:db8::20]:18455',
  })
})

test('management setup is fixed and rejects visitor or Agent ingress fields', () => {
  const form = validSetupForm()
  const domainPayload = normalizeSetupPayload(form)
  assert.equal(domainPayload.profile, 'management')
  assert.deepEqual(domainPayload.domains, {
    admin: 'admin.monitor.test',
  })
  assert.deepEqual(domainPayload.network, { address: '' })
  assert.deepEqual(domainPayload.tls, { mode: 'acme', email: 'admin@example.com' })
  assert.equal(validateSetupStep(4, domainPayload), '')

  form.domains.admin = ''
  form.network.address = '2001:0db8::20'
  const ipPayload = normalizeSetupPayload(form)
  assert.deepEqual(ipPayload.domains, { admin: '' })
  assert.deepEqual(ipPayload.network, { address: '2001:db8::20' })
  assert.deepEqual(setupIPURLs(ipPayload.network.address), {
    admin_url: 'https://[2001:db8::20]:18455',
  })
  assert.equal(validateSetupStep(4, ipPayload), '')

  assert.match(validateSetupStep(2, {
    ...domainPayload,
    domains: { ...domainPayload.domains, unsupported: 'api.monitor.test' },
  }), /包含不支持的入口字段/)
  assert.match(validateSetupStep(2, { ...domainPayload, profile: 'full' }), /必须固定使用 management/)
})

test('setup validation rejects invalid management ingress, unsafe allowlists, and mismatched secrets', () => {
  const payload = normalizeSetupPayload(validSetupForm())

  assert.match(validateSetupStep(1, {
    ...payload,
    database: { ...payload.database, password_confirmation: 'different-password' },
  }), /数据库密码不一致/)
  assert.match(validateSetupStep(2, {
    ...payload,
    domains: { admin: 'https://admin.monitor.test' },
  }), /不含协议和路径/)
  assert.match(validateSetupStep(2, {
    ...payload,
    allowlist: ['0.0.0.0/0'],
  }), /禁止允许整个/)
  assert.match(validateSetupStep(2, {
    ...payload,
    network: { address: '999.1.1.1' },
    domains: { admin: '' },
    tls: { mode: 'private_ca', email: '' },
  }), /规范的 IPv4 或 IPv6/)
  assert.match(validateSetupStep(3, {
    ...payload,
    administrator: { ...payload.administrator, password_confirmation: 'different-password' },
  }), /管理员密码不一致/)
})

test('server-provided default IP URLs are exact and bound to fixed ports', () => {
  assert.equal(canonicalIPAddress('2001:0DB8::20'), '2001:db8::20')
  assert.deepEqual(setupDefaultsValue({ defaults: DEFAULTS }), DEFAULTS)
  assert.equal(setupDefaultsValue({ defaults: { ...DEFAULTS, panel_url: 'https://198.51.100.20:18453' } }), null)
  assert.equal(setupDefaultsValue({ defaults: { ...DEFAULTS, agent_url: 'https://198.51.100.20:18454' } }), null)
  assert.equal(setupDefaultsValue({ defaults: { ...DEFAULTS, admin_url: 'https://user@198.51.100.20:18455' } }), null)
  assert.equal(setupDefaultsValue({ defaults: { ...DEFAULTS, profile: 'full' } }), null)
  assert.equal(setupDefaultsValue({ defaults: { ...DEFAULTS, profile: 'unknown' } }), null)
})

test('installed IP handoff accepts only fixed-port IP access and in-memory public CA metadata', () => {
  const pem = '-----BEGIN CERTIFICATE-----\nQUJD\n-----END CERTIFICATE-----\n'
  const sha256 = 'a'.repeat(64)
  assert.deepEqual(setupPrivateCAValue({ private_ca: { available: true, pem, sha256 } }), {
    available: true,
    pem,
    sha256,
  })
  assert.deepEqual(setupPrivateCAValue({ private_ca: { available: false } }), {
    available: false,
    pem: '',
    sha256: '',
  })
  assert.equal(setupPrivateCAValue({ status: 'installed' }), null)
  assert.equal(setupPrivateCAValue({ private_ca: { available: true, pem: 'PRIVATE KEY', sha256 } }).available, false)
  assert.equal(setupPrivateCAValue({ private_ca: { available: true, pem, sha256: 'A'.repeat(64) } }).available, false)

  assert.deepEqual(setupInstalledIPAccess({ admin_url: 'https://[2001:db8::20]:18455/login' }), {
    server_ip: '2001:db8::20',
    admin_url: 'https://[2001:db8::20]:18455',
    login_url: 'https://[2001:db8::20]:18455/login',
  })
  assert.equal(setupInstalledIPAccess({ admin_url: 'https://admin.monitor.test/login' }), null)
  assert.equal(setupInstalledIPAccess({ admin_url: 'https://192.0.2.10:18454/login' }), null)
  assert.equal(setupInstalledIPAccess({ admin_url: 'https://192.0.2.10:18455/not-login' }), null)
  assert.deepEqual(setupInstalledIPAccess({ admin_url: 'https://192.0.2.10:18455/login' }), {
    server_ip: '192.0.2.10',
    admin_url: 'https://192.0.2.10:18455',
    login_url: 'https://192.0.2.10:18455/login',
  })
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
  assert.match(router, /if \(to\.name === 'Install'\) return true[\s\S]+resolveSetupStatus\(\)/)
  assert.doesNotMatch(router, /setupStatus === 'installed' && to\.name === 'Install'/)
  assert.match(app, /route\.name === 'Install'/)
  assert.match(setupClient, /body:\s*'\{\}'/)
  assert.match(setupClient, /setupSessionPromise/)
  assert.match(setupClient, /X-Probe-Setup-Session/)
  assert.match(setupClient, /X-CSRF-Token/)
  assert.match(setupClient, /payload\?\.profile !== 'management'/)
  assert.doesNotMatch(setupClient, /setup_code|from ['"]\.\/client/)
  assert.doesNotMatch(normalClient, /api\/v1\/setup/)
  assert.match(install, /grid-cols-4/)
  assert.match(install, /serverStatus\.value === 'pending' \|\| serverStatus\.value === 'configuring'[\s\S]+establishSetupSession/)
  assert.match(install, /serverStatus\.value === 'finalizing'[\s\S]+scheduleStatusPoll/)
  assert.match(install, /error\?\.status === 404[\s\S]+showTerminalHandoffFallback\(\)/)
  assert.match(install, /onUnmounted\(\(\) =>[\s\S]+clearCurrentSession\(\)[\s\S]+clearFormSecrets\(\)/)
  assert.match(install, /admin_url[\s\S]+protocol !== 'https:'[\s\S]+window\.location\.replace\(target\)/)
  assert.match(install, /setupPrivateCAValue\(response\)[\s\S]+installedIPResult\.value/)
  assert.match(install, /new Blob\(\[privateCA\.pem\][\s\S]+URL\.createObjectURL[\s\S]+probe-panel-ca\.pem/)
  assert.match(install, /window\.location\.assign\(target\)/)
  assert.match(install, /scp root@SERVER:\/etc\/probe-panel\/tls\/private-ca\/ca\.pem/)
  assert.doesNotMatch(install, /panel-domain|agent-domain|setupProfile|isManagementProfile|18453|18454/)
  assert.doesNotMatch(install, /安装码|setupCode|localStorage|sessionStorage|console\./)
})
