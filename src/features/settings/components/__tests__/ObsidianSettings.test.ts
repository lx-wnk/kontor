import type { Ref } from 'vue'
import type { Grant } from '@/features/settings/composables/useGrants'
import type { SettingView } from '@/features/settings/composables/useSettings'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import ObsidianSettings from '@/features/settings/components/ObsidianSettings.vue'
import { useGrants } from '@/features/settings/composables/useGrants'
import { useSettings } from '@/features/settings/composables/useSettings'
import { selectByLabel } from '@/utils/testSelect'

vi.mock('@/features/settings/composables/useSettings', async () => {
  const actual = await vi.importActual<typeof import('@/features/settings/composables/useSettings')>('@/features/settings/composables/useSettings')
  return {
    ...actual,
    useSettings: vi.fn(),
  }
})

vi.mock('@/features/settings/composables/useGrants', async () => {
  const actual = await vi.importActual<typeof import('@/features/settings/composables/useGrants')>('@/features/settings/composables/useGrants')
  return {
    ...actual,
    useGrants: vi.fn(),
  }
})

const MASK = '********'

const baseGrant: Grant = {
  id: 'g1',
  capabilityName: 'obsidian.search',
  contextKind: 'global',
  contextRef: '',
  pattern: '',
  mode: 'allow',
  limitCount: 0,
  limitWindowSeconds: 0,
  expiresAt: null,
  grantedBy: 'alex',
  grantedAt: '2026-01-01T00:00:00Z',
  revokedAt: null,
  revokedBy: '',
  reason: '',
  nodeId: '',
}

// The Index now button is gated on GET /api/obsidian/status, fetched on
// mount — every test that exercises the button must answer that call too, or
// the button stays disabled and the click this test wants never fires.
function stubIndexFetch(indexResponse: { ok: boolean, status: number, json: () => Promise<unknown> }) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url === '/api/obsidian/status')
      return { ok: true, status: 200, json: async () => ({ configured: true }) }
    return indexResponse
  }))
}

