import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const agents = ref([
  {
    pid: 101,
    status: 'active' as const,
    projectName: 'kontor-hub',
    working: true,
  },
  {
    pid: 100,
    status: 'active' as const,
    projectName: 'dashboard-app',
    working: false,
  },
  {
    pid: 102,
    status: 'waiting' as const,
    projectName: 'web-app',
    working: false,
  },
  {
    pid: 103,
    status: 'idle' as const,
    projectName: 'api-server',
    working: false,
  },
  {
    pid: 104,
    status: 'waiting' as const,
    projectName: 'worker-queue',
    working: true,
  },
])

vi.mock('@/features/agents', () => ({
  useAgents: () => ({ agents }),
}))
vi.mock('../components/NeedsYouQueue.vue', () => ({
  default: {
    props: ['variant'],
    template: '<div data-testid="stub-queue">{{ variant }}</div>',
  },
}))

const { default: HubWidget } = await import('../components/HubWidget.vue')

describe('hubWidget', () => {
  it('docks the queue and lists agents working first, then by status order', () => {
    const w = mount(HubWidget)
    expect(w.get('[data-testid="stub-queue"]').text()).toBe('docked')
    const rows = w.findAll('[data-testid="hub-agent"]').map(r => r.text())
    // working first: pid 101 (active + working) and pid 104 (waiting + working)
    // then active: pid 100
    // then waiting: pid 102
    // then idle: pid 103
    expect(rows[0]).toContain('working')
    expect(rows[1]).toContain('working')
    expect(rows[2]).toContain('active')
    expect(rows[3]).toContain('waiting')
    expect(rows[4]).toContain('idle')
    w.unmount()
  })

  it('displays a waiting agent with working=true as "working"', () => {
    const w = mount(HubWidget)
    // pid 104 has status: waiting, working: true, should display as "working"
    const rows = w.findAll('[data-testid="hub-agent"]')
    expect(rows[1].text()).toContain('working')
    expect(rows[1].text()).toContain('Worker Queue')
    w.unmount()
  })

  // At the hub's 6x6 minimum a tall question can outgrow the tile — the whole
  // card scrolls instead of clipping the queue's action buttons (M6).
  it('lets the whole card scroll instead of clipping the docked queue', () => {
    const w = mount(HubWidget)
    expect(w.get('[data-testid="hub"]').classes()).toContain('overflow-y-auto')
    expect(w.get('[data-testid="hub"]').classes()).not.toContain('overflow-hidden')
    expect(w.get('[data-testid="stub-queue"]').classes()).toContain('shrink-0')
    expect(w.get('[data-testid="hub-list"]').classes()).not.toContain('overflow-y-auto')
    w.unmount()
  })
})
