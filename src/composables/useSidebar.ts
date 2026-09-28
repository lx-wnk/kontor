import { computed, readonly, ref, watch } from 'vue'
import { isTypingTarget } from '@/utils/isTypingTarget'

const storedPinned = typeof localStorage !== 'undefined'
  ? localStorage.getItem('agent-sidebar-pinned') === 'true'
  : false

const pinned = ref<boolean>(storedPinned)
const hovering = ref(false)
const focused = ref(false)
// Set when a nav item is picked with the pointer still over the floating nav.
// Without it the nav stays expanded across the content it just navigated to,
// and anything under those 220px swallows the next click.
const pointerSuppressed = ref(false)
const newPageRequests = ref(0)
const expanded = computed(() =>
  pinned.value || focused.value || (hovering.value && !pointerSuppressed.value))

watch(pinned, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-sidebar-pinned', String(v))
}, { flush: 'sync' })

function togglePinned() {
  pinned.value = !pinned.value
}

function setHovering(v: boolean) {
  hovering.value = v
  // Leaving re-arms hover expansion for the next entry.
  if (!v)
    pointerSuppressed.value = false
}

// Keyboard expansion is tracked separately so a pointer suppression never hides
// the labels from someone tabbing through the nav.
function setFocused(v: boolean) {
  focused.value = v
}

function collapseAfterSelect() {
  if (!hovering.value)
    return
  pointerSuppressed.value = true
  // A real browser focuses the button as part of the click, so `focused` is
  // already set by the time this runs and would hold the nav open on its own.
  // Picking the view that is already active moves focus nowhere afterwards, so
  // nothing would ever clear it again.
  focused.value = false
}

function handleShortcut(e: KeyboardEvent) {
  if (e.key === 'b' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
    if (isTypingTarget(e.target))
      return
    e.preventDefault()
    togglePinned()
  }
}

function requestNewPage() {
  newPageRequests.value++
}

export function useSidebar() {
  return { pinned, hovering, expanded, togglePinned, setHovering, setFocused, collapseAfterSelect, handleShortcut, newPageRequests: readonly(newPageRequests), requestNewPage }
}
