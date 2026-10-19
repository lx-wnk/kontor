import { afterEach, describe, expect, it } from 'vitest'
import { lockBodyScroll } from './scrollLock'

afterEach(() => {
  document.body.style.overflow = ''
})

describe('lockBodyScroll', () => {
  it('keeps the body locked until the last of several nested holders releases', () => {
    const outer = lockBodyScroll()
    const inner = lockBodyScroll()
    expect(document.body.style.overflow).toBe('hidden')

    inner()
    expect(document.body.style.overflow).toBe('hidden')
    inner()
    expect(document.body.style.overflow).toBe('hidden')

    outer()
    expect(document.body.style.overflow).toBe('')
  })
})
