import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PANEL_STATES } from '../panelState'
import GitHubPanel from './GitHubPanel.vue'

function stubFetch(status: number, body: unknown) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })))
}

afterEach(() => vi.unstubAllGlobals())

// PANEL_STATES is the single owner of the state list; a literal here would keep
// passing after a state is added or renamed, silently stopping covering it.
// 'ready' is excluded because it renders the content slot, not a marker.
const markerStates = PANEL_STATES.filter(s => s !== 'ready')
const others = (state: string) => markerStates.filter(s => s !== state)

async function mountPanel() {
  const wrapper = mount(GitHubPanel)
  await flushPromises()
  return wrapper
}

function expectOnly(wrapper: ReturnType<typeof mount>, state: string) {
  expect(wrapper.find(`[data-testid="cockpit-github-${state}"]`).exists()).toBe(true)
  for (const other of others(state))
    expect(wrapper.findAll(`[data-testid="cockpit-github-${other}"]`)).toHaveLength(0)
}

describe('gitHubPanel', () => {
  // The route answers 200 and names the repository it could not reach. A failed
  // repository returns no pull requests, so before this it landed in the empty
  // branch and the panel said "no open pull requests" while GitHub was actually
  // rate-limiting or the token was revoked.
  it('a 200 whose only repository failed is failed, never empty', async () => {
    stubFetch(200, { repos: [{ repo: 'lx-wnk/agent-dashboard', pullRequests: [], error: 'API rate limit exceeded' }] })
    const wrapper = await mountPanel()
    expectOnly(wrapper, 'failed')
    expect(wrapper.get('[data-testid="cockpit-github-failed"]').text()).toContain('API rate limit exceeded')
  })

  // The server reports partial success on purpose, so one broken repository
  // must not hide the pull requests the others returned — nor go unmentioned.
  it('a partial failure still lists what came back, and says what did not', async () => {
    stubFetch(200, {
      repos: [
        { repo: 'lx-wnk/agent-dashboard', pullRequests: [{ number: 42, title: 'Add the cockpit', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z' }] },
        { repo: 'lx-wnk/other', pullRequests: [], error: 'API rate limit exceeded' },
      ],
    })
    const wrapper = await mountPanel()
    expect(wrapper.get('[data-testid="cockpit-github-pr-42"]').text()).toContain('Add the cockpit')
    expect(wrapper.get('[data-testid="cockpit-github-partial-failure"]').text()).toContain('lx-wnk/other')
  })

  // The other cases all assert loading is ABSENT after the fetch settles, so a
  // panel that never enters the loading state would pass every one of them —
  // and would flash the empty state while the request is still in flight.
  it('shows loading while the request is still in flight', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    const wrapper = mount(GitHubPanel)
    await flushPromises()
    expectOnly(wrapper, 'loading')
  })

  it('reports a 503 as not configured, never as an empty repository list', async () => {
    stubFetch(503, { error: 'github is not configured' })
    expectOnly(await mountPanel(), 'notAsked')
  })

  it('renders the icon', async () => {
    stubFetch(200, { repos: [] })
    const wrapper = await mountPanel()
    expect(wrapper.get('header').text()).toContain('⌂')
    wrapper.unmount()
  })

  it('reports a 403 as denied, with the server reason, and shows no rows', async () => {
    stubFetch(403, { error: 'capability denied: github.read' })
    const wrapper = await mountPanel()
    expectOnly(wrapper, 'denied')
    expect(wrapper.get('[data-testid="cockpit-github-denied"]').text()).toContain('capability denied: github.read')
  })

  it('tells a confirmed-empty answer apart from a refusal', async () => {
    stubFetch(200, { repos: [{ repo: 'lx-wnk/agent-dashboard', pullRequests: [] }] })
    expectOnly(await mountPanel(), 'empty')
  })

  it('reports a 500 as failed, which is not the same as denied', async () => {
    stubFetch(500, { error: 'upstream exploded' })
    const wrapper = await mountPanel()
    expectOnly(wrapper, 'failed')
    expect(wrapper.get('[data-testid="cockpit-github-failed"]').text()).toContain('upstream exploded')
  })

  it('renders the pull requests it was given', async () => {
    stubFetch(200, {
      repos: [{
        repo: 'lx-wnk/agent-dashboard',
        pullRequests: [{ number: 42, title: 'Add the cockpit', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z' }],
      }],
    })
    const wrapper = await mountPanel()
    for (const state of markerStates)
      expect(wrapper.findAll(`[data-testid="cockpit-github-${state}"]`)).toHaveLength(0)
    expect(wrapper.get('[data-testid="cockpit-github-pr-42"]').text()).toContain('Add the cockpit')
  })
  it('shows the check state and counts for each pull request', async () => {
    stubFetch(200, {
      repos: [{
        repo: 'lx-wnk/agent-dashboard',
        pullRequests: [
          { number: 42, title: 'Green', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z', checks: { state: 'success', passed: 34, failed: 0, total: 34, url: 'https://example.test/42/checks' } },
          { number: 43, title: 'Red', author: 'lx-wnk', url: 'https://example.test/43', draft: false, updatedAt: '2026-09-01T10:00:00Z', checks: { state: 'failure', passed: 33, failed: 1, total: 34, url: 'https://example.test/43/checks' } },
          { number: 44, title: 'Running', author: 'lx-wnk', url: 'https://example.test/44', draft: false, updatedAt: '2026-09-01T10:00:00Z', checks: { state: 'pending', passed: 12, failed: 0, total: 34, url: 'https://example.test/44/checks' } },
          { number: 45, title: 'Nothing', author: 'lx-wnk', url: 'https://example.test/45', draft: false, updatedAt: '2026-09-01T10:00:00Z', checks: { state: 'none', passed: 0, failed: 0, total: 0, url: 'https://example.test/45/checks' } },
        ],
      }],
    })
    const wrapper = await mountPanel()
    expect(wrapper.get('[data-testid="cockpit-github-checks-42"]').text()).toContain('34/34')
    expect(wrapper.get('[data-testid="cockpit-github-checks-43"]').text()).toContain('1 failed')
    expect(wrapper.get('[data-testid="cockpit-github-checks-44"]').text()).toContain('12/34')
    expect(wrapper.get('[data-testid="cockpit-github-checks-45"]').text()).toContain('no checks')
    wrapper.unmount()
  })

  // The state must survive a colour-blind reader and a screen reader, so the
  // marker carries text and the element carries a full label.
  it('distinguishes the states without relying on colour', async () => {
    stubFetch(200, {
      repos: [{
        repo: 'lx-wnk/agent-dashboard',
        pullRequests: [
          { number: 42, title: 'Green', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z', checks: { state: 'success', passed: 2, failed: 0, total: 2, url: 'https://example.test/42/checks' } },
          { number: 43, title: 'Red', author: 'lx-wnk', url: 'https://example.test/43', draft: false, updatedAt: '2026-09-01T10:00:00Z', checks: { state: 'failure', passed: 1, failed: 1, total: 2, url: 'https://example.test/43/checks' } },
        ],
      }],
    })
    const wrapper = await mountPanel()
    const ok = wrapper.get('[data-testid="cockpit-github-checks-42"]')
    const bad = wrapper.get('[data-testid="cockpit-github-checks-43"]')
    expect(ok.text()).not.toBe(bad.text())
    expect(ok.attributes('aria-label')).toContain('success')
    expect(bad.attributes('aria-label')).toContain('failure')
    wrapper.unmount()
  })

  // A server that predates the check-run field omits the key entirely; the
  // panel must still draw the pull request rather than throwing on it.
  it('renders a pull request from a server that sends no checks field', async () => {
    stubFetch(200, {
      repos: [{
        repo: 'lx-wnk/agent-dashboard',
        pullRequests: [{ number: 42, title: 'Add the cockpit', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z' }],
      }],
    })
    const wrapper = await mountPanel()
    expect(wrapper.get('[data-testid="cockpit-github-pr-42"]').text()).toContain('Add the cockpit')
    expect(wrapper.findAll('[data-testid="cockpit-github-checks-42"]')).toHaveLength(0)
    wrapper.unmount()
  })
})

describe('merging from the cockpit', () => {
  const PR = { number: 42, title: 'Add the cockpit', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z' }

  // Routes by URL: the summary and the merge must answer differently, which
  // the shared stubFetch above cannot do.
  function stubRoutes(mergeStatus: number, mergeBody: unknown) {
    const posts: Array<{ url: string, body: unknown }> = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.includes('/api/github/merge')) {
        posts.push({ url, body: init?.body ? JSON.parse(init.body as string) : null })
        return new Response(JSON.stringify(mergeBody), { status: mergeStatus, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify({ repos: [{ repo: 'lx-wnk/agent-dashboard', pullRequests: [PR] }] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))
    return posts
  }

  // A merge cannot be taken back, so one stray click must never perform one.
  it('asks before merging, naming the pull request', async () => {
    const posts = stubRoutes(200, { sha: 'abc' })
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="cockpit-github-merge-42"]').trigger('click')

    const confirmText = wrapper.get('[data-testid="cockpit-github-merge-confirm-text-42"]').text()
    expect(confirmText).toContain('lx-wnk/agent-dashboard#42')
    expect(confirmText).toContain('Add the cockpit')
    expect(posts).toHaveLength(0)
    wrapper.unmount()
  })

  it('merges only on the second, explicit click', async () => {
    const posts = stubRoutes(200, { sha: 'abc' })
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="cockpit-github-merge-42"]').trigger('click')
    await wrapper.get('[data-testid="cockpit-github-merge-confirm-42"]').trigger('click')
    await flushPromises()

    expect(posts).toHaveLength(1)
    expect(posts[0].body).toMatchObject({ repo: 'lx-wnk/agent-dashboard', number: 42 })
    wrapper.unmount()
  })

  it('cancels back to the plain button without merging', async () => {
    const posts = stubRoutes(200, { sha: 'abc' })
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="cockpit-github-merge-42"]').trigger('click')
    await wrapper.get('[data-testid="cockpit-github-merge-cancel-42"]').trigger('click')

    expect(wrapper.find('[data-testid="cockpit-github-merge-42"]').exists()).toBe(true)
    expect(posts).toHaveLength(0)
    wrapper.unmount()
  })

  // A 403 is this server refusing, and it has two causes the operator fixes in
  // two different places. Naming only one of them sends them to the wrong screen.
  it('names both causes when the server refuses with 403', async () => {
    stubRoutes(403, { error: 'github.merge: denied' })
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="cockpit-github-merge-42"]').trigger('click')
    await wrapper.get('[data-testid="cockpit-github-merge-confirm-42"]').trigger('click')
    await flushPromises()

    const text = wrapper.get('[data-testid="cockpit-github-merge-error"]').text()
    expect(text).toContain('github.merge: denied')
    expect(text).toContain('allow-list')
    expect(text).toContain('Grants')
    wrapper.unmount()
  })

  // A conflict or branch protection comes back from GitHub, not from the gate;
  // pointing at local settings there would send the operator nowhere useful.
  it('shows a GitHub-side failure without the settings hint', async () => {
    stubRoutes(409, { error: 'Pull Request is not mergeable' })
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="cockpit-github-merge-42"]').trigger('click')
    await wrapper.get('[data-testid="cockpit-github-merge-confirm-42"]').trigger('click')
    await flushPromises()

    const text = wrapper.get('[data-testid="cockpit-github-merge-error"]').text()
    expect(text).toContain('not mergeable')
    expect(text).not.toContain('Grants')
    wrapper.unmount()
  })
})
