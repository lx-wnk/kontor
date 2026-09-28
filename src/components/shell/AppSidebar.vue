<script setup lang="ts">
import type { ActiveView } from '../../composables/useViewState'
import { computed, nextTick, ref, watch } from 'vue'
import { addPage, useWorkspace, ZENTRALE_PAGE_ID } from '@/features/workspace'
import { useSidebar } from '../../composables/useSidebar'
import { useViewState } from '../../composables/useViewState'
import { NAV_GROUPS, NAV_ITEMS } from '../../utils/navConfig'
import NavItem from './NavItem.vue'
import SidebarFooter from './SidebarFooter.vue'

const props = defineProps<{
  agentCount: number
  attentionCount: number
  taskCount: number
  live: boolean
  theme: 'dark' | 'light'
  canInstall: boolean
}>()
const emit = defineEmits<{
  openSessions: []
  toggleTheme: []
  install: []
  openSettings: []
}>()

const { expanded, pinned, togglePinned, setHovering, setFocused, collapseAfterSelect, newPageRequests } = useSidebar()
const { activeView } = useViewState()

const grouped = computed(() =>
  NAV_GROUPS.map(group => ({ group, items: NAV_ITEMS.filter(i => i.group === group) })))

const workspace = useWorkspace()
const ownPages = computed(() => workspace.layout.value.pages.filter(p => p.id !== ZENTRALE_PAGE_ID))
const creatingPage = ref(false)
const newPageSlot = ref<HTMLElement | null>(null)

function badgeFor(view: ActiveView): number | null {
  if (view === 'dashboard')
    return props.attentionCount > 0 ? props.attentionCount : props.agentCount
  if (view === 'pipeline')
    return props.taskCount
  return null
}

function badgeDanger(view: ActiveView): boolean {
  return view === 'dashboard' && props.attentionCount > 0
}

// Unpinned expansion floats above the content instead of widening the rail, so
// hovering the nav never reflows the page behind it.
const floating = computed(() => expanded.value && !pinned.value)

function onFocusOut(event: FocusEvent): void {
  const next = event.relatedTarget as Node | null
  if (!next || !(event.currentTarget as HTMLElement).contains(next))
    setFocused(false)
}

function selectView(view: ActiveView): void {
  activeView.value = view
  collapseAfterSelect()
}

async function startNewPage(): Promise<void> {
  creatingPage.value = true
  await nextTick()
  newPageSlot.value?.querySelector('input')?.focus()
}

watch(newPageRequests, () => void startNewPage())

async function cancelNewPage(): Promise<void> {
  creatingPage.value = false
  await nextTick()
  newPageSlot.value?.querySelector('button')?.focus()
}

async function createPage(event: KeyboardEvent): Promise<void> {
  const input = event.target as HTMLInputElement
  if (!input.value.trim()) {
    void cancelNewPage()
    return
  }
  const r = addPage(workspace.layout.value, input.value)
  if (!r.ok) {
    input.setCustomValidity(r.reason)
    input.reportValidity()
    return
  }
  if (!await workspace.save(r.value.layout))
    return
  workspace.editing.value = true
  selectView(`page:${r.value.pageId}`)
  // Blur before the input unmounts: a removed input fires no focusout, which would hold the nav open.
  input.blur()
}
</script>

