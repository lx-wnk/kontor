import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowRef } from 'vue'
import ApplicationSettings from '@/features/settings/components/ApplicationSettings.vue'

const spawnersRef = vi.hoisted(() => ({ current: null as unknown as { value: unknown[] } }))
vi.mock('@/composables/useSpawners', () => ({
  useSpawners: () => ({ spawners: spawnersRef.current }),
}))

class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2
  constructor(public url: string) { MockEventSource.instances.push(this) }
  close() { this.readyState = 2 }
}

function spawner(id: string, name: string, adapterType: string, env: Record<string, string> = {}) {
  return { id, name, slug: id, command: 'claude', args: [], env, adapterType, adapterConfig: {}, builtIn: false, isDefault: false, createdAt: '', updatedAt: '' }
}

const SPAWNERS = [
  spawner('sp-default', 'Claude (default)', 'claude'),
  spawner('sp-work', 'Claude Work', 'claude', { CLAUDE_CONFIG_DIR: '/Users/me/.claude-work' }),
  spawner('sp-ollama', 'Local Ollama', 'ollama'),
]

beforeEach(() => {
  spawnersRef.current = shallowRef(SPAWNERS)
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
})

const MAIL = {
  resourceId: 'res-mail',
  serverName: 'mail',
  attachAll: false,
  requiredEnv: ['MAIL_PASSWORD'],
  secrets: [],
  tools: [
    { capability: 'mcp__mail__search', name: 'search', readOnlyHint: true },
    { capability: 'mcp__mail__send', name: 'send', readOnlyHint: false, destructiveHint: true },
  ],
  entry: { command: 'node', args: ['mail-server.js'], env: {} },
  exportToClaude: false,
  allowedSpawners: [] as string[],
  preset: '',
}

const IMAP = { ...MAIL, resourceId: 'res-imap', serverName: 'mail-private', preset: 'imap-mcp-server' }

