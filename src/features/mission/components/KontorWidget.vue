<script setup lang="ts">
import { onKeyStroke, useEventListener } from '@vueuse/core'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { isTypingTarget } from '@/utils/isTypingTarget'
import { useKontorAgent, useKontorSession } from '../composables/useKontorSession'
import KontorTile from './KontorTile.vue'

const { status, openRequested, setOverlayOpen } = useKontorSession()
const agent = useKontorAgent()

const state = computed(() => {
  const a = agent.value
  if (!a)
    return status.value === 'starting' ? 'starting' : 'no session'
  if (a.status === 'waiting')
    return 'waiting'
  return a.working ? 'working' : 'idle'
})
const last = computed(() => agent.value?.lastOutput ?? (agent.value ? '' : 'Start Kontor — ask for anything'))

const cell = ref<HTMLElement | null>(null)
const overlay = ref<HTMLElement | null>(null)
const open = ref(false)
const box = ref<Record<string, string>>({})
let frameId: number | null = null

function cancelPendingFrame() {
  if (frameId !== null) {
    cancelAnimationFrame(frameId)
    frameId = null
  }
}

// Grows out of its own cell as an overlay, up to 64% of the window: the grid underneath does not re-flow.
function place(heightOverride?: number) {
  const r = cell.value!.getBoundingClientRect()
  const vh = window.innerHeight
  const up = r.top > vh - r.bottom
  const height = heightOverride ?? Math.min(vh * 0.64, (up ? r.bottom : vh - r.top) - 16)
  box.value = {
    left: `${r.left}px`,
    width: `${r.width}px`,
    height: `${height}px`,
    ...(up ? { bottom: `${vh - r.bottom}px` } : { top: `${r.top}px` }),
  }
}

// First frame: the cell's own height. Next frame: place()'s target height.
function grow() {
  cancelPendingFrame()
  place(cell.value!.getBoundingClientRect().height)
  open.value = true
  frameId = requestAnimationFrame(() => {
    frameId = null
    place()
  })
}

useEventListener(computed(() => open.value ? window : null), 'resize', () => place())
useEventListener(computed(() => open.value ? window : null), 'scroll', (e) => {
  // The overlay's own scrolling triggers this same capturing listener; skip it.
  if (overlay.value?.contains(e.target as Node))
    return
  place()
}, { capture: true })

// A pending frame from an open still in flight must not run place() against a cell that collapse/unmount already moved past.
onUnmounted(cancelPendingFrame)

watch(open, setOverlayOpen, { flush: 'sync' })
onUnmounted(() => setOverlayOpen(false))

// A watch's immediate call runs before `cell` exists, so a pending request is picked up in onMounted instead.
function openIfRequested() {
  if (!openRequested.value)
    return
  openRequested.value = false
  if (!open.value)
    grow()
}
onMounted(openIfRequested)
watch(openRequested, openIfRequested, { flush: 'post' })

async function collapse() {
  cancelPendingFrame()
  open.value = false
  await nextTick()
  cell.value?.focus()
}

onKeyStroke('/', (e) => {
  if (open.value || isTypingTarget(e.target))
    return
  e.preventDefault()
  grow()
})
onKeyStroke('Escape', () => {
  if (open.value)
    collapse()
})
</script>

<template>
  <div
    ref="cell"
    data-testid="kontor-collapsed"
    role="button"
    tabindex="0"
    aria-label="Kontor — open the session"
    class="flex h-full min-w-0 cursor-pointer items-center gap-3 rounded-xl border border-line bg-card px-4"
    @click="grow"
    @keydown.enter.prevent="grow"
  >
    <span data-testid="kontor-collapsed-state" class="shrink-0 font-mono text-[11px]" :class="state === 'waiting' ? 'text-warning-text' : state === 'working' ? 'text-success-text' : 'text-fg-mute'">
      ● {{ state }}
    </span>
    <b class="shrink-0 text-[13px] text-fg">Kontor</b>
    <span data-testid="kontor-collapsed-last" class="min-w-0 flex-1 truncate text-[12.5px] text-fg-soft">{{ last }}</span>
    <kbd class="shrink-0 rounded border border-line-strong px-1 font-mono text-[10px] text-fg-mute">/</kbd>
  </div>

  <Teleport to="body">
    <div v-if="open" ref="overlay" data-testid="kontor-expanded" class="fixed z-40 flex flex-col overflow-hidden rounded-xl border border-accent bg-card shadow-2xl transition-[height] duration-200 motion-reduce:transition-none" :style="box">
      <button type="button" data-testid="kontor-collapse" aria-label="Collapse Kontor (Esc)" class="absolute right-2 top-2 z-10 rounded border border-line-strong px-1.5 text-[12px] text-fg-mute" @click="collapse">
        ▾
      </button>
      <KontorTile class="h-full min-h-0 flex-1" />
    </div>
  </Teleport>
</template>
