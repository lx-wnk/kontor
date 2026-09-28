<script setup lang="ts">
import type { Agent, PipelineTask } from '../types'
import type { ActiveView } from '@/composables/useViewState'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ACTIVE_VIEWS, useViewState } from '@/composables/useViewState'
import { useKontorSession } from '@/features/mission/composables/useKontorSession'
import { SLASH_COMMAND_REFUSAL } from '@/features/mission/composables/useReading'
import { pageView, pageWithWidget, useWorkspace, ZENTRALE_PAGE_ID } from '@/features/workspace'
import AppModal from './ui/AppModal.vue'

const emit = defineEmits<{
  navigateTask: [task: PipelineTask]
  navigateAgent: [agent: Agent]
}>()

const { activeView } = useViewState()
const kontor = useKontorSession()
const { layout } = useWorkspace()

interface Command {
  id: string
  label: string
  run: () => void
}

// Derived from ACTIVE_VIEWS so a view added there shows up here without a
// second list to keep in step.
const commands = computed<Command[]>(() => [
  ...ACTIVE_VIEWS.map(view => ({
    id: `view:${view}`,
    label: `Go to ${view}`,
    run: () => { activeView.value = view },
  })),
  ...layout.value.pages.filter(p => p.id !== ZENTRALE_PAGE_ID).map(p => ({
    id: `view:page:${p.id}`,
    label: `Go to ${p.title}`,
    run: () => { activeView.value = `page:${p.id}` },
  })),
])

const busy = ref(false)
const problem = ref('')

const KONTOR_NO_TILE = 'Kontor has no tile — add it to a page with Edit layout.'

// The Kontor tile can be removed or swapped off any page, so hand-off has to
// find where the reply would actually show before it navigates there.
function kontorTargetView(): ActiveView | null {
  const page = pageWithWidget(layout.value, 'kontor')
  return page ? pageView(page.id) : null
}

const open = ref(false)
const query = ref('')
const inputRef = ref<HTMLInputElement | null>(null)
const selectedIdx = ref(0)

// Focus management: store the element that triggered the dialog so we can restore focus on close
let previouslyFocusedElement: Element | null = null

interface SearchResults {
  tasks: PipelineTask[]
  agents: Agent[]
}

const results = ref<SearchResults>({ tasks: [], agents: [] })
const loading = ref(false)
let debounceHandle: ReturnType<typeof setTimeout> | null = null
let abortController: AbortController | null = null

type FlatResult
  = | { type: 'task', item: PipelineTask }
    | { type: 'agent', item: Agent }
    | { type: 'command', item: Command }

const matchingCommands = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q)
    return []
  return commands.value.filter(c => c.label.toLowerCase().includes(q))
})

const flatResults = computed((): FlatResult[] => {
  return [
    ...results.value.tasks.map(t => ({ type: 'task' as const, item: t })),
    ...results.value.agents.map(a => ({ type: 'agent' as const, item: a })),
    ...matchingCommands.value.map(c => ({ type: 'command' as const, item: c })),
  ]
})

// Text that matches nothing goes to the Kontor session, which creates a task only when asked.
const handingOff = computed(() =>
  query.value.trim().length > 0 && !loading.value && flatResults.value.length === 0,
)

// Clamp selectedIdx when results shrink to avoid out-of-bounds
watch(flatResults, (newResults) => {
  selectedIdx.value = Math.min(selectedIdx.value, Math.max(0, newResults.length - 1))
})

async function search(q: string) {
  abortController?.abort()
  abortController = new AbortController()

  if (!q.trim()) {
    results.value = { tasks: [], agents: [] }
    return
  }
  loading.value = true
  try {
    const res = await fetch(`/api/search?q=${encodeURIComponent(q)}&type=all&limit=10`, {
      signal: abortController.signal,
    })
    if (!res.ok) {
      results.value = { tasks: [], agents: [] }
      return
    }
    results.value = await res.json() as SearchResults
    selectedIdx.value = 0
  }
  catch (e) {
    if (e instanceof Error && e.name === 'AbortError')
      return // ignore aborted requests
    results.value = { tasks: [], agents: [] }
  }
  finally {
    loading.value = false
  }
}

