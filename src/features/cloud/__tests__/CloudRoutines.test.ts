import type { CloudRoutineView } from '../composables/useCloudRoutines'
import type { CloudSessionView } from '../composables/useCloudSessions'
import type { Spawner } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowRef } from 'vue'
import { selectOptionsById } from '@/utils/testSelect'

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

type Responder = (url: string, init?: RequestInit) => ReturnType<typeof jsonResponse> | null

let preview = true
let previewRaw: string | null = null
let routines: CloudRoutineView[] = []
let override: Responder | null = null

function respond(url: string, init?: RequestInit) {
  const custom = override?.(url, init)
  if (custom)
    return custom
  if (url === '/api/settings')
    return jsonResponse(200, [{ key: 'features.preview', type: 'bool', value: previewRaw ?? String(preview), default: 'false', apply: 'live', category: 'features' }])
  if (url === '/api/settings/features.preview' && init?.method === 'PATCH') {
    const value = (JSON.parse(String(init.body)) as { value: string }).value
    preview = value === 'true'
    return jsonResponse(200, { key: 'features.preview', value, applied: 'live' })
  }
  if (url === '/api/cloud-routines')
    return jsonResponse(200, structuredClone(routines))
  return jsonResponse(200, [])
}

function urlsCalled(): string[] {
  return vi.mocked(fetch).mock.calls.map(([url]) => String(url))
}

function callTo(url: string, method: string) {
  return vi.mocked(fetch).mock.calls.find(([u, init]) => u === url && (init as RequestInit | undefined)?.method === method)
}

function secretLocations(secret: string): string[] {
  const found: string[] = []
  for (const el of document.body.querySelectorAll('*')) {
    const tag = el.tagName.toLowerCase()
    if (el instanceof HTMLInputElement && el.value.includes(secret))
      found.push(`${tag}.value`)
    for (const attr of el.attributes) {
      if (attr.value.includes(secret))
        found.push(`${tag}[${attr.name}]`)
    }
    for (const node of el.childNodes) {
      if (node.nodeType === Node.TEXT_NODE && node.textContent?.includes(secret))
        found.push(`${tag} text`)
    }
  }
  return found
}

function editState(wrapper: Awaited<ReturnType<typeof mountView>>) {
  const section = wrapper.findComponent({ name: 'CloudRoutinesSection' })
  return (section.vm as unknown as { edit: { name: string, fireUrl: string, token: string } }).edit
}

async function mountView() {
  const { default: CloudSessionsView } = await import('../CloudSessionsView.vue')
  const wrapper = mount(CloudSessionsView, { attachTo: document.body })
  await flushPromises()
  return wrapper
}

async function switchPreview(value: 'true' | 'false') {
  const { useSettings } = await import('@/features/settings/composables/useSettings')
  await useSettings().update('features.preview', value)
  await flushPromises()
}

