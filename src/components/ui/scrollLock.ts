let holders = 0

/** Locks body scroll and returns an idempotent release; the body unlocks when the last holder releases. */
export function lockBodyScroll(): () => void {
  if (holders++ === 0) {
    const scrollbarWidth = window.innerWidth - document.documentElement.clientWidth
    document.body.style.overflow = 'hidden'
    if (scrollbarWidth > 0)
      document.body.style.paddingRight = `${scrollbarWidth}px`
  }
  let released = false
  return () => {
    if (released)
      return
    released = true
    if (--holders === 0) {
      document.body.style.overflow = ''
      document.body.style.paddingRight = ''
    }
  }
}
