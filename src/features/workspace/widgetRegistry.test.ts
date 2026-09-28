import { describe, expect, it } from 'vitest'
import { isWidgetId, WIDGET_IDS, WIDGET_SPECS, widgetIds, WIDGETS } from './index'

describe('widget registry', () => {
  // Both sides are read: a spec without a component, or a component without a
  // spec, is the drift this registry exists to rule out.
  it('has a component for every spec and a spec for every component', () => {
    expect(widgetIds().sort()).toEqual(Object.keys(WIDGET_SPECS).sort())
  })

  it('carries the cockpit panels, live work and today\'s cost', () => {
    for (const id of ['agents', 'pipeline', 'routines', 'memory', 'github', 'live-work', 'cost-today', 'kontor', 'hub'])
      expect(widgetIds()).toContain(id)
  })

  it('gives every widget a title and spans that fit twelve columns', () => {
    for (const id of widgetIds()) {
      const w = WIDGETS[id]
      expect(w.title.length).toBeGreaterThan(0)
      expect(w.minColSpan).toBeGreaterThanOrEqual(1)
      expect(w.minColSpan).toBeLessThanOrEqual(w.defaultColSpan)
      expect(w.defaultColSpan).toBeLessThanOrEqual(12)
      expect(w.minRowSpan).toBeGreaterThanOrEqual(1)
      expect(w.minRowSpan).toBeLessThanOrEqual(w.defaultRowSpan)
    }
  })
})

describe('widget ids', () => {
  it('has one spec and one widget per id, and nothing else', () => {
    expect(Object.keys(WIDGET_SPECS).sort()).toEqual([...WIDGET_IDS].sort())
    expect(widgetIds().sort()).toEqual([...WIDGET_IDS].sort())
  })

  it('recognises known ids and rejects module and unknown ids', () => {
    expect(isWidgetId('hub')).toBe(true)
    expect(isWidgetId('github__prs')).toBe(false)
    expect(isWidgetId('nope')).toBe(false)
  })

  it('loads widget components lazily', () => {
    for (const id of WIDGET_IDS)
      expect((WIDGETS[id].component as { __asyncLoader?: unknown }).__asyncLoader, id).toBeTypeOf('function')
  })
})
