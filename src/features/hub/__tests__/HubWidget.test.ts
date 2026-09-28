import type { GraphStatus, HubNote } from '../composables/useObsidianGraph'
import type { Agent } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, shallowRef } from 'vue'
import { NEEDS_YOU, OPEN_SETTINGS, OPEN_TASK, PENDING_PERMISSIONS } from '@/composables/openTask'
import { useSidebar } from '@/composables/useSidebar'
import { useViewState } from '@/composables/useViewState'
import { DEFAULT_LAYOUT, failedWidgets, useWorkspace } from '@/features/workspace'
import HubBrainCanvas from '../components/HubBrainCanvas.vue'
import { lastHubView } from '../composables/useHubCamera'
import { hubFocusRequest } from '../composables/useHubFocus'
import { fitScale } from '../hubCamera'
import { agentDotBox, agentLabelBox, boxesOverlap, sectorLabelBox } from '../hubCanvas'
import * as hubGeometry from '../hubGeometry'
import { AGENT_SECTOR_STAGGER_PX, AGENT_SECTOR_TIERS_MAX, AGENT_SPACING_PX, AGENT_STAGE_MARGIN_PX, agentRingPx, DAY_MS, LAUNCHER_PX, notePoint, planSectors } from '../hubGeometry'
import { labelSize, stubLabelMeasurement } from './labelMeasurement'

const NOTE_AGE_DAYS = 30
const graph = {
  status: ref<GraphStatus>('idle'),
  message: ref(''),
  notes: shallowRef<HubNote[]>([]),
  refresh: vi.fn(async () => {}),
  recentNotes: (count: number) => [...graph.notes.value].sort((a, b) => b.mtimeMs - a.mtimeMs).slice(0, count),
  noteByPath: (path: string) => graph.notes.value.find(n => n.path === path),
  openInObsidian: vi.fn(async () => null),
}
const openSettings = vi.fn()

vi.mock('../composables/useObsidianGraph', () => ({ useObsidianGraph: () => graph }))

function vaultNote(index: number, path: string): HubNote {
  return { index, path, title: path, mtimeMs: Date.now() - NOTE_AGE_DAYS * DAY_MS, links: [], backlinks: [] }
}

const agents = ref([
  { pid: 101, status: 'active', projectName: 'kontor-hub', working: true },
  { pid: 102, status: 'waiting', projectName: 'web-app', working: false, pendingPermissions: [{}] },
  { pid: 103, status: 'finished', projectName: 'web-app', working: false },
  { pid: 104, status: 'active', projectName: 'api-server', working: false, heldPermissions: [{}] },
  { pid: 105, status: 'idle', projectName: 'worker-queue', working: false },
] as unknown as Agent[])
const initialAgents = agents.value
const ask = vi.fn()
const overlayOpen = ref(false)

vi.mock('@/features/agents', () => ({
  useAgents: () => ({ agents, selectAgent: vi.fn() }),
}))
vi.mock('@/features/mission', () => ({
  NeedsYouQueue: {
    props: ['variant'],
    template: '<div data-testid="stub-queue">{{ variant }}</div>',
  },
  useKontorSession: () => ({ ask, overlayOpen }),
  useKontorAgent: () => computed(() => null),
}))

const { default: HubWidget } = await import('../components/HubWidget.vue')

class MockResizeObserver {
  static last: MockResizeObserver | null = null
  callback: ResizeObserverCallback
  observe = vi.fn()
  unobserve = vi.fn()
  disconnect = vi.fn()
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    MockResizeObserver.last = this
  }
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', MockResizeObserver)
  window.matchMedia = vi.fn((query: string) => ({
    matches: query.includes('reduce'),
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })) as unknown as typeof window.matchMedia
  ask.mockClear()
  overlayOpen.value = false
  useViewState().activeView.value = 'zentrale'
  // jsdom has no canvas; the brain layer only needs a context that accepts every call, plus the
  // metrics measureText owes its callers.
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(new Proxy({}, {
    get: (_, key) => key === 'measureText' ? (text: string) => ({ width: text.length * 7 }) : () => {},
    set: () => true,
  }) as never)
  stubLabelMeasurement()
})

afterEach(() => {
  graph.status.value = 'idle'
  graph.message.value = ''
  graph.notes.value = []
  graph.refresh.mockClear()
  openSettings.mockClear()
  agents.value = initialAgents
  useWorkspace().layout.value = DEFAULT_LAYOUT
  useWorkspace().wide.value = null
  hubFocusRequest.value = null
  lastHubView.value = null
  failedWidgets.clear()
})

// The hub tile as the default layout draws it on a 1512-wide screen; the roomier default stage
// hides every collision the real tile has.
const TILE = { width: 584, height: 734 }

async function mountHub(size = { width: 1090, height: 1130 }) {
  const w = mount(HubWidget, {
    attachTo: document.body,
    global: {
      provide: {
        [NEEDS_YOU]: computed(() => []),
        [PENDING_PERMISSIONS]: { items: ref([]), refresh: vi.fn() },
        [OPEN_TASK]: vi.fn(),
        [OPEN_SETTINGS]: openSettings,
      },
    },
  })
  // vueuse's post-flush observers bind to the stage only after the next tick.
  await flushPromises()
  const observer = MockResizeObserver.last!
  observer.callback([{ contentRect: size } as ResizeObserverEntry], observer as unknown as ResizeObserver)
  await flushPromises()
  return w
}

type Hub = Awaited<ReturnType<typeof mountHub>>

function agentButton(w: Hub, pid: number) {
  return w.get(`[data-testid="hub-agent-${pid}"]`)
}

function label(w: Hub, pid: number) {
  return agentButton(w, pid).get('[data-testid="hub-label"]')
}

// A culled label is `invisible` (visibility: hidden), which keeps the box HubOrbit measures.
function labelHidden(w: Hub, pid: number): boolean {
  return label(w, pid).classes().includes('invisible')
}

