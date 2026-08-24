import test from 'node:test'
import assert from 'node:assert/strict'
import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import vm from 'node:vm'
import { fileURLToPath } from 'node:url'
import {
  DEFAULT_THEME,
  LIGHT_THEME,
  THEME_STORAGE_KEY,
  appliedTheme,
  initializeTheme,
  readStoredTheme,
  setTheme,
  toggleTheme,
} from '../src/utils/theme.js'

const projectRoot = fileURLToPath(new URL('../', import.meta.url))
const sourceRoot = path.join(projectRoot, 'src')

function fakeRoot(initialTheme = DEFAULT_THEME) {
  const classes = new Set(initialTheme === DEFAULT_THEME ? ['dark'] : [])
  return {
    dataset: { theme: initialTheme },
    style: {},
    classList: {
      contains: (name) => classes.has(name),
      toggle(name, force) {
        if (force) classes.add(name)
        else classes.delete(name)
      },
    },
  }
}

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial))
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    value: (key) => values.get(key),
  }
}

async function collectVueFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const nested = await Promise.all(entries.map((entry) => {
    const location = path.join(directory, entry.name)
    if (entry.isDirectory()) return collectVueFiles(location)
    return entry.name.endsWith('.vue') ? [location] : []
  }))
  return nested.flat()
}

test('dark is the deterministic default and invalid persisted values are ignored', () => {
  assert.equal(readStoredTheme(null), DEFAULT_THEME)
  assert.equal(readStoredTheme(memoryStorage({ [THEME_STORAGE_KEY]: 'system' })), DEFAULT_THEME)

  const root = fakeRoot(LIGHT_THEME)
  assert.equal(initializeTheme({ root, storage: memoryStorage() }), DEFAULT_THEME)
  assert.equal(root.dataset.theme, DEFAULT_THEME)
  assert.equal(root.classList.contains('dark'), true)
  assert.equal(root.style.colorScheme, DEFAULT_THEME)
  assert.equal(appliedTheme(root), DEFAULT_THEME)
})

test('the manual choice is applied, toggled and persisted only in the admin namespace', () => {
  const storage = memoryStorage({ [THEME_STORAGE_KEY]: LIGHT_THEME })
  const root = fakeRoot()

  assert.equal(initializeTheme({ root, storage }), LIGHT_THEME)
  assert.equal(root.dataset.theme, LIGHT_THEME)
  assert.equal(root.classList.contains('dark'), false)
  assert.equal(root.style.colorScheme, LIGHT_THEME)

  assert.equal(setTheme(DEFAULT_THEME, { root, storage }), DEFAULT_THEME)
  assert.equal(storage.value(THEME_STORAGE_KEY), DEFAULT_THEME)
  assert.equal(root.classList.contains('dark'), true)

  assert.equal(toggleTheme({ root, storage }), LIGHT_THEME)
  assert.equal(storage.value(THEME_STORAGE_KEY), LIGHT_THEME)
  assert.equal(root.dataset.theme, LIGHT_THEME)
  assert.match(THEME_STORAGE_KEY, /^probe-admin-/)
})

test('blocked localStorage never prevents a usable in-memory theme', () => {
  const blockedStorage = {
    getItem() { throw new Error('storage blocked') },
    setItem() { throw new Error('storage blocked') },
  }
  const root = fakeRoot(LIGHT_THEME)

  assert.equal(readStoredTheme(blockedStorage), DEFAULT_THEME)
  assert.doesNotThrow(() => initializeTheme({ root, storage: blockedStorage }))
  assert.equal(root.dataset.theme, DEFAULT_THEME)
  assert.doesNotThrow(() => setTheme(LIGHT_THEME, { root, storage: blockedStorage }))
  assert.equal(root.dataset.theme, LIGHT_THEME)
  assert.equal(root.classList.contains('dark'), false)
})

