import type { Agent } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

let PromptInput: any

// Only the fields PromptInput and useAgentPrompt actually read. A partial
// fixture is safe here because neither path formats cost or tokens.
let sessionCounter = 0
function makeAgent(overrides: Partial<Agent> = {}): Agent {
  sessionCounter++
  return {
    pid: 4242,
    sessionId: `sess-${sessionCounter}`,
    cwd: '/repo',
    liveInjectable: true,
    ...overrides,
  } as unknown as Agent
}

let commandsResponse: Record<string, unknown> = {}

function makeFetch() {
  return vi.fn().mockImplementation((url: string) => {
    if (url.startsWith('/api/slash-commands'))
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(commandsResponse) })
    throw new Error(`unexpected fetch ${url}`)
  })
}

async function mountWithQuery(query: string) {
  const w = mount(PromptInput, { props: { agent: makeAgent() } })
  await flushPromises()
  await w.find('input[role="combobox"]').setValue(query)
  await flushPromises()
  return w
}

beforeEach(async () => {
  commandsResponse = { commands: [], builtinsMayBeStale: true, engineVersion: '2.1.300' }
  vi.stubGlobal('fetch', makeFetch())
  vi.resetModules()
  PromptInput = (await import('../PromptInput.vue')).default
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('promptInput resume-hint copy', () => {
  it('marks an internal process as not a session you can message', async () => {
    const w = mount(PromptInput, { props: { agent: makeAgent({ liveInjectable: false, internalProcess: true, channelAvailable: true }) } })
    await flushPromises()
    const hint = w.find('[data-testid="resume-hint"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('not a session you can message')
  })

  it('says "not connected" when the channel is unavailable', async () => {
    const w = mount(PromptInput, { props: { agent: makeAgent({ liveInjectable: false, internalProcess: false, channelAvailable: false }) } })
    await flushPromises()
    const hint = w.find('[data-testid="resume-hint"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('Not connected to the dashboard')
  })

  it('says sending resumes as a new session when connected but not injectable', async () => {
    const w = mount(PromptInput, { props: { agent: makeAgent({ liveInjectable: false, internalProcess: false, channelAvailable: true }) } })
    await flushPromises()
    const hint = w.find('[data-testid="resume-hint"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('resumes this session as a')
    expect(hint.text()).toContain('new')
  })

  it('shows no resume hint for a live-injectable session', async () => {
    const w = mount(PromptInput, { props: { agent: makeAgent({ liveInjectable: true }) } })
    await flushPromises()
    expect(w.find('[data-testid="resume-hint"]').exists()).toBe(false)
  })
})

describe('promptInput drift notice', () => {
  it('renders the note for a slash query that matches nothing', async () => {
    const w = await mountWithQuery('/definitely-not-a-command')

    expect(w.findAll('[role="option"]')).toHaveLength(0)
    const note = w.find('[data-testid="builtins-stale-note"]')
    expect(note.exists()).toBe(true)
    expect(note.text()).toContain('2.1.300')
  })

  it('renders the note alongside matching suggestions', async () => {
    const w = await mountWithQuery('/spawn')

    expect(w.findAll('[role="option"]').length).toBeGreaterThan(0)
    expect(w.find('[data-testid="builtins-stale-note"]').exists()).toBe(true)
  })

  it('announces the note as a status message outside the listbox', async () => {
    const w = await mountWithQuery('/definitely-not-a-command')

    const note = w.find('[data-testid="builtins-stale-note"]')
    expect(note.attributes('role')).toBe('status')
    expect(w.find('[role="listbox"] [data-testid="builtins-stale-note"]').exists()).toBe(false)
  })

  it('keeps the listbox free of non-option children when suggestions exist', async () => {
    const w = await mountWithQuery('/spawn')

    const listbox = w.find('[role="listbox"]')
    expect(listbox.exists()).toBe(true)
    for (const child of Array.from(listbox.element.children))
      expect(child.getAttribute('role')).toBe('option')
  })

  it('stays hidden when the session reports no drift', async () => {
    commandsResponse = { commands: [], builtinsMayBeStale: false }
    const w = await mountWithQuery('/definitely-not-a-command')

    expect(w.find('[data-testid="builtins-stale-note"]').exists()).toBe(false)
  })

  it('stays hidden while the input is not a slash query', async () => {
    const w = await mountWithQuery('hello')

    expect(w.find('[data-testid="builtins-stale-note"]').exists()).toBe(false)
  })
})

describe('promptInput command refresh', () => {
  // Only Date is faked: flushPromises awaits a real timer, which a full fake
  // clock would leave pending forever.
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function commandFetches(): number {
    return vi.mocked(globalThis.fetch).mock.calls.length
  }

  it('lists a command installed after mount once the cache has expired, and not before', async () => {
    commandsResponse = { commands: [], builtinsMayBeStale: false }
    const w = mount(PromptInput, { props: { agent: makeAgent() } })
    await flushPromises()
    expect(commandFetches()).toBe(1)

    commandsResponse = {
      commands: [{ name: '/installed-later', description: 'Installed after mount' }],
      builtinsMayBeStale: false,
    }
    const input = w.find('input[role="combobox"]')

    await input.setValue('/installed-later')
    await flushPromises()
    expect(commandFetches()).toBe(1)
    expect(w.findAll('[role="option"]')).toHaveLength(0)

    await input.setValue('')
    vi.setSystemTime(Date.now() + 61_000)
    await input.setValue('/installed-later')
    await flushPromises()

    expect(commandFetches()).toBe(2)
    expect(w.find('[role="listbox"]').text()).toContain('/installed-later')
  })
})

describe('promptInput argument hints', () => {
  // A hint can come from an installed plugin's frontmatter, so the row must not
  // be stretchable by its content — the server cap is not the only guard.
  it('clips a long argument hint and keeps the full value in the title', async () => {
    const longHint = `[${'x'.repeat(200)}]`
    commandsResponse = {
      commands: [{ name: '/plugin-cmd', description: 'From a plugin', argumentHint: longHint }],
      builtinsMayBeStale: false,
    }
    const w = await mountWithQuery('/plugin-cmd')

    const usage = w.find('[data-testid="command-usage"]')
    expect(usage.exists()).toBe(true)
    expect(usage.text().length).toBeLessThanOrEqual(61)
    expect(usage.text().endsWith('…')).toBe(true)
    expect(usage.attributes('title')).toBe(longHint)
  })

  it('renders a short hint unchanged', async () => {
    commandsResponse = {
      commands: [{ name: '/deploy', description: 'Deploy', argumentHint: '[env] [--dry-run]' }],
      builtinsMayBeStale: false,
    }
    const w = await mountWithQuery('/deploy')

    expect(w.find('[data-testid="command-usage"]').text()).toBe('[env] [--dry-run]')
  })
})