function settingsFixture(overrides: Partial<Record<string, string>> = {}): SettingView[] {
  const values: Record<string, string> = {
    'obsidian.baseURL': 'https://127.0.0.1:27124',
    'obsidian.vaultRoot': 'claude-memory',
    'obsidian.apiKey': MASK,
    'obsidian.tlsMode': 'verify',
    ...overrides,
  }
  return [
    { key: 'obsidian.baseURL', type: 'string', value: values['obsidian.baseURL'], default: '', apply: 'restart', category: 'obsidian' },
    { key: 'obsidian.vaultRoot', type: 'string', value: values['obsidian.vaultRoot'], default: '', apply: 'restart', category: 'obsidian' },
    { key: 'obsidian.apiKey', type: 'string', value: values['obsidian.apiKey'], default: '', apply: 'restart', category: 'obsidian' },
    { key: 'obsidian.tlsMode', type: 'enum', value: values['obsidian.tlsMode'], default: 'verify', apply: 'restart', category: 'obsidian', enum: ['verify', 'pinned', 'insecure-loopback'] },
  ]
}

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('obsidianSettings', () => {
  let items: Ref<SettingView[]>
  let update: ReturnType<typeof vi.fn>
  let grants: Ref<Grant[]>
  let createGrant: ReturnType<typeof vi.fn>

  beforeEach(() => {
    items = ref(settingsFixture())
    update = vi.fn(async (key: string, value: string) => {
      items.value = items.value.map(i => (i.key === key ? { ...i, value: key === 'obsidian.apiKey' ? MASK : value } : i))
      return 'restart' as const
    })

    vi.mocked(useSettings).mockReturnValue({
      items,
      loading: ref(false),
      error: ref(null),
      refetch: vi.fn(),
      update,
    } as unknown as ReturnType<typeof useSettings>)

    // Full trio present by default — most tests exercise Index now, not the
    // grant-creation flow, and a missing grant would show the Allow indexing
    // button unasked in those.
    grants = ref([
      { ...baseGrant, id: 'g1', capabilityName: 'obsidian.search' },
      { ...baseGrant, id: 'g2', capabilityName: 'obsidian.read' },
      { ...baseGrant, id: 'g3', capabilityName: 'memory.write' },
    ])
    createGrant = vi.fn(async (input: Record<string, unknown>) => {
      const created = { ...baseGrant, id: 'g-new', capabilityName: input.capabilityName as string }
      grants.value = [created, ...grants.value]
      return created
    })

    vi.mocked(useGrants).mockReturnValue({
      grants,
      capabilities: ref([]),
      loading: ref(false),
      error: ref(null),
      fetchGrants: vi.fn(),
      createGrant,
      revokeGrant: vi.fn(),
    } as unknown as ReturnType<typeof useGrants>)
  })

  it('never renders a real API key — only the server-supplied mask', () => {
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    const apiKeyInput = wrapper.get('[data-testid="obsidian-apikey"]').element as HTMLInputElement
    expect(apiKeyInput.value).toBe(MASK)
    expect(apiKeyInput.type).toBe('password')
  })

  it('saving without touching the API key PATCHes the mask sentinel unchanged, not an empty string', async () => {
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    await wrapper.get('[data-testid="obsidian-vaultroot"]').setValue('new-vault')
    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()

    expect(update).toHaveBeenCalledWith('obsidian.apiKey', MASK)
    expect(update).not.toHaveBeenCalledWith('obsidian.apiKey', '')
    expect(update).toHaveBeenCalledWith('obsidian.vaultRoot', 'new-vault')

    const apiKeyInput = wrapper.get('[data-testid="obsidian-apikey"]').element as HTMLInputElement
    expect(apiKeyInput.value).toBe(MASK)
  })

  it('clearing every field PATCHes all three empty, which is what turns the vault off', async () => {
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    await wrapper.get('[data-testid="obsidian-baseurl"]').setValue('')
    await wrapper.get('[data-testid="obsidian-vaultroot"]').setValue('')
    await wrapper.get('[data-testid="obsidian-apikey"]').setValue('')
    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()

    // The API key is the one that could not be cleared before: the field
    // shows the mask, which the server reads as "leave unchanged", so an
    // emptied field HAD to be sent as an empty string or the encrypted row
    // survived and the next boot hit the partial trio and refused to start.
    expect(update).toHaveBeenCalledWith('obsidian.baseURL', '')
    expect(update).toHaveBeenCalledWith('obsidian.vaultRoot', '')
    expect(update).toHaveBeenCalledWith('obsidian.apiKey', '')
  })

  it('sends a freshly typed API key', async () => {
    items.value = settingsFixture({ 'obsidian.apiKey': '' })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    await wrapper.get('[data-testid="obsidian-apikey"]').setValue('real-secret-key')
    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()

    expect(update).toHaveBeenCalledWith('obsidian.apiKey', 'real-secret-key')
  })

  it('renders the seeded TLS mode value, not the pre-load default', () => {
    items.value = settingsFixture({ 'obsidian.tlsMode': 'pinned' })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    expect(wrapper.get('[data-testid="obsidian-tlsmode"]').text()).toContain('pinned')
  })

  it('selecting a different TLS mode and saving PATCHes the chosen value', async () => {
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    await selectByLabel(wrapper.get('[data-testid="obsidian-tlsmode"]').element, 'insecure-loopback')
    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()

    expect(update).toHaveBeenCalledWith('obsidian.tlsMode', 'insecure-loopback')
  })

  it('surfaces the restart requirement after a successful save', async () => {
    const toastMod = await import('@/composables/useToast')
    const successSpy = vi.spyOn(toastMod.toast, 'success')
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()

    expect(successSpy).toHaveBeenCalledWith(expect.stringContaining('restart'))
  })

  it('blocks the save when the trio is incomplete — that state fails the server boot', async () => {
    items.value = settingsFixture({ 'obsidian.vaultRoot': '' })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    expect(wrapper.find('[data-testid="obsidian-trio-warning"]').exists()).toBe(true)

    const saveButton = wrapper.get('[data-testid="obsidian-save"]')
    expect((saveButton.element as HTMLButtonElement).disabled).toBe(true)

    await saveButton.trigger('click')
    await flushPromises()
    expect(update).not.toHaveBeenCalled()
  })

  it('allows the save once the missing member of the trio is filled in', async () => {
    items.value = settingsFixture({ 'obsidian.vaultRoot': '' })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })

    await wrapper.get('[data-testid="obsidian-vaultroot"]').setValue('claude-memory')
    expect(wrapper.find('[data-testid="obsidian-trio-warning"]').exists()).toBe(false)

    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()
    expect(update).toHaveBeenCalledWith('obsidian.vaultRoot', 'claude-memory')
  })

  it('shows no trio warning when the vault is fully configured', () => {
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    expect(wrapper.find('[data-testid="obsidian-trio-warning"]').exists()).toBe(false)
  })

  it('shows no trio warning when the vault is entirely unconfigured', () => {
    items.value = settingsFixture({ 'obsidian.baseURL': '', 'obsidian.vaultRoot': '', 'obsidian.apiKey': '' })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    expect(wrapper.find('[data-testid="obsidian-trio-warning"]').exists()).toBe(false)
  })

  it('reports the indexed count on success', async () => {
    stubIndexFetch({ ok: true, status: 200, json: async () => ({ indexed: 7 }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/obsidian/index', expect.objectContaining({ method: 'POST' }))
    expect(wrapper.get('[data-testid="obsidian-index-result"]').text()).toContain('7')
  })

  it('turns a 403 denial into a readable message, not a raw status code', async () => {
    stubIndexFetch({ ok: false, status: 403, json: async () => ({ error: 'capability denied' }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    const text = wrapper.get('[data-testid="obsidian-index-result"]').text()
    expect(text).not.toContain('403')
    expect(text.toLowerCase()).toContain('grant')
  })

  it('turns a 503 unconfigured-vault response into a readable message, not a raw status code', async () => {
    stubIndexFetch({ ok: false, status: 503, json: async () => ({ error: 'obsidian vault not configured' }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    const text = wrapper.get('[data-testid="obsidian-index-result"]').text()
    expect(text).not.toContain('503')
    expect(text.toLowerCase()).toContain('not configured')
  })

  it('turns a 409 in-progress response into a readable message, not a raw status code', async () => {
    stubIndexFetch({ ok: false, status: 409, json: async () => ({ error: 'an obsidian index run is already in progress' }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    const text = wrapper.get('[data-testid="obsidian-index-result"]').text()
    expect(text).not.toContain('409')
    expect(text.toLowerCase()).toContain('already')
  })

  it('shows the unreachable error and hint, and disables Index now, when status reports reachable: false', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        configured: true,
        reachable: false,
        error: 'certificate not trusted',
        hint: 'The vault uses a self-signed certificate: set TLS mode to "insecure-loopback" (127.0.0.1 only) or "pinned".',
      }),
    }))
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    const indexButton = wrapper.get('[data-testid="obsidian-index"]').element as HTMLButtonElement
    expect(indexButton.disabled).toBe(true)
    const hint = wrapper.get('[data-testid="obsidian-unreachable-hint"]').text()
    expect(hint).toContain('certificate not trusted')
    expect(hint).toContain('insecure-loopback')
  })

  it('renders a grants link on a 403 denial and clicking it emits open-grants', async () => {
    stubIndexFetch({ ok: false, status: 403, json: async () => ({ error: 'capability denied' }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    const grantsLink = wrapper.get('[data-testid="obsidian-open-grants"]')
    await grantsLink.trigger('click')
    expect(wrapper.emitted('openGrants')).toHaveLength(1)
  })

  it('disables Index now and shows a hint when GET /api/obsidian/status reports the vault is not configured', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ configured: false }) }))
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/obsidian/status')
    const indexButton = wrapper.get('[data-testid="obsidian-index"]').element as HTMLButtonElement
    expect(indexButton.disabled).toBe(true)
    expect(wrapper.get('[data-testid="obsidian-index-unconfigured-hint"]').text()).toContain('Save a base URL, vault root and API key first.')
  })

  it('enables Index now once a save flips the status to configured', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ configured: false }) })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()
    expect((wrapper.get('[data-testid="obsidian-index"]').element as HTMLButtonElement).disabled).toBe(true)

    fetchMock.mockResolvedValue({ ok: true, status: 200, json: async () => ({ configured: true }) })
    await wrapper.get('[data-testid="obsidian-save"]').trigger('click')
    await flushPromises()

    expect((wrapper.get('[data-testid="obsidian-index"]').element as HTMLButtonElement).disabled).toBe(false)
    expect(wrapper.find('[data-testid="obsidian-index-unconfigured-hint"]').exists()).toBe(false)
  })

  it('shows Allow indexing on a 403 denial', async () => {
    stubIndexFetch({ ok: false, status: 403, json: async () => ({ error: 'capability denied' }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="obsidian-grant-indexing"]').exists()).toBe(true)
  })

  it('shows Allow indexing proactively when a required grant is missing, without a 403', async () => {
    grants.value = grants.value.filter(g => g.capabilityName !== 'memory.write')
    stubIndexFetch({ ok: true, status: 200, json: async () => ({ indexed: 0 }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    expect(wrapper.find('[data-testid="obsidian-grant-indexing"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="obsidian-grant-indexing"]').attributes('title')).toBe('Grants memory.write as a global, unlimited allow.')
  })

  it('hides Allow indexing when all three required grants already exist', () => {
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    expect(wrapper.find('[data-testid="obsidian-grant-indexing"]').exists()).toBe(false)
  })

  it('clicking Allow indexing POSTs exactly the missing grants with the right body, then confirms', async () => {
    grants.value = grants.value.filter(g => g.capabilityName === 'obsidian.search')
    stubIndexFetch({ ok: false, status: 403, json: async () => ({ error: 'capability denied' }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-index"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-grant-indexing"]').trigger('click')
    await flushPromises()

    expect(createGrant).toHaveBeenCalledTimes(2)
    expect(createGrant).toHaveBeenCalledWith({
      capabilityName: 'obsidian.read',
      contextKind: 'global',
      contextRef: '',
      pattern: '',
      mode: 'allow',
      limitCount: 0,
      limitWindowSeconds: 0,
      reason: 'Obsidian indexing (created from the Obsidian settings)',
    })
    expect(createGrant).toHaveBeenCalledWith({
      capabilityName: 'memory.write',
      contextKind: 'global',
      contextRef: '',
      pattern: '',
      mode: 'allow',
      limitCount: 0,
      limitWindowSeconds: 0,
      reason: 'Obsidian indexing (created from the Obsidian settings)',
    })
    expect(createGrant).not.toHaveBeenCalledWith(expect.objectContaining({ capabilityName: 'obsidian.search' }))

    expect(wrapper.find('[data-testid="obsidian-grant-indexing"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="obsidian-grant-confirmation"]').text()).toContain('Indexing allowed')
    expect(wrapper.find('[data-testid="obsidian-index-result"]').exists()).toBe(false)
  })

  it('renders an inline error when creating a grant fails', async () => {
    grants.value = grants.value.filter(g => g.capabilityName !== 'memory.write')
    createGrant.mockRejectedValueOnce(new Error('server exploded'))
    stubIndexFetch({ ok: true, status: 200, json: async () => ({ indexed: 0 }) })
    const wrapper = mount(ObsidianSettings, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="obsidian-grant-indexing"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="obsidian-grant-error"]').text()).toContain('server exploded')
  })
})