function activate(result: FlatResult) {
  if (result.type === 'task')
    emit('navigateTask', result.item)
  else if (result.type === 'agent')
    emit('navigateAgent', result.item)
  else
    result.item.run()
  closeDialog()
}

async function handOff() {
  const text = query.value.trim()
  if (!text || busy.value)
    return
  const targetView = kontorTargetView()
  if (!targetView) {
    problem.value = KONTOR_NO_TILE
    return
  }
  if (text.startsWith('/') && kontor.pid.value === null) {
    await kontor.refresh()
    if (kontor.pid.value === null) {
      problem.value = SLASH_COMMAND_REFUSAL
      return
    }
  }
  busy.value = true
  problem.value = ''
  activeView.value = targetView
  if (await kontor.send(text))
    closeDialog()
  else
    problem.value = kontor.error.value
  busy.value = false
}

function openDialog() {
  previouslyFocusedElement = document.activeElement
  open.value = true
  void nextTick(() => inputRef.value?.focus())
}

function closeDialog() {
  open.value = false
  query.value = ''
  problem.value = ''
  results.value = { tasks: [], agents: [] }
  // Restore focus to the element that was focused before the dialog opened
  if (previouslyFocusedElement instanceof HTMLElement) {
    previouslyFocusedElement.focus()
  }
}

watch(query, (q) => {
  if (debounceHandle)
    clearTimeout(debounceHandle)
  debounceHandle = setTimeout(() => {
    search(q).catch(() => {
      results.value = { tasks: [], agents: [] }
    })
  }, 200)
})