function stubFetch(responses: Record<string, unknown>) {
  const calls: Array<{ url: string, init?: RequestInit }> = []
  const merged: Record<string, unknown> = { 'GET /api/applications/drift': { found: [], changed: [] }, ...responses }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    const body = merged[`${init?.method ?? 'GET'} ${url}`]
    return { ok: true, status: body === undefined ? 204 : 200, json: async () => body }
  }))
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('applicationSettings', () => {
  it('lists applications with their tools and marks a missing required secret', async () => {
    stubFetch({ 'GET /api/applications': [MAIL] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(wrapper.text()).toContain('mail')
    expect(wrapper.text()).toContain('mcp__mail__search')
    expect(wrapper.find('[data-testid="secret-missing-MAIL_PASSWORD"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows the empty state when no server is registered', async () => {
    stubFetch({ 'GET /api/applications': [] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()
    expect(wrapper.find('[data-testid="applications-empty"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('sends a secret and never renders its value afterwards', async () => {
    const calls = stubFetch({ 'GET /api/applications': [MAIL] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="secret-input-res-mail-MAIL_PASSWORD"]').setValue('hunter2')
    await wrapper.find('[data-testid="secret-save-res-mail-MAIL_PASSWORD"]').trigger('click')
    await flushPromises()

    const put = calls.find(c => c.init?.method === 'PUT')
    expect(put?.url).toBe('/api/applications/res-mail/secrets/MAIL_PASSWORD')
    expect(JSON.parse(String(put?.init?.body))).toEqual({ value: 'hunter2' })
    expect(wrapper.html()).not.toContain('hunter2')
    wrapper.unmount()
  })

  it('adds a required variable', async () => {
    const calls = stubFetch({
      'GET /api/applications': [{ ...MAIL, requiredEnv: [] }],
      'PATCH /api/applications/res-mail': { ...MAIL, requiredEnv: ['MAIL_PASSWORD'] },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(wrapper.text()).toContain('No secrets declared. Add the environment variable names this server reads its credentials from.')

    await wrapper.find('[data-testid="required-env-input-res-mail"]').setValue('MAIL_PASSWORD')
    await wrapper.find('[data-testid="required-env-add-res-mail"]').trigger('click')
    await flushPromises()

    const patch = calls.find(c => c.init?.method === 'PATCH')
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ requiredEnv: ['MAIL_PASSWORD'] })
    expect(wrapper.find('[data-testid="secret-input-res-mail-MAIL_PASSWORD"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('rejects an invalid name without a request', async () => {
    const calls = stubFetch({ 'GET /api/applications': [{ ...MAIL, requiredEnv: [] }] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="required-env-input-res-mail"]').setValue('mail-password')
    await wrapper.find('[data-testid="required-env-add-res-mail"]').trigger('click')
    await flushPromises()

    expect(calls.some(c => c.init?.method === 'PATCH')).toBe(false)
    expect(wrapper.text()).toContain('is not an environment variable name')
    wrapper.unmount()
  })

  it('stops requiring a variable', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'PATCH /api/applications/res-mail': { ...MAIL, requiredEnv: [] },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="required-env-remove-res-mail-MAIL_PASSWORD"]').trigger('click')
    await flushPromises()

    const patch = calls.find(c => c.init?.method === 'PATCH')
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ requiredEnv: [] })
    wrapper.unmount()
  })

  it('adds a server via the form and refetches the list', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'POST /api/applications': { ...MAIL, resourceId: 'res-cal', serverName: 'calendar' },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="application-add-toggle"]').trigger('click')
    await wrapper.find('[data-testid="application-add-name"]').setValue('calendar')
    await wrapper.find('[data-testid="application-add-command"]').setValue('npx')
    await wrapper.find('[data-testid="application-add-args"]').setValue('-y calendar-mcp')
    await wrapper.find('[data-testid="application-add-env"]').setValue('API_KEY=abc')
    await wrapper.find('[data-testid="application-add-form"]').trigger('submit')
    await flushPromises()

    const post = calls.find(c => c.init?.method === 'POST')
    expect(post?.url).toBe('/api/applications')
    expect(JSON.parse(String(post?.init?.body))).toEqual({
      name: 'calendar',
      command: 'npx',
      args: ['-y', 'calendar-mcp'],
      env: { API_KEY: 'abc' },
    })
    expect(calls.filter(c => (c.init?.method ?? 'GET') === 'GET' && c.url === '/api/applications').length).toBe(2)
    wrapper.unmount()
  })

  it('shows a create error and leaves the form open', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST')
        return { ok: false, status: 400, json: async () => ({ error: 'slug must match ^[a-z0-9-]+$' }) }
      return { ok: true, status: 200, json: async () => [MAIL] }
    }))
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="application-add-toggle"]').trigger('click')
    await wrapper.find('[data-testid="application-add-name"]').setValue('Bad Name!')
    await wrapper.find('[data-testid="application-add-command"]').setValue('npx')
    await wrapper.find('[data-testid="application-add-form"]').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('slug must match')
    expect(wrapper.find('[data-testid="application-add-form"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('edits the server entry', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'PATCH /api/applications/res-mail': { ...MAIL, entry: { command: 'node', args: ['server.js'], env: { FOO: 'bar' } } },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="application-edit-res-mail"]').trigger('click')
    await wrapper.find('[data-testid="application-edit-command-res-mail"]').setValue('node')
    await wrapper.find('[data-testid="application-edit-args-res-mail"]').setValue('server.js')
    await wrapper.find('[data-testid="application-edit-env-res-mail"]').setValue('FOO=bar')
    await wrapper.find('[data-testid="application-edit-save-res-mail"]').trigger('click')
    await flushPromises()

    const patch = calls.find(c => c.init?.method === 'PATCH')
    expect(JSON.parse(String(patch?.init?.body))).toEqual({
      entry: { command: 'node', args: ['server.js'], env: { FOO: 'bar' } },
    })
    wrapper.unmount()
  })

  it('toggles export to Claude and reflects the response', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'PATCH /api/applications/res-mail': { ...MAIL, exportToClaude: true },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    const toggle = wrapper.find('[data-testid="application-export-res-mail"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    await toggle.trigger('click')
    await flushPromises()

    const patch = calls.find(c => c.init?.method === 'PATCH')
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ exportToClaude: true })
    expect(wrapper.find('[data-testid="application-export-res-mail"]').attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })

  it('shows a 502 export error and keeps the switch off', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'PATCH')
        return { ok: false, status: 502, json: async () => ({ error: 'failed to write Claude config' }) }
      return { ok: true, status: 200, json: async () => [MAIL] }
    }))
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="application-export-res-mail"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('failed to write Claude config')
    expect(wrapper.find('[data-testid="application-export-res-mail"]').attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('removes a server after confirming in place', async () => {
    const calls = stubFetch({ 'GET /api/applications': [MAIL] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(wrapper.find('[data-testid="application-remove-confirm-res-mail"]').exists()).toBe(false)
    await wrapper.find('[data-testid="application-remove-res-mail"]').trigger('click')
    expect(wrapper.find('[data-testid="application-remove-confirm-res-mail"]').exists()).toBe(true)

    await wrapper.find('[data-testid="application-remove-confirm-res-mail"]').trigger('click')
    await flushPromises()

    const del = calls.find(c => c.init?.method === 'DELETE')
    expect(del?.url).toBe('/api/applications/res-mail')
    wrapper.unmount()
  })

  it('shows a 409 error naming attached routines and keeps the card', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE')
        return { ok: false, status: 409, json: async () => ({ error: 'still attached to: inbox' }) }
      return { ok: true, status: 200, json: async () => [MAIL] }
    }))
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid="application-remove-res-mail"]').trigger('click')
    await wrapper.find('[data-testid="application-remove-confirm-res-mail"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('still attached to: inbox')
    expect(wrapper.find('[data-testid="application-res-mail"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('imports a found server from the drift banner', async () => {
    const calls = stubFetch({
      'GET /api/applications': [],
      'GET /api/applications/drift': { found: ['notes'], changed: [] },
      'POST /api/applications/import': { ...MAIL, resourceId: 'res-notes', serverName: 'notes' },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    const banner = wrapper.find('[data-testid="application-drift-found-notes"]')
    expect(banner.text()).toContain('Found notes')

    const getCountBefore = calls.filter(c => (c.init?.method ?? 'GET') === 'GET').length
    await banner.find('button').trigger('click')
    await flushPromises()

    const post = calls.find(c => c.init?.method === 'POST' && c.url === '/api/applications/import')
    expect(JSON.parse(String(post?.init?.body))).toEqual({ name: 'notes' })
    const getCountAfter = calls.filter(c => (c.init?.method ?? 'GET') === 'GET').length
    expect(getCountAfter).toBe(getCountBefore + 2)
    expect(calls.some(c => (c.init?.method ?? 'GET') === 'GET' && c.url === '/api/applications')).toBe(true)
    expect(calls.some(c => (c.init?.method ?? 'GET') === 'GET' && c.url === '/api/applications/drift')).toBe(true)
    wrapper.unmount()
  })

  it('takes the file version by disabling export', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'GET /api/applications/drift': { found: [], changed: ['mail'] },
      'PATCH /api/applications/res-mail': { ...MAIL, exportToClaude: false },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    const banner = wrapper.find('[data-testid="application-drift-changed-mail"]')
    expect(banner.text()).toContain('mail')
    const buttons = banner.findAll('button')
    expect(buttons).toHaveLength(2)
    await buttons[0].trigger('click')
    await flushPromises()

    const patch = calls.find(c => c.init?.method === 'PATCH')
    expect(patch?.url).toBe('/api/applications/res-mail')
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ exportToClaude: false })
    wrapper.unmount()
  })

  it('writes the app version back to Claude config', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'GET /api/applications/drift': { found: [], changed: ['mail'] },
      'PATCH /api/applications/res-mail': { ...MAIL, exportToClaude: true },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    const banner = wrapper.find('[data-testid="application-drift-changed-mail"]')
    const buttons = banner.findAll('button')
    await buttons[1].trigger('click')
    await flushPromises()

    const patch = calls.find(c => c.init?.method === 'PATCH')
    expect(patch?.url).toBe('/api/applications/res-mail')
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ exportToClaude: true })
    wrapper.unmount()
  })

  it('shows no drift banner when nothing changed', async () => {
    stubFetch({ 'GET /api/applications': [MAIL] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(wrapper.find('[data-testid^="application-drift-"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('refetches drift on an applications_changed event', async () => {
    const calls = stubFetch({ 'GET /api/applications': [MAIL] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    const driftCallsBefore = calls.filter(c => (c.init?.method ?? 'GET') === 'GET' && c.url === '/api/applications/drift').length
    expect(driftCallsBefore).toBe(1)

    MockEventSource.instances[0].onmessage?.({ data: JSON.stringify({ type: 'applications_changed' }) } as MessageEvent)
    await flushPromises()

    const driftCallsAfter = calls.filter(c => (c.init?.method ?? 'GET') === 'GET' && c.url === '/api/applications/drift').length
    expect(driftCallsAfter).toBe(2)
    wrapper.unmount()
  })

  it('keeps the list and shows no panel error when the drift fetch fails', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url === '/api/applications/drift')
        throw new Error('network down')
      return { ok: true, status: 200, json: async () => [MAIL] }
    }))
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(wrapper.text()).toContain('mail')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('closes the event stream on unmount', async () => {
    stubFetch({ 'GET /api/applications': [MAIL] })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(MockEventSource.instances).toHaveLength(1)
    wrapper.unmount()

    expect(MockEventSource.instances[0].readyState).toBe(MockEventSource.CLOSED)
  })

  const SESSION = {
    resourceId: 'res-mail',
    port: 51234,
    url: 'http://127.0.0.1:51234',
    startedAt: '2026-09-18T10:00:00Z',
    warning: 'Setup is reachable on your local network until you click Done.',
  }

  it('starts a setup session and shows the frame with the warning and fallback link', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'POST /api/applications/res-mail/setup': SESSION,
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid=\"application-setup-start-res-mail\"]').trigger('click')
    await flushPromises()

    const post = calls.find(c => c.init?.method === 'POST' && c.url === '/api/applications/res-mail/setup')
    expect(post).toBeTruthy()

    const frame = wrapper.find('[data-testid=\"application-setup-frame-res-mail\"]')
    expect(frame.exists()).toBe(true)
    expect(frame.attributes('src')).toBe('http://127.0.0.1:51234')
    // The frame shows a third-party page: without the sandbox it could reach
    // the dashboard's own origin, so the attribute is part of the contract.
    expect(frame.attributes('sandbox')).toBe('allow-forms allow-scripts allow-same-origin')

    const warning = wrapper.find('[data-testid=\"application-setup-warning-res-mail\"]')
    expect(warning.text()).toBe('Setup is reachable on your local network until you click Done.')

    const link = wrapper.find('[data-testid=\"application-setup-open-res-mail\"]')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(link.attributes('href')).toBe('http://127.0.0.1:51234')

    wrapper.unmount()
  })

  it('finishes setup, removes the frame and shows one password field per returned account name', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'POST /api/applications/res-mail/setup': SESSION,
      'POST /api/applications/res-mail/accounts': { names: ['IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD'] },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid=\"application-setup-start-res-mail\"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid=\"application-setup-done-res-mail\"]').trigger('click')
    await flushPromises()

    const del = calls.find(c => c.init?.method === 'DELETE' && c.url === '/api/applications/res-mail/setup')
    expect(del).toBeTruthy()
    const post = calls.find(c => c.init?.method === 'POST' && c.url === '/api/applications/res-mail/accounts')
    expect(post).toBeTruthy()

    expect(wrapper.find('[data-testid=\"application-setup-frame-res-mail\"]').exists()).toBe(false)

    const field = wrapper.find('[data-testid=\"application-setup-secret-IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD\"]')
    expect(field.exists()).toBe(true)
    expect(field.attributes('type')).toBe('password')

    wrapper.unmount()
  })

  it('saves a setup-derived secret and clears the field', async () => {
    const calls = stubFetch({
      'GET /api/applications': [MAIL],
      'POST /api/applications/res-mail/setup': SESSION,
      'POST /api/applications/res-mail/accounts': { names: ['IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD'] },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid=\"application-setup-start-res-mail\"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid=\"application-setup-done-res-mail\"]').trigger('click')
    await flushPromises()

    const input = wrapper.find('[data-testid=\"application-setup-secret-IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD\"]')
    await input.setValue('s3cret')
    await wrapper.find('[data-testid=\"application-setup-secret-save-IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD\"]').trigger('click')
    await flushPromises()

    const put = calls.find(c => c.init?.method === 'PUT')
    expect(put?.url).toBe('/api/applications/res-mail/secrets/IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD')
    expect(JSON.parse(String(put?.init?.body))).toEqual({ value: 's3cret' })
    expect((wrapper.find('[data-testid=\"application-setup-secret-IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD\"]').element as HTMLInputElement).value).toBe('')

    wrapper.unmount()
  })

  it('shows a 502 from setup and renders no frame', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url === '/api/applications/res-mail/setup')
        return { ok: false, status: 502, json: async () => ({ error: 'child process failed to start' }) }
      if (url === '/api/applications/drift')
        return { ok: true, status: 200, json: async () => ({ found: [], changed: [] }) }
      return { ok: true, status: 200, json: async () => [MAIL] }
    }))
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid=\"application-setup-start-res-mail\"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('child process failed to start')
    expect(wrapper.find('[data-testid=\"application-setup-frame-res-mail\"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('shows a 502 from the accounts call and renders the manual fallback input', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url === '/api/applications/res-mail/setup')
        return { ok: true, status: 200, json: async () => SESSION }
      if (init?.method === 'DELETE' && url === '/api/applications/res-mail/setup')
        return { ok: true, status: 204, json: async () => null }
      if (init?.method === 'POST' && url === '/api/applications/res-mail/accounts')
        return { ok: false, status: 502, json: async () => ({ error: 'tool does not report accounts' }) }
      if (url === '/api/applications/drift')
        return { ok: true, status: 200, json: async () => ({ found: [], changed: [] }) }
      return { ok: true, status: 200, json: async () => [MAIL] }
    }))
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    await wrapper.find('[data-testid=\"application-setup-start-res-mail\"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid=\"application-setup-done-res-mail\"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('tool does not report accounts')
    expect(wrapper.find('[data-testid=\"application-setup-account-manual-res-mail\"]').exists()).toBe(true)

    wrapper.unmount()
  })

  it('shows each tool as denied, asking or allowed, and names where it is allowed', async () => {
    stubFetch({
      'GET /api/applications': [{
        ...MAIL,
        tools: [
          { capability: 'mcp__mail__imap_send_email', name: 'imap_send_email', readOnlyHint: false, state: 'denied', allowedIn: [] },
          { capability: 'mcp__mail__imap_search_emails', name: 'imap_search_emails', readOnlyHint: true, state: 'asks', allowedIn: [] },
          { capability: 'mcp__mail__imap_save_draft', name: 'imap_save_draft', readOnlyHint: false, state: 'asks', allowedIn: ['routine:routine-7'] },
        ],
      }],
      'GET /api/applications/drift': { found: [], changed: [] },
    })
    const wrapper = mount(ApplicationSettings)
    await flushPromises()

    expect(wrapper.find('[data-testid="application-tool-state-denied-imap_send_email"]').text()).toBe('denied by default')
    expect(wrapper.find('[data-testid="application-tool-state-asks-imap_search_emails"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="application-tool-allowed-in-imap_save_draft"]').text()).toContain('routine:routine-7')
    // A tool that asks carries no allowed-in label at all.
    expect(wrapper.find('[data-testid="application-tool-allowed-in-imap_search_emails"]').exists()).toBe(false)
    // The server's own hints stay, separate from the grant state.
    expect(wrapper.html()).toContain('read-only (per server)')

    wrapper.unmount()
  })

  describe('spawner restriction', () => {
    it('lists only claude spawners, labelled with their account directory', async () => {
      stubFetch({ 'GET /api/applications': [IMAP] })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      const fieldset = wrapper.find('[data-testid="application-spawners-res-imap"]')
      expect(fieldset.text()).toContain('Claude (default) (~/.claude)')
      expect(fieldset.text()).toContain('Claude Work (/Users/me/.claude-work)')
      expect(fieldset.text()).not.toContain('Local Ollama')
      expect(fieldset.findAll('input[type="checkbox"]')).toHaveLength(2)
      wrapper.unmount()
    })

    it('patches the chosen spawner ids for that application', async () => {
      const calls = stubFetch({
        'GET /api/applications': [IMAP],
        'PATCH /api/applications/res-imap': { ...IMAP, allowedSpawners: ['sp-default'] },
      })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      await wrapper.find('[data-testid="application-spawner-res-imap-sp-default"]').setValue(true)
      await flushPromises()

      const patch = calls.find(c => c.init?.method === 'PATCH')
      expect(patch?.url).toBe('/api/applications/res-imap')
      expect(JSON.parse(String(patch?.init?.body))).toEqual({ allowedSpawners: ['sp-default'] })
      expect((wrapper.find('[data-testid="application-spawner-res-imap-sp-default"]').element as HTMLInputElement).checked).toBe(true)
      wrapper.unmount()
    })

    it('removes an id when its checkbox is cleared', async () => {
      const calls = stubFetch({
        'GET /api/applications': [{ ...IMAP, allowedSpawners: ['sp-default', 'sp-work'] }],
        'PATCH /api/applications/res-imap': { ...IMAP, allowedSpawners: ['sp-work'] },
      })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      await wrapper.find('[data-testid="application-spawner-res-imap-sp-default"]').setValue(false)
      await flushPromises()

      const patch = calls.find(c => c.init?.method === 'PATCH')
      expect(JSON.parse(String(patch?.init?.body))).toEqual({ allowedSpawners: ['sp-work'] })
      wrapper.unmount()
    })

    it('shows the server error when the PATCH is rejected', async () => {
      vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === 'PATCH')
          return { ok: false, status: 400, json: async () => ({ error: 'allowedSpawners: unknown spawner sp-gone' }) }
        if (url === '/api/applications/drift')
          return { ok: true, status: 200, json: async () => ({ found: [], changed: [] }) }
        return { ok: true, status: 200, json: async () => [IMAP] }
      }))
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      await wrapper.find('[data-testid="application-spawner-res-imap-sp-default"]').setValue(true)
      await flushPromises()

      expect(wrapper.text()).toContain('allowedSpawners: unknown spawner sp-gone')
      expect((wrapper.find('[data-testid="application-spawner-res-imap-sp-default"]').element as HTMLInputElement).checked).toBe(false)
      wrapper.unmount()
    })

    it('warns that any spawner can read an unrestricted mailbox', async () => {
      stubFetch({ 'GET /api/applications': [IMAP] })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      expect(wrapper.find('[data-testid="application-spawners-warning-res-imap"]').text()).toBe('Any spawner can read this mailbox.')
      wrapper.unmount()
    })

    it('drops the mailbox warning once the application is restricted', async () => {
      stubFetch({ 'GET /api/applications': [{ ...IMAP, allowedSpawners: ['sp-default'] }] })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      expect(wrapper.text()).not.toContain('Any spawner can read this mailbox.')
      wrapper.unmount()
    })

    it('shows no mailbox warning for a non-imap application', async () => {
      stubFetch({ 'GET /api/applications': [MAIL] })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      expect(wrapper.text()).not.toContain('Any spawner can read this mailbox.')
      wrapper.unmount()
    })

    it('disables the export toggle with a hint while restricted', async () => {
      stubFetch({ 'GET /api/applications': [{ ...IMAP, allowedSpawners: ['sp-default'] }] })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      expect(wrapper.find('[data-testid="application-export-res-imap"]').attributes('disabled')).toBeDefined()
      expect(wrapper.find('[data-testid="application-export-hint-res-imap"]').text()).toBe('Export is off while the application is limited to spawners.')
      wrapper.unmount()
    })

    it('keeps the export toggle enabled without a restriction', async () => {
      stubFetch({ 'GET /api/applications': [IMAP] })
      const wrapper = mount(ApplicationSettings)
      await flushPromises()

      expect(wrapper.find('[data-testid="application-export-res-imap"]').attributes('disabled')).toBeUndefined()
      expect(wrapper.find('[data-testid="application-export-hint-res-imap"]').exists()).toBe(false)
      wrapper.unmount()
    })
  })
})
