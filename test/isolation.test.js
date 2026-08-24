import test from 'node:test'
import assert from 'node:assert/strict'
import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const projectRoot = fileURLToPath(new URL('../', import.meta.url))
const sourceRoot = path.join(projectRoot, 'src')

async function collectFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const nested = await Promise.all(entries.map((entry) => {
    const location = path.join(directory, entry.name)
    return entry.isDirectory() ? collectFiles(location) : [location]
  }))
  return nested.flat()
}

test('admin project contains no public monitoring views', async () => {
  const viewEntries = await readdir(path.join(sourceRoot, 'views'), { withFileTypes: true })
  const rootViews = viewEntries.filter((entry) => entry.isFile()).map((entry) => entry.name).sort()
  const adminViews = (await readdir(path.join(sourceRoot, 'views', 'admin'))).sort()

  assert.deepEqual(rootViews, ['Install.vue', 'Login.vue'])
  assert.deepEqual(adminViews, ['AuditLogs.vue', 'NodeTokens.vue', 'ProbeTargets.vue', 'SystemStatus.vue', 'Users.vue'])

  const removedViews = ['Overview.vue', 'NodeDetail.vue', 'ProbeAnalysis.vue']
  for (const fileName of removedViews) assert.equal(rootViews.includes(fileName), false)
})

test('admin source has no public route, public audience copy, or cross-project import', async () => {
  const files = (await collectFiles(sourceRoot)).filter((file) => /\.(?:js|vue|css)$/.test(file))
  const sources = await Promise.all(files.map(async (file) => ({ file, text: await readFile(file, 'utf8') })))
  const disallowedAudienceLabel = String.fromCharCode(28216, 23458)

  for (const { file, text } of sources) {
    const setupOnlySource = (
      file.endsWith(`${path.sep}views${path.sep}Install.vue`)
      || file.endsWith(`${path.sep}utils${path.sep}setup.js`)
    )
    if (!setupOnlySource) {
      assert.equal(text.includes(disallowedAudienceLabel), false, `${file} contains public-audience copy`)
    }
    assert.doesNotMatch(text, /(?:from\s*|import\s*\()['"][^'"]*probe-web/i, `${file} imports probe-web`)
    assert.doesNotMatch(text, /['"]\/overview['"]/, `${file} exposes overview`)
    assert.doesNotMatch(text, /path:\s*['"]\/(?:probes|nodes\/[^'"]*)['"]/, `${file} exposes a public route`)
  }
})

test('package, lockfile and deployment target are independently named', async () => {
  const [manifestText, lockText, deploy] = await Promise.all([
    readFile(path.join(projectRoot, 'package.json'), 'utf8'),
    readFile(path.join(projectRoot, 'package-lock.json'), 'utf8'),
    readFile(path.join(projectRoot, 'deploy', 'static-site.yaml'), 'utf8'),
  ])
  const manifest = JSON.parse(manifestText)
  const lock = JSON.parse(lockText)

  assert.equal(manifest.name, 'probe-admin')
  assert.equal(lock.name, 'probe-admin')
  assert.equal(lock.packages[''].name, 'probe-admin')
  assert.match(deploy, /destination:\s*\/srv\/probe\/admin\b/)
  assert.doesNotMatch(deploy, /destination:\s*\/srv\/probe\/web\b/)
  assert.match(deploy, /audience:\s*administrators/)
  assert.match(deploy, /authentication:\s*admin-session/)
  assert.match(deploy, /fetch_credentials:\s*include/)
})

test('management API clients stay on the frozen same-origin namespaces', async () => {
  const [authSource, adminSource, panelSource, normalClientSource, setupSource] = await Promise.all([
    readFile(path.join(sourceRoot, 'api', 'auth.js'), 'utf8'),
    readFile(path.join(sourceRoot, 'api', 'admin.js'), 'utf8'),
    readFile(path.join(sourceRoot, 'api', 'panel.js'), 'utf8'),
    readFile(path.join(sourceRoot, 'api', 'client.js'), 'utf8'),
    readFile(path.join(sourceRoot, 'api', 'setup.js'), 'utf8'),
  ])

  assert.match(authSource, /\/api\/v1\/auth\/login/)
  assert.match(authSource, /\/api\/v1\/auth\/access/)
  assert.match(authSource, /\/api\/v1\/auth\/me/)
  assert.match(adminSource, /\/api\/v1\/admin\/nodes/)
  assert.match(adminSource, /\/api\/v1\/admin\/users/)
  assert.match(adminSource, /\/api\/v1\/admin\/audit-logs/)
  assert.match(adminSource, /\/api\/v1\/admin\/system\/status/)
  assert.match(panelSource, /\/api\/v1\/panel\/nodes/)
  assert.doesNotMatch(panelSource, /getNodeDetail|getNodeMetrics|getNodeDisks|getProbeTargets|getProbeResults/)
  for (const source of [authSource, adminSource, panelSource]) {
    assert.doesNotMatch(source, /https?:\/\//)
    assert.doesNotMatch(source, /probe-web/i)
  }
  assert.doesNotMatch(normalClientSource, /api\/v1\/setup/)
  assert.match(setupSource, /\/api\/v1\/setup\/status/)
  assert.match(setupSource, /\/api\/v1\/setup\/session/)
  assert.match(setupSource, /\/api\/v1\/setup\/complete/)
  assert.doesNotMatch(setupSource, /from\s+['"]\.\/client/)
})

test('development proxy exposes only management-panel namespaces', async () => {
  const viteConfig = await readFile(path.join(projectRoot, 'vite.config.js'), 'utf8')
  assert.match(viteConfig, /['"]\/api\/v1\/auth['"]:\s*\{/)
  assert.match(viteConfig, /['"]\/api\/v1\/admin['"]:\s*\{/)
  assert.match(viteConfig, /['"]\/api\/v1\/panel['"]:\s*\{/)
  assert.match(viteConfig, /['"]\/api\/v1\/setup['"]:\s*\{/)
  assert.doesNotMatch(viteConfig, /['"]\/api['"]:\s*\{/)
})
