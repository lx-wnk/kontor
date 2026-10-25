import type { CloudAccount, CloudSessionView } from '../composables/useCloudSessions'
import type { Project, ProjectFolder, Spawner } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref, shallowRef } from 'vue'
import { selectByLabel, selectOptionsById } from '@/utils/testSelect'
import CloudSessionsView from '../CloudSessionsView.vue'
import { StartUnconfirmedError } from '../composables/useCloudSessions'

const state = vi.hoisted(() => ({
  sendMessage: vi.fn(),
  importSession: vi.fn(),
  startSession: vi.fn(),
  fetchProjectFolders: vi.fn(),
  removeSession: vi.fn(),
  refresh: vi.fn(),
  refetchAccounts: vi.fn(),
  refetch: vi.fn(),
}))

const sessions = shallowRef<CloudSessionView[]>([])
const accounts = shallowRef<CloudAccount[]>([])
const spawners = shallowRef<Spawner[]>([])
const projects = shallowRef<Project[]>([])
const accountsLoading = ref(false)
const sessionsError = ref<string | null>(null)
const accountsError = ref<string | null>(null)

vi.mock('../composables/useCloudSessions', async importOriginal => ({
  StartUnconfirmedError: (await importOriginal<typeof import('../composables/useCloudSessions')>()).StartUnconfirmedError,
  useCloudSessions: () => ({ sessions, isLoading: ref(false), error: sessionsError, refetch: state.refetch }),
  useCloudAccounts: () => ({ accounts, isLoading: accountsLoading, error: accountsError, refresh: state.refresh, refetch: state.refetchAccounts }),
  sendMessage: state.sendMessage,
  importSession: state.importSession,
  startSession: state.startSession,
  removeSession: state.removeSession,
}))

vi.mock('@/composables/useProjects', () => ({
  useProjects: () => ({ projects }),
}))

