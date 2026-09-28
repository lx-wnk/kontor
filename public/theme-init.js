// Runs before first paint to prevent a flash of the wrong theme. It lives in a
// file rather than inline because the server sends `script-src 'self'`, which
// blocks an inline script outright — and a blocked theme bootstrap fails
// silently, leaving exactly the flash it exists to prevent.
;(function () {
  const stored = localStorage.getItem('agent-theme')
  const isDark = stored === 'dark'
    || (stored !== 'light' && !window.matchMedia('(prefers-color-scheme: light)').matches)
  if (isDark)
    document.documentElement.classList.add('dark')
})()