beforeEach(() => {
  preview = true
  previewRaw = null
  routines = [makeRoutine('r1')]
  override = null
  spawners.value = [makeSpawner('sp1', 'claude'), makeSpawner('sp2', 'ollama')]
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
    expect(token.attributes('autocomplete')).toBe('new-password')

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
    expect(secretLocations(SECRET_TOKEN)).toEqual([])
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
    expect(result.attributes('role')).toBe('status')
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
    expect(button.attributes('aria-disabled')).toBe('true')
    expect(button.attributes('disabled')).toBeUndefined()
    await button.trigger('click')
    expect(vi.mocked(fetch).mock.calls.filter(([u]) => u === '/api/cloud-routines/r1/fire')).toHaveLength(1)
    release(jsonResponse(201, makeSession('s1')))
    await flushPromises()
    expect(wrapper.get('[data-testid="fire-r1"]').attributes('aria-disabled')).toBeUndefined()
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

  it('cancel clears the typed token from state and the DOM', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    const field = wrapper.get('[data-testid="edit-token-r1"]')
    expect(field.attributes('autocomplete')).toBe('new-password')
    await field.setValue(SECRET_TOKEN)
    expect(editState(wrapper).token).toBe(SECRET_TOKEN)
    await wrapper.get('[data-testid="edit-cancel-r1"]').trigger('click')

    expect(editState(wrapper)).toEqual({ name: '', fireUrl: '', token: '' })
    expect(wrapper.find('[data-testid="edit-token-r1"]').exists()).toBe(false)
    expect(secretLocations(SECRET_TOKEN)).toEqual([])
    wrapper.unmount()
  })

  it('a successful save clears the typed token from state', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-token-r1"]').setValue(SECRET_TOKEN)
    await wrapper.get('[data-testid="edit-form-r1"]').trigger('submit')
    await flushPromises()

    expect(JSON.parse(String(callTo('/api/cloud-routines/r1', 'PATCH')?.[1]?.body)).token).toBe(SECRET_TOKEN)
    expect(editState(wrapper).token).toBe('')
    expect(wrapper.find('[data-testid="edit-form-r1"]').exists()).toBe(false)
    expect(secretLocations(SECRET_TOKEN)).toEqual([])
    wrapper.unmount()
  })

  it('clears the typed token when the routine disappears during an edit', async () => {
    override = (url, init) => {
      if (url === '/api/cloud-routines/r1' && init?.method === 'DELETE') {
        routines = []
        return { ok: true, status: 204, json: () => Promise.resolve({}) }
      }
      return null
    }
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-token-r1"]').setValue(SECRET_TOKEN)
    await wrapper.get('[data-testid="delete-r1"]').trigger('click')
    await wrapper.get('[data-testid="delete-confirm-r1"]').trigger('click')
    await flushPromises()

    expect(editState(wrapper).token).toBe('')
    wrapper.unmount()
  })

  it('a failed create keeps the token for a retry and shows the error', async () => {
    override = (url, init) => url === '/api/cloud-routines' && init?.method === 'POST'
      ? jsonResponse(400, { error: 'fire url is not allowed' })
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="routine-name"]').setValue('Nightly')
    await wrapper.get('[data-testid="routine-url"]').setValue('https://example.com/fire')
    await wrapper.get('[data-testid="routine-token"]').setValue(SECRET_TOKEN)
    await wrapper.get('[data-testid="routine-create-form"]').trigger('submit')
    await flushPromises()

    expect((wrapper.get('[data-testid="routine-token"]').element as HTMLInputElement).value).toBe(SECRET_TOKEN)
    expect(wrapper.get('[data-testid="routine-create-error"]').text()).toContain('fire url is not allowed')
    wrapper.unmount()
  })

  it('moves focus into the edit form on open and back to the Edit button on Cancel', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('[data-testid="edit-name-r1"]').element)

    await wrapper.get('[data-testid="edit-cancel-r1"]').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('[data-testid="edit-r1"]').element)
    wrapper.unmount()
  })

  it('returns focus to the Edit button after a successful save', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-form-r1"]').trigger('submit')
    await flushPromises()

    expect(document.activeElement).toBe(wrapper.get('[data-testid="edit-r1"]').element)
    wrapper.unmount()
  })

  it('offers a long failure body in full behind a toggle', async () => {
    const longBody = `${'x'.repeat(400)}END`
    override = url => url === '/api/cloud-routines/r1/fire'
      ? jsonResponse(502, { error: 'upstream', status: 500, body: longBody })
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    const details = wrapper.get('[data-testid="fire-error-r1"] details')
    expect(details.get('summary').text()).toBe('Show full response')
    expect(details.text()).toContain(longBody)
    wrapper.unmount()
  })
})

