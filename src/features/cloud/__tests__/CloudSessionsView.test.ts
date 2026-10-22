import type { CloudAccount, CloudSessionView } from '../composables/useCloudSessions'
import type { Spawner } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref, shallowRef } from 'vue'
import { selectOptionsById } from '@/utils/testSelect'
import CloudSessionsView from '../CloudSessionsView.vue'

const state = vi.hoisted(() => ({
  sendMessage: vi.fn(),
  importSession: vi.fn(),
  removeSession: vi.fn(),
  refresh: vi.fn(),
}))

const sessions = shallowRef<CloudSessionView[]>([])
const accounts = shallowRef<CloudAccount[]>([])
const spawners = shallowRef<Spawner[]>([])

vi.mock('../composables/useCloudSessions', () => ({
  useCloudSessions: () => ({ sessions, isLoading: ref(false), error: ref(null) }),
  useCloudAccounts: () => ({ accounts, isLoading: ref(false), error: ref(null), refresh: state.refresh }),
  sendMessage: state.sendMessage,
  importSession: state.importSession,
  removeSession: state.removeSession,
}))

vi.mock('@/composables/useSpawners', () => ({
  useSpawners: () => ({ spawners }),
}))

function makeSession(id: string, overrides: Partial<CloudSessionView> = {}): CloudSessionView {
  return {
    id,
    sessionId: `session_${id}`,
    url: `https://claude.ai/code/session_${id}`,
    spawnerId: 'sp1',
    title: `Session ${id}`,
    origin: 'imported',
    lastMessage: '',
    lastMessageAt: null,
    lastError: '',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

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

function makeAccount(configDir: string, spawnerIds: string[], overrides: Partial<CloudAccount> = {}): CloudAccount {
  return {
    configDir,
    spawners: spawnerIds.map(id => ({ id, name: `Spawner ${id}` })),
    auth: { loggedIn: true, authMethod: 'claude.ai', apiProvider: 'firstParty', subscriptionType: 'max' },
    ...overrides,
  }
}

function mountView() {
  return mount(CloudSessionsView)
}

beforeEach(() => {
  vi.clearAllMocks()
  sessions.value = []
  accounts.value = []
  spawners.value = [makeSpawner('sp1', 'claude')]
})

describe('cloudSessionsView', () => {
  it('shows an empty state without sessions', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('No cloud sessions')
  })

  it('groups sessions by account and puts unmapped spawners under Unknown account', () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1']), makeAccount('/home/b/.claude', ['sp2'])]
    sessions.value = [
      makeSession('1', { spawnerId: 'sp1' }),
      makeSession('2', { spawnerId: 'sp2' }),
      makeSession('3', { spawnerId: 'sp1' }),
      makeSession('4', { spawnerId: 'gone' }),
    ]
    const wrapper = mountView()
    const groups = wrapper.findAll('[data-testid="cloud-account"]')
    expect(groups).toHaveLength(3)
    expect(groups[0]!.text()).toContain('/home/a/.claude')
    expect(groups[0]!.findAll('[data-testid="cloud-session"]')).toHaveLength(2)
    expect(groups[1]!.text()).toContain('/home/b/.claude')
    expect(groups[1]!.findAll('[data-testid="cloud-session"]')).toHaveLength(1)
    expect(groups[2]!.text()).toContain('Unknown account')
    expect(groups[2]!.text()).toContain('Session 4')
  })

  it('shows Ready for a claude.ai login and the login hint otherwise', () => {
    accounts.value = [
      makeAccount('/home/a/.claude', ['sp1']),
      makeAccount('/home/b/.claude', ['sp2'], { auth: { loggedIn: true, authMethod: 'api_key', apiProvider: 'firstParty', subscriptionType: '' } }),
    ]
    sessions.value = [makeSession('1', { spawnerId: 'sp1' }), makeSession('2', { spawnerId: 'sp2' })]
    const groups = mountView().findAll('[data-testid="cloud-account"]')
    expect(groups[0]!.text()).toContain('Ready')
    expect(groups[0]!.text()).not.toContain('claude auth login')
    expect(groups[1]!.text()).toContain('Not signed in with claude.ai. Run: CLAUDE_CONFIG_DIR=/home/b/.claude claude auth login')
  })

  it('shows the account error and refreshes on request', async () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'], { auth: null, error: 'claude not found' })]
    sessions.value = [makeSession('1')]
    const wrapper = mountView()
    expect(wrapper.text()).toContain('claude not found')
    await wrapper.find('[data-testid="cloud-refresh"]').trigger('click')
    expect(state.refresh).toHaveBeenCalledOnce()
  })

  it('renders the origin chip, last message and the error badge with its tooltip', () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
    sessions.value = [makeSession('1', { origin: 'routine', lastMessage: 'ping', lastError: 'Session not found' })]
    const wrapper = mountView()
    expect(wrapper.text()).toContain('routine')
    expect(wrapper.text()).toContain('ping')
    expect(wrapper.find('[title="Session not found"]').exists()).toBe(true)
  })

  it('links to claude.ai in a new tab with noopener', () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
    sessions.value = [makeSession('1')]
    const link = mountView().find('a[href="https://claude.ai/code/session_1"]')
    expect(link.text()).toBe('Open in claude.ai')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener')
  })

  describe('send', () => {
    beforeEach(() => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
      sessions.value = [makeSession('1')]
    })

    it('posts the text and clears the box on success', async () => {
      state.sendMessage.mockResolvedValue(makeSession('1'))
      const wrapper = mountView()
      const box = wrapper.find<HTMLTextAreaElement>('[data-testid="send-box-1"]')
      await box.setValue('hello')
      await wrapper.find('[data-testid="send-1"]').trigger('click')
      await flushPromises()
      expect(state.sendMessage).toHaveBeenCalledWith('1', 'hello')
      expect(box.element.value).toBe('')
    })

    it('keeps the text and shows the error when the send fails', async () => {
      state.sendMessage.mockRejectedValue(new Error('claude exited 1'))
      const wrapper = mountView()
      const box = wrapper.find<HTMLTextAreaElement>('[data-testid="send-box-1"]')
      await box.setValue('hello')
      await wrapper.find('[data-testid="send-1"]').trigger('click')
      await flushPromises()
      expect(box.element.value).toBe('hello')
      expect(wrapper.text()).toContain('claude exited 1')
    })

    it('disables the box while a send is in flight', async () => {
      let resolve!: (v: CloudSessionView) => void
      state.sendMessage.mockReturnValue(new Promise<CloudSessionView>(r => (resolve = r)))
      const wrapper = mountView()
      await wrapper.find('[data-testid="send-box-1"]').setValue('hello')
      await wrapper.find('[data-testid="send-1"]').trigger('click')
      expect(wrapper.find('[data-testid="send-box-1"]').attributes('disabled')).toBeDefined()
      expect(wrapper.find('[data-testid="send-1"]').attributes('disabled')).toBeDefined()
      resolve(makeSession('1'))
      await flushPromises()
      expect(wrapper.find('[data-testid="send-box-1"]').attributes('disabled')).toBeUndefined()
    })
  })

  describe('remove', () => {
    beforeEach(() => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
      sessions.value = [makeSession('1')]
    })

    it('asks for confirmation with the exact text before removing', async () => {
      state.removeSession.mockResolvedValue(undefined)
      const wrapper = mountView()
      await wrapper.find('[data-testid="remove-1"]').trigger('click')
      expect(wrapper.text()).toContain('Removes the session from Kontor only. It stays on claude.ai.')
      expect(state.removeSession).not.toHaveBeenCalled()
      await wrapper.find('[data-testid="remove-confirm-1"]').trigger('click')
      expect(state.removeSession).toHaveBeenCalledWith('1')
    })

    it('cancels the confirmation', async () => {
      const wrapper = mountView()
      await wrapper.find('[data-testid="remove-1"]').trigger('click')
      await wrapper.find('[data-testid="remove-cancel-1"]').trigger('click')
      expect(wrapper.text()).not.toContain('Removes the session from Kontor only.')
      expect(state.removeSession).not.toHaveBeenCalled()
    })
  })

  describe('import', () => {
    it('offers only claude spawners, including those with an empty adapter type', () => {
      spawners.value = [
        makeSpawner('a', 'claude'),
        makeSpawner('b', 'ollama'),
        makeSpawner('c', '' as Spawner['adapterType']),
        makeSpawner('d', 'custom'),
      ]
      const options = selectOptionsById(mountView(), 'cloud-import-spawner')
      expect(options.map(o => o.value)).toEqual(['a', 'c'])
    })

    it('posts the import and shows the server error inline', async () => {
      state.importSession.mockRejectedValue(new Error('session not found'))
      const wrapper = mountView()
      await wrapper.find('[data-testid="import-url"]').setValue('https://claude.ai/code/session_x')
      await wrapper.find('[data-testid="import-title"]').setValue('My title')
      await wrapper.find('[data-testid="import-submit"]').trigger('click')
      await flushPromises()
      expect(state.importSession).toHaveBeenCalledWith({
        spawnerId: 'sp1',
        url: 'https://claude.ai/code/session_x',
        title: 'My title',
      })
      expect(wrapper.text()).toContain('session not found')
    })
  })
})
