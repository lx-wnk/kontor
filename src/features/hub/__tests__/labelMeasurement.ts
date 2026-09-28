import type { MockInstance } from 'vitest'
import { vi } from 'vitest'

// jsdom lays nothing out, so every getBoundingClientRect comes back 0×0 and no label would ever be
// culled. This stands in for a real font, and is deliberately wider per character than the
// `length * 6.3 + 24` estimate the hub used to place labels with: a fixture that clears under the
// estimate collides under the measurement.
const CHAR_PX = 12
const LABEL_H = 16

export function labelSize(text: string): { w: number, h: number } {
  return { w: text.length * CHAR_PX, h: LABEL_H }
}

// Only the elements HubOrbit measures get a size; everything else keeps jsdom's zero rect, which
// the camera's own pointer maths relies on. Keys in `blind` measure 0×0, the way a label with no
// layout box does; the set is read per call, so clearing it mid-test lets the next pass see a size.
export function stubLabelMeasurement(blind: ReadonlySet<string> = new Set()): MockInstance<() => DOMRect> {
  const spy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const key = this.dataset.labelKey
    const { w, h } = key === undefined || blind.has(key) ? { w: 0, h: 0 } : labelSize(key)
    return { x: 0, y: 0, top: 0, left: 0, right: w, bottom: h, width: w, height: h, toJSON: () => ({}) } as DOMRect
  })
  spy.mockClear()
  return spy
}
