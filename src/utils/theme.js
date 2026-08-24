export const DEFAULT_THEME = 'dark'
export const LIGHT_THEME = 'light'
export const THEME_STORAGE_KEY = 'probe-admin-theme'

function normalizeTheme(theme) {
  return theme === LIGHT_THEME ? LIGHT_THEME : DEFAULT_THEME
}

function defaultRoot() {
  try {
    return globalThis.document?.documentElement ?? null
  } catch {
    return null
  }
}

function defaultStorage() {
  try {
    return globalThis.window?.localStorage ?? globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function readStoredTheme(storage = defaultStorage()) {
  try {
    return normalizeTheme(storage?.getItem(THEME_STORAGE_KEY))
  } catch {
    return DEFAULT_THEME
  }
}

export function appliedTheme(root = defaultRoot()) {
  try {
    return normalizeTheme(root?.dataset?.theme)
  } catch {
    return DEFAULT_THEME
  }
}

export function applyTheme(theme, root = defaultRoot()) {
  const normalized = normalizeTheme(theme)
  if (!root) return normalized

  try {
    root.dataset.theme = normalized
    root.classList?.toggle('dark', normalized === DEFAULT_THEME)
    if (root.style) root.style.colorScheme = normalized
  } catch {
    // The UI still uses the dark CSS defaults when a restricted DOM rejects writes.
  }
  return normalized
}

export function initializeTheme({ root = defaultRoot(), storage = defaultStorage() } = {}) {
  return applyTheme(readStoredTheme(storage), root)
}

export function setTheme(theme, { root = defaultRoot(), storage = defaultStorage() } = {}) {
  const normalized = applyTheme(theme, root)
  try {
    storage?.setItem(THEME_STORAGE_KEY, normalized)
  } catch {
    // Persistence is optional; changing the active document theme must still work.
  }
  return normalized
}

export function toggleTheme({ root = defaultRoot(), storage = defaultStorage() } = {}) {
  const nextTheme = appliedTheme(root) === LIGHT_THEME ? DEFAULT_THEME : LIGHT_THEME
  return setTheme(nextTheme, { root, storage })
}
