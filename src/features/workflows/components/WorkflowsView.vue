<script setup lang="ts">
import type { WorkflowsFilters, WorkflowTab } from '@/features/workflows/composables/useWorkflows'
import { computed, onMounted, ref, watch } from 'vue'
import AppCard from '@/components/ui/AppCard.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { usePatterns } from '@/composables/usePatterns'
import { useSessions } from '@/composables/useSessions'
import CoOccurrenceMatrix from '@/features/workflows/components/visualizations/CoOccurrenceMatrix.vue'
import SankeyChart from '@/features/workflows/components/visualizations/SankeyChart.vue'
import SessionDagChart from '@/features/workflows/components/visualizations/SessionDagChart.vue'
import SpawnTreeChart from '@/features/workflows/components/visualizations/SpawnTreeChart.vue'
import { defaultWorkflowsFilters, useWorkflows } from '@/features/workflows/composables/useWorkflows'

const props = defineProps<{ from?: string, to?: string }>()
defineEmits<{ navigate: [sessionId: string] }>()
const TOOL_ARGS_RE = /\(.*$/
const TOOL_SPLIT_RE = / → /

const filters = ref<WorkflowsFilters>(defaultWorkflowsFilters())
const followsPage = computed(() => props.to !== undefined)

// Date-only props widen to the whole day: the filter inputs are datetime-local.
function applyPageRange() {
  if (props.to === undefined)
    return
  filters.value.from = props.from ? `${props.from}T00:00` : undefined
  filters.value.to = `${props.to}T23:59:59`
}
applyPageRange()
watch(() => [props.from, props.to], applyPageRange)

const { sankey, dag, spawnTree, coOccurrence, activeTab, setActiveTab } = useWorkflows(filters)
const { sessions, refetch: refetchSessions } = useSessions()
const { patterns, isLoading: patternsLoading } = usePatterns()

onMounted(() => {
  void refetchSessions()
})

watch(() => filters.value.sessionId, (id) => {
  if (!id && activeTab.value === 'dag') {
    setActiveTab('sankey')
  }
})

function sessionLabel(s: { projectName: string, firstPrompt: string | null, sessionId: string, isRunning: boolean }): string {
  const shortId = s.sessionId ? s.sessionId.slice(0, 8) : '????????'
  const base = s.firstPrompt
    ? `${s.projectName} · ${s.firstPrompt.slice(0, 50)}${s.firstPrompt.length > 50 ? '…' : ''}`
    : `${s.projectName} · ${shortId}`
  return s.isRunning ? `${base} (running)` : base
}

function onSessionSelect(value: string) {
  filters.value.sessionId = value
  if (value) {
    setActiveTab('dag')
  }
}

const sessionOptions = computed(() => [
  { value: '', label: '— Select session —' },
  ...sessions.value.map(s => ({ value: s.sessionId, label: sessionLabel(s) })),
])

function openSessionDag(id: string) {
  filters.value.sessionId = id
  setActiveTab('dag')
}

function onReset() {
  if (activeTab.value === 'dag') {
    setActiveTab('sankey')
  }
  filters.value = defaultWorkflowsFilters()
  applyPageRange()
}

interface TabDescriptor {
  key: WorkflowTab
  label: string
}

const TABS: TabDescriptor[] = [
  { key: 'sankey', label: 'Sankey' },
  { key: 'spawnTree', label: 'Spawn Tree' },
  { key: 'coOccurrence', label: 'Co-occurrence' },
]

// Tool tone mapping matching the design reference.
const TOOL_TONE: Record<string, string> = {
  Read: 'var(--info)',
  Edit: 'var(--accent)',
  Bash: 'var(--warning)',
  Grep: 'var(--success)',
  Write: 'var(--accent)',
  WebSearch: 'var(--fg-mute)',
  Glob: 'var(--success)',
}

function toolColor(toolName: string): string {
  const head = toolName.replace(TOOL_ARGS_RE, '')
  return TOOL_TONE[head] ?? 'var(--fg-mute)'
}

// Per-tool frequency derived from the pattern set (count occurrences weighted by frequency).
const toolFrequency = computed(() => {
  const counts: Record<string, number> = {}
  for (const p of patterns.value) {
    for (const t of p.tools.split(TOOL_SPLIT_RE)) {
      const head = t.replace(TOOL_ARGS_RE, '').trim()
      counts[head] = (counts[head] ?? 0) + p.frequency
    }
  }
  return Object.entries(counts)
    .map(([tool, n]) => ({ tool, n }))
    .sort((a, b) => b.n - a.n)
})

const maxPatternFreq = computed(() => Math.max(0, ...patterns.value.map(p => p.frequency)))
const maxToolFreq = computed(() => Math.max(0, ...toolFrequency.value.map(f => f.n)))
</script>

<template>
  <div class="workflows-view">
    <!-- Design panels: Discovered Sequences + Tool Frequency + Recent Run Waterfalls -->
    <div class="p-4 border-b border-line grid gap-4" style="grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); align-items: start;">
      <!-- Discovered Sequences -->
      <AppCard class="p-4">
        <h3 class="text-sm font-semibold text-fg-soft mb-1">
          Discovered Sequences
        </h3>
        <p class="text-xs text-fg-mute mb-3">
          Most frequent 3-tool n-grams across all sessions.
        </p>
        <div v-if="patternsLoading" class="text-xs text-fg-mute">
          Loading…
        </div>
        <div v-else-if="patterns.length === 0" class="text-xs text-fg-mute">
          No pattern data yet. Refresh patterns via Settings → Analytics.
        </div>
        <div v-else class="flex flex-col gap-2.5">
          <div
            v-for="p in patterns"
            :key="p.tools"
            class="bg-raised rounded-md px-3 py-2.5"
          >
            <div class="flex items-center justify-between gap-2 mb-1.5 flex-wrap">
              <div class="flex items-center gap-2 flex-wrap">
                <template v-for="(tool, i) in p.tools.split(' → ')" :key="i">
                  <span class="inline-flex items-center gap-1 font-mono text-[11px] text-fg-soft">
                    <span
                      class="w-2 h-2 rounded-[2px] shrink-0"
                      :style="{ background: toolColor(tool) }"
                      aria-hidden="true"
                    />
                    {{ tool }}
                  </span>
                  <span
                    v-if="i < p.tools.split(' → ').length - 1"
                    class="text-fg-faint text-[11px]"
                    aria-hidden="true"
                  >→</span>
                </template>
              </div>
              <span class="font-mono text-[11px] text-fg-faint shrink-0">×{{ p.frequency }}</span>
            </div>
            <div class="h-1 bg-line rounded-full overflow-hidden">
              <div
                class="h-full rounded-full bg-accent"
                :style="{ width: maxPatternFreq > 0 ? `${(p.frequency / maxPatternFreq) * 100}%` : '0%' }"
              />
            </div>
          </div>
        </div>
      </AppCard>

      <!-- Tool Frequency + Recent Run Waterfalls stacked -->
      <div class="flex flex-col gap-4">
        <!-- Tool Frequency -->
        <AppCard class="p-4">
          <h3 class="text-sm font-semibold text-fg-soft mb-1">
            Tool Frequency
          </h3>
          <p class="text-xs text-fg-mute mb-3">
            Total invocations by tool, all agents.
          </p>
          <div v-if="patternsLoading" class="text-xs text-fg-mute">
            Loading…
          </div>
          <div v-else-if="toolFrequency.length === 0" class="text-xs text-fg-mute">
            No data yet.
          </div>
          <div v-else class="flex flex-col gap-2">
            <div
              v-for="f in toolFrequency"
              :key="f.tool"
              class="flex items-center gap-2.5"
            >
              <span class="w-20 shrink-0 inline-flex items-center gap-1 font-mono text-[11px] text-fg-soft">
                <span
                  class="w-2 h-2 rounded-[2px] shrink-0"
                  :style="{ background: toolColor(f.tool) }"
                  aria-hidden="true"
                />
                {{ f.tool }}
              </span>
              <div class="flex-1 h-3 bg-raised rounded-full overflow-hidden">
                <div
                  class="h-full rounded-full"
                  :style="{ width: maxToolFreq > 0 ? `${(f.n / maxToolFreq) * 100}%` : '0%', background: toolColor(f.tool) }"
                />
              </div>
              <span class="w-10 text-right font-mono text-[11px] text-fg-mute">{{ f.n }}</span>
            </div>
          </div>
        </AppCard>

        <!-- Recent Run Waterfalls -->
        <AppCard class="px-4 pt-4 pb-1">
          <h3 class="text-sm font-semibold text-fg-soft mb-1">
            Recent Run Waterfalls
          </h3>
          <p class="text-xs text-fg-mute mb-3">
            Wall-clock split by tool for the latest pipeline runs.
          </p>
          <p class="text-xs text-fg-mute pb-3">
            No per-run timing data available from the current data source. Run timing is not yet exported by the backend.
          </p>
        </AppCard>
      </div>
    </div>

    <!-- Filter bar -->
    <div class="flex flex-wrap items-center gap-3 px-4 py-3 border-b border-line bg-card">
      <label class="text-xs text-fg-mute flex items-center gap-1">
        Session
        <AppSelect
          :model-value="filters.sessionId ?? ''"
          :options="sessionOptions"
          size="compact"
          class="w-[260px] max-w-[260px] truncate"
          @update:model-value="onSessionSelect($event)"
        />
      </label>
      <label class="text-xs text-fg-mute flex items-center gap-1">
        or paste ID
        <input
          v-model="filters.sessionId"
          type="text"
          class="bg-raised border border-line rounded-md px-2 py-1 text-[12px] text-fg w-[150px] focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent"
          placeholder="Session ID"
        >
      </label>
      <label v-if="!followsPage" class="text-xs text-fg-mute flex items-center gap-1">
        From
        <input
          v-model="filters.from"
          type="datetime-local"
          class="bg-raised border border-line rounded-md px-2 py-1 text-[12px] text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent"
        >
      </label>
      <label v-if="!followsPage" class="text-xs text-fg-mute flex items-center gap-1">
        To
        <input
          v-model="filters.to"
          type="datetime-local"
          class="bg-raised border border-line rounded-md px-2 py-1 text-[12px] text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent"
        >
      </label>
      <button
        type="button"
        class="ml-auto text-xs px-3 py-1 rounded-md bg-raised text-fg-mute hover:text-fg border border-line cursor-pointer"
        @click="onReset"
      >
        Reset
      </button>
    </div>

    <!-- Tab bar -->
    <div role="tablist" class="flex gap-1 px-4 py-2 border-b border-line bg-card">
      <button
        v-for="tab in TABS"
        :key="tab.key"
        type="button"
        role="tab"
        :aria-selected="activeTab === tab.key"
        class="text-xs px-3 py-1 rounded-md border-none cursor-pointer"
        :class="activeTab === tab.key ? 'bg-blue-600 text-white' : 'bg-raised text-fg-mute hover:text-fg-soft'"
        @click="setActiveTab(tab.key)"
      >
        {{ tab.label }}
      </button>
      <button
        v-if="filters.sessionId"
        type="button"
        role="tab"
        :aria-selected="activeTab === 'dag'"
        class="text-xs px-3 py-1 rounded-md border-none cursor-pointer"
        :class="activeTab === 'dag' ? 'bg-blue-600 text-white' : 'bg-raised text-fg-mute hover:text-fg-soft'"
        @click="setActiveTab('dag')"
      >
        Session DAG
      </button>
    </div>

    <!-- Visualization panel -->
    <div class="p-4">
      <SankeyChart
        v-show="activeTab === 'sankey'"
        :data="sankey.data"
        :loading="sankey.loading"
        :error="sankey.error"
      />
      <SessionDagChart
        v-show="activeTab === 'dag'"
        :data="dag.data"
        :loading="dag.loading"
        :error="dag.error"
      />
      <SpawnTreeChart
        v-show="activeTab === 'spawnTree'"
        :data="spawnTree.data"
        :loading="spawnTree.loading"
        :error="spawnTree.error"
        @navigate="openSessionDag"
      />
      <CoOccurrenceMatrix
        v-show="activeTab === 'coOccurrence'"
        :data="coOccurrence.data"
        :loading="coOccurrence.loading"
        :error="coOccurrence.error"
      />
    </div>
  </div>
</template>
