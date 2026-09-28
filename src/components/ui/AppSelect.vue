<script setup lang="ts" generic="T extends string | number">
import type { SelectOption } from './selectOption'
import { computed, nextTick, onUnmounted, ref, useId, watch } from 'vue'

defineOptions({ name: 'AppSelect', inheritAttrs: false })

const props = withDefaults(defineProps<{
  modelValue: T
  options: readonly SelectOption<T>[]
  id?: string
  ariaLabel?: string
  disabled?: boolean
  size?: 'default' | 'compact'
}>(), {
  size: 'default',
})

const emit = defineEmits<{ 'update:modelValue': [value: T] }>()

// `compact` matches the pre-migration native `<select>` dimensions restored at
// the four call sites that sit beside `py-1` siblings (filter-bar inputs, the
// TaskDependenciesTab stage selects); `default` is the original AppSelect size.
const SIZE_CLASSES: Record<'default' | 'compact', string> = {
  default: 'px-3 py-2 text-sm',
  compact: 'px-2 py-1 text-xs',
}
const sizeClass = computed(() => SIZE_CLASSES[props.size])

// Panel is measured by actual rendered height once mounted; this is only the
// pre-render estimate used to decide flip direction before that measurement
// exists, and the CSS max-height applied to the panel itself.
const PANEL_MAX_HEIGHT = 320

const TRIGGER_CLASS = 'bg-card border border-line rounded-md text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent'

const panelId = useId()
const buttonRef = ref<HTMLButtonElement | null>(null)
const inputRef = ref<HTMLInputElement | null>(null)
const panelRef = ref<HTMLDivElement | null>(null)

const isOpen = ref(false)
const query = ref('')
const activeIndex = ref(-1)
const inputSize = ref<{ width?: string, height?: string }>({})
const panelPosition = ref<{ top?: string, bottom?: string, left: string, minWidth: string, maxWidth: string }>({ left: '0px', minWidth: '0px', maxWidth: '0px' })

function triggerEl(): HTMLElement | null {
  return inputRef.value ?? buttonRef.value
}

// One-shot suppression for the click that follows a dismissing outside
// mousedown, so closing the panel doesn't also activate whatever was under
// the pointer (a native popup consumed that click too). Cleared on the very
// next mousedown as well (onAnyMouseDown, added only while armed), in case
// the pointer is dragged away and no click ever follows, and on a 0ms
// timeout fallback so it can never outlive the gesture that armed it — a
// click that belongs to the same gesture is already a pending/dispatching
// task by the time this timeout is scheduled, so it always runs first (see
// AppSelect.test.ts for the scenario this guards).
let suppressNextClick: ((e: MouseEvent) => void) | null = null

function clearClickSuppression() {
  if (suppressNextClick) {
    document.removeEventListener('click', suppressNextClick, true)
    document.removeEventListener('mousedown', onAnyMouseDown, true)
    suppressNextClick = null
  }
}

const selectedLabel = computed(() => props.options.find(o => o.value === props.modelValue)?.label ?? '')
const visibleOptions = computed(() => {
  const q = query.value.toLowerCase()
  if (!q)
    return props.options
  return props.options.filter(o => o.label.toLowerCase().includes(q) || (typeof o.value === 'string' && o.value.toLowerCase().includes(q)))
})
const activeOptionId = computed(() => (isOpen.value && activeIndex.value >= 0 ? optionId(activeIndex.value) : undefined))

function optionId(idx: number): string {
  return `${panelId}-option-${idx}`
}

function isSelected(opt: SelectOption<T>): boolean {
  return opt.value === props.modelValue
}

