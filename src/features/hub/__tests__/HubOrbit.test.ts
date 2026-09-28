import type { HubLevel } from '../hubCamera'
import type { LabelSize } from '../hubCanvas'
import type { Agent } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'
import HubOrbit from '../components/HubOrbit.vue'
import { agentLabelKey, agentLabelOffset, inwardUnit, sectorLabelKey } from '../hubCanvas'
import { buildSectors, sectorLabelRadius } from '../hubGeometry'
import { labelSize, stubLabelMeasurement } from './labelMeasurement'

const sectors = buildSectors([{ key: 'kontor-hub', label: 'kontor-hub', weight: 1 }, { key: 'web-app', label: 'web-app', weight: 1 }])

function agent(pid: number, projectName: string): Agent {
  return { pid, projectName, status: 'active', working: false } as Agent
}

const ORBIT_AGENTS = [
  { agent: agent(1, 'kontor-hub'), x: 82, y: 0, state: 'working' as const, needsOperator: false },
  { agent: agent(2, 'web-app'), x: -60, y: 0, state: 'waiting' as const, needsOperator: true },
]

interface OrbitOptions {
  showSectorNames?: boolean
  agentRingPx?: number
  sectorNameRadius?: number
  coreDisabled?: boolean
  labelledAgents?: ReadonlySet<number>
  namedSectors?: ReadonlySet<string>
  drawnAgents?: ReadonlySet<number>
  labelDirections?: ReadonlyMap<number, readonly [number, number]>
}

function mountOrbit(level: HubLevel, { showSectorNames = true, agentRingPx = 116, sectorNameRadius = sectorLabelRadius(1, agentRingPx), coreDisabled = false, labelledAgents, namedSectors, drawnAgents, labelDirections }: OrbitOptions = {}) {
  return mount(HubOrbit, {
    props: {
      cam: { k: 1, tx: 500, ty: 500 },
      agentRingPx,
      sectorNameRadius,
      showSectorNames,
      sectors,
      agents: ORBIT_AGENTS,
      level,
      running: 3,
      waiting: 1,
      needsYou: 2,
      coreTitle: 'Open Kontor (idle)',
      coreDisabled,
      labelledAgents,
      namedSectors,
      drawnAgents,
      labelDirections,
    },
  })
}

let measureSpy: ReturnType<typeof stubLabelMeasurement>

beforeEach(() => {
  measureSpy = stubLabelMeasurement()
})

