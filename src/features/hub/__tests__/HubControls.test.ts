import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HubControls from '../components/HubControls.vue'

function mountControls(wide = false) {
  return mount(HubControls, { props: { level: 1, wide } })
}

describe('hubControls', () => {
  it('names each control with its key and emits its action', async () => {
    const w = mountControls()
    const expected: Array<[string, string, string]> = [
      ['Zoom in', 'Zoom in (+)', 'zoomIn'],
      ['Zoom out', 'Zoom out (−)', 'zoomOut'],
      ['Show all', 'Show all (0)', 'fit'],
      ['Widen', 'Widen (F)', 'wide'],
      ['List', 'List (L)', 'list'],
    ]
    for (const [label, title, event] of expected) {
      const button = w.get(`button[aria-label="${label}"]`)
      expect(button.attributes('title')).toBe(title)
      await button.trigger('click')
      expect(w.emitted(event)).toHaveLength(1)
    }
  })

  it('marks the widen toggle as pressed while the hub is wide', () => {
    expect(mountControls(false).get('button[aria-label="Widen"]').attributes('aria-pressed')).toBe('false')
    expect(mountControls(true).get('button[aria-label="Widen"]').attributes('aria-pressed')).toBe('true')
  })

  it('presses the current level and emits the picked one', async () => {
    const w = mountControls()
    const levels = ['Overview', 'Topics', 'Notes'].map(name => w.findAll('button').find(b => b.text() === name)!)
    expect(levels.map(b => b.attributes('aria-pressed'))).toEqual(['false', 'true', 'false'])
    await levels[2].trigger('click')
    await levels[0].trigger('click')
    expect(w.emitted('level')).toEqual([[2], [0]])
  })
})