function onKeydown(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
    e.preventDefault()
    if (open.value)
      closeDialog()
    else
      openDialog()
    return
  }
  if (!open.value)
    return
  if (e.key === 'Escape') {
    closeDialog()
    return
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    selectedIdx.value = Math.min(selectedIdx.value + 1, flatResults.value.length - 1)
    return
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    selectedIdx.value = Math.max(selectedIdx.value - 1, 0)
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    if (handingOff.value) {
      void handOff()
      return
    }
    const selected = flatResults.value[selectedIdx.value]
    if (!selected)
      return
    activate(selected)
  }
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <AppModal :open="open" :z-index="2000" size="auto" labelled-by="spotlight-search-label" @close="closeDialog">
    <div
      class="bg-card rounded-xl border border-line shadow-2xl w-full max-w-lg overflow-hidden"
    >
      <span id="spotlight-search-label" class="sr-only">Quick search</span>
      <!-- Live region for result count -->
      <div aria-live="polite" class="sr-only">
        {{ flatResults.length }} results
      </div>
      <div class="flex items-center gap-2 px-4 py-3 border-b border-line focus-within:ring-[3px] focus-within:ring-accent">
        <span class="text-fg-faint text-sm" aria-hidden="true">⌘K</span>
        <input
          ref="inputRef"
          v-model="query"
          type="text"
          role="combobox"
          :aria-expanded="flatResults.length > 0"
          aria-controls="spotlight-listbox"
          aria-autocomplete="list"
          :aria-activedescendant="selectedIdx >= 0 && flatResults.length > 0 ? `spotlight-opt-${selectedIdx}` : undefined"
          placeholder="Search tasks and agents…"
          class="flex-1 bg-transparent text-sm text-fg focus-visible:outline-none placeholder:text-fg-faint"
        >
        <span v-if="loading" class="text-xs text-fg-faint">Searching…</span>
      </div>
      <div
        id="spotlight-listbox"
        role="listbox"
        :aria-busy="loading"
        class="max-h-80 overflow-y-auto"
      >
        <template v-if="flatResults.length === 0 && query">
          <p class="px-4 py-3 text-sm text-fg-faint">
            No results for "{{ query }}"
          </p>
        </template>
        <template v-else>
          <!-- Tasks section -->
          <template v-if="results.tasks.length > 0">
            <div class="px-3 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-fg-faint">
              Tasks
            </div>
            <div
              v-for="(task, idx) in results.tasks"
              :id="`spotlight-opt-${idx}`"
              :key="`task-${task.id}`"
              role="option"
              tabindex="-1"
              :aria-selected="selectedIdx === idx"
              class="w-full text-left px-4 py-2 text-sm flex items-center gap-3 transition-colors cursor-pointer"
              :class="selectedIdx === idx
                ? 'bg-accent-soft text-accent'
                : 'text-fg-soft hover:bg-raised'"
              @click="activate({ type: 'task', item: task })"
              @mouseenter="selectedIdx = idx"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-faint w-10 flex-shrink-0">Task</span>
              <span class="truncate">{{ task.title }}</span>
              <span class="ml-auto text-[10px] text-fg-faint">{{ task.currentStage }}</span>
            </div>
          </template>

          <!-- Agents section -->
          <template v-if="results.agents.length > 0">
            <div
              class="px-3 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-fg-faint"
              :class="{ 'border-t border-line mt-1': results.tasks.length > 0 }"
            >
              Agents
            </div>
            <div
              v-for="(agent, idx) in results.agents"
              :id="`spotlight-opt-${results.tasks.length + idx}`"
              :key="`agent-${agent.sessionId}`"
              role="option"
              tabindex="-1"
              :aria-selected="selectedIdx === (results.tasks.length + idx)"
              class="w-full text-left px-4 py-2 text-sm flex items-center gap-3 transition-colors cursor-pointer"
              :class="selectedIdx === (results.tasks.length + idx)
                ? 'bg-accent-soft text-accent'
                : 'text-fg-soft hover:bg-raised'"
              @click="activate({ type: 'agent', item: agent })"
              @mouseenter="selectedIdx = results.tasks.length + idx"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-faint w-10 flex-shrink-0">Agent</span>
              <span class="truncate">{{ agent.projectName }}</span>
              <span class="ml-auto text-[10px] text-fg-faint">{{ agent.status }}</span>
            </div>
          </template>

          <!-- Commands section -->
          <template v-if="matchingCommands.length > 0">
            <div
              class="px-3 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-fg-faint"
              :class="{ 'border-t border-line mt-1': results.tasks.length + results.agents.length > 0 }"
            >
              Commands
            </div>
            <div
              v-for="(command, idx) in matchingCommands"
              :id="`spotlight-opt-${results.tasks.length + results.agents.length + idx}`"
              :key="command.id"
              role="option"
              tabindex="-1"
              :aria-selected="selectedIdx === (results.tasks.length + results.agents.length + idx)"
              :data-testid="`spotlight-command-${command.id}`"
              class="w-full text-left px-4 py-2 text-sm flex items-center gap-3 transition-colors cursor-pointer"
              :class="selectedIdx === (results.tasks.length + results.agents.length + idx)
                ? 'bg-accent-soft text-accent'
                : 'text-fg-soft hover:bg-raised'"
              @click="activate({ type: 'command', item: command })"
              @mouseenter="selectedIdx = results.tasks.length + results.agents.length + idx"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-faint w-10 flex-shrink-0">Go</span>
              <span class="truncate">{{ command.label }}</span>
            </div>
          </template>
        </template>

        <p
          v-if="problem"
          data-testid="spotlight-problem"
          role="alert"
          class="mx-3 my-2 text-[12px] rounded-md px-2 py-1.5 bg-warning-soft text-warning-text"
        >
          {{ problem }}
        </p>
        <p
          v-else-if="handingOff"
          data-testid="spotlight-kontor"
          class="px-4 py-2 text-[12px] text-fg-faint"
        >
          {{ busy ? 'Handing to Kontor…' : 'Nothing matched — ↵ hands this to Kontor.' }}
        </p>
      </div>
      <div class="px-4 py-2 border-t border-line flex gap-3 text-[10px] text-fg-faint">
        <span>↑↓ navigate</span>
        <span>↵ open or ask Kontor</span>
        <span>Esc close</span>
      </div>
    </div>
  </AppModal>
</template>

<style scoped>
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border-width: 0;
}
</style>