describe('hubOrbit', () => {
  it('draws the core, one button per agent and one per sector at the overview level', async () => {
    const w = mountOrbit(0)
    const core = w.get('[data-testid="hub-core"]')
    expect(core.text()).toContain('3 running · 1 need you')
    expect(w.get('[data-testid="hub-core-needs-you"]').text()).toContain('2')
    expect(core.attributes('style')).toContain('translate(500px, 500px)')
    expect(w.get('[data-testid="hub-agent-1"]').attributes('aria-label')).toBe('Kontor Hub, Working')
    expect(w.get('[data-testid="hub-agent-2"]').attributes('aria-label')).toBe('Web App, Quiet, needs you')
    expect(w.get('[data-testid="hub-agent-1"]').attributes('style')).toContain('translate(582px, 500px)')
    expect(w.findAll('[data-testid^="hub-sector-"]')).toHaveLength(2)

    await core.trigger('click')
    expect(w.emitted('core')).toHaveLength(1)
    w.unmount()
  })

  it('marks the core aria-disabled when no page holds the Kontor tile', () => {
    const w = mountOrbit(0, { coreDisabled: true })
    expect(w.get('[data-testid="hub-core"]').attributes('aria-disabled')).toBe('true')
    w.unmount()

    const enabled = mountOrbit(0, { coreDisabled: false })
    expect(enabled.get('[data-testid="hub-core"]').attributes('aria-disabled')).toBe('false')
    enabled.unmount()
  })

  it('emits the clicked agent and sector', async () => {
    const w = mountOrbit(0)
    await w.get('[data-testid="hub-agent-2"]').trigger('click')
    await w.get('[data-testid="hub-sector-1"]').trigger('click')
    expect(w.emitted('agent')?.[0]).toEqual([agent(2, 'web-app')])
    expect(w.emitted('sector')?.[0]).toEqual([sectors[1]])
    w.unmount()
  })

  it('bounds an agent label to a truncated name and keeps the full name in its title', () => {
    const w = mountOrbit(0)
    const button = w.get('[data-testid="hub-agent-1"]')
    expect(button.attributes('title')).toBe('Kontor Hub')
    expect(button.get('[data-testid="hub-label-name"]').classes()).toEqual(expect.arrayContaining(['truncate', 'max-w-[14ch]']))
    expect(button.find('[data-testid="hub-label-title"]').exists()).toBe(false)
    w.unmount()
  })

  it('shows the session title on hover and names it in the title and the accessible name', async () => {
    const w = mountOrbit(0)
    await w.setProps({ agents: [{ ...ORBIT_AGENTS[0], agent: { ...agent(1, 'kontor-hub'), sessionTitle: 'Fix the zoom' } }] })
    const button = w.get('[data-testid="hub-agent-1"]')
    expect(button.attributes('title')).toBe('Kontor Hub: Fix the zoom')
    expect(button.attributes('aria-label')).toBe('Kontor Hub, Fix the zoom, Working')
    const title = button.get('[data-testid="hub-label-title"]')
    expect(title.text()).toBe('Fix the zoom')
    expect(title.classes()).toEqual(expect.arrayContaining(['hidden', 'group-hover:inline']))
    w.unmount()
  })

  it('leaves out ring labels inside the agent ring and ones that would crowd the last drawn label', () => {
    const w = mountOrbit(0)
    expect(w.findAll('[data-testid="hub-ring-label"]').map(l => l.text())).toEqual(['week', 'month', 'year'])
    w.unmount()
  })

  it('draws no sector names in agent-only mode, where each agent label already names its sector', () => {
    const w = mountOrbit(0, { showSectorNames: false })
    expect(w.findAll('[data-testid^="hub-sector-"]')).toHaveLength(0)
    expect(w.findAll('[data-testid^="hub-agent-"]')).toHaveLength(2)
    w.unmount()
  })

  it('pushes sector names outside the agent ring', () => {
    const w = mountOrbit(0, { agentRingPx: 400 })
    const [, x, y] = /translate\(([-\d.]+)px, ([-\d.]+)px/.exec(w.get('[data-testid="hub-sector-0"]').attributes('style')!)!
    expect(Math.hypot(Number(x) - 500, Number(y) - 500)).toBeCloseTo(440)
    w.unmount()
  })

  it('shows every label when labelledAgents is omitted', () => {
    const w = mountOrbit(0)
    const label = w.get('[data-testid="hub-agent-1"]').get('[data-testid="hub-label-name"]').element.parentElement!
    expect(label.className).not.toContain('hidden')
    w.unmount()
  })

  // `invisible` (visibility: hidden), not `hidden` (display: none): a display:none box measures 0×0,
  // and feeding that back into the culler would make the culled set oscillate.
  it('hides a culled label without dropping its box, and keeps it out of the a11y tree', () => {
    const w = mountOrbit(0, { labelledAgents: new Set([2]) })
    const button1 = w.get('[data-testid="hub-agent-1"]')
    const label1 = button1.get('[data-testid="hub-label"]')
    expect(label1.classes()).toEqual(expect.arrayContaining(['invisible', 'group-hover:visible', 'group-focus-visible:visible']))
    expect(label1.classes()).not.toContain('hidden')
    expect(label1.element.getBoundingClientRect().width).toBeGreaterThan(0)
    expect(button1.findAll('span')[0].classes()).toContain('rounded-full')
    // The button's aria-label is the only place the name and status reach a screen reader.
    expect(button1.attributes('aria-label')).toBe('Kontor Hub, Working')
    expect(label1.attributes('aria-label')).toBeUndefined()

    expect(w.get('[data-testid="hub-agent-2"]').get('[data-testid="hub-label"]').classes()).not.toContain('invisible')
    w.unmount()
  })

  it('keeps a culled label out of the button\'s hit box by taking it out of flow', () => {
    const w = mountOrbit(0, { labelledAgents: new Set([2]) })
    expect(w.get('[data-testid="hub-agent-1"]').get('[data-testid="hub-label"]').classes()).toContain('absolute')
    w.unmount()
  })

  // Siblings without a z-index stack in DOM order, so the agent layer must come last to take the
  // pointer over a sector name it overlaps.
  it('draws the agents after the sector names so the legend cannot intercept a dot', () => {
    const w = mountOrbit(0)
    const ids = w.findAll('[data-testid^="hub-sector-"], [data-testid^="hub-agent-"]').map(e => e.attributes('data-testid'))
    expect(ids).toEqual(['hub-sector-0', 'hub-sector-1', 'hub-agent-1', 'hub-agent-2'])
    w.unmount()
  })

  // The rendered span and hubCanvas' agentLabelBox must sit on the same pixels, or the culler judges
  // a box the DOM never draws. Both come from agentLabelOffset, and this proves the template uses it.
  it('translates a label onto the box the culler judges, inward from its dot', async () => {
    const w = mountOrbit(0)
    await flushPromises()
    for (const [pid, x, y] of [[1, 82, 0], [2, -60, 0]] as const) {
      const label = w.get(`[data-testid="hub-agent-${pid}"]`).get('[data-testid="hub-label"]')
      const [dx, dy] = agentLabelOffset(inwardUnit(x, y), labelSize(label.attributes('data-label-key')!))
      expect(Math.sign(dx), `agent ${pid} hangs its label toward the core`).toBe(-Math.sign(x))
      expect(label.attributes('style')).toBe(`transform: translate(-50%, -50%) translate(${dx}px, ${dy}px);`)
    }
    w.unmount()
  })

  it('hangs a label the way the culler placed it, not always toward the core', async () => {
    const outward: [number, number] = [1, 0] // agent 1 sits east of the core, so this is away from it
    const w = mountOrbit(0, { labelDirections: new Map([[1, outward]]) })
    await flushPromises()
    const label = w.get('[data-testid="hub-agent-1"]').get('[data-testid="hub-label"]')
    const [dx, dy] = agentLabelOffset(outward, labelSize(label.attributes('data-label-key')!))
    expect(label.attributes('style')).toBe(`transform: translate(-50%, -50%) translate(${dx}px, ${dy}px);`)
    w.unmount()
  })

  // A launcher button is opaque: half a sector name reads as a shorter, wrong one.
  it('hides a sector name the caller cannot place, without dropping its box', async () => {
    const w = mountOrbit(0, { namedSectors: new Set(['web-app']) })
    await flushPromises()
    expect(w.get('[data-testid="hub-sector-0"]').classes()).toContain('invisible')
    expect(w.get('[data-testid="hub-sector-1"]').classes()).not.toContain('invisible')
    expect(w.get('[data-testid="hub-sector-0"]').element.getBoundingClientRect().width).toBeGreaterThan(0)
    w.unmount()
  })

  // A dot under the docked rail still looks interactive while the rail swallows the press.
  it('leaves an agent the caller cannot place undrawn, without dropping its box', async () => {
    const w = mountOrbit(0, { drawnAgents: new Set([2]) })
    await flushPromises()
    const undrawn = w.get('[data-testid="hub-agent-1"]')
    expect(undrawn.classes()).toContain('invisible')
    expect(w.get('[data-testid="hub-agent-2"]').classes()).not.toContain('invisible')
    expect(undrawn.get('[data-testid="hub-label"]').element.getBoundingClientRect().width).toBeGreaterThan(0)
    w.unmount()
  })

  it('measures every label once, and not again as the camera moves', async () => {
    const w = mountOrbit(0)
    await flushPromises()
    const keys = [
      agentLabelKey('kontor-hub', 'working'),
      agentLabelKey('web-app', 'waiting'),
      sectorLabelKey('kontor-hub', 1),
      sectorLabelKey('web-app', 1),
    ]
    const sizes = w.emitted('measure')!.at(-1)![0] as ReadonlyMap<string, LabelSize>
    expect([...sizes.keys()].sort()).toEqual([...keys].sort())
    expect(sizes.get(keys[0])).toEqual(labelSize(keys[0]))

    const measured = measureSpy.mock.calls.length
    expect(measured).toBe(keys.length)
    for (const tx of [520, 540, 560]) {
      await w.setProps({ cam: { k: 1, tx, ty: 500 } })
      await flushPromises()
    }
    expect(measureSpy.mock.calls.length).toBe(measured)
    expect(w.emitted('measure')).toHaveLength(1)
    w.unmount()
  })

  // A zero box collides with nothing, so caching one would drop that text out of collision
  // avoidance for the life of the component.
  it('re-measures a label that first came back at zero instead of caching an empty box', async () => {
    const key = agentLabelKey('web-app', 'waiting')
    const blind = new Set([key])
    stubLabelMeasurement(blind)
    const w = mountOrbit(0)
    await flushPromises()
    const sizes = () => w.emitted('measure')!.at(-1)![0] as ReadonlyMap<string, LabelSize>
    expect(sizes().has(key), 'a zero measurement is not cached').toBe(false)
    expect(sizes().has(agentLabelKey('kontor-hub', 'working'))).toBe(true)

    blind.clear()
    await w.setProps({ agents: [...ORBIT_AGENTS, { agent: agent(3, 'api-server'), x: 0, y: -90, state: 'working' as const, needsOperator: false }] })
    await flushPromises()
    expect(sizes().get(key)).toEqual(labelSize(key))
    w.unmount()
  })

  it('dims sector names at the topics level and drops them at the notes level', () => {
    const topics = mountOrbit(1)
    expect(topics.get('[data-testid="hub-sector-0"]').classes()).toContain('opacity-55')
    topics.unmount()

    const notes = mountOrbit(2)
    expect(notes.findAll('[data-testid^="hub-sector-"]')).toHaveLength(0)
    expect(notes.findAll('[data-testid^="hub-agent-"]')).toHaveLength(2)
    notes.unmount()
  })
})
