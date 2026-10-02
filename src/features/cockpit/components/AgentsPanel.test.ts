import type { VueWrapper } from '@vue/test-utils'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// AgentsPanel reads the module-level singleton in useAgents, so a test drives
// it by stubbing the barrel rather than by passing props.
vi.mock('@/features/agents', async () => {
  const { ref, shallowRef } = await import('vue')
  const agents = shallowRef<any[]>([])
  const selectAgent = vi.fn()
  return {
    useAgents: () => ({ agents, isLoading: ref(false), error: ref(null), selectAgent }),
  }
})

const { useAgents } = await import('@/features/agents')
const { default: AgentsPanel } = await import('./AgentsPanel.vue')
const { agents, selectAgent } = useAgents({ autoStart: false })

function makeAgent(overrides: Record<string, unknown> = {}): any {
  return {
    sessionId: 's1',
    projectName: 'Kontor',
    sessionTitle: 'My task',
    status: 'active',
    pid: 1234,
    ...overrides,
  }
}

let wrapper: VueWrapper | undefined

function mountWith(list: any[]) {
  agents.value = list
  wrapper = mount(AgentsPanel)
  return wrapper
}

function rowButton(w: VueWrapper, sessionId = 's1') {
  return w.get(`[data-testid="cockpit-agent-${sessionId}"]`)
}

// The label is the one span that is not the right-aligned status.
function labelOf(button: ReturnType<typeof rowButton>) {
  return button.get('span:not(.shrink-0)').text()
}

describe('agentsPanel', () => {
  beforeEach(() => {
    vi.mocked(selectAgent).mockReset()
    agents.value = []
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
  })

  it('renders the icon', () => {
    const w = mountWith([])
    expect(w.get('header').text()).toContain('◉')
  })

  it('labels the row "project - session title" when the agent has a session title', () => {
    const button = rowButton(mountWith([makeAgent({ projectName: 'Kontor', sessionTitle: 'My task' })]))
    expect(button.text()).toContain('Kontor - My task')
  })

  it('labels the row with the project name only when the session title is undefined', () => {
    const button = rowButton(mountWith([makeAgent({ projectName: 'Kontor', sessionTitle: undefined })]))
    expect(labelOf(button)).toBe('Kontor')
  })

  it('labels the row with the project name only when the session title is empty', () => {
    const button = rowButton(mountWith([makeAgent({ projectName: 'Kontor', sessionTitle: '' })]))
    expect(labelOf(button)).toBe('Kontor')
  })

  it('sets the title attribute to the full label so a truncated row stays readable', () => {
    const button = rowButton(mountWith([makeAgent({ projectName: 'Kontor', sessionTitle: 'My task' })]))
    expect(button.attributes('title')).toBe('Kontor - My task')
  })

  it('sets the aria-label to the full label followed by the status', () => {
    const button = rowButton(mountWith([makeAgent({ projectName: 'Kontor', sessionTitle: 'My task', status: 'waiting' })]))
    expect(button.attributes('aria-label')).toBe('Kontor - My task, waiting')
  })

  it('omits the dash from the aria-label when the session title is missing', () => {
    const button = rowButton(mountWith([makeAgent({ projectName: 'Kontor', sessionTitle: undefined, status: 'idle' })]))
    expect(button.attributes('aria-label')).toBe('Kontor, idle')
  })

  it('calls selectAgent with that agent when the row is clicked', async () => {
    const agent = makeAgent()
    const button = rowButton(mountWith([agent]))

    await button.trigger('click')

    expect(vi.mocked(selectAgent)).toHaveBeenCalledExactlyOnceWith(agent)
  })

  it('calls selectAgent with the clicked agent, not another row', async () => {
    const first = makeAgent({ sessionId: 's1' })
    const second = makeAgent({ sessionId: 's2', projectName: 'Other' })
    const w = mountWith([first, second])

    await rowButton(w, 's2').trigger('click')

    expect(vi.mocked(selectAgent)).toHaveBeenCalledExactlyOnceWith(second)
  })

  // jsdom does not turn Enter or Space into a click, so keyboard activation
  // itself is the browser's job and belongs to E2E. What a unit test can pin
  // is the precondition: an enabled, tab-reachable native button of type button.
  it('keeps the row keyboard-activatable: a native, enabled, tab-reachable button', () => {
    const button = rowButton(mountWith([makeAgent()]))
    expect([
      button.element.tagName,
      button.attributes('type'),
      button.attributes('disabled'),
      button.attributes('tabindex'),
    ]).toEqual(['BUTTON', 'button', undefined, undefined])
  })

  it('renders at most six rows when more agents are running', () => {
    const many = Array.from({ length: 8 }, (_, i) => makeAgent({ sessionId: `s${i}` }))
    const w = mountWith(many)

    const ids = w.findAll('button[data-testid^="cockpit-agent-"]').map(b => b.attributes('data-testid'))

    expect(ids).toEqual(['s0', 's1', 's2', 's3', 's4', 's5'].map(id => `cockpit-agent-${id}`))
  })
})
