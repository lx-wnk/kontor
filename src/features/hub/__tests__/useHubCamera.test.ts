import type { HubCameraOptions } from '../composables/useHubCamera'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { useHubCamera } from '../composables/useHubCamera'
import { MAX_REL, toScreen } from '../hubCamera'

class MockResizeObserver {
  static instances: MockResizeObserver[] = []
  callback: ResizeObserverCallback
  observe = vi.fn()
  unobserve = vi.fn()
  disconnect = vi.fn()
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    MockResizeObserver.instances.push(this)
  }
}

function stubReducedMotion(reduce: boolean) {
  window.matchMedia = vi.fn((query: string) => ({
    matches: reduce && query.includes('reduce'),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

function pointer(type: string, init: { clientX: number, clientY: number, pointerId?: number }) {
  return new PointerEvent(type, { bubbles: true, cancelable: true, pointerId: 1, ...init })
}

async function mountCamera(options?: HubCameraOptions) {
  let api!: ReturnType<typeof useHubCamera>
  const wrapper = mount(defineComponent({
    setup() {
      const stage = ref<HTMLElement | null>(null)
      api = useHubCamera(stage, options)
      return { stage }
    },
    template: '<div ref="stage"></div>',
  }), { attachTo: document.body })
  // vueuse's flush:'post' watchers run their immediate call before the ref exists; they rerun once it's set.
  await flushPromises()
  const el = wrapper.element as HTMLElement
  el.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1090, height: 1130, right: 1090, bottom: 1130, x: 0, y: 0, toJSON: () => ({}) })
  return { api, el }
}

function resize(width: number, height: number) {
  const observer = MockResizeObserver.instances.at(-1)!
  observer.callback([{ contentRect: { width, height } } as ResizeObserverEntry], observer as unknown as ResizeObserver)
}

beforeEach(() => {
  MockResizeObserver.instances = []
  vi.stubGlobal('ResizeObserver', MockResizeObserver)
  stubReducedMotion(false)
})

describe('useHubCamera', () => {
  it('centres the core at fit on the first resize', async () => {
    const { api } = await mountCamera()
    resize(1090, 1130)

    expect(api.rel.value).toBe(1)
    expect(toScreen(api.cam.value, 0, 0)).toEqual([545, 565])
  })

  it('doubles rel and keeps the centre fixed on zoomBy, clamping beyond 14x', async () => {
    const { api } = await mountCamera()
    resize(1090, 1130)
    const centreBefore = api.centreWorld()

    api.zoomBy(2)

    expect(api.rel.value).toBe(2)
    expect(api.centreWorld()[0]).toBeCloseTo(centreBefore[0])
    expect(api.centreWorld()[1]).toBeCloseTo(centreBefore[1])

    api.zoomBy(100)

    expect(api.rel.value).toBe(MAX_REL)
  })

  it('jumps to the end frame synchronously under reduced motion', async () => {
    stubReducedMotion(true)
    const { api } = await mountCamera()
    resize(1090, 1130)

    api.flyTo(100, 0, 3)

    expect(api.rel.value).toBe(3)
    const [cx, cy] = api.centreWorld()
    expect(cx).toBeCloseTo(100)
    expect(cy).toBeCloseTo(0)
  })

  it('taps on a click that did not move, pans on a drag, ignores interactive targets and pointercancel', async () => {
    const onTap = vi.fn()
    const { api, el } = await mountCamera({ onTap })
    resize(1090, 1130)
    const txBefore = api.cam.value.tx

    el.dispatchEvent(pointer('pointerdown', { clientX: 50, clientY: 50 }))
    el.dispatchEvent(pointer('pointerup', { clientX: 50, clientY: 50 }))
    expect(onTap).toHaveBeenCalledOnce()
    expect(onTap).toHaveBeenCalledWith(50, 50)
    expect(api.cam.value.tx).toBe(txBefore)

    onTap.mockClear()
    el.dispatchEvent(pointer('pointerdown', { clientX: 50, clientY: 50 }))
    el.dispatchEvent(pointer('pointermove', { clientX: 70, clientY: 50 }))
    expect(api.dragging.value).toBe(true)
    el.dispatchEvent(pointer('pointerup', { clientX: 70, clientY: 50 }))
    expect(onTap).not.toHaveBeenCalled()
    expect(api.cam.value.tx).toBe(txBefore + 20)
    expect(api.dragging.value).toBe(false)

    const button = document.createElement('button')
    el.appendChild(button)
    const txBeforeButton = api.cam.value.tx
    button.dispatchEvent(pointer('pointerdown', { clientX: 10, clientY: 10 }))
    expect(api.dragging.value).toBe(false)
    button.dispatchEvent(pointer('pointerup', { clientX: 10, clientY: 10 }))
    expect(api.cam.value.tx).toBe(txBeforeButton)
    expect(onTap).not.toHaveBeenCalled()

    el.dispatchEvent(pointer('pointerdown', { clientX: 200, clientY: 200 }))
    el.dispatchEvent(pointer('pointercancel', { clientX: 200, clientY: 200 }))
    expect(api.dragging.value).toBe(false)
    el.dispatchEvent(pointer('pointerup', { clientX: 300, clientY: 300 }))
    expect(onTap).not.toHaveBeenCalled()
  })

  it.each([false, true])('flies to a target requested before the stage had a size once it is measured (reduced motion %s)', async (reduce) => {
    stubReducedMotion(reduce)
    const { api } = await mountCamera()

    api.flyTo(100, 0, 5)
    resize(700, 700)

    expect(api.rel.value).toBeCloseTo(5)
    const [sx, sy] = toScreen(api.cam.value, 100, 0)
    expect(sx).toBeCloseTo(350)
    expect(sy).toBeCloseTo(350)
  })

  it('settles on the target when the stage resizes mid-flight', async () => {
    const { api } = await mountCamera()
    resize(1090, 1130)

    api.flyTo(100, 0, 5)
    resize(700, 700)
    await new Promise(resolve => requestAnimationFrame(resolve))

    expect(api.rel.value).toBeCloseTo(5)
    const [sx, sy] = toScreen(api.cam.value, 100, 0)
    expect(sx).toBeCloseTo(350)
    expect(sy).toBeCloseTo(350)
  })

  it('keeps the centre world point and rel across a later resize', async () => {
    const { api } = await mountCamera()
    resize(1090, 1130)
    api.zoomBy(2)
    const centreBefore = api.centreWorld()
    const relBefore = api.rel.value

    resize(545, 1130)

    expect(api.rel.value).toBeCloseTo(relBefore)
    expect(api.centreWorld()[0]).toBeCloseTo(centreBefore[0])
    expect(api.centreWorld()[1]).toBeCloseTo(centreBefore[1])
  })
})