describe('cloud routines preview switch', () => {
  const HINT = 'Routines are a preview feature: Settings › Server › Preview features.'

  it('points to the preview switch with one muted line while preview is off', async () => {
    preview = false

    const wrapper = await mountView()

    const hint = wrapper.get('[data-testid="routines-preview-hint"]')
    expect(hint.text()).toBe(HINT)
    expect(hint.classes()).toContain('text-fg-faint')
    wrapper.unmount()
  })

  it('shows no hint while preview is on', async () => {
    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="routines-preview-hint"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows no hint before the settings have loaded', async () => {
    const pending = new Promise<ReturnType<typeof jsonResponse>>(() => {})
    override = url => (url === '/api/settings' ? (pending as unknown as ReturnType<typeof jsonResponse>) : null)

    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="routines-preview-hint"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the section as soon as preview is switched on while the view is open', async () => {
    preview = false
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(false)

    await switchPreview('true')

    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="routines-preview-hint"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('hides the section and shows the hint when preview is switched off while the view is open', async () => {
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(true)

    await switchPreview('false')

    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="routines-preview-hint"]').text()).toBe(HINT)
    wrapper.unmount()
  })

  it.each(['1', 't', 'T', 'TRUE', 'true', 'True'])('counts "%s" as on', async (raw) => {
    previewRaw = raw

    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it.each(['0', 'f', 'false', 'yes', ''])('counts "%s" as off', async (raw) => {
    previewRaw = raw

    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="routines-section"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('cloud routines token handling', () => {
  it('renders the edit token as a password input that never autofills', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')

    const field = wrapper.get('[data-testid="edit-token-r1"]')
    expect(field.attributes('type')).toBe('password')
    expect(field.attributes('autocomplete')).toBe('new-password')
    wrapper.unmount()
  })

  it('a token typed into one routine edit is gone after Cancel and a second Edit', async () => {
    routines = [makeRoutine('r1'), makeRoutine('r2')]
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-token-r1"]').setValue(SECRET_TOKEN)
    await wrapper.get('[data-testid="edit-cancel-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-r2"]').trigger('click')

    expect((wrapper.get('[data-testid="edit-token-r2"]').element as HTMLInputElement).value).toBe('')
    expect(editState(wrapper).token).toBe('')
    expect(secretLocations(SECRET_TOKEN)).toEqual([])
    await wrapper.get('[data-testid="edit-form-r2"]').trigger('submit')
    await flushPromises()
    expect(JSON.parse(String(callTo('/api/cloud-routines/r2', 'PATCH')?.[1]?.body))).not.toHaveProperty('token')
    wrapper.unmount()
  })

  it('a token typed into one routine edit does not carry over when another Edit is opened directly', async () => {
    routines = [makeRoutine('r1'), makeRoutine('r2')]
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-token-r1"]').setValue(SECRET_TOKEN)
    await wrapper.get('[data-testid="edit-r2"]').trigger('click')

    expect((wrapper.get('[data-testid="edit-token-r2"]').element as HTMLInputElement).value).toBe('')
    expect(editState(wrapper).token).toBe('')
    expect(secretLocations(SECRET_TOKEN)).toEqual([])
    wrapper.unmount()
  })

  it('create trims the pasted token before sending it', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="routine-name"]').setValue('Nightly')
    await wrapper.get('[data-testid="routine-url"]').setValue('https://api.anthropic.com/v1/claude_code/routines/trig_n/fire')
    await wrapper.get('[data-testid="routine-token"]').setValue(`  ${SECRET_TOKEN}\n `)
    await wrapper.get('[data-testid="routine-create-form"]').trigger('submit')
    await flushPromises()

    expect(JSON.parse(String(callTo('/api/cloud-routines', 'POST')?.[1]?.body)).token).toBe(SECRET_TOKEN)
    wrapper.unmount()
  })

  it('create stays disabled for a whitespace-only token', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="routine-name"]').setValue('Nightly')
    await wrapper.get('[data-testid="routine-url"]').setValue('https://api.anthropic.com/v1/claude_code/routines/trig_n/fire')
    await wrapper.get('[data-testid="routine-token"]').setValue('   ')

    expect(wrapper.get('[data-testid="routine-create"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('edit trims the pasted token before sending it', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-token-r1"]').setValue(` ${SECRET_TOKEN}\n`)
    await wrapper.get('[data-testid="edit-form-r1"]').trigger('submit')
    await flushPromises()

    expect(JSON.parse(String(callTo('/api/cloud-routines/r1', 'PATCH')?.[1]?.body)).token).toBe(SECRET_TOKEN)
    wrapper.unmount()
  })

  it('edit with a whitespace-only token keeps the stored one', async () => {
    const wrapper = await mountView()

    await wrapper.get('[data-testid="edit-r1"]').trigger('click')
    await wrapper.get('[data-testid="edit-token-r1"]').setValue('   ')
    await wrapper.get('[data-testid="edit-form-r1"]').trigger('submit')
    await flushPromises()

    expect(JSON.parse(String(callTo('/api/cloud-routines/r1', 'PATCH')?.[1]?.body))).not.toHaveProperty('token')
    wrapper.unmount()
  })
})

describe('cloud routines spawners and fire form', () => {
  it('offers only claude spawners', async () => {
    spawners.value = [makeSpawner('sp1', 'claude'), makeSpawner('sp2', 'ollama'), makeSpawner('sp3', '' as Spawner['adapterType'])]
    const wrapper = await mountView()

    expect(selectOptionsById(wrapper, 'routine-spawner').map(o => o.value)).toEqual(['sp1', 'sp3'])
    wrapper.unmount()
  })

  it('cannot create a routine without a claude spawner', async () => {
    spawners.value = [makeSpawner('sp2', 'ollama')]
    const wrapper = await mountView()

    await wrapper.get('[data-testid="routine-name"]').setValue('Nightly')
    await wrapper.get('[data-testid="routine-url"]').setValue('https://api.anthropic.com/v1/claude_code/routines/trig_n/fire')
    await wrapper.get('[data-testid="routine-token"]').setValue(SECRET_TOKEN)

    expect(selectOptionsById(wrapper, 'routine-spawner')).toEqual([])
    expect(wrapper.get('[data-testid="routine-create"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="routines-section"]').text()).toContain('No claude spawner')
    wrapper.unmount()
  })

  it('marks the section with the info Preview badge', async () => {
    const wrapper = await mountView()

    const badge = wrapper.get('[data-testid="routines-section"]').findComponent({ name: 'AppBadge' })
    expect(badge.props('variant')).toBe('info')
    expect(badge.props('label')).toBe('Preview')
    wrapper.unmount()
  })

  it.each(['', '   '])('fire without text (%j) sends an empty object', async (typed) => {
    override = (url, init) => url === '/api/cloud-routines/r1/fire' && init?.method === 'POST'
      ? jsonResponse(201, makeSession('s1'))
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-text-r1"]').setValue(typed)
    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    expect(JSON.parse(String(callTo('/api/cloud-routines/r1/fire', 'POST')?.[1]?.body))).toEqual({})
    wrapper.unmount()
  })

  it('clears the typed text after a successful fire', async () => {
    override = url => url === '/api/cloud-routines/r1/fire' ? jsonResponse(201, makeSession('s1')) : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-text-r1"]').setValue('run it')
    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    expect((wrapper.get('[data-testid="fire-text-r1"]').element as HTMLInputElement).value).toBe('')
    wrapper.unmount()
  })

  it('keeps the typed text after a failed fire', async () => {
    override = url => url === '/api/cloud-routines/r1/fire'
      ? jsonResponse(502, { error: 'upstream', status: 429, body: 'rate limited' })
      : null
    const wrapper = await mountView()

    await wrapper.get('[data-testid="fire-text-r1"]').setValue('run it')
    await wrapper.get('[data-testid="fire-r1"]').trigger('click')
    await flushPromises()

    expect((wrapper.get('[data-testid="fire-text-r1"]').element as HTMLInputElement).value).toBe('run it')
    wrapper.unmount()
  })
})

describe('cloud routines unconfirmed fire', () => {
  const STARTED_MESSAGE = 'the routine may have started a run; check claude.ai before firing again'
  const CHECK_LABEL = 'Check claude.ai before firing again'

  function unconfirmedFor(id: string) {
    return (url: string, init?: RequestInit) => url === `/api/cloud-routines/${id}/fire` && init?.method === 'POST'
      ? jsonResponse(502, { error: STARTED_MESSAGE, mayHaveStarted: true })
      : null
  }

  async function fireRoutine(wrapper: Awaited<ReturnType<typeof mountView>>, id: string) {
    await wrapper.get(`[data-testid="fire-${id}"]`).trigger('click')
    await flushPromises()
  }

  it('shows the server message as it is and asks to check claude.ai on that routine only', async () => {
    routines = [makeRoutine('r1'), makeRoutine('r2')]
    override = unconfirmedFor('r1')
    const wrapper = await mountView()

    await fireRoutine(wrapper, 'r1')

    const alert = wrapper.get('[data-testid="fire-error-r1"]')
    expect(alert.attributes('role')).toBe('alert')
    expect(alert.text()).toBe(STARTED_MESSAGE)
    const button = wrapper.get('[data-testid="fire-r1"]')
    expect(button.text()).toBe(CHECK_LABEL)
    expect(button.attributes('aria-label')).toContain(CHECK_LABEL)
    expect(button.attributes('aria-label')).toContain('Routine r1')
    expect(wrapper.get('[data-testid="fire-r2"]').text()).toBe('Fire')
    wrapper.unmount()
  })

  it('keeps the button usable so the user can still fire on purpose', async () => {
    override = unconfirmedFor('r1')
    const wrapper = await mountView()

    await fireRoutine(wrapper, 'r1')
    await fireRoutine(wrapper, 'r1')

    expect(wrapper.get('[data-testid="fire-r1"]').attributes('aria-disabled')).toBeUndefined()
    expect(vi.mocked(fetch).mock.calls.filter(([u]) => u === '/api/cloud-routines/r1/fire')).toHaveLength(2)
    wrapper.unmount()
  })

  it('detects the warning by the mayHaveStarted flag, not by the message text', async () => {
    override = url => url === '/api/cloud-routines/r1/fire' ? jsonResponse(502, { error: STARTED_MESSAGE }) : null
    const wrapper = await mountView()

    await fireRoutine(wrapper, 'r1')

    expect(wrapper.get('[data-testid="fire-error-r1"]').text()).toBe(STARTED_MESSAGE)
    expect(wrapper.get('[data-testid="fire-r1"]').text()).toBe('Fire')
    wrapper.unmount()
  })

  it('does not warn when the API refused the fire', async () => {
    override = url => url === '/api/cloud-routines/r1/fire'
      ? jsonResponse(502, { error: 'routine fire failed with HTTP 429', status: 429, body: 'slow down' })
      : null
    const wrapper = await mountView()

    await fireRoutine(wrapper, 'r1')

    expect(wrapper.get('[data-testid="fire-error-r1"]').text()).toContain('HTTP 429')
    expect(wrapper.get('[data-testid="fire-r1"]').text()).toBe('Fire')
    wrapper.unmount()
  })

  it('goes back to Fire after the next successful list refresh', async () => {
    routines = [makeRoutine('r1'), makeRoutine('r2')]
    override = unconfirmedFor('r1')
    const wrapper = await mountView()
    await fireRoutine(wrapper, 'r1')
    expect(wrapper.get('[data-testid="fire-r1"]').text()).toBe(CHECK_LABEL)

    await wrapper.get('[data-testid="edit-r2"]').trigger('click')
    await wrapper.get('[data-testid="edit-form-r2"]').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-testid="fire-r1"]').text()).toBe('Fire')
    wrapper.unmount()
  })

  it('goes back to Fire after a successful fire', async () => {
    const calls: number[] = []
    override = (url, init) => {
      if (url !== '/api/cloud-routines/r1/fire' || init?.method !== 'POST')
        return null
      calls.push(1)
      return calls.length === 1 ? jsonResponse(502, { error: STARTED_MESSAGE, mayHaveStarted: true }) : jsonResponse(201, makeSession('s1'))
    }
    const wrapper = await mountView()

    await fireRoutine(wrapper, 'r1')
    expect(wrapper.get('[data-testid="fire-r1"]').text()).toBe(CHECK_LABEL)
    await fireRoutine(wrapper, 'r1')

    expect(wrapper.get('[data-testid="fire-r1"]').text()).toBe('Fire')
    wrapper.unmount()
  })
})