// Active row uses the solid accent fill (same bg-accent/text-accent-contrast
// pairing as AppButton's primary variant) instead of bg-card, whose contrast
// against the bg-raised panel measured 1.10:1/1.13:1 (light/dark) — far below
// the 3:1 WCAG 2.2 non-text minimum and visually indistinguishable from the
// panel. Selected-only rows keep the lighter text-accent + checkmark cue so
// active and selected stay visually distinct even when combined.
function optionClass(opt: SelectOption<T>, idx: number) {
  if (opt.disabled)
    return 'opacity-50 cursor-not-allowed text-fg-faint'
  if (idx === activeIndex.value) {
    return ['bg-accent text-accent-contrast', isSelected(opt) ? 'font-medium' : '']
  }
  return isSelected(opt) ? 'text-accent font-medium' : 'text-fg'
}

function firstEnabledIndex(): number {
  return visibleOptions.value.findIndex(o => !o.disabled)
}

function moveActive(delta: number) {
  const len = visibleOptions.value.length
  if (len === 0)
    return
  let idx = activeIndex.value
  for (let step = 0; step < len; step++) {
    idx = (idx + delta + len) % len
    if (!visibleOptions.value[idx]?.disabled) {
      activeIndex.value = idx
      scrollActiveIntoView()
      return
    }
  }
}

function scrollActiveIntoView() {
  nextTick(() => {
    const panel = panelRef.value
    const idx = activeIndex.value
    if (!panel || idx < 0)
      return
    const el = panel.children[idx] as HTMLElement | undefined
    el?.scrollIntoView?.({ block: 'nearest' })
  })
}

// Small gap kept between the panel and the viewport edge so it never
// touches the browser chrome.
const VIEWPORT_MARGIN = 8

function updatePosition() {
  const trigger = triggerEl()
  if (!trigger)
    return
  const rect = trigger.getBoundingClientRect()
  const measuredHeight = panelRef.value?.getBoundingClientRect().height ?? PANEL_MAX_HEIGHT
  const spaceBelow = window.innerHeight - rect.bottom
  const spaceAbove = rect.top
  const flip = spaceBelow < measuredHeight && spaceAbove > spaceBelow
  // `left` is clamped to keep the trigger's own width on-screen; `maxWidth`
  // is then derived from whatever space remains to the right of that left
  // edge, so a long option label (or a long selected-value label) can never
  // push the fixed-position panel past the viewport's right edge.
  const left = Math.max(VIEWPORT_MARGIN, Math.min(rect.left, window.innerWidth - VIEWPORT_MARGIN - rect.width))
  const maxWidth = window.innerWidth - VIEWPORT_MARGIN - left
  panelPosition.value = flip
    ? { bottom: `${window.innerHeight - rect.top}px`, left: `${left}px`, minWidth: `${rect.width}px`, maxWidth: `${maxWidth}px` }
    : { top: `${rect.bottom}px`, left: `${left}px`, minWidth: `${rect.width}px`, maxWidth: `${maxWidth}px` }
}

// The input stands in for the (hidden, still mounted) button while open,
// sized to it so the surrounding layout does not jump.
async function openPanel(initialQuery = '') {
  if (props.disabled || isOpen.value)
    return
  const rect = buttonRef.value?.getBoundingClientRect()
  inputSize.value = rect ? { width: `${rect.width}px`, height: `${rect.height}px` } : {}
  query.value = initialQuery
  isOpen.value = true
  const selected = visibleOptions.value.findIndex(isSelected)
  activeIndex.value = !initialQuery && selected >= 0 ? selected : firstEnabledIndex()
  updatePosition()
  await nextTick()
  // WKWebView (the desktop app's webview) does not focus a <button> on
  // click, so focus is always moved explicitly. `preventScroll` keeps focus
  // from scrolling the input into view before updatePosition() below reads
  // its (still pre-scroll) getBoundingClientRect().
  inputRef.value?.focus({ preventScroll: true })
  updatePosition()
  scrollActiveIntoView()
}

function closePanel(opts: { refocus?: boolean } = {}) {
  if (!isOpen.value)
    return
  isOpen.value = false
  query.value = ''
  if (opts.refocus)
    nextTick(() => buttonRef.value?.focus())
}

function toggle() {
  if (isOpen.value)
    closePanel()
  else
    openPanel()
}