vi.mock('@/composables/useProjectFolders', () => ({
  fetchProjectFolders: state.fetchProjectFolders,
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

function makeProject(id: string, name: string): Project {
  return { id, slug: id, name, createdAt: '', updatedAt: '' }
}

function makeFolder(id: string, overrides: Partial<ProjectFolder> = {}): ProjectFolder {
  return { id, projectId: 'p1', path: `/work/${id}`, isDefault: false, createdAt: '', ...overrides }
}

function makeAccount(configDir: string, spawnerIds: string[], overrides: Partial<CloudAccount> = {}): CloudAccount {
  return {
    configDir,
    spawners: spawnerIds.map(id => ({ id, name: `Spawner ${id}` })),
    auth: { loggedIn: true, authMethod: 'claude.ai', apiProvider: 'firstParty', subscriptionType: 'max' },
    ready: true,
    loginHint: `CLAUDE_CONFIG_DIR=${configDir} claude auth login`,
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
  accountsLoading.value = false
  sessionsError.value = null
  accountsError.value = null
  spawners.value = [makeSpawner('sp1', 'claude')]
  projects.value = [makeProject('p1', 'Project One')]
  state.fetchProjectFolders.mockResolvedValue([
    makeFolder('f1', { label: 'Docs' }),
    makeFolder('f2', { isDefault: true }),
  ])
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('cloudSessionsView', () => {
  it('shows an empty state without sessions', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('No cloud sessions')
  })

  it('shows a loading state instead of grouping under Unknown account while accounts load', () => {
    accountsLoading.value = true
    sessions.value = [makeSession('1')]
    const wrapper = mountView()
    expect(wrapper.text()).toContain('Loading accounts')
    expect(wrapper.text()).not.toContain('Unknown account')
    expect(wrapper.find('[data-testid="cloud-session"]').exists()).toBe(false)
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

  it('lists an account without sessions with its header and sign-in hint', () => {
    accounts.value = [makeAccount('/home/c/.claude', ['sp3'], { ready: false, auth: { loggedIn: false, authMethod: '', apiProvider: '', subscriptionType: '' } })]
    const groups = mountView().findAll('[data-testid="cloud-account"]')
    expect(groups).toHaveLength(1)
    expect(groups[0]!.text()).toContain('/home/c/.claude')
    expect(groups[0]!.text()).toContain('Not signed in with claude.ai. Run: CLAUDE_CONFIG_DIR=/home/c/.claude claude auth login')
    expect(groups[0]!.text()).toContain('No sessions yet.')
  })

  it('shows Ready for a claude.ai login and the login hint otherwise', () => {
    accounts.value = [
      makeAccount('/home/a/.claude', ['sp1']),
      makeAccount('/home/b/.claude', ['sp2'], { ready: false, auth: { loggedIn: true, authMethod: 'api_key', apiProvider: 'firstParty', subscriptionType: '' } }),
    ]
    sessions.value = [makeSession('1', { spawnerId: 'sp1' }), makeSession('2', { spawnerId: 'sp2' })]
    const groups = mountView().findAll('[data-testid="cloud-account"]')
    expect(groups[0]!.text()).toContain('Ready')
    expect(groups[0]!.text()).not.toContain('claude auth login')
    expect(groups[1]!.text()).toContain('Not signed in with claude.ai. Run: CLAUDE_CONFIG_DIR=/home/b/.claude claude auth login')
  })

  it('shows the account error and refreshes on request', async () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'], { auth: null, ready: false, loginHint: 'claude auth login', error: 'claude not found' })]
    sessions.value = [makeSession('1')]
    const wrapper = mountView()
    expect(wrapper.text()).toContain('claude not found')
    expect(wrapper.find('[role="alert"]').text()).toBe('claude not found')
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
    expect(wrapper.find('[title="Session not found"]').findAll('.sr-only').map(e => e.text())).toContain('Session not found')
  })

  it('links to claude.ai in a new tab with noopener', () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
    sessions.value = [makeSession('1')]
    const link = mountView().find('a[href="https://claude.ai/code/session_1"]')
    expect(link.text()).toBe('Open in claude.ai')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener')
  })

  it('renders no link for a session URL outside https://claude.ai/code/', () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
    sessions.value = [makeSession('1', { url: 'javascript:alert(1)' }), makeSession('2', { url: 'https://evil.example/code/session_2' })]
    const wrapper = mountView()
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.html()).not.toContain('javascript:')
    expect(wrapper.text()).toContain('Session 1')
  })

  describe('load errors', () => {
    it('shows a sessions load error instead of the empty state', () => {
      sessionsError.value = 'HTTP 500'
      const wrapper = mountView()
      expect(wrapper.find('[data-testid="sessions-error"]').attributes('role')).toBe('alert')
      expect(wrapper.find('[data-testid="sessions-error"]').text()).toContain('HTTP 500')
      expect(wrapper.text()).not.toContain('No cloud sessions yet')
    })

    it('shows an accounts load error and lists sessions ungrouped, not under Unknown account', () => {
      accountsError.value = 'HTTP 502'
      sessions.value = [makeSession('1'), makeSession('2', { spawnerId: 'sp2' })]
      const wrapper = mountView()
      expect(wrapper.find('[data-testid="accounts-error"]').attributes('role')).toBe('alert')
      expect(wrapper.find('[data-testid="accounts-error"]').text()).toContain('HTTP 502')
      expect(wrapper.text()).not.toContain('Unknown account')
      expect(wrapper.findAll('[data-testid="cloud-session"]')).toHaveLength(2)
    })

    it('shows the lookup error without a Not signed in badge or login hint', () => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'], { auth: null, ready: false, error: 'claude not found' })]
      const text = mountView().text()
      expect(text).toContain('claude not found')
      expect(text).not.toContain('Not signed in')
      expect(text).not.toContain('claude auth login')
    })
  })

  describe('account header', () => {
    it('renders the server-built login hint verbatim', () => {
      const hint = 'CLAUDE_CONFIG_DIR=\'/home/o brien/.claude\' claude auth login'
      accounts.value = [makeAccount('/home/o brien/.claude', ['sp1'], { ready: false, loginHint: hint })]
      expect(mountView().find('code').text()).toBe(hint)
    })

    it('takes readiness from the server flag, not from the auth fields', () => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'], { ready: false })]
      const text = mountView().text()
      expect(text).toContain('Not signed in')
      expect(text).not.toContain('Ready')
    })

    it('names the default account', () => {
      accounts.value = [makeAccount('', ['sp1'], { loginHint: 'claude auth login' })]
      const wrapper = mountView()
      expect(wrapper.find('[data-testid="cloud-account"]').text()).toContain('default (no CLAUDE_CONFIG_DIR)')
      expect(wrapper.text()).not.toContain('Unknown account')
    })
  })

  describe('refresh', () => {
    it('is visible without any account row', () => {
      expect(mountView().find('[data-testid="cloud-refresh"]').exists()).toBe(true)
    })

    it('is disabled and labelled while the refresh runs', async () => {
      let resolve!: () => void
      state.refresh.mockReturnValue(new Promise<void>(r => (resolve = r)))
      const wrapper = mountView()
      await wrapper.find('[data-testid="cloud-refresh"]').trigger('click')
      expect(wrapper.find('[data-testid="cloud-refresh"]').attributes('disabled')).toBeDefined()
      expect(wrapper.find('[data-testid="cloud-refresh"]').text()).toBe('Refreshing…')
      resolve()
      await flushPromises()
      expect(wrapper.find('[data-testid="cloud-refresh"]').attributes('disabled')).toBeUndefined()
      expect(wrapper.find('[data-testid="cloud-refresh"]').text()).toBe('Refresh')
    })

    it('reloads the sessions as well', async () => {
      const wrapper = mountView()
      await wrapper.find('[data-testid="cloud-refresh"]').trigger('click')
      expect(state.refetch).toHaveBeenCalledOnce()
    })

    it('refetches the cached accounts once per unknown spawner, never with refresh=1', async () => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
      sessions.value = [makeSession('1', { spawnerId: 'sp1' })]
      mountView()
      await flushPromises()
      expect(state.refetchAccounts).not.toHaveBeenCalled()

      sessions.value = [...sessions.value, makeSession('2', { spawnerId: 'sp-new' })]
      await flushPromises()
      expect(state.refetchAccounts).toHaveBeenCalledOnce()

      sessions.value = [...sessions.value, makeSession('3', { spawnerId: 'sp-new' })]
      await flushPromises()
      expect(state.refetchAccounts).toHaveBeenCalledOnce()
      expect(state.refresh).not.toHaveBeenCalled()
    })

    it('fetches once for a session that is orphaned on mount', async () => {
      sessions.value = [makeSession('1', { spawnerId: 'gone' })]
      mountView()
      await flushPromises()
      expect(state.refetchAccounts).toHaveBeenCalledOnce()
      expect(state.refresh).not.toHaveBeenCalled()
    })

    it('queues one follow-up fetch for a spawner that turns up while a fetch runs', async () => {
      let resolve!: () => void
      state.refetchAccounts.mockReturnValueOnce(new Promise<void>(r => (resolve = r)))
      sessions.value = [makeSession('1', { spawnerId: 'gone-a' })]
      mountView()
      await flushPromises()
      expect(state.refetchAccounts).toHaveBeenCalledOnce()

      sessions.value = [...sessions.value, makeSession('2', { spawnerId: 'gone-b' })]
      await flushPromises()
      expect(state.refetchAccounts).toHaveBeenCalledOnce()

      resolve()
      await flushPromises()
      expect(state.refetchAccounts).toHaveBeenCalledTimes(2)
    })

    it('uses refresh=1 only for the manual Refresh button', async () => {
      sessions.value = [makeSession('1', { spawnerId: 'gone' })]
      const wrapper = mountView()
      await flushPromises()
      await wrapper.find('[data-testid="cloud-refresh"]').trigger('click')
      expect(state.refresh).toHaveBeenCalledOnce()
      expect(state.refetchAccounts).toHaveBeenCalledOnce()
    })
  })

  describe('accessibility', () => {
    beforeEach(() => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
      sessions.value = [makeSession('1')]
    })

    it('announces import and remove failures as alerts', async () => {
      state.importSession.mockRejectedValue(new Error('session not found'))
      state.removeSession.mockRejectedValue(new Error('delete failed'))
      const wrapper = mountView()
      await wrapper.find('[data-testid="import-url"]').setValue('https://claude.ai/code/session_x')
      await wrapper.find('[data-testid="import-form"]').trigger('submit')
      await wrapper.find('[data-testid="remove-1"]').trigger('click')
      await wrapper.find('[data-testid="remove-confirm-1"]').trigger('click')
      await flushPromises()
      expect(wrapper.findAll('[role="alert"]').map(e => e.text())).toEqual(['session not found', 'delete failed'])
    })

    it('announces Sending… in a live region that exists before the send starts', async () => {
      state.sendMessage.mockReturnValue(new Promise(() => {}))
      const wrapper = mountView()
      const region = wrapper.find('[data-testid="send-status-1"]')
      expect(region.attributes('aria-live') ?? region.attributes('role')).toBeDefined()
      expect(region.text()).toBe('')
      await wrapper.find('[data-testid="send-box-1"]').setValue('hello')
      await wrapper.find('[data-testid="send-1"]').trigger('click')
      expect(wrapper.find('[data-testid="send-status-1"]').text()).toBe('Sending…')
    })
  })

  it('clamps a long last message and keeps the full text in the title', () => {
    accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
    const long = 'x'.repeat(5000)
    sessions.value = [makeSession('1', { lastMessage: long })]
    const message = mountView().find('[data-testid="last-message-1"]')
    expect(message.classes()).toContain('line-clamp-3')
    expect(message.attributes('title')).toBe(long)
  })

  describe('send', () => {
    beforeEach(() => {
      accounts.value = [makeAccount('/home/a/.claude', ['sp1'])]
      sessions.value = [makeSession('1')]
    })

    it('labels the box and the row buttons with the session title', () => {
      const wrapper = mountView()
      expect(wrapper.find('[data-testid="send-box-1"]').attributes('aria-label')).toBe('Message to Session 1')
      expect(wrapper.find('[data-testid="send-1"]').attributes('aria-label')).toBe('Send message to Session 1')
      expect(wrapper.find('[data-testid="remove-1"]').attributes('aria-label')).toBe('Remove from Kontor: Session 1')
    })

    it('clears the send error when the text is edited', async () => {
      state.sendMessage.mockRejectedValue(new Error('claude exited 1'))
      const wrapper = mountView()
      await wrapper.find('[data-testid="send-box-1"]').setValue('hello')
      await wrapper.find('[data-testid="send-1"]').trigger('click')
      await flushPromises()
      expect(wrapper.text()).toContain('claude exited 1')
      await wrapper.find('[data-testid="send-box-1"]').setValue('hello again')
      expect(wrapper.text()).not.toContain('claude exited 1')
    })

    it('drops a removed session\'s draft and error', async () => {
      state.sendMessage.mockRejectedValue(new Error('claude exited 1'))
      const session = makeSession('1')
      const wrapper = mountView()
      await wrapper.find('[data-testid="send-box-1"]').setValue('hello')
      await wrapper.find('[data-testid="send-1"]').trigger('click')
      await flushPromises()
      sessions.value = []
      await nextTick()
      sessions.value = [session]
      await nextTick()
      expect(wrapper.text()).not.toContain('claude exited 1')
      expect(wrapper.find<HTMLTextAreaElement>('[data-testid="send-box-1"]').element.value).toBe('')
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
      expect(wrapper.find('[data-testid="send-1"]').text()).toBe('Sending…')
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

    it('shows a failed remove next to the remove control, not under the send box', async () => {
      state.removeSession.mockRejectedValue(new Error('delete failed'))
      const wrapper = mountView()
      await wrapper.find('[data-testid="remove-1"]').trigger('click')
      await wrapper.find('[data-testid="remove-confirm-1"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-testid="remove-error-1"]').text()).toBe('delete failed')
      expect(wrapper.find('[data-testid="send-box-1"]').element.parentElement!.textContent).not.toContain('delete failed')
    })

    it('moves focus to Confirm and labels the confirm buttons with the title', async () => {
      const wrapper = mount(CloudSessionsView, { attachTo: document.body })
      await wrapper.find('[data-testid="remove-1"]').trigger('click')
      await nextTick()
      const confirm = wrapper.find('[data-testid="remove-confirm-1"]')
      expect(document.activeElement).toBe(confirm.element)
      expect(confirm.attributes('aria-label')).toBe('Confirm removing Session 1')
      expect(wrapper.find('[data-testid="remove-cancel-1"]').attributes('aria-label')).toBe('Cancel removing Session 1')
      wrapper.unmount()
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

    it('clears the URL and title after a successful import', async () => {
      state.importSession.mockResolvedValue(makeSession('9'))
      const wrapper = mountView()
      await wrapper.find('[data-testid="import-url"]').setValue('https://claude.ai/code/session_x')
      await wrapper.find('[data-testid="import-title"]').setValue('My title')
      await wrapper.find('[data-testid="import-form"]').trigger('submit')
      await flushPromises()
      expect(state.importSession).toHaveBeenCalledOnce()
      expect(wrapper.find<HTMLInputElement>('[data-testid="import-url"]').element.value).toBe('')
      expect(wrapper.find<HTMLInputElement>('[data-testid="import-title"]').element.value).toBe('')
    })

    it('shows Importing… while the import is in flight', async () => {
      state.importSession.mockReturnValue(new Promise(() => {}))
      const wrapper = mountView()
      await wrapper.find('[data-testid="import-url"]').setValue('https://claude.ai/code/session_x')
      await wrapper.find('[data-testid="import-form"]').trigger('submit')
      const submit = wrapper.find('[data-testid="import-submit"]')
      expect(submit.text()).toBe('Importing…')
      expect(submit.attributes('disabled')).toBeDefined()
    })

    it('posts the import and shows the server error inline', async () => {
      state.importSession.mockRejectedValue(new Error('session not found'))
      const wrapper = mountView()
      await wrapper.find('[data-testid="import-url"]').setValue('https://claude.ai/code/session_x')
      await wrapper.find('[data-testid="import-title"]').setValue('My title')
      await wrapper.find('[data-testid="import-form"]').trigger('submit')
      await flushPromises()
      expect(state.importSession).toHaveBeenCalledWith({
        spawnerId: 'sp1',
        url: 'https://claude.ai/code/session_x',
        title: 'My title',
      })
      expect(wrapper.text()).toContain('session not found')
    })
  })
  describe('start', () => {
    async function pickProject(wrapper: ReturnType<typeof mountView>) {
      await selectByLabel(wrapper.get('[data-testid="start-project"]').element, 'Project One')
    }

    async function fillStartForm(wrapper: ReturnType<typeof mountView>, description = 'Fix the flaky test') {
      await pickProject(wrapper)
      await wrapper.get('[data-testid="start-description"]').setValue(description)
    }

    it('loads the folders of the chosen project, labelled by label or path, with the default preselected', async () => {
      const wrapper = mountView()
      await pickProject(wrapper)
      expect(state.fetchProjectFolders).toHaveBeenCalledWith('p1')
      const options = selectOptionsById(wrapper, 'cloud-start-folder')
      expect(options.map(o => o.label)).toEqual(['Docs', '/work/f2'])
      expect(wrapper.get('[data-testid="start-folder"]').text()).toContain('/work/f2')
    })

    it('starts the session with the default folder and exactly the three fields', async () => {
      state.startSession.mockResolvedValue(makeSession('9', { origin: 'started' }))
      const wrapper = mountView()
      await fillStartForm(wrapper)
      await wrapper.get('[data-testid="start-form"]').trigger('submit')
      await flushPromises()
      expect(state.startSession).toHaveBeenCalledOnce()
      expect(state.startSession).toHaveBeenCalledWith({ spawnerId: 'sp1', folderId: 'f2', description: 'Fix the flaky test' })
    })

    it('posts the three fields to /api/cloud-sessions and upserts the created session', async () => {
      const created = makeSession('9', { origin: 'started' })
      const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(created) })
      vi.stubGlobal('fetch', fetchMock)
      const actual = await vi.importActual<typeof import('../composables/useCloudSessions')>('../composables/useCloudSessions')
      const input = { spawnerId: 'sp1', folderId: 'f2', description: 'Fix the flaky test' }
      await expect(actual.startSession(input)).resolves.toEqual(created)
      expect(actual.useCloudSessions({ autoStart: false }).sessions.value).toContainEqual(created)
      expect(fetchMock).toHaveBeenCalledWith('/api/cloud-sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input),
      })
    })

    it('disables Start until a description is typed', async () => {
      const wrapper = mountView()
      await pickProject(wrapper)
      const submit = wrapper.get('[data-testid="start-submit"]')
      expect(submit.attributes('disabled')).toBeDefined()
      await wrapper.get('[data-testid="start-description"]').setValue('   ')
      expect(submit.attributes('disabled')).toBeDefined()
      await wrapper.get('[data-testid="start-description"]').setValue('Do the thing')
      expect(submit.attributes('disabled')).toBeUndefined()
    })

    it('disables Start while no project is chosen', async () => {
      const wrapper = mountView()
      await wrapper.get('[data-testid="start-description"]').setValue('Do the thing')
      expect(wrapper.get('[data-testid="start-submit"]').attributes('disabled')).toBeDefined()
    })

    it('shows the server error and keeps the description', async () => {
      state.startSession.mockRejectedValue(new Error('folder is not trusted'))
      const wrapper = mountView()
      await fillStartForm(wrapper)
      await wrapper.get('[data-testid="start-form"]').trigger('submit')
      await flushPromises()
      expect(wrapper.get('[data-testid="start-error"]').text()).toBe('folder is not trusted')
      expect(wrapper.get<HTMLTextAreaElement>('[data-testid="start-description"]').element.value).toBe('Fix the flaky test')
    })

    it('shows a folder load failure', async () => {
      state.fetchProjectFolders.mockRejectedValue(new Error('HTTP 500'))
      const wrapper = mountView()
      await pickProject(wrapper)
      expect(wrapper.get('[data-testid="start-error"]').text()).toBe('HTTP 500')
    })

    it('clears only the description after a successful start', async () => {
      state.startSession.mockResolvedValue(makeSession('9', { origin: 'started' }))
      const wrapper = mountView()
      await fillStartForm(wrapper)
      await wrapper.get('[data-testid="start-form"]').trigger('submit')
      await flushPromises()
      expect(wrapper.get<HTMLTextAreaElement>('[data-testid="start-description"]').element.value).toBe('')
      expect(wrapper.get('[data-testid="start-folder"]').text()).toContain('/work/f2')
    })

    it('shows Starting… and stays disabled while the POST is pending', async () => {
      state.startSession.mockReturnValue(new Promise(() => {}))
      const wrapper = mountView()
      await fillStartForm(wrapper)
      await wrapper.get('[data-testid="start-form"]').trigger('submit')
      const submit = wrapper.get('[data-testid="start-submit"]')
      expect(submit.text()).toBe('Starting…')
      expect(submit.attributes('disabled')).toBeDefined()
      await wrapper.get('[data-testid="start-form"]').trigger('submit')
      expect(state.startSession).toHaveBeenCalledOnce()
    })

    it('tells the user claude must trust the folder under the spawner\'s account', () => {
      const text = mountView().text()
      expect(text).toContain('Claude must trust this folder under the spawner\'s account. If it does not, the error shows the command to run once.')
      expect(text).not.toContain('open it once with claude')
    })

    it('posts the selected spawner and the trimmed description', async () => {
      spawners.value = [makeSpawner('sp1', 'claude'), makeSpawner('sp2', 'claude')]
      state.startSession.mockResolvedValue(makeSession('9', { origin: 'started' }))
      const wrapper = mountView()
      await selectByLabel(wrapper.get('#cloud-start-spawner').element, 'Spawner sp2')
      await fillStartForm(wrapper, '  Fix the flaky test \n')
      await wrapper.get('[data-testid="start-form"]').trigger('submit')
      await flushPromises()
      expect(state.startSession).toHaveBeenCalledWith({ spawnerId: 'sp2', folderId: 'f2', description: 'Fix the flaky test' })
    })

    describe('unconfirmed start', () => {
      const url = 'https://claude.ai/code/session_abc'

      async function submitUnconfirmed(error: StartUnconfirmedError) {
        state.startSession.mockRejectedValue(error)
        const wrapper = mountView()
        await fillStartForm(wrapper)
        await wrapper.get('[data-testid="start-form"]').trigger('submit')
        await flushPromises()
        return wrapper
      }

      it('warns with the server text, links the session and keeps the description', async () => {
        const message = `the cloud session started but Kontor could not record it; import it by URL: ${url}`
        const wrapper = await submitUnconfirmed(new StartUnconfirmedError(message, url))
        const warning = wrapper.get('[data-testid="start-unconfirmed"]')
        expect(warning.attributes('role')).toBe('alert')
        expect(warning.text()).toContain(message)
        expect(warning.text()).toContain('Import it by URL below if it is not listed.')
        const link = warning.get('a')
        expect(link.attributes('href')).toBe(url)
        expect(link.attributes('target')).toBe('_blank')
        expect(link.attributes('rel')).toBe('noopener noreferrer')
        expect(wrapper.find('[data-testid="start-error"]').exists()).toBe(false)
        expect(wrapper.get<HTMLTextAreaElement>('[data-testid="start-description"]').element.value).toBe('Fix the flaky test')
      })

      it('shows no link when the server sent no url', async () => {
        const wrapper = await submitUnconfirmed(new StartUnconfirmedError('claude timed out'))
        const warning = wrapper.get('[data-testid="start-unconfirmed"]')
        expect(warning.text()).toContain('claude timed out')
        expect(warning.find('a').exists()).toBe(false)
      })

      it('renders no link for a url outside https://claude.ai/code/', async () => {
        const wrapper = await submitUnconfirmed(new StartUnconfirmedError('may have started', 'javascript:alert(1)'))
        expect(wrapper.get('[data-testid="start-unconfirmed"]').find('a').exists()).toBe(false)
        expect(wrapper.html()).not.toContain('javascript:')
      })

      it('clears the warning on the next start', async () => {
        const wrapper = await submitUnconfirmed(new StartUnconfirmedError('claude timed out'))
        state.startSession.mockResolvedValue(makeSession('9', { origin: 'started' }))
        await wrapper.get('[data-testid="start-form"]').trigger('submit')
        await flushPromises()
        expect(wrapper.find('[data-testid="start-unconfirmed"]').exists()).toBe(false)
      })
    })

    describe('stale folder answers', () => {
      function deferred<T>() {
        let resolve!: (value: T) => void
        let reject!: (reason: unknown) => void
        const promise = new Promise<T>((res, rej) => {
          resolve = res
          reject = rej
        })
        return { promise, resolve, reject }
      }

      async function pickBothProjects() {
        projects.value = [makeProject('p1', 'Project One'), makeProject('p2', 'Project Two')]
        const a = deferred<ProjectFolder[]>()
        const b = deferred<ProjectFolder[]>()
        state.fetchProjectFolders.mockImplementation((id: string) => (id === 'p1' ? a : b).promise)
        const wrapper = mountView()
        const trigger = wrapper.get('[data-testid="start-project"]').element
        await selectByLabel(trigger, 'Project One')
        await selectByLabel(trigger, 'Project Two')
        return { wrapper, a, b }
      }

      it('ignores a late folder list of an earlier project', async () => {
        const { wrapper, a, b } = await pickBothProjects()
        b.resolve([makeFolder('fb', { projectId: 'p2', isDefault: true })])
        await flushPromises()
        a.resolve([makeFolder('fa', { projectId: 'p1', isDefault: true })])
        await flushPromises()
        expect(selectOptionsById(wrapper, 'cloud-start-folder').map(o => o.value)).toEqual(['fb'])
        state.startSession.mockResolvedValue(makeSession('9', { origin: 'started' }))
        await wrapper.get('[data-testid="start-description"]').setValue('Go')
        await wrapper.get('[data-testid="start-form"]').trigger('submit')
        await flushPromises()
        expect(state.startSession).toHaveBeenCalledWith({ spawnerId: 'sp1', folderId: 'fb', description: 'Go' })
      })

      it('shows no error for a late failure of an earlier project', async () => {
        const { wrapper, a, b } = await pickBothProjects()
        b.resolve([makeFolder('fb', { projectId: 'p2', isDefault: true })])
        await flushPromises()
        a.reject(new Error('HTTP 500'))
        await flushPromises()
        expect(wrapper.find('[data-testid="start-error"]').exists()).toBe(false)
      })
    })
  })
})
