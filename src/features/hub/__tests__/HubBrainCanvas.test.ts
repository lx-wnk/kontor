import type { HubNote } from '../composables/useObsidianGraph'
import type { Leaf, Sector } from '../hubGeometry'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import HubBrainCanvas from '../components/HubBrainCanvas.vue'
import { DAY_MS } from '../hubGeometry'

interface Call { name: string, args: unknown[], state: Record<string | symbol, unknown> }

const FRAME_MS = 16
let calls: Call[]

// Stands in for a real font: wide enough that two labels a few pixels apart collide.
const MEASURED_PX_PER_CHAR = 7

// Records every method call with the style state at that moment; property writes are kept as plain values.
function recordingContext(): CanvasRenderingContext2D {
  const state: Record<string | symbol, unknown> = {}
  const record = (key: string | symbol) => (...args: unknown[]) => calls.push({ name: String(key), args, state: { ...state } })
  return new Proxy(state, {
    get: (_, key) => {
      if (key in state)
        return state[key]
      if (key === 'measureText') {
        return (text: string) => {
          record(key)(text)
          return { width: text.length * MEASURED_PX_PER_CHAR }
        }
      }
      return record(key)
    },
    set: (_, key, value) => {
      state[key] = value
      return true
    },
  }) as unknown as CanvasRenderingContext2D
}

const named = (name: string) => calls.filter(c => c.name === name)
const texts = () => named('fillText').map(c => c.args[0])

function note(index: number, title: string, ageDays = 30): HubNote {
  return { index, path: `n/${title}.md`, title, mtimeMs: Date.now() - ageDays * DAY_MS, kind: 'note', links: [], backlinks: [] }
}

const NOTES = [note(0, 'Alpha'), note(1, 'Beta'), note(2, 'Gamma')]

function mountBrain(props: Partial<InstanceType<typeof HubBrainCanvas>['$props']> = {}) {
  return mount(HubBrainCanvas, {
    props: {
      cam: { k: 1, tx: 0, ty: 0 },
      size: { width: 400, height: 300 },
      level: 0,
      points: [[50, 50], [200, 100], [300, 200]],
      colours: [0, 1, 2],
      notes: NOTES,
      links: [],
      hubNotes: new Set<number>(),
      selected: null,
      edges: [],
      sectors: [],
      leaves: [],
      noteLeaf: ['', '', ''],
      hoveredNote: null,
      ...props,
    },
  })
}

const CATEGORY_SECTORS: Sector[] = [
  { key: 'a', label: 'a', weight: 1, start: -90, end: 0 },
  { key: 'b', label: 'b', weight: 1, start: 0, end: 90 },
]
const LEAF_SECTORS: Leaf[] = [
  { key: 'a/x', label: 'x', weight: 1, parent: 'a', start: -90, end: -45 },
  { key: 'a/y', label: 'y', weight: 1, parent: 'a', start: -45, end: 0 },
  { key: 'b/z', label: 'z', weight: 1, parent: 'b', start: 0, end: 90 },
]

async function nextFrame() {
  await vi.advanceTimersByTimeAsync(FRAME_MS)
}

