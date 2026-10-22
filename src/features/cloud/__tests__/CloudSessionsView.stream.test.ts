import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

class MockEventSource {
  static instances: MockEventSource[] = []
  static CLOSED = 2
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0

  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {
    this.readyState = MockEventSource.CLOSED
  }
}

function jsonResponse(body: unknown) {
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

function streamFor(url: string) {
  return MockEventSource.instances.filter(i => i.url === url)
}

beforeEach(() => {
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse([])))
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('cloudSessionsView with the real composables', () => {
  it('opens the cloud session and spawner streams on mount and closes them on unmount', async () => {
    const { default: CloudSessionsView } = await import('../CloudSessionsView.vue')
    const wrapper = mount(CloudSessionsView)
    await flushPromises()

    const cloud = streamFor('/api/cloud-sessions/stream')
    const spawners = streamFor('/api/spawners/stream')
    expect(cloud).toHaveLength(1)
    expect(spawners).toHaveLength(1)
    expect(fetch).toHaveBeenCalledWith('/api/cloud-sessions')
    expect(fetch).toHaveBeenCalledWith('/api/cloud-accounts')
    expect(cloud[0]!.readyState).not.toBe(MockEventSource.CLOSED)
    expect(spawners[0]!.readyState).not.toBe(MockEventSource.CLOSED)

    wrapper.unmount()
    expect(cloud[0]!.readyState).toBe(MockEventSource.CLOSED)
    expect(spawners[0]!.readyState).toBe(MockEventSource.CLOSED)
  })
})
