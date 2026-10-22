import type { CloudRoutineView } from '../composables/useCloudRoutines'
import type { CloudSessionView } from '../composables/useCloudSessions'
import type { Spawner } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowRef } from 'vue'

class MockEventSource {
  static CLOSED = 2
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  close() {
    this.readyState = MockEventSource.CLOSED
  }
}

const spawners = shallowRef<Spawner[]>([])

vi.mock('@/composables/useSpawners', () => ({
  useSpawners: () => ({ spawners }),
}))

const SECRET_TOKEN = 'sk-ant-oat01-super-secret'

function makeSpawner(id: string, adapterType: Spawner['adapterType']): Spawner {
  return {
    id,
    name: `Spawner ${id}`,
    slug: id,
    command: 'claude',
    args: [],
    env: {},
    adapterType,
    adapterConfig: {},
    builtIn: false,
    isDefault: false,
    createdAt: '',
    updatedAt: '',
  }
}

function makeRoutine(id: string, overrides: Partial<CloudRoutineView> = {}): CloudRoutineView {
  return {
    id,
    name: `Routine ${id}`,
    fireUrl: `https://api.anthropic.com/v1/claude_code/routines/trig_${id}/fire`,
    spawnerId: 'sp1',
    hasToken: true,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function makeSession(id: string, overrides: Partial<CloudSessionView> = {}): CloudSessionView {
  return {
    id,
    sessionId: `session_${id}`,
    url: `https://claude.ai/code/session_${id}`,
    spawnerId: 'sp1',
    title: `Fired ${id}`,
    origin: 'routine',
    lastMessage: '',
    lastMessageAt: null,
    lastError: '',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function jsonResponse(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) }
}

type Responder = (url: string, init?: RequestInit) => ReturnType<typeof jsonResponse>

let preview = true
let routines: CloudRoutineView[] = []
let override: Responder | null = null

function respond(url: string, init?: RequestInit) {
  const custom = override?.(url, init)
  if (custom)
    return custom
  if (url === '/api/settings')
    return jsonResponse(200, [{ key: 'features.preview', type: 'bool', value: String(preview), default: 'false', apply: 'live', category: 'features' }])
  if (url === '/api/cloud-routines')
    return jsonResponse(200, routines)
  return jsonResponse(200, [])
}

function urlsCalled(): string[] {
  return vi.mocked(fetch).mock.calls.map(([url]) => String(url))
}

function callTo(url: string, method: string) {
  return vi.mocked(fetch).mock.calls.find(([u, init]) => u === url && (init as RequestInit | undefined)?.method === method)
}

async function mountView() {
  const { default: CloudSessionsView } = await import('../CloudSessionsView.vue')
  const wrapper = mount(CloudSessionsView, { attachTo: document.body })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  preview = true
  routines = [makeRoutine('r1')]
  override = null
  spawners.value = [makeSpawner('sp1', 'claude'), makeSpawner('sp2', 'codex')]
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => respond(url, init)))
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('cloud routines section', () => {
  it('is hidden and makes no routine requests while preview is off', async () => {
    preview = false

    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(false)
    expect(urlsCalled().filter(u => u.startsWith('/api/cloud-routines'))).toEqual([])
    wrapper.unmount()
  })

  it('renders the Preview badge and the stored routines without any token', async () => {
    const wrapper = await mountView()

    const section = wrapper.get('[data-testid="routines-section"]')
    expect(section.text()).toContain('Preview')
    expect(section.text()).toContain('Routine r1')
    expect(section.text()).toContain('token stored')
    wrapper.unmount()
  })

  it('create sends the body, clears the token field and never renders the token', async () => {
    const wrapper = await mountView()
    const token = wrapper.get('[data-testid="routine-token"]')
    expect(token.attributes('type')).toBe('password')
    expect(token.attributes('autocomplete')).toBe('off')

    await wrapper.get('[data-testid="routine-name"]').setValue('Nightly')
    await wrapper.get('[data-testid="routine-url"]').setValue('https://api.anthropic.com/v1/claude_code/routines/trig_n/fire')
    await token.setValue(SECRET_TOKEN)
    await wrapper.get('[data-testid="routine-create-form"]').trigger('submit')
    await flushPromises()

    const post = callTo('/api/cloud-routines', 'POST')
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({
      name: 'Nightly',
      fireUrl: 'https://api.anthropic.com/v1/claude_code/routines/trig_n/fire',
      spawnerId: 'sp1',
      token: SECRET_TOKEN,
    })
    expect((token.element as HTMLInputElement).value).toBe('')
    expect(document.body.innerHTML).not.toContain(SECRET_TOKEN)
    wrapper.unmount()
  })

  it('edit with an empty token keeps the stored one and says so', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    expect(wrapper.get('[data-testid="edit-form-r1"]').text()).toContain('Leave empty to keep the stored token')
    await wrapper.get('[data-testid="edit-name-r1"]').setValue('Renamed')
    await wrapper.get('[data-testid="edit-form-r1"]').trigger('submit')
    await flushPromises()

    const patch = callTo('/api/cloud-routines/r1', 'PATCH')
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({
      name: 'Renamed',
      fireUrl: 'https://api.anthropic.com/v1/claude_code/routines/trig_r1/fire',
    })
    wrapper.unmount()
  })

  it('fire sends the optional text, shows the session link and adds the session to the list', async () => {
    override = (url, init) => url === '/api/cloud-routines/r1/fire' && init?.method === 'POST'
      ? jsonResponse(201, makeSession('s1'))
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-text-r1"]').setValue('run it')
    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    const post = callTo('/api/cloud-routines/r1/fire', 'POST')
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ text: 'run it' })
    const result = wrapper.get('[data-testid="fire-result-r1"]')
    expect(result.text()).toContain('Fired s1')
    expect(result.get('a').attributes('href')).toBe('https://claude.ai/code/session_s1')
    expect(wrapper.findAll('[data-testid="cloud-session"]').map(s => s.text())).toEqual([expect.stringContaining('Fired s1')])
    wrapper.unmount()
  })

  it('disables the fire button while firing', async () => {
    let release: (value: ReturnType<typeof jsonResponse>) => void = () => {}
    const pending = new Promise<ReturnType<typeof jsonResponse>>((resolve) => {
      release = resolve
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) =>
      url === '/api/cloud-routines/r1/fire' ? pending : respond(url, init)))
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-r1"]').trigger('click')

    const button = wrapper.get('[data-testid="fire-r1"]')
    expect(button.text()).toBe('Firing…')
    expect(button.attributes('disabled')).toBeDefined()
    release(jsonResponse(201, makeSession('s1')))
    await flushPromises()
    expect(wrapper.get('[data-testid="fire-r1"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('renders no Open link for a url outside claude.ai/code', async () => {
    override = url => url === '/api/cloud-routines/r1/fire'
      ? jsonResponse(201, makeSession('s1', { url: 'javascript:alert(1)' }))
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="fire-result-r1"]').find('a').exists()).toBe(false)
    wrapper.unmount()
  })

  it('a failed fire shows the upstream status and body as plain text', async () => {
    override = url => url === '/api/cloud-routines/r1/fire'
      ? jsonResponse(502, { error: 'upstream', status: 429, body: '<b>rate limited</b>' })
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    const alert = wrapper.get('[data-testid="fire-error-r1"]')
    expect(alert.attributes('role')).toBe('alert')
    expect(alert.text()).toContain('HTTP 429')
    expect(alert.text()).toContain('<b>rate limited</b>')
    expect(alert.find('b').exists()).toBe(false)
    wrapper.unmount()
  })

  it('a 409 shows the server message', async () => {
    override = url => url === '/api/cloud-routines/r1/fire'
      ? jsonResponse(409, { error: 'routine is already firing' })
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="fire-error-r1"]').text()).toContain('routine is already firing')
    wrapper.unmount()
  })

  it('delete needs a confirm step and moves focus to it', async () => {
    override = (url, init) => url === '/api/cloud-routines/r1' && init?.method === 'DELETE'
      ? { ok: true, status: 204, json: () => Promise.resolve({}) }
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="delete-r1"]').trigger('click')
    await flushPromises()
    expect(callTo('/api/cloud-routines/r1', 'DELETE')).toBeUndefined()
    const confirm = wrapper.get('[data-testid="delete-confirm-r1"]')
    expect(confirm.attributes('aria-label')).toBe('Confirm deleting Routine r1')
    expect(document.activeElement).toBe(confirm.element)

    await wrapper.get('[data-testid="delete-cancel-r1"]').trigger('click')
    expect(wrapper.find('[data-testid="delete-confirm-r1"]').exists()).toBe(false)

    await wrapper.get('[data-testid="delete-r1"]').trigger('click')
    await wrapper.get('[data-testid="delete-confirm-r1"]').trigger('click')
    await flushPromises()
    expect(callTo('/api/cloud-routines/r1', 'DELETE')).toBeDefined()
    wrapper.unmount()
  })
})
