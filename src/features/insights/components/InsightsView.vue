<script setup lang="ts">
import type { Ref } from 'vue'
import type { InsightsProject, InsightsSessionRow, InsightsSessionsPage, InsightsSort, InsightsSummary, Preset } from '../insightsApi'
import type { InsightsTab } from '../insightsTab'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import { CostAnalyticsView, EvalView } from '@/features/analytics'
import { WorkflowsView } from '@/features/workflows'
import { fetchProjects, fetchSessions, fetchSummary, presetHours, presetRange } from '../insightsApi'
import { INSIGHTS_TABS, readInsightsTab, writeInsightsTab } from '../insightsTab'
import InsightsKpiRow from './InsightsKpiRow.vue'
import InsightsOverview from './InsightsOverview.vue'
import InsightsSessionDrawer from './InsightsSessionDrawer.vue'
import InsightsSessionsTable from './InsightsSessionsTable.vue'

const props = defineProps<{ liveSessionIds: string[] }>()
const emit = defineEmits<{ navigate: [sessionId: string] }>()

const PRESETS: { value: Preset, label: string }[] = [
  { value: '7d', label: '7d' },
  { value: '30d', label: '30d' },
  { value: '90d', label: '90d' },
  { value: 'all', label: 'All' },
]
const TAB_LABELS: Record<InsightsTab, string> = { overview: 'Overview', sessions: 'Sessions', cost: 'Cost', quality: 'Quality', workflows: 'Workflows' }
const PROJECT_FILTER_FREE: readonly InsightsTab[] = ['quality', 'workflows']
const PROJECT_HINT = 'Project filter does not apply to this tab.'
const PAGE_SIZE = 50
const REFRESH_MS = 60_000

const preset = ref<Preset>('30d')
const project = ref('')
const tab = ref<InsightsTab>(readInsightsTab())
const sort = ref<InsightsSort>('recorded_at')
const order = ref<'asc' | 'desc'>('desc')
const offset = ref(0)

const filters = computed(() => ({
  preset: preset.value,
  ...presetRange(preset.value, new Date()),
  ...(project.value ? { project: project.value } : {}),
}))
const projectDisabled = computed(() => PROJECT_FILTER_FREE.includes(tab.value))

const summary = ref<InsightsSummary | null>(null)
const projects = ref<InsightsProject[]>([])
const sessions = ref<InsightsSessionsPage | null>(null)
const summaryError = ref<string | null>(null)
const projectsError = ref<string | null>(null)
const sessionsError = ref<string | null>(null)
const drawerRow = ref<InsightsSessionRow | null>(null)
let drawerOpener: HTMLElement | null = null

const controllers = { summary: null as AbortController | null, projects: null as AbortController | null, sessions: null as AbortController | null }

async function load<T>(slot: keyof typeof controllers, error: Ref<string | null>, run: (signal: AbortSignal) => Promise<T>, apply: (v: T) => void) {
  controllers[slot]?.abort()
  const ctrl = new AbortController()
  controllers[slot] = ctrl
  try {
    apply(await run(ctrl.signal))
    error.value = null
  }
  catch (e) {
    if (!ctrl.signal.aborted)
      error.value = e instanceof Error ? e.message : String(e)
  }
}

function loadSummary() {
  return load('summary', summaryError, s => fetchSummary(filters.value, s), (v) => {
    summary.value = v
  })
}
function loadSessions() {
  return load('sessions', sessionsError, s => fetchSessions(filters.value, { sort: sort.value, order: order.value, limit: PAGE_SIZE, offset: offset.value }, s), (v) => {
    sessions.value = v
  })
}

watch(filters, () => {
  offset.value = 0
  void loadSummary()
}, { immediate: true })
watch(() => [filters.value.from, filters.value.to], () => {
  void load('projects', projectsError, s => fetchProjects(filters.value, s), (v) => {
    projects.value = v
  })
}, { immediate: true })
watch([filters, tab, sort, order, offset], () => {
  if (tab.value === 'sessions')
    void loadSessions()
}, { immediate: true })

let timer: ReturnType<typeof setInterval> | null = null
onMounted(() => {
  timer = setInterval(() => void loadSummary(), REFRESH_MS)
})
onUnmounted(() => {
  if (timer)
    clearInterval(timer)
  Object.values(controllers).forEach(c => c?.abort())
})

const tabButtons = ref<HTMLButtonElement[]>([])

function selectTab(t: InsightsTab) {
  tab.value = t
  writeInsightsTab(t)
}

function onTabKeydown(e: KeyboardEvent, index: number) {
  const last = INSIGHTS_TABS.length - 1
  const next = ({
    ArrowRight: index === last ? 0 : index + 1,
    ArrowLeft: index === 0 ? last : index - 1,
    Home: 0,
    End: last,
  } as Record<string, number | undefined>)[e.key]
  if (next === undefined)
    return
  e.preventDefault()
  selectTab(INSIGHTS_TABS[next])
  void nextTick(() => tabButtons.value[next]?.focus())
}

