import type { Spawner } from '../../types'
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

let useSpawnersMod: typeof import('../useSpawners')

function makeSpawner(id: string, slug: string, overrides: Partial<Spawner> = {}): Spawner {
  return {
    id,
    name: slug,
    slug,
    command: 'claude',
    args: [],
    env: {},
    adapterType: 'claude',
    adapterConfig: {},
    builtIn: false,
    isDefault: false,
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
    json: () => Promise.resolve([makeSpawner('s1', 'claude-default', { builtIn: true })]),
  }))
  vi.resetModules()
  useSpawnersMod = await import('../useSpawners')
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

describe('useSpawners', () => {
  it('fetches /api/spawners on first subscribe and populates spawners', async () => {
    const { result, wrapper } = withSetup(() => useSpawnersMod.useSpawners())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(fetch).toHaveBeenCalledWith('/api/spawners')
    expect(result.spawners.value).toHaveLength(1)
    expect(result.spawners.value[0].slug).toBe('claude-default')
    wrapper.unmount()
  })

  it('opens an EventSource on /api/spawners/stream', async () => {
    const { wrapper } = withSetup(() => useSpawnersMod.useSpawners())
    await nextTick()
    expect(MockEventSource.instances).toHaveLength(1)
    expect(MockEventSource.instances[0].url).toBe('/api/spawners/stream')
    wrapper.unmount()
  })

  it('prepends on spawner_created event', async () => {
    const { result, wrapper } = withSetup(() => useSpawnersMod.useSpawners())
    await Promise.resolve()
    await nextTick()

    const es = MockEventSource.instances[0]
    const newSpawner = makeSpawner('s2', 'custom')
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'spawner_created', spawnerId: 's2', payload: newSpawner }),
    }))

    expect(result.spawners.value).toHaveLength(2)
    expect(result.spawners.value[0].id).toBe('s2')
    wrapper.unmount()
  })

  it('mutates entry on spawner_updated event', async () => {
    const { result, wrapper } = withSetup(() => useSpawnersMod.useSpawners())
    await Promise.resolve()
    await nextTick()

    const es = MockEventSource.instances[0]
    const renamed = makeSpawner('s1', 'claude-default', { name: 'Renamed', builtIn: true })
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'spawner_updated', spawnerId: 's1', payload: renamed }),
    }))

    expect(result.spawners.value).toHaveLength(1)
    expect(result.spawners.value[0].name).toBe('Renamed')
    wrapper.unmount()
  })

  it('removes entry on spawner_deleted event', async () => {
    const { result, wrapper } = withSetup(() => useSpawnersMod.useSpawners())
    await Promise.resolve()
    await nextTick()

    const es = MockEventSource.instances[0]
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'spawner_deleted', spawnerId: 's1' }),
    }))

    expect(result.spawners.value).toHaveLength(0)
    wrapper.unmount()
  })

  it('createSpawner POSTs to /api/spawners', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeSpawner('s9', 'new-one')),
    })
    vi.stubGlobal('fetch', fetchMock)

    const body = {
      slug: 'new-one',
      name: 'New',
      command: 'claude',
      adapterType: 'claude' as const,
      adapterConfig: {},
    }
    const result = await useSpawnersMod.createSpawner(body)

    expect(fetchMock).toHaveBeenCalledWith('/api/spawners', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }))
    expect(result.slug).toBe('new-one')
  })

  it('createSpawner sends adapterType+adapterConfig for ollama rows', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeSpawner('s10', 'local-ollama', {
        command: '',
        adapterType: 'ollama',
        adapterConfig: { host: 'http://x' },
      })),
    })
    vi.stubGlobal('fetch', fetchMock)

    const body = {
      slug: 'local-ollama',
      name: 'Local Ollama',
      command: '',
      adapterType: 'ollama' as const,
      adapterConfig: { host: 'http://x' },
    }
    const result = await useSpawnersMod.createSpawner(body)

    const call = fetchMock.mock.calls[0]
    expect(call[0]).toBe('/api/spawners')
    const sent = JSON.parse(call[1].body)
    expect(sent.adapterType).toBe('ollama')
    expect(sent.adapterConfig).toEqual({ host: 'http://x' })
    expect(sent.command).toBe('')
    expect(result.adapterType).toBe('ollama')
    expect(result.adapterConfig).toEqual({ host: 'http://x' })
  })

  it('updateSpawner PATCHes /api/spawners/:id', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeSpawner('s1', 'claude-default', { name: 'Modified' })),
    })
    vi.stubGlobal('fetch', fetchMock)

    await useSpawnersMod.updateSpawner('s1', { name: 'Modified' })

    expect(fetchMock).toHaveBeenCalledWith('/api/spawners/s1', expect.objectContaining({
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'Modified' }),
    }))
  })

  it('deleteSpawner issues DELETE /api/spawners/:id', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve({}) })
    vi.stubGlobal('fetch', fetchMock)

    await useSpawnersMod.deleteSpawner('s1')

    expect(fetchMock).toHaveBeenCalledWith('/api/spawners/s1', { method: 'DELETE' })
  })

  it('deleteSpawner surfaces server error messages', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      json: () => Promise.resolve({ error: 'spawner in use' }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(useSpawnersMod.deleteSpawner('s1')).rejects.toThrow('spawner in use')
  })


  // A passive consumer never raised the shared subscriber count, so its
  // unmount must not lower it and close the stream for the owner.
  it('a caller opted out of owning the stream (autoStart: false) does not tear it down for an owner on unmount', async () => {
    const owner = withSetup(() => useSpawnersMod.useSpawners())
    await nextTick()
    expect(MockEventSource.instances).toHaveLength(1)

    withSetup(() => useSpawnersMod.useSpawners({ autoStart: false })).wrapper.unmount()
    await nextTick()

    expect(MockEventSource.instances[0].readyState).not.toBe(MockEventSource.CLOSED)
    owner.wrapper.unmount()
  })
})
