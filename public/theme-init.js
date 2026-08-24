;(() => {
  const defaultTheme = 'dark'
  const storageKey = 'probe-admin-theme'
  let theme = defaultTheme

  try {
    if (window.localStorage.getItem(storageKey) === 'light') theme = 'light'
  } catch {
    // Storage can be blocked by browser privacy policy; dark remains the safe default.
  }

  const root = document.documentElement
  root.dataset.theme = theme
  root.classList.toggle('dark', theme === defaultTheme)
  root.style.colorScheme = theme
})()