function translateOf(el: { attributes: (name: string) => string | undefined }): { sx: number, sy: number } {
  const [, x, y] = /translate\(([-\d.]+)px, ([-\d.]+)px/.exec(el.attributes('style')!)!
  return { sx: Number(x), sy: Number(y) }
}

function screenOf(w: Hub, pid: number): { sx: number, sy: number } {
  return translateOf(agentButton(w, pid))
}

// Drags the map by an exact offset; the camera pans by the pointer delta, so the dots move with it.
async function dragBy(w: Hub, dx: number, dy: number) {
  const stage = w.get('[data-testid="hub-stage"]').element
  for (const [type, x, y] of [['pointerdown', 0, 0], ['pointermove', dx, dy], ['pointerup', dx, dy]] as const)
    stage.dispatchEvent(new PointerEvent(type, { bubbles: true, pointerId: 1, clientX: x, clientY: y }))
  await flushPromises()
}

// The direction the hub placed this label in, read back from the offset it rendered.
function labelDirectionOf(w: Hub, pid: number): [number, number] {
  const [, dx, dy] = /translate\(-50%, -50%\) translate\(([-\d.]+)px, ([-\d.]+)px\)/.exec(label(w, pid).attributes('style')!)!
  const d = Math.hypot(Number(dx), Number(dy))
  return [Number(dx) / d, Number(dy) / d]
}

// Every sector name the hub actually draws, with the box it occupies.
function drawnSectorNames(w: Hub) {
  return w.findAll('[data-testid^="hub-sector-"]')
    .filter(s => !s.classes().includes('invisible'))
    .map((s) => {
      const { sx, sy } = translateOf(s)
      return sectorLabelBox(sx, sy, labelSize(s.attributes('data-label-key')!))
    })
}

// Six note folders; seven agents crowd the third, the other five sit one per folder.
const VAULT_PIDS = Array.from({ length: 12 }, (_, i) => 400 + i)

function vaultFolders(): HubNote[] {
  return Array.from({ length: 6 }, (_, folder) => Array.from({ length: 20 }, (_, i) => vaultNote(folder * 20 + i, `folder${folder}/n${i}.md`))).flat()
}

function vaultAgents(): Agent[] {
  return VAULT_PIDS.map((pid, i) => ({
    pid,
    status: i % 3 === 0 ? 'active' : 'idle',
    projectName: i < 7 ? 'folder2' : `folder${i - 7 + (i - 7 >= 2 ? 1 : 0)}`,
    working: i % 3 === 0,
  })) as unknown as Agent[]
}

// Five agents in one note folder: three radial tiers in use, the middle one exactly on the sector's
// mid angle — where its name is drawn.
const TIERED_PIDS = Array.from({ length: 5 }, (_, i) => 700 + i)

function tieredAgents(): Agent[] {
  return TIERED_PIDS.map(pid => ({ pid, status: 'idle', projectName: 'folder2', working: false })) as unknown as Agent[]
}

// The core sits at the world origin, so its rendered point is the centre every radius is read from.
function distanceFromCore(w: Hub, pid: number): number {
  const core = translateOf(w.get('[data-testid="hub-core"]'))
  const { sx, sy } = screenOf(w, pid)
  return Math.hypot(sx - core.sx, sy - core.sy)
}

function camera(w: Hub): number[] {
  return /translate\(([-\d.e]+),([-\d.e]+)\) scale\(([-\d.e]+)\)/.exec(w.get('svg g').attributes('transform')!)!.slice(1).map(Number)
}

function scale(w: Awaited<ReturnType<typeof mountHub>>): number {
  return Number(/scale\(([-\d.]+)\)/.exec(w.get('svg g').attributes('transform')!)![1])
}

async function press(w: Awaited<ReturnType<typeof mountHub>>, key: string) {
  await w.get('[data-testid="hub-stage"]').trigger('keydown', { key })
}

// A real key lands on whatever holds focus and bubbles from there.
async function pressFocused(key: string): Promise<KeyboardEvent> {
  const e = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
  document.activeElement!.dispatchEvent(e)
  await flushPromises()
  return e
}

const LIST = '[role="dialog"][aria-label="Zentrale as a list"]'

describe('hubWidget', () => {
  it('draws the stage at the overview level with one button per live agent and the docked queue', async () => {
    const w = await mountHub()
    expect(w.get('[data-testid="hub-stage"]').attributes('data-level')).toBe('0')
    expect(w.find('[data-testid="hub-agent-101"]').exists()).toBe(true)
    expect(w.find('[data-testid="hub-agent-102"]').exists()).toBe(true)
    expect(w.find('[data-testid="hub-agent-103"]').exists()).toBe(false)
    expect(w.get('[data-testid="hub-agent-102"]').attributes('aria-label')).toContain('needs you')
    expect(w.get('[data-testid="hub-core"]').text()).toContain('2 running · 2 need you')
    expect(w.get('[data-testid="stub-queue"]').text()).toBe('docked')
    w.unmount()
  })

  it('places a needs-you agent closer to the core than a working one', async () => {
    const w = await mountHub()
    const dist = (pid: number) => distanceFromCore(w, pid)
    expect(dist(102)).toBeCloseTo(88)
    expect(dist(101)).toBeCloseTo(116)
    w.unmount()
  })

  it('widens the agent ring with the agent count so labels get room', async () => {
    agents.value = Array.from({ length: 12 }, (_, i) => ({ pid: 200 + i, status: 'idle', projectName: `project-${i}`, working: false })) as unknown as Agent[]
    const w = await mountHub()
    expect(distanceFromCore(w, 200)).toBeCloseTo(12 * AGENT_SPACING_PX / (2 * Math.PI))
    w.unmount()
  })

  it('caps the agent ring by the stage so a crowd stays inside it', async () => {
    agents.value = Array.from({ length: 40 }, (_, i) => ({ pid: 200 + i, status: 'idle', projectName: `project-${i}`, working: false })) as unknown as Agent[]
    const w = await mountHub()
    expect(distanceFromCore(w, 200)).toBeCloseTo(1090 / 2 - AGENT_STAGE_MARGIN_PX)
    w.unmount()
  })

  it('docks the launchers when the ring the sector names push out leaves the stage, and keeps the ring when it fits', async () => {
    const launcherX = (w: Awaited<ReturnType<typeof mountHub>>) => Number(/translate\(([-\d.]+)px/.exec(w.get('[data-testid^="hub-launcher-"]').attributes('style')!)![1])
    const roomy = await mountHub()
    expect(launcherX(roomy)).not.toBe(30)
    roomy.unmount()

    agents.value = Array.from({ length: 40 }, (_, i) => ({ pid: 200 + i, status: 'idle', projectName: `project-${i}`, working: false })) as unknown as Agent[]
    const crowded = await mountHub()
    expect(launcherX(crowded)).toBe(30)
    crowded.unmount()
  })

  it('moves the agents and the launcher ring with the map when it zooms', async () => {
    const w = await mountHub()
    const launcherFromCore = () => {
      const core = translateOf(w.get('[data-testid="hub-core"]'))
      const { sx, sy } = translateOf(w.get('[data-testid^="hub-launcher-"]'))
      return Math.hypot(sx - core.sx, sy - core.sy)
    }
    const agentBefore = distanceFromCore(w, 101)
    const launcherBefore = launcherFromCore()
    await press(w, '-')
    expect(distanceFromCore(w, 101)).toBeCloseTo(agentBefore / 1.4)
    expect(launcherFromCore()).toBeCloseTo(launcherBefore / 1.4)
    w.unmount()
  })

  it('draws no sector names without notes, where every sector is one agent\'s project', async () => {
    const w = await mountHub()
    expect(w.findAll('[data-testid^="hub-sector-"]')).toHaveLength(0)
    w.unmount()
  })

  // An agent on a project that is no vault folder inserts `__other__`, which sorts first.
  it('keeps every sector its colour when a catch-all sector appears before it', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'misc/one.md'), vaultNote(1, 'private/two.md'), vaultNote(2, 'work/three.md')]
    const colours = (w: Hub) => w.findAll('svg')[0].findAll('path').map(p => p.attributes('style'))

    agents.value = [{ pid: 200, status: 'idle', projectName: 'misc', working: false }] as unknown as Agent[]
    const vaultOnly = await mountHub(TILE)
    const before = colours(vaultOnly)
    expect(before).toHaveLength(3)
    expect(new Set(before).size, 'the fixture discriminates: not every sector on one slot').toBeGreaterThan(1)
    vaultOnly.unmount()

    agents.value = [...agents.value, { pid: 201, status: 'idle', projectName: 'kontor', working: false }] as unknown as Agent[]
    const withOther = await mountHub(TILE)
    expect(colours(withOther)).toHaveLength(4)
    expect(colours(withOther).slice(1)).toEqual(before)
    withOther.unmount()
  })

  it('keeps a minimum height while tiles stack in one column, and fills its tile from md up', async () => {
    const w = await mountHub()
    expect(w.get('[data-testid="hub"]').classes()).toEqual(expect.arrayContaining(['min-h-[34rem]', 'md:min-h-0']))
    w.unmount()
  })

  it('rings a held permission as needing the operator, but not an agent that merely has its turn', async () => {
    const w = await mountHub()
    const dist = (pid: number) => distanceFromCore(w, pid)
    const dot = (pid: number) => w.get(`[data-testid="hub-agent-${pid}"] span`).classes()

    expect(w.get('[data-testid="hub-agent-104"]').attributes('aria-label')).toBe('Api Server, Active, needs you')
    expect(dot(104)).toEqual(expect.arrayContaining(['outline-warning', 'motion-safe:animate-pulse']))
    expect(dist(104)).toBeCloseTo(88)

    expect(w.get('[data-testid="hub-agent-105"]').attributes('aria-label')).toBe('Worker Queue, Idle')
    expect(dot(105)).not.toContain('outline-warning')
    expect(dist(105)).toBeCloseTo(116)
    w.unmount()
  })

  it('culls overlapping labels in a crowded, floor-narrow sector but never a dot, and always keeps a needs-operator label', async () => {
    graph.status.value = 'ready'
    // Six well-weighted note folders push the agents' unmatched "Other" sector down to the 24° floor,
    // the same shape the real vault produces (Ruling R23's regression).
    graph.notes.value = Array.from({ length: 6 }, (_, folder) => Array.from({ length: 20 }, (_, i) => vaultNote(folder * 20 + i, `folder${folder}/n${i}.md`))).flat()
    agents.value = [
      { pid: 301, status: 'idle', projectName: 'crowded-project', working: false },
      { pid: 302, status: 'active', projectName: 'crowded-project', working: true },
      { pid: 303, status: 'waiting', projectName: 'crowded-project', working: false, pendingPermissions: [{}] },
      { pid: 304, status: 'idle', projectName: 'crowded-project', working: false },
      { pid: 305, status: 'idle', projectName: 'crowded-project', working: false },
      { pid: 306, status: 'active', projectName: 'crowded-project', working: true },
      { pid: 307, status: 'idle', projectName: 'crowded-project', working: false },
      { pid: 308, status: 'idle', projectName: 'crowded-project', working: false },
    ] as unknown as Agent[]
    const w = await mountHub()

    const pids = [301, 302, 303, 304, 305, 306, 307, 308]
    for (const pid of pids) expect(() => w.get(`[data-testid="hub-agent-${pid}"] span`)).not.toThrow()

    expect(labelHidden(w, 303)).toBe(false)
    expect(pids.some(pid => labelHidden(w, pid))).toBe(true)
    w.unmount()
  })

  // Defect 1 (found by the coordinator's real-vault measurement): a label's box was taken from the
  // project name alone, while the rendered label also shows the status word. Four agents share one
  // project, four fillers fill out the circle. The test proves its own fixture: at these positions, in the directions the
  // hub placed them, the bare-name boxes all clear each other while the rendered ones do not, so the
  // cull can only come from measuring what is really rendered.
  it('culls a label whose bare name would clear its neighbour but whose name+status does not (defect 1)', async () => {
    agents.value = [
      { pid: 300, status: 'idle', projectName: 'target-proj', working: false },
      { pid: 301, status: 'active', projectName: 'target-proj', working: true },
      { pid: 302, status: 'waiting', projectName: 'target-proj', working: false, pendingPermissions: [{}] },
      { pid: 303, status: 'idle', projectName: 'target-proj', working: false },
      { pid: 310, status: 'idle', projectName: 'filler-a', working: false },
      { pid: 311, status: 'idle', projectName: 'filler-b', working: false },
      { pid: 312, status: 'idle', projectName: 'filler-c', working: false },
      { pid: 313, status: 'idle', projectName: 'filler-d', working: false },
    ] as unknown as Agent[]
    const w = await mountHub()

    const shared = [300, 301, 302, 303]
    const boxesOf = (text?: string) => shared.map((pid) => {
      const key = text ?? label(w, pid).attributes('data-label-key')!
      return agentLabelBox({ index: pid, ...screenOf(w, pid), text: key, priority: 0 }, labelSize(key), labelDirectionOf(w, pid))
    })
    const anyOverlap = (boxes: ReturnType<typeof boxesOf>) => boxes.some((a, i) => boxes.some((b, j) => i !== j && boxesOverlap(a, b)))
    expect(anyOverlap(boxesOf('Target Proj')), 'bare-name boxes clear each other at these positions').toBe(false)
    expect(anyOverlap(boxesOf()), 'name+status boxes collide at the same positions').toBe(true)

    expect(shared.filter(pid => labelHidden(w, pid)).length).toBeGreaterThan(0)
    expect(labelHidden(w, 302)).toBe(false) // needs-operator always keeps its label
    w.unmount()
  })

  // Defects 2 (a culled agent's dot hides under a neighbour's `bg-card/85` label) and 3 (a label
  // covers a sector name, the map's legend) together: 25 same-project agents crammed into a
  // 24°-floor sector (5 heavy note folders + a nearly-empty 6th), the shape that produced both
  // "label-vs-dot" and "label-vs-sector" collisions in the real vault. Every shown label's real box
  // (built from its actual rendered text, so the defect-1 fix is exercised too) is checked against
  // every OTHER agent's real dot and every rendered, currently-visible sector name's real box.
  it('never lets a shown label cover another agent\'s dot or a sector name (defects 2 and 3)', async () => {
    graph.status.value = 'ready'
    graph.notes.value = Array.from({ length: 6 }, (_, folder) => Array.from({ length: folder === 2 ? 1 : 20 }, (_, i) => vaultNote(folder * 20 + i, `folder${folder}/n${i}.md`))).flat()
    agents.value = Array.from({ length: 25 }, (_, i) => ({
      pid: 500 + i,
      status: i % 3 === 0 ? 'active' : 'idle',
      projectName: 'folder2',
      working: i % 3 === 0,
    })) as unknown as Agent[]
    const w = await mountHub()

    const pids = Array.from({ length: 25 }, (_, i) => 500 + i)
    const dots = pids.map(pid => ({ pid, ...screenOf(w, pid) }))
    const shown = pids.filter(pid => !labelHidden(w, pid))
    expect(shown.length).toBeGreaterThan(0)
    expect(shown.length).toBeLessThan(pids.length) // the crowd really is being culled, not just rendered whole

    // Both boxes are rebuilt from the key the DOM carries, so they are the ones the culler used.
    const sectorBoxes = drawnSectorNames(w)
    expect(sectorBoxes.length).toBeGreaterThan(0)

    for (const pid of shown) {
      const key = label(w, pid).attributes('data-label-key')!
      const box = agentLabelBox({ index: pid, ...screenOf(w, pid), text: key, priority: 0 }, labelSize(key), labelDirectionOf(w, pid))
      for (const other of dots) {
        if (other.pid === pid)
          continue
        expect(boxesOverlap(box, agentDotBox(other.sx, other.sy))).toBe(false)
      }
      for (const sectorBox of sectorBoxes)
        expect(boxesOverlap(box, sectorBox)).toBe(false)
    }
    w.unmount()
  })

  // A sector's agents are staggered outward across three tiers, and with five of them the middle
  // one lands exactly on the sector's mid angle — where its name is drawn. The legend used to clear
  // the base ring only, so that dot sat on the name's first letters.
  it('never lets an agent on the outermost tier cover a sector name', async () => {
    graph.status.value = 'ready'
    graph.notes.value = vaultFolders()
    const pids = TIERED_PIDS
    agents.value = tieredAgents()
    const w = await mountHub(TILE)

    const base = agentRingPx(pids.length, TILE.width)
    expect(Math.max(...pids.map(pid => distanceFromCore(w, pid))), 'the fixture reaches the outermost tier')
      .toBeCloseTo(base + (AGENT_SECTOR_TIERS_MAX - 1) * AGENT_SECTOR_STAGGER_PX)
    const crowded = w.findAll('[data-testid^="hub-sector-"]').find(s => s.attributes('data-label-key')!.startsWith('folder2'))!
    expect(crowded.classes(), 'the crowded sector still shows its name').not.toContain('invisible')

    const sectorBoxes = drawnSectorNames(w)
    expect(sectorBoxes.length).toBeGreaterThan(0)
    for (const pid of pids) {
      const { sx, sy } = screenOf(w, pid)
      for (const sectorBox of sectorBoxes)
        expect(boxesOverlap(agentDotBox(sx, sy), sectorBox), `agent ${pid}`).toBe(false)
    }
    w.unmount()
  })

  // The real vault's shape: six note folders, twelve agents, seven of them in one project. Before
  // labels hung inward, the southern two thirds of the ring lost every label to the sector names.
  it('keeps a label on most of a vault-shaped crowd', async () => {
    graph.status.value = 'ready'
    graph.notes.value = vaultFolders()
    agents.value = vaultAgents()
    const w = await mountHub(TILE)

    const shown = VAULT_PIDS.filter(pid => !labelHidden(w, pid))
    expect(shown.length, `labels kept of ${VAULT_PIDS.length}`).toBeGreaterThanOrEqual(6)
    w.unmount()
  })

  it('never lets a launcher cover a sector name, on the ring or docked', async () => {
    graph.status.value = 'ready'
    graph.notes.value = vaultFolders()
    agents.value = vaultAgents()
    const w = await mountHub(TILE)

    for (let presses = 0; presses < 4; presses++) {
      if (presses > 0)
        await press(w, '+')
      const sectorBoxes = drawnSectorNames(w)
      expect(sectorBoxes.length, `names drawn at zoom ${presses}`).toBeGreaterThan(0)
      for (const launcher of w.findAll('[data-testid^="hub-launcher-"]')) {
        const { sx, sy } = translateOf(launcher)
        const box = { x: sx - LAUNCHER_PX / 2, y: sy - LAUNCHER_PX / 2, w: LAUNCHER_PX, h: LAUNCHER_PX }
        for (const sectorBox of sectorBoxes)
          expect(boxesOverlap(box, sectorBox), `${launcher.attributes('data-testid')} at zoom ${presses}`).toBe(false)
      }
    }
    w.unmount()
  })

  // The docked rail is fixed to the screen while the map pans under it, so a dot that ends up
  // beneath it can be neither hovered nor clicked.
  it('leaves an agent the docked launcher rail covers undrawn', async () => {
    graph.status.value = 'ready'
    graph.notes.value = vaultFolders()
    agents.value = tieredAgents()
    const w = await mountHub(TILE)

    const rail = translateOf(w.get('[data-testid^="hub-launcher-"]'))
    expect(rail.sx, 'the rail is docked').toBe(30)
    const target = TIERED_PIDS[0]
    const from = screenOf(w, target)
    await dragBy(w, rail.sx - from.sx, rail.sy - from.sy)
    expect(screenOf(w, target).sy, 'the dot sits on the first launcher').toBeCloseTo(rail.sy)

    const railBoxes = w.findAll('[data-testid^="hub-launcher-"]').map((l) => {
      const { sx, sy } = translateOf(l)
      return { x: sx - LAUNCHER_PX / 2, y: sy - LAUNCHER_PX / 2, w: LAUNCHER_PX, h: LAUNCHER_PX }
    })
    const undrawn = TIERED_PIDS.filter((pid) => {
      const { sx, sy } = screenOf(w, pid)
      const covered = railBoxes.some(b => boxesOverlap(agentDotBox(sx, sy), b))
      expect(agentButton(w, pid).classes().includes('invisible'), `agent ${pid}`).toBe(covered)
      return covered
    })
    expect(undrawn).toContain(target)
    expect(undrawn.length, 'the rail does not swallow the whole crowd').toBeLessThan(TIERED_PIDS.length)
    w.unmount()
  })

  it('opens the Kontor session when the core is pressed', async () => {
    const w = await mountHub()
    await w.get('[data-testid="hub-core"]').trigger('click')
    expect(ask).toHaveBeenCalledOnce()
    w.unmount()
  })

  it('zooms with + and returns to the overview with 0', async () => {
    const w = await mountHub()
    await press(w, '+')
    expect(scale(w)).toBeCloseTo(1.4)
    await press(w, '0')
    expect(scale(w)).toBeCloseTo(1)
    w.unmount()
  })

  it('ignores keys typed into an input inside the stage and keys held with a modifier', async () => {
    const w = await mountHub()
    const input = document.createElement('input')
    w.get('[data-testid="hub-stage"]').element.appendChild(input)
    input.dispatchEvent(new KeyboardEvent('keydown', { key: '+', bubbles: true }))
    await press(w, '-')
    await w.get('[data-testid="hub-stage"]').trigger('keydown', { key: '+', ctrlKey: true })
    expect(scale(w)).toBeCloseTo(1 / 1.4)
    w.unmount()
  })

  it('toggles the hub wide with F', async () => {
    const w = await mountHub()
    await press(w, 'F')
    expect(useWorkspace().wide.value).toBe('hub')
    expect(w.get('button[aria-label="Widen"]').attributes('aria-pressed')).toBe('true')
    await press(w, 'f')
    expect(useWorkspace().wide.value).toBeNull()
    w.unmount()
  })

  it('offers every other view and a new page as launchers, and fires the slot a digit names', async () => {
    const w = await mountHub()
    const ids = w.findAll('[data-testid^="hub-launcher-"]').map(b => b.attributes('data-testid'))
    expect(ids).toEqual(['dashboard', 'pipeline', 'schedules', 'workflows', 'cost', 'eval', 'new-page'].map(id => `hub-launcher-${id}`))
    await press(w, '2')
    expect(useViewState().activeView.value).toBe('pipeline')
    w.unmount()
  })

  it('asks the sidebar for a new page from the new-page launcher', async () => {
    const w = await mountHub()
    const before = useSidebar().newPageRequests.value
    await w.get('[data-testid="hub-launcher-new-page"]').trigger('click')
    expect(useSidebar().newPageRequests.value).toBe(before + 1)
    w.unmount()
  })

  it('closes the focused list on Escape before it fits', async () => {
    const w = await mountHub()
    await press(w, '+')
    await press(w, 'L')
    expect(w.get(LIST).element.contains(document.activeElement)).toBe(true)
    await pressFocused('Escape')
    expect(w.find(LIST).exists()).toBe(false)
    expect(scale(w)).toBeCloseTo(1.4)
    await pressFocused('Escape')
    expect(scale(w)).toBeCloseTo(1)
    w.unmount()
  })

  it('flies to an agent picked in the orbit and opens its card; Escape from the focused list closes the card and flies back, then closes the list, then fits', async () => {
    const w = await mountHub()
    const stage = w.get('[data-testid="hub-stage"]').element
    await press(w, '+')
    await w.get('[data-testid="hub-agent-101"]').trigger('click')
    expect(scale(w)).toBeCloseTo(3)
    expect(w.get('[role="dialog"][aria-label="Kontor Hub"]').text()).toContain('Working')
    await press(w, 'L')
    expect(document.activeElement).toBe(w.get(`${LIST} button`).element)
    await pressFocused('Escape')
    expect(w.find('[aria-label="Kontor Hub"]').exists()).toBe(false)
    expect(w.find(LIST).exists()).toBe(true)
    await pressFocused('Escape')
    expect(w.find(LIST).exists()).toBe(false)
    expect(document.activeElement).toBe(stage)
    expect(scale(w)).toBeCloseTo(1.4)
    await pressFocused('Escape')
    expect(scale(w)).toBeCloseTo(1)
    w.unmount()
  })

  it('flies back to the camera the first card opened from when the last card closes', async () => {
    const w = await mountHub()
    await press(w, '+')
    await dragBy(w, 40, -30)
    const before = camera(w)
    await w.get('[data-testid="hub-agent-101"]').trigger('click')
    expect(scale(w)).toBeCloseTo(3)
    await press(w, 'L')
    await w.findAll('[data-testid="hub-list-agent"]').find(r => r.attributes('aria-label') === 'Web App, Quiet')!.trigger('click')
    expect(w.find('[role="dialog"][aria-label="Web App"]').exists()).toBe(true)

    await w.get('[role="dialog"][aria-label="Web App"] button[aria-label="Close card"]').trigger('click')

    expect(w.find('[role="dialog"]').exists()).toBe(false)
    camera(w).forEach((v, i) => expect(v).toBeCloseTo(before[i]))
    w.unmount()
  })

  it('comes back to the camera from before the card when the hub is left with a card open', async () => {
    const w = await mountHub()
    await press(w, '+')
    await dragBy(w, 40, -30)
    const before = camera(w)
    await w.get('[data-testid="hub-agent-101"]').trigger('click')
    w.unmount()

    const again = await mountHub()

    camera(again).forEach((v, i) => expect(v).toBeCloseTo(before[i]))
    again.unmount()
  })

  it('closes the card on Escape pressed inside it, hands focus back to the stage and flies back', async () => {
    const w = await mountHub()
    await w.get('[data-testid="hub-agent-101"]').trigger('click')
    const open = w.findAll('[aria-label="Kontor Hub"] button').find(b => b.text() === 'Open session')!
    ;(open.element as HTMLElement).focus()
    await pressFocused('Escape')
    expect(w.find('[aria-label="Kontor Hub"]').exists()).toBe(false)
    expect(document.activeElement).toBe(w.get('[data-testid="hub-stage"]').element)
    expect(scale(w)).toBeCloseTo(1)
    w.unmount()
  })

  it('closes the list on an agent picked in it, flies there and opens its card', async () => {
    const w = await mountHub()
    await press(w, 'L')
    const row = w.findAll('[data-testid="hub-list-agent"]').find(r => r.attributes('aria-label') === 'Web App, Quiet')!
    await row.trigger('click')
    expect(w.find('[aria-label="Zentrale as a list"]').exists()).toBe(false)
    expect(w.find('[role="dialog"][aria-label="Web App"]').exists()).toBe(true)
    expect(scale(w)).toBeCloseTo(3)
    w.unmount()
  })

  it('focuses a card opened from the list', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md')]
    const w = await mountHub()
    await press(w, 'L')
    await w.findAll('[data-testid="hub-list-agent"]').find(r => r.attributes('aria-label') === 'Web App, Quiet')!.trigger('click')
    await flushPromises()
    expect(w.get('[role="dialog"][aria-label="Web App"]').element.contains(document.activeElement)).toBe(true)

    await press(w, 'L')
    await w.get('[data-testid="hub-list-note"]').trigger('click')
    await flushPromises()
    expect(w.find('[role="dialog"][aria-label="Web App"]').exists()).toBe(false)
    expect(w.get('[role="dialog"][aria-label="alpha/one.md"]').element.contains(document.activeElement)).toBe(true)
    w.unmount()
  })

  it('closes the card of an agent that finished, hands focus back to the stage and keeps it closed', async () => {
    const w = await mountHub()
    await w.get('[data-testid="hub-agent-101"]').trigger('click')
    ;(w.get('[aria-label="Kontor Hub"] button').element as HTMLElement).focus()

    agents.value = initialAgents.filter(a => a.pid !== 101)
    await flushPromises()
    expect(w.find('[aria-label="Kontor Hub"]').exists()).toBe(false)
    expect(document.activeElement).toBe(w.get('[data-testid="hub-stage"]').element)

    agents.value = initialAgents
    await flushPromises()
    expect(w.find('[aria-label="Kontor Hub"]').exists()).toBe(false)
    w.unmount()
  })

  it('leaves focus outside the hub alone when the open card closes on its own', async () => {
    const w = await mountHub()
    await w.get('[data-testid="hub-agent-101"]').trigger('click')
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    outside.focus()

    agents.value = initialAgents.filter(a => a.pid !== 101)
    await flushPromises()
    expect(w.find('[aria-label="Kontor Hub"]').exists()).toBe(false)
    expect(document.activeElement).toBe(outside)

    outside.remove()
    w.unmount()
  })

  it('closes the card of a note gone after a refetch and hands focus back to the stage', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    await press(w, 'L')
    await w.findAll('[data-testid="hub-list-note"]')[1].trigger('click')
    ;(w.get('[role="dialog"][aria-label="beta/two.md"] button').element as HTMLElement).focus()

    graph.notes.value = [vaultNote(0, 'alpha/one.md')]
    await flushPromises()
    expect(w.find('[role="dialog"][aria-label="beta/two.md"]').exists()).toBe(false)
    expect(document.activeElement).toBe(w.get('[data-testid="hub-stage"]').element)

    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    await flushPromises()
    expect(w.find('[role="dialog"][aria-label="beta/two.md"]').exists()).toBe(false)
    w.unmount()
  })

  it('does not recompute the sector plan when an SSE tick only changes an agent status', async () => {
    const w = await mountHub()
    const spy = vi.spyOn(hubGeometry, 'planSectors')
    agents.value = agents.value.map(a => a.pid === 101 ? { ...a, status: 'idle', working: false } : a)
    await flushPromises()
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
    w.unmount()
  })

  it('keeps the notes and an open note card when a refetch fails', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    await press(w, 'L')
    await w.findAll('[data-testid="hub-list-note"]')[0].trigger('click')
    expect(w.find('[role="dialog"][aria-label="alpha/one.md"]').exists()).toBe(true)

    graph.status.value = 'failed'
    graph.message.value = 'network error'
    await flushPromises()

    expect(w.find('[role="dialog"][aria-label="alpha/one.md"]').exists()).toBe(true)
    w.unmount()
  })

  it('flies to the point picked on the minimap', async () => {
    const w = await mountHub()
    const map = w.get('svg[aria-label="Overview map"]')
    map.element.getBoundingClientRect = () => ({ left: 0, top: 0, width: 108, height: 108 }) as DOMRect
    await map.trigger('click', { clientX: 81, clientY: 54 })
    expect(scale(w)).toBeCloseTo(2)
    expect(w.get('svg g').attributes('transform')).toBe('translate(5,565) scale(2)')
    w.unmount()
  })

  it('leaves Escape from the focused list to the open Kontor overlay above it', async () => {
    const w = await mountHub()
    await press(w, '+')
    await press(w, 'L')
    overlayOpen.value = true
    const escape = await pressFocused('Escape')
    expect(escape.defaultPrevented).toBe(false)
    expect(w.find(LIST).exists()).toBe(true)
    overlayOpen.value = false
    await pressFocused('Escape')
    expect(w.find(LIST).exists()).toBe(false)
    expect(scale(w)).toBeCloseTo(1.4)
    await pressFocused('Escape')
    expect(scale(w)).toBeCloseTo(1)
    w.unmount()
  })

  it('flies to the picked level', async () => {
    const w = await mountHub()
    await w.findAll('button').find(b => b.text() === 'Notes')!.trigger('click')
    expect(scale(w)).toBeCloseTo(5.5)
    expect(w.get('[data-testid="hub-stage"]').attributes('data-level')).toBe('2')
    w.unmount()
  })

  it('opens the page holding the Kontor tile before asking, from a page without one', async () => {
    useWorkspace().layout.value = { version: 1, pages: [...DEFAULT_LAYOUT.pages, { id: 'p-a', title: 'Morning', tiles: [] }] }
    useViewState().activeView.value = 'page:p-a'
    const w = await mountHub()
    await w.get('[data-testid="hub-core"]').trigger('click')
    expect(useViewState().activeView.value).toBe('zentrale')
    expect(ask).toHaveBeenCalledOnce()
    w.unmount()
  })

  // The chunk 404s when the server is rebuilt under an open tab: PageLoadError takes the tile's place
  // and nothing is left to receive a prompt, so both controls that send one say why instead.
  it('blocks the core and Ask Kontor with a reason when the Kontor chunk failed to load', async () => {
    failedWidgets.add('kontor')
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md')]
    const w = await mountHub()
    const core = w.get('[data-testid="hub-core"]')
    expect(core.attributes('title')).toBe('Kontor could not load — reload the app')
    expect(core.attributes('aria-disabled')).toBe('true')
    await core.trigger('click')

    await press(w, 'L')
    await w.get('[data-testid="hub-list-note"]').trigger('click')
    const askButton = w.findAll('button').find(b => b.text() === 'Ask Kontor about this')!
    expect(askButton.attributes('disabled')).toBeDefined()
    expect(askButton.attributes('title')).toBe('Kontor could not load — reload the app')
    await askButton.trigger('click')

    expect(ask).not.toHaveBeenCalled()
    w.unmount()
  })

  // One condition, two wordings: each control names the action it would have taken.
  it('does nothing on the core when no page holds the Kontor tile, and each control says so in its own words', async () => {
    useWorkspace().layout.value = { version: 1, pages: [{ id: 'zentrale', title: 'Zentrale', tiles: [] }] }
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md')]
    const w = await mountHub()
    const core = w.get('[data-testid="hub-core"]')
    expect(core.attributes('title')).toBe('Add the Kontor tile to a page to open it here')
    await core.trigger('click')

    await press(w, 'L')
    await w.get('[data-testid="hub-list-note"]').trigger('click')
    const askButton = w.findAll('button').find(b => b.text() === 'Ask Kontor about this')!
    expect(askButton.attributes('disabled')).toBeDefined()
    expect(askButton.attributes('title')).toBe('Add the Kontor tile to a page to ask Kontor here')
    await askButton.trigger('click')

    expect(ask).not.toHaveBeenCalled()
    w.unmount()
  })

  it('refreshes the vault graph on mount and when the window regains focus', async () => {
    const w = await mountHub()
    expect(graph.refresh).toHaveBeenCalledOnce()
    window.dispatchEvent(new Event('focus'))
    expect(graph.refresh).toHaveBeenCalledTimes(2)
    w.unmount()
  })

  it('names a sector per top-level vault folder once the graph is ready', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    const names = w.findAll('[data-testid^="hub-sector-"]').map(b => b.text()).join(' ')
    expect(names).toContain('alpha')
    expect(names).toContain('beta')
    expect(w.find('canvas[aria-hidden="true"]').exists()).toBe(true)
    w.unmount()
  })

  it('keeps the sectors and the notes on screen while the graph refetches', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    const sectorNames = () => w.findAll('[data-testid^="hub-sector-"]').map(b => b.text())
    const namesBefore = sectorNames()
    const pointsBefore = w.getComponent(HubBrainCanvas).props('points')
    expect(pointsBefore).toHaveLength(2)

    graph.status.value = 'loading'
    await flushPromises()
    expect(sectorNames()).toEqual(namesBefore)
    expect(w.getComponent(HubBrainCanvas).props('points')).toEqual(pointsBefore)
    w.unmount()
  })

  it('flies to a note tapped at the overview level', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    const { sectors, sectorOfNote } = planSectors(['alpha/one.md', 'beta/two.md'], ['kontor-hub', 'web-app', 'api-server', 'worker-queue'])
    const [x, y] = notePoint('alpha/one.md', sectors.find(s => s.key === sectorOfNote.get('alpha/one.md'))!, NOTE_AGE_DAYS)
    const stage = w.get('[data-testid="hub-stage"]').element
    for (const type of ['pointerdown', 'pointerup'])
      stage.dispatchEvent(new PointerEvent(type, { bubbles: true, pointerId: 1, clientX: 545 + x, clientY: 565 + y }))
    await flushPromises()
    expect(scale(w)).toBeCloseTo(2.6)
    w.unmount()
  })

  it('offers to connect Obsidian while the vault is unconfigured', async () => {
    graph.status.value = 'unconfigured'
    const w = await mountHub()
    const notice = w.get('[data-testid="hub-graph-notice"]')
    expect(notice.text()).toContain('Connect Obsidian to see your notes here.')
    await notice.get('button').trigger('click')
    expect(openSettings).toHaveBeenCalledOnce()
    w.unmount()
  })

  it('explains a denied or failed vault read, and says nothing while loading', async () => {
    graph.status.value = 'denied'
    graph.message.value = 'memory.read denied'
    const w = await mountHub()
    const notice = w.get('[data-testid="hub-graph-notice"]')
    expect(notice.text()).toBe('Memory reads are not granted, so your notes stay hidden. memory.read denied')
    expect(notice.attributes('title')).toBe('memory.read denied')
    expect(w.get('[data-testid="hub-graph-notice-detail"]').text()).toBe('memory.read denied')

    graph.status.value = 'failed'
    await flushPromises()
    expect(w.get('[data-testid="hub-graph-notice"]').text()).toBe('Your notes could not be loaded; retrying when you come back to this window.')
    expect(w.find('[data-testid="hub-graph-notice-detail"]').exists()).toBe(false)

    graph.status.value = 'loading'
    await flushPromises()
    expect(w.find('[data-testid="hub-graph-notice"]').exists()).toBe(false)
    w.unmount()
  })

  it('lists the recently touched notes by sector, and a row closes the list, flies to the note and opens its card', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    await press(w, 'L')
    const rows = w.findAll('[data-testid="hub-list-note"]')
    expect(rows.map(r => r.attributes('aria-label'))).toEqual([expect.stringMatching(/^alpha\/one\.md, alpha, /), expect.stringMatching(/^beta\/two\.md, beta, /)])
    await rows[1].trigger('click')
    expect(w.find(LIST).exists()).toBe(false)
    expect(scale(w)).toBeCloseTo(5)
    expect(w.get('[role="dialog"][aria-label="beta/two.md"]').text()).toContain('beta')
    expect(w.getComponent(HubBrainCanvas).props('selected')).toBe(1)
    w.unmount()
  })

  it('flies from a link chip to the linked note at least at rel 3 and opens its card; Escape closes the card first and flies back to where the first card opened', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [{ ...vaultNote(0, 'alpha/one.md'), links: [1] }, { ...vaultNote(1, 'beta/two.md'), backlinks: [0] }]
    const w = await mountHub()
    await press(w, 'L')
    await w.findAll('[data-testid="hub-list-note"]')[0].trigger('click')
    await w.get('[data-testid="hub-note-link"]').trigger('click')
    expect(scale(w)).toBeCloseTo(5)
    expect(w.find('[role="dialog"][aria-label="beta/two.md"]').exists()).toBe(true)
    expect(w.find('[role="dialog"][aria-label="alpha/one.md"]').exists()).toBe(false)
    await pressFocused('Escape')
    expect(w.find('[role="dialog"][aria-label="beta/two.md"]').exists()).toBe(false)
    expect(w.getComponent(HubBrainCanvas).props('selected')).toBeNull()
    expect(scale(w)).toBeCloseTo(1)
    w.unmount()
  })

  it('keeps the selected note by path when a refetch shifts the indices', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    await press(w, 'L')
    await w.findAll('[data-testid="hub-list-note"]')[1].trigger('click')
    graph.notes.value = [vaultNote(0, 'alpha/new.md'), vaultNote(1, 'alpha/one.md'), vaultNote(2, 'beta/two.md')]
    await flushPromises()
    expect(w.getComponent(HubBrainCanvas).props('selected')).toBe(2)
    expect(w.find('[role="dialog"][aria-label="beta/two.md"]').exists()).toBe(true)
    w.unmount()
  })

  it('asks Kontor about the open note as a wiki link', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md')]
    const w = await mountHub()
    await press(w, 'L')
    await w.get('[data-testid="hub-list-note"]').trigger('click')
    await w.findAll('button').find(b => b.text() === 'Ask Kontor about this')!.trigger('click')
    expect(ask).toHaveBeenCalledWith('[[alpha/one]] ')
    w.unmount()
  })

  it('flies to a note focus request at rel 5, opens its card and clears the request', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    hubFocusRequest.value = { kind: 'note', path: 'beta/two.md' }
    await flushPromises()
    expect(scale(w)).toBeCloseTo(5)
    expect(w.get('[role="dialog"][aria-label="beta/two.md"]').text()).toContain('beta')
    expect(hubFocusRequest.value).toBeNull()
    w.unmount()
  })

  it('flies to an agent focus request at rel 3, opens its card and clears the request', async () => {
    const w = await mountHub()
    hubFocusRequest.value = { kind: 'agent', pid: 102 }
    await flushPromises()
    expect(scale(w)).toBeCloseTo(3)
    expect(w.get('[role="dialog"][aria-label="Web App"]')).toBeTruthy()
    expect(hubFocusRequest.value).toBeNull()
    w.unmount()
  })

  it('clears an unresolved focus request without flying or opening a card', async () => {
    const w = await mountHub()
    hubFocusRequest.value = { kind: 'agent', pid: 9999 }
    await flushPromises()
    expect(scale(w)).toBeCloseTo(1)
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(hubFocusRequest.value).toBeNull()
    w.unmount()
  })

  // A revoked memory.read keeps `notes` — so a failed refetch never blanks the brain — while the map
  // drops them, so a request made while the vault was readable resolves to a note the map has not got.
  it('drops a focus request for a note the denied vault no longer shows', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md'), vaultNote(1, 'beta/two.md')]
    const w = await mountHub()
    graph.status.value = 'denied'
    graph.message.value = 'memory.read denied'
    await flushPromises()

    hubFocusRequest.value = { kind: 'note', path: 'beta/two.md' }
    await flushPromises()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(hubFocusRequest.value).toBeNull()
    expect(scale(w)).toBeCloseTo(1)
    expect(w.get('[data-testid="hub-graph-notice"]').text()).toContain('Memory reads are not granted')
    await press(w, '+')
    expect(scale(w), 'the hub still answers').toBeCloseTo(1.4)
    w.unmount()
  })

  it('consumes a focus request already pending when the widget mounts', async () => {
    graph.status.value = 'ready'
    graph.notes.value = [vaultNote(0, 'alpha/one.md')]
    hubFocusRequest.value = { kind: 'note', path: 'alpha/one.md' }
    const w = await mountHub()
    expect(w.get('[role="dialog"][aria-label="alpha/one.md"]')).toBeTruthy()
    expect(scale(w) / fitScale(1090, 1130)).toBeCloseTo(5)
    expect(hubFocusRequest.value).toBeNull()
    w.unmount()
  })
})
