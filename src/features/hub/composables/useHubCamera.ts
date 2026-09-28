import type { ComputedRef, Ref, ShallowRef } from 'vue'
import type { Camera, HubLevel } from '../hubCamera'
import { useEventListener, usePreferredReducedMotion, useResizeObserver } from '@vueuse/core'
import { computed, onUnmounted, readonly, ref, shallowRef } from 'vue'
import { clampScale, fitScale, FLY_MS, flyFrame, levelOf, toWorld, zoomAt } from '../hubCamera'

export interface HubCameraOptions {
  /** A click that did not move the pointer more than 3 px, in stage coordinates. */
  onTap?: (sx: number, sy: number) => void
}

export interface UseHubCamera {
  cam: ShallowRef<Camera>
  k0: Ref<number>
  size: Readonly<Ref<{ width: number, height: number }>>
  rel: ComputedRef<number>
  level: ComputedRef<HubLevel>
  dragging: Ref<boolean>
  zoomBy: (factor: number, mx?: number, my?: number) => void
  panBy: (dx: number, dy: number) => void
  flyTo: (wx: number, wy: number, rel: number) => void
  fit: () => void
  centreWorld: () => [number, number]
}

const TAP_THRESHOLD_PX = 3
// pointerdown ignored on interactive chrome so it stays draggable everywhere else
const INTERACTIVE_SELECTOR = 'button, input, a, [data-hub-layer]'

export function useHubCamera(stage: Ref<HTMLElement | null>, options?: HubCameraOptions): UseHubCamera {
  const cam = shallowRef<Camera>({ k: 1, tx: 0, ty: 0 })
  const k0 = ref(1)
  const size = ref({ width: 0, height: 0 })
  const dragging = ref(false)
  let sized = false

  const rel = computed(() => cam.value.k / k0.value)
  const level = computed<HubLevel>(() => levelOf(rel.value))

  const reducedMotion = usePreferredReducedMotion()
  let rafId: number | null = null

  function cancelFlight() {
    if (rafId !== null) {
      cancelAnimationFrame(rafId)
      rafId = null
    }
  }

  function centreWorld(): [number, number] {
    return toWorld(cam.value, size.value.width / 2, size.value.height / 2)
  }

  function zoomBy(factor: number, mx = size.value.width / 2, my = size.value.height / 2) {
    cancelFlight()
    cam.value = zoomAt(cam.value, factor, mx, my, k0.value)
  }

  function panBy(dx: number, dy: number) {
    cancelFlight()
    cam.value = { ...cam.value, tx: cam.value.tx + dx, ty: cam.value.ty + dy }
  }

  function flyTo(wx: number, wy: number, relTarget: number) {
    cancelFlight()
    const from = cam.value
    const to = { wx, wy, k: clampScale(k0.value * relTarget, k0.value) }
    if (reducedMotion.value === 'reduce') {
      cam.value = flyFrame(from, to, size.value.width, size.value.height, 1)
      return
    }
    const start = performance.now()
    const step = (now: number) => {
      const p = (now - start) / FLY_MS
      cam.value = flyFrame(from, to, size.value.width, size.value.height, p)
      rafId = p < 1 ? requestAnimationFrame(step) : null
    }
    rafId = requestAnimationFrame(step)
  }

  function fit() {
    flyTo(0, 0, 1)
  }

  useResizeObserver(stage, (entries) => {
    const { width, height } = entries[0].contentRect
    const [cx, cy] = sized ? centreWorld() : [0, 0]
    const relCurrent = sized ? rel.value : 1
    sized = true
    size.value = { width, height }
    k0.value = fitScale(width, height)
    const k = k0.value * relCurrent
    cam.value = { k, tx: width / 2 - cx * k, ty: height / 2 - cy * k }
  })

  function stageCoords(e: PointerEvent | WheelEvent): [number, number] {
    const rect = stage.value?.getBoundingClientRect()
    if (!rect)
      return [0, 0]
    return [e.clientX - rect.left, e.clientY - rect.top]
  }

  useEventListener(stage, 'wheel', (e: WheelEvent) => {
    e.preventDefault()
    const [mx, my] = stageCoords(e)
    zoomBy(Math.exp(-e.deltaY * (e.ctrlKey ? 0.012 : 0.0016)), mx, my)
  }, { passive: false })

  let dragStart: { x: number, y: number, tx: number, ty: number } | null = null
  let moved = false

  useEventListener(stage, 'pointerdown', (e: PointerEvent) => {
    if ((e.target as HTMLElement).closest(INTERACTIVE_SELECTOR))
      return
    cancelFlight()
    dragStart = { x: e.clientX, y: e.clientY, tx: cam.value.tx, ty: cam.value.ty }
    moved = false
    dragging.value = true
    stage.value?.setPointerCapture?.(e.pointerId)
  })

  useEventListener(stage, 'pointermove', (e: PointerEvent) => {
    if (!dragStart)
      return
    const dx = e.clientX - dragStart.x
    const dy = e.clientY - dragStart.y
    if (Math.abs(dx) + Math.abs(dy) > TAP_THRESHOLD_PX)
      moved = true
    cam.value = { ...cam.value, tx: dragStart.tx + dx, ty: dragStart.ty + dy }
  })

  useEventListener(stage, 'pointerup', (e: PointerEvent) => {
    dragging.value = false
    if (!dragStart)
      return
    dragStart = null
    if (!moved) {
      const [sx, sy] = stageCoords(e)
      options?.onTap?.(sx, sy)
    }
  })

  useEventListener(stage, 'pointercancel', () => {
    dragging.value = false
    dragStart = null
  })

  onUnmounted(cancelFlight)

  return {
    cam,
    k0,
    size: readonly(size),
    rel,
    level,
    dragging,
    zoomBy,
    panBy,
    flyTo,
    fit,
    centreWorld,
  }
}