function onSort(key: InsightsSort) {
  if (key === sort.value) {
    order.value = order.value === 'desc' ? 'asc' : 'desc'
  }
  else {
    sort.value = key
    order.value = 'desc'
  }
  offset.value = 0
}

function openDrawer(row: InsightsSessionRow, el: HTMLElement) {
  drawerOpener = el
  drawerRow.value = row
}

function closeDrawer() {
  drawerRow.value = null
  void nextTick(() => drawerOpener?.focus())
}

function openLiveSession(sessionId: string) {
  drawerRow.value = null
  emit('navigate', sessionId)
}
</script>

<template>
  <div class="flex flex-col gap-4" data-testid="insights-view">
    <h2 class="text-xl font-semibold text-fg">
      Insights
    </h2>

    <div class="flex flex-wrap items-center gap-3">
      <div role="group" aria-label="Period" class="flex gap-1">
        <AppButton
          v-for="p in PRESETS"
          :key="p.value"
          size="sm"
          :data-testid="`preset-${p.value}`"
          :aria-pressed="preset === p.value"
          :variant="preset === p.value ? 'primary' : undefined"
          @click="preset = p.value"
        >
          {{ p.label }}
        </AppButton>
      </div>
      <label class="flex items-center gap-2 text-sm text-fg-mute">
        Project
        <select
          v-model="project"
          data-testid="project-select"
          class="bg-raised border border-line rounded-md px-2 py-1 text-fg disabled:opacity-50"
          :disabled="projectDisabled"
          :aria-describedby="projectDisabled ? 'insights-project-hint' : undefined"
        >
          <option value="">All projects</option>
          <option v-for="p in projects" :key="p.projectPath" :value="p.projectPath">{{ p.projectName }}</option>
        </select>
      </label>
      <p v-if="projectDisabled" id="insights-project-hint" class="text-xs text-fg-mute">
        {{ PROJECT_HINT }}
      </p>
    </div>

    <p v-if="summaryError" role="alert" data-testid="summary-error" class="text-sm text-danger-text">
      {{ summaryError }}
    </p>
    <p v-if="projectsError" role="alert" data-testid="projects-error" class="text-sm text-danger-text">
      {{ projectsError }}
    </p>

    <InsightsKpiRow :summary="summary" />

    <div role="tablist" aria-label="Insights sections" class="flex gap-1 border-b border-line">
      <button
        v-for="(t, i) in INSIGHTS_TABS"
        :id="`insights-tab-${t}`"
        :key="t"
        ref="tabButtons"
        type="button"
        role="tab"
        :data-testid="`tab-${t}`"
        :aria-selected="tab === t"
        aria-controls="insights-panel"
        :tabindex="tab === t ? 0 : -1"
        class="px-3 py-2 text-sm border-b-2 -mb-px focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:outline-none"
        :class="tab === t ? 'border-accent text-fg font-semibold' : 'border-transparent text-fg-mute hover:text-fg'"
        @click="selectTab(t)"
        @keydown="onTabKeydown($event, i)"
      >
        {{ TAB_LABELS[t] }}
      </button>
    </div>

    <div id="insights-panel" role="tabpanel" :aria-labelledby="`insights-tab-${tab}`" class="min-h-0">
      <InsightsOverview v-if="tab === 'overview'" :filters="filters" :estimated="(summary?.current.estimated ?? 0) > 0" @open="openDrawer" />
      <InsightsSessionsTable
        v-else-if="tab === 'sessions' && sessions"
        :page="sessions"
        :sort="sort"
        :order="order"
        @sort="onSort"
        @page="offset = $event"
        @open="openDrawer"
      />
      <p v-else-if="tab === 'sessions' && sessionsError" role="alert" data-testid="sessions-error" class="text-sm text-danger-text">
        {{ sessionsError }}
      </p>
      <p v-else-if="tab === 'sessions'" data-testid="sessions-loading" class="text-sm text-fg-mute">
        Loading sessions…
      </p>
      <CostAnalyticsView v-else-if="tab === 'cost'" :from="filters.from" :to="filters.to" :project="filters.project" />
      <EvalView v-else-if="tab === 'quality'" :hours="presetHours(preset)" />
      <WorkflowsView v-else-if="tab === 'workflows'" :from="filters.from" :to="filters.to" @navigate="$emit('navigate', $event)" />
    </div>

    <InsightsSessionDrawer :row="drawerRow" :can-open="drawerRow !== null && props.liveSessionIds.includes(drawerRow.sessionId)" @close="closeDrawer" @navigate="openLiveSession" />
  </div>
</template>
