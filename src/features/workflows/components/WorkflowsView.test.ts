import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import WorkflowsView from '@/features/workflows/components/WorkflowsView.vue'
import { openListbox, optionByLabel } from '@/utils/testSelect'

function emptySankey() {
  return { nodes: [], links: [], meta: { sessionCount: 0, callCount: 0 } }
}

function makeFetchMock(sessions: unknown[] = []) {
  return vi.fn().mockImplementation((url: string) => {
    if (String(url).includes('/api/sessions')) {
      return Promise.resolve({ ok: true, json: async () => sessions })
    }
    return Promise.resolve({ ok: true, json: async () => emptySankey() })
  })
}

// AppSelect (used for the session filter) is a custom listbox, not a native
// <select> — its panel teleports to <body> while open, so option counts and
// value changes are exercised through the trigger button + the teleported
// [role="option"] elements instead of select.findAll('option') /
// select.setValue(), which only work against native <select> internals. See
// testSelect.ts for the shared openListbox()/optionByLabel() helpers.

afterEach(() => {
  document.body.innerHTML = ''
})

describe('workflowsView', () => {
  it('renders the three static tab buttons (no Session DAG initially)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptySankey(),
    }))
    const wrapper = mount(WorkflowsView)
    await flushPromises()
    for (const label of ['Sankey', 'Spawn Tree', 'Co-occurrence'])
      expect(wrapper.text()).toContain(label)
    // Session DAG is not visible without a session
    const dagButton = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Session DAG')
    expect(dagButton).toBeUndefined()
  })

  it('shows the empty sankey state when the backend returns no data', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptySankey(),
    }))
    const wrapper = mount(WorkflowsView)
    await flushPromises()
    expect(wrapper.text()).toContain('No tool calls found in this window.')
  })

  it('does not render the DAG tab button when sessionId is empty', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptySankey(),
    }))
    const wrapper = mount(WorkflowsView)
    await flushPromises()
    const dagButton = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Session DAG')
    expect(dagButton).toBeUndefined()
  })

  it('renders session dropdown options from useSessions', async () => {
    const mockSessions = [
      {
        sessionId: 'abc12345-0000-0000-0000-000000000000',
        projectName: 'my-project',
        firstPrompt: 'Build a new feature',
        isRunning: false,
        projectPath: '/tmp/my-project',
        lastModified: '2026-06-01T10:00:00Z',
        model: null,
        lastResponse: null,
        totalInputTokens: 0,
        totalOutputTokens: 0,
        costEstimate: 0,
      },
      {
        sessionId: 'def67890-0000-0000-0000-000000000000',
        projectName: 'other-project',
        firstPrompt: null,
        isRunning: true,
        projectPath: '/tmp/other-project',
        lastModified: '2026-06-01T11:00:00Z',
        model: null,
        lastResponse: null,
        totalInputTokens: 0,
        totalOutputTokens: 0,
        costEstimate: 0,
      },
    ]
    vi.stubGlobal('fetch', makeFetchMock(mockSessions))
    const wrapper = mount(WorkflowsView, { attachTo: document.body })
    await flushPromises()

    const panel = await openListbox(wrapper.get('[role="combobox"]'))
    const options = panel.querySelectorAll('[role="option"]')
    expect(options).toHaveLength(3)

    // First option is the placeholder, selected by default (no sessionId)
    expect(options[0].textContent).toContain('Select session')
    expect(options[0].getAttribute('aria-selected')).toBe('true')

    // Second option: projectName · firstPrompt (no running suffix)
    expect(options[1].textContent).toContain('my-project')
    expect(options[1].textContent).toContain('Build a new feature')
    expect(options[1].textContent).not.toContain('(running)')

    // Third option: projectName · first 8 chars of sessionId + (running)
    expect(options[2].textContent).toContain('other-project')
    expect(options[2].textContent).toContain('def67890')
    expect(options[2].textContent).toContain('(running)')
  })

  it('shows Session DAG tab and makes it active when a session is selected from the dropdown', async () => {
    const mockSessions = [
      {
        sessionId: 'abc12345-0000-0000-0000-000000000000',
        projectName: 'my-project',
        firstPrompt: 'Some prompt',
        isRunning: false,
        projectPath: '/tmp/my-project',
        lastModified: '2026-06-01T10:00:00Z',
        model: null,
        lastResponse: null,
        totalInputTokens: 0,
        totalOutputTokens: 0,
        costEstimate: 0,
      },
    ]
    vi.stubGlobal('fetch', makeFetchMock(mockSessions))
    const wrapper = mount(WorkflowsView, { attachTo: document.body })
    await flushPromises()

    // Confirm DAG tab is absent initially (no session)
    const dagButtonBefore = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Session DAG')
    expect(dagButtonBefore).toBeUndefined()

    // Select a session via the dropdown
    const panel = await openListbox(wrapper.get('[role="combobox"]'))
    optionByLabel(panel, 'my-project · Some prompt').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    // DAG tab should now be present
    const dagButton = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Session DAG')
    expect(dagButton).toBeTruthy()

    // DAG tab should be active (has the active class)
    expect(dagButton!.classes()).toContain('bg-blue-600')
  })

  it('hides Session DAG tab and returns to Sankey after Reset', async () => {
    const mockSessions = [
      {
        sessionId: 'abc12345-0000-0000-0000-000000000000',
        projectName: 'my-project',
        firstPrompt: 'Some prompt',
        isRunning: false,
        projectPath: '/tmp/my-project',
        lastModified: '2026-06-01T10:00:00Z',
        model: null,
        lastResponse: null,
        totalInputTokens: 0,
        totalOutputTokens: 0,
        costEstimate: 0,
      },
    ]
    vi.stubGlobal('fetch', makeFetchMock(mockSessions))
    const wrapper = mount(WorkflowsView, { attachTo: document.body })
    await flushPromises()

    // Select a session to show the DAG tab
    const panel = await openListbox(wrapper.get('[role="combobox"]'))
    optionByLabel(panel, 'my-project · Some prompt').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    // Confirm DAG tab is active
    const dagButtonActive = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Session DAG')
    expect(dagButtonActive).toBeTruthy()
    expect(dagButtonActive!.classes()).toContain('bg-blue-600')

    // Click Reset
    const resetButton = wrapper.findAll('button').find(b => b.text() === 'Reset')!
    await resetButton.trigger('click')
    await flushPromises()

    // DAG tab should be gone
    const dagButtonAfter = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Session DAG')
    expect(dagButtonAfter).toBeUndefined()

    // Sankey tab should be active
    const sankeyButton = wrapper.findAll('button[role="tab"]').find(b => b.text() === 'Sankey')
    expect(sankeyButton).toBeTruthy()
    expect(sankeyButton!.classes()).toContain('bg-blue-600')
  })

  it('follows from/to props: writes them to the request URL and hides the date inputs', async () => {
    const mockFetch = makeFetchMock()
    vi.stubGlobal('fetch', mockFetch)
    const wrapper = mount(WorkflowsView, { props: { from: '2026-10-01', to: '2026-10-30' } })
    await flushPromises()
    const url = (mockFetch.mock.calls as [string][]).map(([u]) => String(u)).find(u => u.includes('/api/visualizations/sankey'))!
    const params = new URL(url, 'http://localhost').searchParams
    expect(params.get('from')).toBe(new Date('2026-10-01T00:00').toISOString())
    expect(params.get('to')).toBe(new Date('2026-10-30T23:59:59').toISOString())
    expect(wrapper.find('input[type="datetime-local"]').exists()).toBe(false)
  })

  it('without props keeps the date inputs', async () => {
    vi.stubGlobal('fetch', makeFetchMock())
    const wrapper = mount(WorkflowsView)
    await flushPromises()
    expect(wrapper.findAll('input[type="datetime-local"]')).toHaveLength(2)
  })
})
