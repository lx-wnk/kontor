import type { GraphStatus } from '../composables/useObsidianGraph'
import type { Launcher } from '../hubLaunchers'
import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { OPEN_SETTINGS } from '@/composables/openTask'
import HubList from '../components/HubList.vue'

const a1 = { pid: 1, projectName: 'kontor-hub' } as Agent
const a2 = { pid: 2, projectName: 'web-app' } as Agent
const launchers: Launcher[] = [
  { id: 'pipeline', label: 'Pipeline', icon: '⇶', kind: 'view', view: 'pipeline' },
  { id: 'new-page', label: 'New page', icon: '+', kind: 'new-page' },
]

function mountList(openSettings = vi.fn(), graphStatus: GraphStatus = 'ready', graphMessage = '') {
  return mount(HubList, {
    attachTo: document.body,
    props: { agents: [{ agent: a1, state: 'working' as const }, { agent: a2, state: 'waiting' as const }], notes: [], graphStatus, graphMessage, launchers },
    global: { provide: { [OPEN_SETTINGS]: openSettings } },
  })
}

describe('hubList', () => {
  it('is a labelled dialog that takes focus on its first button', () => {
    const w = mountList()
    const panel = w.get('[role="dialog"]')
    expect(panel.attributes('aria-label')).toBe('Zentrale as a list')
    expect(panel.attributes('data-hub-layer')).toBeDefined()
    expect(document.activeElement).toBe(w.findAll('button')[0].element)
    w.unmount()
  })

  it('lists each agent with its state word and emits the agent picked', async () => {
    const w = mountList()
    const rows = w.findAll('[data-testid="hub-list-agent"]')
    expect(rows.map(r => r.attributes('aria-label'))).toEqual(['Kontor Hub, Working', 'Web App, Quiet'])
    await rows[1].trigger('click')
    expect(w.emitted('agent')).toEqual([[a2]])
    w.unmount()
  })

  it('offers the launchers under Go to', async () => {
    const w = mountList()
    await w.findAll('button').find(b => b.text().includes('Pipeline'))!.trigger('click')
    expect(w.emitted('launch')).toEqual([[launchers[0]]])
    w.unmount()
  })

  it('asks to connect Obsidian while unconfigured, and opens settings for it', async () => {
    const openSettings = vi.fn()
    const w = mountList(openSettings, 'unconfigured')
    expect(w.text()).toContain('Connect Obsidian to see recently touched notes.')
    await w.findAll('button').find(b => b.text() === 'Open settings')!.trigger('click')
    expect(openSettings).toHaveBeenCalledOnce()
    w.unmount()
  })

  it('says the vault read was denied, with the server message visible and in the title, and offers no settings button', () => {
    const w = mountList(vi.fn(), 'denied', 'memory.read denied')
    const notice = w.get('[data-testid="hub-list-note-notice"]')
    expect(notice.text()).toBe('Memory reads are not granted, so your notes stay hidden. memory.read denied')
    expect(notice.attributes('title')).toBe('memory.read denied')
    expect(w.get('[data-testid="hub-list-note-notice-detail"]').text()).toBe('memory.read denied')
    expect(w.findAll('button').find(b => b.text() === 'Open settings')).toBeUndefined()
    w.unmount()
  })

  it('says the vault could not be loaded when the read failed', () => {
    const w = mountList(vi.fn(), 'failed')
    expect(w.get('[data-testid="hub-list-note-notice"]').text()).toBe('Your notes could not be loaded; retrying when you come back to this window.')
    w.unmount()
  })

  it('says there are no notes yet once the vault is ready and empty', () => {
    const w = mountList(vi.fn(), 'ready')
    expect(w.get('[data-testid="hub-list-note-notice"]').text()).toBe('No notes yet.')
    w.unmount()
  })

  it('shows no notice while idle or loading', () => {
    for (const graphStatus of ['idle', 'loading'] as const) {
      const w = mountList(vi.fn(), graphStatus)
      expect(w.find('[data-testid="hub-list-note-notice"]').exists()).toBe(false)
      w.unmount()
    }
  })

  it('lists recently touched notes with their sector and age', async () => {
    const w = mountList()
    await w.setProps({ notes: [{ path: 'kontor/Plan.md', title: 'Plan', sector: 'kontor', mtimeMs: Date.now() - 2 * 3600_000 }] })
    const row = w.get('[data-testid="hub-list-note"]')
    expect(row.attributes('aria-label')).toBe('Plan, kontor, 2h ago')
    await row.trigger('click')
    expect(w.emitted('note')).toEqual([['kontor/Plan.md']])
    w.unmount()
  })

  it('closes from its button', async () => {
    const w = mountList()
    await w.get('button[aria-label="Close list"]').trigger('click')
    expect(w.emitted('close')).toHaveLength(1)
    w.unmount()
  })

  it('lists each agent\'s recent notes under it, and picking one emits its path', async () => {
    const at = new Date().toISOString()
    const w = mount(HubList, {
      attachTo: document.body,
      props: {
        agents: [{ agent: a1, state: 'working' as const, notes: [{ path: 'work/plan.md', title: 'plan', kind: 'write' as const, at }] }],
        notes: [],
        graphStatus: 'ready' as GraphStatus,
        graphMessage: '',
        launchers,
      },
      global: { provide: { [OPEN_SETTINGS]: vi.fn() } },
    })
    const rows = w.findAll('[data-testid="hub-list-agent-note"]')
    expect(rows).toHaveLength(1)
    expect(rows[0].attributes('aria-label')).toMatch(/^plan, wrote /)
    await rows[0].trigger('click')
    expect(w.emitted('note')).toEqual([['work/plan.md']])
    const group = rows[0].element.closest('[role="group"]')
    expect(group).not.toBeNull()
    expect(group!.getAttribute('aria-labelledby')).toBe(w.get('[data-testid="hub-list-agent"]').attributes('id'))
    w.unmount()
  })
})
