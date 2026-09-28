import type { Project } from '../../types'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'

class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2

  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {
    this.readyState = 2
  }
}

let useProjectsMod: typeof import('../useProjects')

function makeProject(id: string, slug: string, overrides: Partial<Project> = {}): Project {
  return {
    id,
    slug,
    name: slug,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

beforeEach(async () => {
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: true,
    json: () => Promise.resolve([makeProject('p1', 'alpha')]),
  }))
  vi.resetModules()
  useProjectsMod = await import('../useProjects')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function withSetup<T>(composable: () => T) {
  let result!: T
  const Wrapper = defineComponent({
    setup() {
      result = composable()
      return {}
    },
    template: '<div />',
  })
  const wrapper = mount(Wrapper, { attachTo: document.body })
  return { result, wrapper }
}

describe('useProjects', () => {
  it('fetches /api/projects on first subscribe and populates projects', async () => {
    const { result, wrapper } = withSetup(() => useProjectsMod.useProjects())

    await vi.waitFor(() => expect(result.projects.value).toHaveLength(1))
    expect(fetch).toHaveBeenCalledWith('/api/projects', undefined)
    expect(result.projects.value[0].slug).toBe('alpha')
    wrapper.unmount()
  })

  it('retries a rate-limited load instead of leaving the list empty', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce({ ok: false, status: 429, headers: new Headers({ 'Retry-After': '0' }) })
      .mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve([makeProject('p1', 'alpha')]) }))
    const { result, wrapper } = withSetup(() => useProjectsMod.useProjects())

    await vi.waitFor(() => expect(result.projects.value).toHaveLength(1))
    expect(result.error.value).toBeNull()
    wrapper.unmount()
  })

  it('opens an EventSource on /api/projects/stream', async () => {
    const { wrapper } = withSetup(() => useProjectsMod.useProjects())
    await nextTick()
    expect(MockEventSource.instances).toHaveLength(1)
    expect(MockEventSource.instances[0].url).toBe('/api/projects/stream')
    wrapper.unmount()
  })

  it('prepends on project_created event', async () => {
    const { result, wrapper } = withSetup(() => useProjectsMod.useProjects())
    await vi.waitFor(() => expect(result.projects.value).toHaveLength(1))

    const es = MockEventSource.instances[0]
    const newProject = makeProject('p2', 'beta')
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'project_created', projectId: 'p2', payload: newProject }),
    }))

    expect(result.projects.value).toHaveLength(2)
    expect(result.projects.value[0].id).toBe('p2')
    wrapper.unmount()
  })

  it('mutates entry on project_updated event', async () => {
    const { result, wrapper } = withSetup(() => useProjectsMod.useProjects())
    await vi.waitFor(() => expect(result.projects.value).toHaveLength(1))

    const es = MockEventSource.instances[0]
    const renamed = makeProject('p1', 'alpha', { name: 'Renamed' })
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'project_updated', projectId: 'p1', payload: renamed }),
    }))

    expect(result.projects.value).toHaveLength(1)
    expect(result.projects.value[0].name).toBe('Renamed')
    wrapper.unmount()
  })

  it('removes entry on project_deleted event', async () => {
    const { result, wrapper } = withSetup(() => useProjectsMod.useProjects())
    await vi.waitFor(() => expect(result.projects.value).toHaveLength(1))

    const es = MockEventSource.instances[0]
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'project_deleted', projectId: 'p1' }),
    }))

    expect(result.projects.value).toHaveLength(0)
    wrapper.unmount()
  })

  it('createProject POSTs to /api/projects with JSON body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeProject('p9', 'omega')),
    })
    vi.stubGlobal('fetch', fetchMock)

    const result = await useProjectsMod.createProject({ slug: 'omega', name: 'Omega' })

    expect(fetchMock).toHaveBeenCalledWith('/api/projects', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ slug: 'omega', name: 'Omega' }),
    }))
    expect(result.slug).toBe('omega')
  })

  it('updateProject PATCHes /api/projects/:id', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeProject('p1', 'alpha', { name: 'Renamed' })),
    })
    vi.stubGlobal('fetch', fetchMock)

    await useProjectsMod.updateProject('p1', { name: 'Renamed' })

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/p1', expect.objectContaining({
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'Renamed' }),
    }))
  })

  it('deleteProject issues DELETE /api/projects/:id', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve({}) })
    vi.stubGlobal('fetch', fetchMock)

    await useProjectsMod.deleteProject('p1')

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/p1', { method: 'DELETE' })
  })

  it('createProject throws on non-ok response with server error message', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      json: () => Promise.resolve({ error: 'slug taken' }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(useProjectsMod.createProject({ slug: 'x', name: 'X' })).rejects.toThrow('slug taken')
  })
})