beforeEach(() => {
  calls = []
  vi.useFakeTimers()
  vi.spyOn(globalThis, 'requestAnimationFrame')
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(recordingContext as never)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('hubBrainCanvas', () => {
  it('draws one dot per note and no labels at the overview level', async () => {
    const w = mountBrain()
    await nextFrame()
    expect(named('arc')).toHaveLength(3)
    expect(named('fillText')).toHaveLength(0)
    expect(w.get('canvas').attributes('aria-hidden')).toBe('true')
  })

  it('labels only the hub notes at the topics level and the labels cullLabels keeps at the notes level', async () => {
    const w = mountBrain({ level: 1, points: [[100, 100], [102, 100], [300, 100]], hubNotes: new Set([0]) })
    await nextFrame()
    expect(texts()).toEqual(['Alpha'])

    calls = []
    await w.setProps({ level: 2 })
    await nextFrame()
    expect(texts()).toEqual(['Alpha', 'Gamma'])
  })

  it('at the topics level, a note title overlapping the project-name legend yields to it', async () => {
    mountBrain({
      level: 1,
      points: [[100, 100], [102, 100], [300, 100]],
      hubNotes: new Set([0, 2]),
      legendBoxes: [{ box: { x: 100, y: 90, w: 60, h: 20 } }],
    })
    await nextFrame()
    expect(texts()).toEqual(['Gamma'])
  })

  // The width used to be counted off the characters, the estimate that twice misplaced the agent
  // labels next door. Here it is the canvas' own measurement, and one per distinct text.
  it('culls on measured widths and measures each text once, however many frames it draws', async () => {
    // 30px apart: inside 'Alpha' at the measured width, clear of it at a width of zero.
    const w = mountBrain({ level: 2, points: [[100, 100], [130, 100], [400, 100]] })
    await nextFrame()
    expect(texts()).toEqual(['Alpha', 'Gamma'])
    expect(named('measureText').map(c => c.args[0])).toEqual(['Alpha', 'Beta', 'Gamma'])
    expect(named('measureText').every(c => c.state.font === '11px system-ui, sans-serif')).toBe(true)

    calls = []
    for (const tx of [5, 10, 15]) {
      await w.setProps({ cam: { k: 1, tx, ty: 0 } })
      await nextFrame()
    }
    expect(named('measureText')).toHaveLength(0)
    expect(new Set(texts())).toEqual(new Set(['Alpha', 'Gamma']))
  })

  it('skips notes outside the viewport', async () => {
    mountBrain({ level: 2, points: [[50, 50], [5000, 50], [50, -500]] })
    await nextFrame()
    expect(named('arc')).toHaveLength(1)
    expect(texts()).toEqual(['Alpha'])
  })

  it('rings a note touched today and the selected note from the topics level up', async () => {
    const notes = [note(0, 'Alpha', 0), note(1, 'Beta'), note(2, 'Gamma')]
    mountBrain({ level: 1, notes, selected: 2 })
    await nextFrame()
    expect(named('arc')).toHaveLength(5)
  })

  it('fills notes of one colour and kind in one path, hub notes above the rest', async () => {
    vi.spyOn(globalThis, 'getComputedStyle').mockReturnValue({ getPropertyValue: (name: string) => `tok(${name})` } as never)
    mountBrain({ colours: [0, 0, 1], hubNotes: new Set([2]) })
    await nextFrame()
    expect(named('arc')).toHaveLength(3)
    expect(named('fill').map(c => [c.state.fillStyle, c.state.globalAlpha])).toEqual([['tok(--sector-0)', 0.75], ['tok(--sector-1)', 1]])
  })

  it('strokes the selection ring in the foreground token', async () => {
    vi.spyOn(globalThis, 'getComputedStyle').mockReturnValue({ getPropertyValue: (name: string) => `tok(${name})` } as never)
    mountBrain({ selected: 1 })
    await nextFrame()
    expect(named('stroke').at(-1)!.state.strokeStyle).toBe('tok(--fg)')
  })

  it('resizes the backing store only when the stage size changes', async () => {
    const setWidth = vi.spyOn(HTMLCanvasElement.prototype, 'width', 'set')
    const w = mountBrain()
    await nextFrame()
    expect(setWidth).toHaveBeenCalledOnce()
    await w.setProps({ cam: { k: 1, tx: 5, ty: 0 } })
    await nextFrame()
    expect(setWidth).toHaveBeenCalledOnce()
    await w.setProps({ size: { width: 500, height: 300 } })
    await nextFrame()
    expect(setWidth).toHaveBeenCalledTimes(2)
  })

  it('coalesces camera changes into one redraw per frame', async () => {
    const w = mountBrain()
    await nextFrame()
    calls = []
    vi.mocked(requestAnimationFrame).mockClear()

    await w.setProps({ cam: { k: 1, tx: 5, ty: 0 } })
    await w.setProps({ cam: { k: 1, tx: 10, ty: 0 } })
    await w.setProps({ cam: { k: 1.2, tx: 10, ty: 0 } })
    expect(requestAnimationFrame).toHaveBeenCalledOnce()
    await nextFrame()
    expect(named('clearRect')).toHaveLength(1)
  })

  it('redraws when the theme class on the document changes', async () => {
    mountBrain()
    await nextFrame()
    calls = []
    document.documentElement.classList.add('dark')
    await nextTick()
    await nextFrame()
    document.documentElement.classList.remove('dark')
    expect(named('clearRect')).toHaveLength(1)
  })

  it('strokes read edges dashed and write edges dotted, faded by age, then resets the dash', async () => {
    mountBrain({ edges: [
      { from: [0, 0], to: 1, kind: 'read', alpha: 0.9 },
      { from: [0, 0], to: 2, kind: 'write', alpha: 0.5 },
    ] })
    await nextFrame()
    expect(named('setLineDash').map(c => c.args[0])).toEqual([[4, 3], [1, 3], []])
    const lines = named('lineTo')
    expect(lines.map(c => c.state.globalAlpha)).toEqual([0.9, 0.5])
    expect(lines.map(c => c.state.lineCap)).toEqual(['butt', 'round'])
  })

  it('touches no dash state when there are no edges', async () => {
    mountBrain()
    await nextFrame()
    expect(named('setLineDash')).toHaveLength(0)
  })

  it('fills note-kind dots, rings sessions-kind dots, and punches a hole in other kinds', async () => {
    vi.spyOn(globalThis, 'getComputedStyle').mockReturnValue({ getPropertyValue: (name: string) => `tok(${name})` } as never)
    const notes = [
      { ...note(0, 'Alpha'), kind: 'note' },
      { ...note(1, 'Beta'), kind: 'sessions' },
      { ...note(2, 'Gamma'), kind: 'task' },
    ]
    mountBrain({ notes, colours: [0, 1, 2] })
    await nextFrame()
    expect(named('fill').map(c => c.state.fillStyle)).toEqual(['tok(--sector-0)', 'tok(--sector-2)', 'tok(--app)'])
    expect(named('stroke').some(c => c.state.strokeStyle === 'tok(--sector-1)')).toBe(true)
  })

  it('level 0 bundles links per category pair and hides same-category links', async () => {
    vi.spyOn(globalThis, 'getComputedStyle').mockReturnValue({ getPropertyValue: (name: string) => `tok(${name})` } as never)
    mountBrain({
      sectors: CATEGORY_SECTORS,
      leaves: LEAF_SECTORS,
      noteLeaf: ['a/x', 'a/y', 'b/z'],
      links: [[0, 1], [0, 2], [1, 2]],
    })
    await nextFrame()
    const accentStrokes = named('stroke').filter(c => c.state.strokeStyle === 'tok(--accent)')
    expect(accentStrokes).toHaveLength(1)
    expect(accentStrokes[0].state.lineWidth).toBeCloseTo(1.3)
  })

  it('at level 2, a hovered note draws its links brighter than the rest', async () => {
    vi.spyOn(globalThis, 'getComputedStyle').mockReturnValue({ getPropertyValue: (name: string) => `tok(${name})` } as never)
    mountBrain({
      level: 2,
      sectors: CATEGORY_SECTORS,
      leaves: LEAF_SECTORS,
      noteLeaf: ['a/x', 'a/y', 'b/z'],
      links: [[0, 1], [0, 2], [1, 2]],
      hoveredNote: 0,
    })
    await nextFrame()
    const linkStrokes = named('stroke').slice(0, 3)
    expect(linkStrokes.map(c => c.state.globalAlpha)).toEqual([0.95, 0.95, 0.06])
    expect(Number(linkStrokes[0].state.lineWidth)).toBeGreaterThan(Number(linkStrokes[2].state.lineWidth))
  })
})