function onInput(e: Event) {
  query.value = (e.target as HTMLInputElement).value
  activeIndex.value = firstEnabledIndex()
  scrollActiveIntoView()
}

function selectOption(opt: SelectOption<T>) {
  if (opt.disabled)
    return
  // A native `change` never fired when the value was unchanged — re-picking
  // the current selection must not trigger downstream side effects (PATCH
  // requests, folder re-resolution, tab switches, ...).
  if (opt.value !== props.modelValue)
    emit('update:modelValue', opt.value)
  closePanel({ refocus: true })
}

function commitActive() {
  const opt = visibleOptions.value[activeIndex.value]
  if (opt)
    selectOption(opt)
}

function onOptionMouseMove(idx: number, opt: SelectOption<T>) {
  if (opt.disabled || activeIndex.value === idx)
    return
  activeIndex.value = idx
}

// Escape bubbles from the closed button so it can still close an enclosing
// modal (SpawnDialog's window listener, AppModal's @keydown.escape).
function onButtonKeydown(e: KeyboardEvent) {
  if (props.disabled)
    return
  if (['Enter', ' ', 'ArrowDown', 'ArrowUp'].includes(e.key)) {
    e.preventDefault()
    openPanel()
  }
  else if (e.key.length === 1 && !e.altKey && !e.ctrlKey && !e.metaKey) {
    e.preventDefault()
    openPanel(e.key)
  }
}

// Tab is left to the browser: focus leaves the input and @blur closes.
function onInputKeydown(e: KeyboardEvent) {
  switch (e.key) {
    case 'Enter':
      e.preventDefault()
      commitActive()
      break
    case 'ArrowDown':
    case 'ArrowUp':
      e.preventDefault()
      moveActive(e.key === 'ArrowDown' ? 1 : -1)
      break
    case 'Escape':
      e.preventDefault()
      e.stopPropagation()
      closePanel({ refocus: true })
      break
  }
}

function onWindowScrollOrResize() {
  if (isOpen.value)
    updatePosition()
}

function onDocumentMouseDown(e: MouseEvent) {
  const target = e.target as Node
  if (triggerEl()?.contains(target) || panelRef.value?.contains(target))
    return
  closePanel()
  // Only a primary-button mousedown is ever followed by a same-gesture
  // click. A right- or middle-click has no click event coming — arming here
  // would leave the suppressor sitting on `document` until it swallows an
  // unrelated later click (keyboard activation, `element.click()`, ...).
  if (e.button !== 0)
    return
  // The mousedown that dismisses the panel is immediately followed by a
  // click on the same element (the modal backdrop, an agent card, ...) — a
  // native popup swallowed that click too. Intercept it once, in the
  // capture phase, so it never reaches the target's own click handler.
  clearClickSuppression()
  suppressNextClick = (clickEvent: MouseEvent) => {
    clickEvent.preventDefault()
    clickEvent.stopPropagation()
    clearClickSuppression()
  }
  document.addEventListener('click', suppressNextClick, true)
  document.addEventListener('mousedown', onAnyMouseDown, true)
  setTimeout(clearClickSuppression, 0)
}

// Added only while a suppression is armed (see onDocumentMouseDown), so it
// runs on every mousedown that follows, independent of isOpen, purely to
// drop a pending click-suppression left over from a previous
// outside-mousedown whose click never arrived (e.g. the pointer was pressed
// down and dragged elsewhere) — otherwise it would wrongly swallow an
// unrelated later click. Removed again by clearClickSuppression().
function onAnyMouseDown() {
  clearClickSuppression()
}

// Scroll uses the capture phase so a scroll on any ancestor (not just window)
// still repositions the teleported panel — chosen over closing on scroll
// because the panel would otherwise vanish for the common case of scrolling
// a modal or a card list that merely shifts the trigger, not the whole page.
watch(isOpen, (open) => {
  if (open) {
    window.addEventListener('scroll', onWindowScrollOrResize, true)
    window.addEventListener('resize', onWindowScrollOrResize)
    document.addEventListener('mousedown', onDocumentMouseDown, true)
  }
  else {
    window.removeEventListener('scroll', onWindowScrollOrResize, true)
    window.removeEventListener('resize', onWindowScrollOrResize)
    document.removeEventListener('mousedown', onDocumentMouseDown, true)
  }
})