<template>
  <div
    class="relative shrink-0 h-full transition-[width] duration-200 motion-reduce:transition-none"
    :class="pinned ? 'w-[220px]' : 'w-[56px]'"
    data-testid="sidebar-rail"
  >
    <nav
      aria-label="Primary"
      class="absolute inset-y-0 left-0 z-30 bg-card border-r border-line flex flex-col py-3 px-1.5 overflow-x-hidden transition-[width] duration-200 motion-reduce:transition-none"
      :class="[
        expanded ? 'w-[220px]' : 'w-[56px]',
        floating ? 'shadow-[4px_0_16px_rgba(0,0,0,0.18)]' : '',
      ]"
      @mouseenter="setHovering(true)"
      @mouseleave="setHovering(false)"
      @focusin="setFocused(true)"
      @focusout="onFocusOut"
    >
      <!-- Fixed height so the two-line title+status text (only readable once
           expanded) never grows the block and drops every item below it. -->
      <div class="flex items-center gap-2 px-1.5 pb-3 mb-2 border-b border-line h-11" data-testid="sidebar-brand">
        <div class="w-7 h-7 rounded-lg bg-accent shrink-0" aria-hidden="true" />
        <div
          class="min-w-0 flex flex-col transition-opacity duration-150 motion-reduce:transition-none"
          :class="expanded ? 'opacity-100 delay-75' : 'opacity-0 delay-0'"
        >
          <span class="text-[13px] font-semibold text-fg truncate leading-tight">Agent Overview</span>
          <span class="flex items-center gap-1 text-[10px] text-fg-faint whitespace-nowrap" role="status">
            <span
              class="w-1.5 h-1.5 rounded-full shrink-0"
              :class="live ? 'bg-success motion-safe:animate-pulse' : 'bg-warning'"
              aria-hidden="true"
            />
            {{ live ? 'Live · all systems normal' : 'Reconnecting…' }}
          </span>
        </div>
        <button
          type="button"
          data-testid="sidebar-pin"
          class="ml-auto text-fg-faint hover:text-fg text-[14px] rounded px-1 min-h-[28px] focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
          :aria-expanded="pinned"
          :aria-label="pinned ? 'Unpin sidebar' : 'Pin sidebar open'"
          @click="togglePinned"
        >
          <span aria-hidden="true">{{ pinned ? '«' : '»' }}</span>
        </button>
      </div>

      <div class="flex-1 flex flex-col gap-0.5 overflow-y-auto overflow-x-hidden" data-testid="nav-items">
        <div
          v-for="(g, gi) in grouped"
          :key="g.group"
          class="flex flex-col gap-0.5"
          :class="{ 'mt-auto': gi === grouped.length - 1 }"
        >
          <!-- One box of a fixed height in both states. The heading used to
               render only when expanded, so hovering inserted three rows and
               pushed every nav item down by a different amount per group —
               you aimed at an icon and clicked whatever slid under the cursor. -->
          <div
            data-testid="nav-group-slot"
            class="relative h-7 shrink-0 overflow-hidden"
          >
            <span
              class="absolute inset-0 flex items-center px-2 text-[9px] uppercase tracking-wider text-fg-faint font-bold whitespace-nowrap transition-opacity duration-150 motion-reduce:transition-none"
              :class="expanded ? 'opacity-100 delay-75' : 'opacity-0 delay-0'"
            >
              {{ g.group }}
            </span>
            <span
              v-if="gi > 0"
              aria-hidden="true"
              data-testid="nav-group-divider"
              class="absolute inset-0 flex items-center justify-center transition-opacity duration-150 motion-reduce:transition-none"
              :class="expanded ? 'opacity-0 delay-0' : 'opacity-100 delay-75'"
            >
              <span class="h-px w-6 bg-line" />
            </span>
          </div>
          <NavItem
            v-for="item in g.items"
            :key="item.view"
            :icon="item.icon"
            :label="item.label"
            :active="activeView === item.view"
            :expanded="expanded"
            @select="selectView(item.view)"
          >
            <template v-if="badgeFor(item.view) !== null" #badge>
              <span
                class="text-[9px] rounded-full px-1.5 py-0.5"
                :class="badgeDanger(item.view)
                  ? 'bg-red-500 text-white font-bold'
                  : 'bg-raised text-fg-mute'"
              >{{ badgeFor(item.view) }}</span>
            </template>
          </NavItem>
        </div>

        <div class="flex flex-col gap-0.5" data-testid="nav-pages">
          <div data-testid="nav-group-slot" class="relative h-7 shrink-0 overflow-hidden">
            <span
              class="absolute inset-0 flex items-center px-2 text-[9px] uppercase tracking-wider text-fg-faint font-bold whitespace-nowrap transition-opacity duration-150 motion-reduce:transition-none"
              :class="expanded ? 'opacity-100 delay-75' : 'opacity-0 delay-0'"
            >
              Pages
            </span>
            <span
              aria-hidden="true"
              data-testid="nav-group-divider"
              class="absolute inset-0 flex items-center justify-center transition-opacity duration-150 motion-reduce:transition-none"
              :class="expanded ? 'opacity-0 delay-0' : 'opacity-100 delay-75'"
            >
              <span class="h-px w-6 bg-line" />
            </span>
          </div>
          <NavItem
            v-for="p in ownPages"
            :key="p.id"
            :data-testid="`nav-page-${p.id}`"
            icon="▢"
            :label="p.title"
            :active="activeView === `page:${p.id}`"
            :expanded="expanded"
            @select="selectView(`page:${p.id}`)"
          />
          <!-- The input takes the button's place in the same box, so no row moves.
               It only opens from a click or key on the button, i.e. while expanded. -->
          <div
            v-if="workspace.loaded.value && !workspace.locked.value"
            ref="newPageSlot"
            data-testid="nav-new-page-slot"
            class="h-10 shrink-0"
          >
            <div v-if="creatingPage" class="flex h-full items-center gap-3 px-2.5">
              <span class="text-[16px] w-5 shrink-0 text-center text-fg-mute" aria-hidden="true">+</span>
              <input
                data-testid="nav-new-page-input"
                aria-label="New page title"
                placeholder="Page title"
                class="min-w-0 flex-1 rounded-md border border-line-strong bg-app px-2 py-1 text-[12.5px] text-fg"
                @input="($event.target as HTMLInputElement).setCustomValidity('')"
                @keydown.enter.prevent="createPage"
                @keydown.esc.prevent="cancelNewPage"
                @blur="creatingPage = false"
              >
            </div>
            <NavItem
              v-else
              data-testid="nav-new-page"
              icon="+"
              label="New page"
              :active="false"
              :expanded="expanded"
              @select="startNewPage"
            />
          </div>
        </div>
      </div>

      <SidebarFooter
        :expanded="expanded"
        :theme="theme"
        :can-install="canInstall"
        @open-sessions="emit('openSessions')"
        @toggle-theme="emit('toggleTheme')"
        @install="emit('install')"
        @open-settings="emit('openSettings')"
      />
    </nav>
  </div>
</template>
