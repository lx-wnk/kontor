<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { lockBodyScroll } from './scrollLock'

const props = withDefaults(defineProps<{
  open: boolean
  zIndex?: number
  labelledBy?: string
  /**
   * Panel sizing.
   * - `standard` (default): chrome panel with configurable width, max-h 85vh.
   *   `width` prop controls the max panel width (px value or CSS class).
   * - `auto`: no sizing or chrome — the slot brings its own box. Used by the
   *   deliberate exceptions (command palette, settings with its own layout).
   */
  size?: 'standard' | 'auto'
  /** Width for the standard panel (px string like "560px" or CSS class). Defaults to 900px. */
  width?: string
}>(), {
  zIndex: 200,
  size: 'standard',
  width: '900px',
})

const emit = defineEmits<{ close: [] }>()
// Chrome for the standard size. `auto` is a transparent passthrough.
const STANDARD_CHROME = 'bg-card border border-line rounded-xl shadow-modal overflow-hidden flex flex-col'
const panelClass = computed(() => (props.size === 'standard' ? STANDARD_CHROME : ''))
const panelStyle = computed(() =>
  props.size === 'standard'
    ? { width: `min(${props.width}, calc(100vw - 2rem))`, maxHeight: '85vh' }
    : undefined,
)

const modalPanelRef = ref<HTMLElement | null>(null)
const previouslyFocused = ref<HTMLElement | null>(null)
let releaseScroll: (() => void) | null = null

// Nested modals share one body scroll lock; the last to close releases it.
watch(() => props.open, (isOpen) => {
  if (isOpen) {
    releaseScroll ??= lockBodyScroll()
    // Capture currently focused element so it can be restored on close
    previouslyFocused.value = document.activeElement as HTMLElement | null
    // Focus the modal panel so keyboard events are captured immediately
    nextTick(() => {
      if (modalPanelRef.value)
        modalPanelRef.value.focus()
    })
  }
  else {
    releaseScroll?.()
    releaseScroll = null
    restoreFocus()
  }
}, { immediate: true })

onUnmounted(() => {
  releaseScroll?.()
  restoreFocus()
})

function restoreFocus() {
  const target = previouslyFocused.value
  previouslyFocused.value = null
  if (target && document.contains(target))
    target.focus()
}

function trapFocus(event: KeyboardEvent) {
  if (event.key !== 'Tab')
    return
  const FOCUSABLE_SELECTOR = [
    'a[href]',
    'button:not([disabled])',
    'textarea:not([disabled])',
    'input:not([disabled]):not([type="hidden"])',
    'select:not([disabled])',
    '[contenteditable]:not([contenteditable="false"])',
    'audio[controls]',
    'video[controls]',
    'details > summary:first-of-type',
    '[tabindex]:not([tabindex="-1"])',
  ].join(',')
  const all = Array.from(modalPanelRef.value?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? [])
  const focusable = all.filter(el => el.offsetParent !== null || el === document.activeElement)
  if (focusable.length === 0)
    return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey) {
    if (document.activeElement === first) {
      event.preventDefault()
      last.focus()
    }
  }
  else {
    if (document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div
        v-if="open"
        class="fixed inset-0 flex items-center justify-center p-4 bg-black/55 backdrop-blur-sm"
        :style="{ zIndex }"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="labelledBy || undefined"
        @click.self="emit('close')"
        @keydown.escape="emit('close')"
      >
        <div
          ref="modalPanelRef"
          class="base-modal-box outline-none"
          :class="panelClass"
          :style="panelStyle"
          tabindex="-1"
          @keydown="trapFocus"
        >
          <slot />
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style>
/* Transition styles must be global (not scoped) to target children */
.dialog-enter-active,
.dialog-leave-active {
  transition: opacity 0.2s ease;
}
.dialog-enter-active .base-modal-box,
.dialog-leave-active .base-modal-box {
  transition: transform 0.2s ease, opacity 0.2s ease;
}
.dialog-enter-from,
.dialog-leave-to {
  opacity: 0;
}
.dialog-enter-from .base-modal-box,
.dialog-leave-to .base-modal-box {
  transform: scale(0.95);
  opacity: 0;
}
</style>
