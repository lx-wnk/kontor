import { afterEach, describe, expect, it } from 'vitest'
import { lockBodyScroll } from './scrollLock'

const releases: Array<() => void> = []

afterEach(() => {
  releases.splice(0).forEach(release => release())
})

describe('lockBodyScroll', () => {
  it('keeps the body locked until the last of several nested holders releases', () => {
    const outer = lockBodyScroll()
    const inner = lockBodyScroll()
    releases.push(outer, inner)
    expect(document.body.style.overflow).toBe('hidden')

    inner()
    expect(document.body.style.overflow).toBe('hidden')
    inner()
    expect(document.body.style.overflow).toBe('hidden')

    outer()
    expect(document.body.style.overflow).toBe('')
  })
})
