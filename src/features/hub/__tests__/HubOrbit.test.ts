import type { HubLevel } from '../hubCamera'
import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HubOrbit from '../components/HubOrbit.vue'
import { buildSectors } from '../hubGeometry'

const sectors = buildSectors([{ key: 'kontor-hub', label: 'kontor-hub', weight: 1 }, { key: 'web-app', label: 'web-app', weight: 1 }])

function agent(pid: number, projectName: string): Agent {
  return { pid, projectName, status: 'active', working: false } as Agent
}

function mountOrbit(level: HubLevel, { showSectorNames = true, agentRingPx = 116 } = {}) {
  return mount(HubOrbit, {
    props: {
      cam: { k: 1, tx: 500, ty: 500 },
      agentRingPx,
      showSectorNames,
      sectors,
      agents: [
        { agent: agent(1, 'kontor-hub'), x: 82, y: 0, state: 'working', needsOperator: false },
        { agent: agent(2, 'web-app'), x: -60, y: 0, state: 'waiting', needsOperator: true },
      ],
      level,
      running: 3,
      waiting: 1,
      needsYou: 2,
      coreTitle: 'Open Kontor (idle)',
    },
  })
}

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