onUnmounted(() => {
  window.removeEventListener('scroll', onWindowScrollOrResize, true)
  window.removeEventListener('resize', onWindowScrollOrResize)
  document.removeEventListener('mousedown', onDocumentMouseDown, true)
  // Also removes onAnyMouseDown's listener if a suppression is armed.
  clearClickSuppression()
})
</script>

<template>
  <input
    v-if="isOpen"
    :id="id"
    ref="inputRef"
    type="text"
    role="combobox"
    aria-expanded="true"
    aria-autocomplete="list"
    :aria-controls="panelId"
    :aria-activedescendant="activeOptionId"
    :aria-label="ariaLabel"
    autocomplete="off"
    spellcheck="false"
    :value="query"
    :placeholder="selectedLabel"
    :class="[$attrs.class, sizeClass, TRIGGER_CLASS]"
    class="placeholder:text-fg-mute"
    :style="inputSize"
    @input="onInput"
    @keydown="onInputKeydown"
    @blur="closePanel()"
  >
  <button
    v-show="!isOpen"
    :id="isOpen ? undefined : id"
    ref="buttonRef"
    v-bind="$attrs"
    type="button"
    role="combobox"
    :aria-expanded="isOpen"
    aria-haspopup="listbox"
    :aria-controls="panelId"
    :aria-label="ariaLabel"
    :disabled="disabled"
    :class="[sizeClass, TRIGGER_CLASS]"
    class="disabled:opacity-50 cursor-pointer inline-flex items-center justify-between gap-2 text-left"
    @click="toggle"
    @keydown="onButtonKeydown"
  >
    <span class="truncate" :title="selectedLabel || undefined">{{ selectedLabel }}</span>
    <span aria-hidden="true" class="text-fg-mute text-xs leading-none flex-shrink-0">▾</span>
  </button>

  <Teleport to="body">
    <div
      v-if="isOpen"
      :id="panelId"
      ref="panelRef"
      role="listbox"
      :aria-label="ariaLabel"
      class="fixed z-[1500] bg-raised border border-line-strong rounded-md shadow-modal py-1 overflow-y-auto"
      :style="{ ...panelPosition, maxHeight: `${PANEL_MAX_HEIGHT}px` }"
      @mousedown.prevent
    >
      <!--
        z-[1500] must stay strictly above every AppModal instance (highest
        is EditGateModal's :z-index="1100"; TaskModal/AgentModal are 1000)
        and strictly below the always-on-top layer at z-index 2000
        (SpotlightSearch, ToastHost, App.vue's toast host) — equal z-index
        left paint order to Teleport DOM insertion order, which only
        worked by accident. mousedown.prevent keeps focus in the filter
        input, whose blur would otherwise close the panel before a click.
      -->
      <div
        v-for="(opt, idx) in visibleOptions"
        :id="optionId(idx)"
        :key="opt.value"
        role="option"
        :aria-selected="isSelected(opt)"
        :aria-disabled="opt.disabled ? 'true' : undefined"
        :title="opt.label || undefined"
        class="px-3 py-1.5 text-sm flex items-center justify-between gap-2"
        :class="optionClass(opt, idx)"
        @click="selectOption(opt)"
        @mousemove="onOptionMouseMove(idx, opt)"
      >
        <span class="truncate">{{ opt.label }}</span>
        <span v-if="isSelected(opt)" aria-hidden="true" class="flex-shrink-0" :class="idx === activeIndex ? 'text-accent-contrast' : 'text-accent'">✓</span>
      </div>
      <div v-if="!visibleOptions.length" role="option" aria-disabled="true" aria-selected="false" class="px-3 py-1.5 text-sm text-fg-mute">
        No matches
      </div>
    </div>
  </Teleport>
</template>