test('the CSP-compatible head bootstrap applies storage before the application entry', async () => {
  const [index, bootstrap] = await Promise.all([
    readFile(path.join(projectRoot, 'index.html'), 'utf8'),
    readFile(path.join(projectRoot, 'public', 'theme-init.js'), 'utf8'),
  ])
  const lightRoot = fakeRoot()
  vm.runInNewContext(bootstrap, {
    window: { localStorage: memoryStorage({ [THEME_STORAGE_KEY]: LIGHT_THEME }) },
    document: { documentElement: lightRoot },
  })
  assert.equal(lightRoot.dataset.theme, LIGHT_THEME)
  assert.equal(lightRoot.classList.contains('dark'), false)
  assert.equal(lightRoot.style.colorScheme, LIGHT_THEME)

  const blockedRoot = fakeRoot(LIGHT_THEME)
  const blockedWindow = {}
  Object.defineProperty(blockedWindow, 'localStorage', { get() { throw new Error('blocked') } })
  assert.doesNotThrow(() => vm.runInNewContext(bootstrap, {
    window: blockedWindow,
    document: { documentElement: blockedRoot },
  }))
  assert.equal(blockedRoot.dataset.theme, DEFAULT_THEME)
  assert.equal(blockedRoot.classList.contains('dark'), true)

  assert.match(index, /<html[^>]+class="dark"[^>]+data-theme="dark"/)
  assert.match(index, /name="color-scheme" content="dark light"/)
  assert.ok(index.indexOf('/theme-init.js') < index.indexOf('/src/main.js'))
  assert.match(bootstrap, new RegExp(THEME_STORAGE_KEY))
})

test('setup, login and authenticated header expose the same accessible toggle', async () => {
  const [main, header, login, install, toggle] = await Promise.all([
    readFile(path.join(sourceRoot, 'main.js'), 'utf8'),
    readFile(path.join(sourceRoot, 'components', 'PanelHeader.vue'), 'utf8'),
    readFile(path.join(sourceRoot, 'views', 'Login.vue'), 'utf8'),
    readFile(path.join(sourceRoot, 'views', 'Install.vue'), 'utf8'),
    readFile(path.join(sourceRoot, 'components', 'ThemeToggle.vue'), 'utf8'),
  ])

  assert.match(main, /import \{ initializeTheme \} from '\.\/utils\/theme'/)
  assert.ok(main.indexOf('initializeTheme()') < main.indexOf('createApp(App)'))
  assert.match(header, /import ThemeToggle from '\.\/ThemeToggle\.vue'/)
  assert.match(header, /<ThemeToggle\s*\/>/)
  assert.match(login, /import ThemeToggle from '\.\.\/components\/ThemeToggle\.vue'/)
  assert.match(login, /<ThemeToggle class="absolute right-4 top-4"\s*\/>/)
  assert.match(install, /import ThemeToggle from '\.\.\/components\/ThemeToggle\.vue'/)
  assert.match(install, /<ThemeToggle class="absolute right-4 top-4"\s*\/>/)
  assert.match(toggle, /<button[\s\S]+type="button"/)
  assert.match(toggle, /:aria-label="actionLabel"/)
  assert.match(toggle, /:title="actionLabel"/)
  assert.match(toggle, /aria-hidden="true"/)
  assert.match(toggle, /toggleTheme\(\)/)
})

test('light palette covers every structural surface and foreground utility in all admin views', async () => {
  const files = await collectVueFiles(sourceRoot)
  const sources = await Promise.all(files.map((file) => readFile(file, 'utf8')))
  const style = await readFile(path.join(sourceRoot, 'style.css'), 'utf8')
  const utilityPattern = /(?:hover:)?(?:bg|border|divide|text|placeholder|shadow)-(?:dark|slate|emerald|rose|amber|sky|orange)-[A-Za-z0-9/.-]+/g
  const tokens = new Set(sources.flatMap((source) => [...source.matchAll(utilityPattern)].map((match) => match[0])))
  const mustOverride = [...tokens].filter((token) => (
    token.includes('-dark-')
    || token === 'bg-slate-700/50'
    || token === 'border-slate-600/50'
    || /^text-slate-[1-6]00$/.test(token)
    || token === 'placeholder-slate-600'
    || /^(?:hover:)?text-(?:emerald|rose|amber|sky|orange)-/.test(token)
    || token.startsWith('shadow-emerald-')
  ))

  assert.match(style, /html\[data-theme="light"\]/)
  assert.match(style, /\.card-glass/)
  assert.match(style, /:where\(a, button, input, select, textarea, summary, \[tabindex\]\):focus-visible/)
  assert.match(style, /outline: 2px solid var\(--theme-focus-ring\)/)
  assert.match(style, /:where\(input, textarea\)::placeholder/)
  assert.match(style, /input:-webkit-autofill/)
  assert.match(style, /\[class~="bg-emerald-600"\]\s*\{\s*background-color: #047857;/)
  assert.match(style, /\[class~="hover:bg-emerald-500"\]:hover\s*\{\s*background-color: #065f46;/)
  assert.match(style, /\[class~="text-emerald-400\/80"\]\s*\{\s*color: #047857;/)
  for (const token of mustOverride) {
    assert.match(style, new RegExp(`\\[class~=["']${token.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}["']\\]`), `missing light override for ${token}`)
  }
})
