<script setup lang="ts">
import type { InsightsFilters, InsightsSessionRow } from '../insightsApi'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import AppCard from '@/components/ui/AppCard.vue'
import ChartDataTable from '@/components/ui/ChartDataTable.vue'
import { useCostAnalytics, useEvalMetrics } from '@/features/analytics'
import { metricLabel } from '@/utils/evalMetrics'
import { formatCost } from '@/utils/format'
import { estimateMark } from '../estimateMark'
import { fetchSessions } from '../insightsApi'

const props = defineProps<{ filters: InsightsFilters, estimated: boolean }>()
const emit = defineEmits<{ open: [row: InsightsSessionRow, el: HTMLElement] }>()

const ALL_TIME_FROM = '2000-01-01'

const { summary, error: costError, setRange } = useCostAnalytics()
const { openAlerts, error: evalError, startAlerts } = useEvalMetrics()

const top = ref<InsightsSessionRow[]>([])
const topError = ref<string | null>(null)
let controller: AbortController | null = null

async function loadTop() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  try {
    const page = await fetchSessions(props.filters, { sort: 'cost', order: 'desc', limit: 5, offset: 0 }, ctrl.signal)
    top.value = page.rows
    topError.value = null
  }
  catch (e) {
    if (!ctrl.signal.aborted)
      topError.value = e instanceof Error ? e.message : String(e)
  }
}

watch(() => props.filters, (f) => {
  setRange(f.from ?? ALL_TIME_FROM, f.to, f.project ?? '')
  void loadTop()
}, { immediate: true })

onMounted(startAlerts)
onUnmounted(() => controller?.abort())

const perDay = computed(() => {
  const sums = new Map<string, number>()
  for (const p of summary.value.byDay)
    sums.set(p.day, (sums.get(p.day) ?? 0) + p.costUsd)
  return [...sums].sort(([a], [b]) => a.localeCompare(b)).map(([day, costUsd]) => ({ day, costUsd }))
})
const maxDay = computed(() => Math.max(0, ...perDay.value.map(d => d.costUsd)))
const tableRows = computed(() => perDay.value.map(d => ({ day: d.day, cost: formatCost(d.costUsd) })))
</script>

<template>
  <div class="flex flex-col gap-4" data-testid="insights-overview">
    <AppCard>
      <h2 class="text-sm font-semibold mb-3 text-fg-soft">
        Open drift alerts
        <span v-if="openAlerts.length" class="ml-1.5 text-xs bg-red-600 text-white rounded-full px-1.5 py-0.5">{{ openAlerts.length }}</span>
      </h2>
      <p v-if="evalError" role="alert" class="text-sm text-danger-text">
        {{ evalError }}
      </p>
      <p v-else-if="!openAlerts.length" class="text-sm text-fg-mute">
        No open drift alerts.
      </p>
      <ul v-else class="flex flex-col gap-1.5 text-xs">
        <li v-for="a in openAlerts" :key="a.id" class="flex gap-3 px-3 py-2 bg-raised rounded-md">
          <span class="font-semibold text-fg">{{ metricLabel(a.metricKey) }}</span>
          <span class="font-mono text-fg-mute">{{ a.spawnerId }} / {{ a.model }} / {{ a.stage }}</span>
          <span class="font-mono text-red-600 dark:text-red-400">{{ a.direction === 'up' ? '▲' : '▼' }} {{ a.baselineValue.toFixed(3) }} → {{ a.recentValue.toFixed(3) }}</span>
        </li>
      </ul>
    </AppCard>

    <AppCard>
      <h2 class="text-sm font-semibold mb-3 text-fg-soft">
        Top 5 sessions by cost
      </h2>
      <p v-if="topError" role="alert" class="text-sm text-danger-text">
        {{ topError }}
      </p>
      <p v-else-if="!top.length" class="text-sm text-fg-mute">
        No sessions in this period.
      </p>
      <ol v-else class="flex flex-col gap-1">
        <li v-for="row in top" :key="row.sessionId">
          <button
            type="button"
            data-testid="top-session"
            class="w-full flex justify-between gap-3 px-3 py-2 rounded-md text-left text-sm hover:bg-raised focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:outline-none"
            @click="emit('open', row, $event.currentTarget as HTMLElement)"
          >
            <span class="truncate">{{ row.title || row.sessionId }}</span>
            <span class="font-mono shrink-0" data-testid="top-session-cost" :title="estimateMark(row.costSource, row.costUnknownModel).title">{{ estimateMark(row.costSource, row.costUnknownModel).prefix }}{{ formatCost(row.costUsd) }}</span>
          </button>
        </li>
      </ol>
    </AppCard>

    <AppCard>
      <h2 class="text-sm font-semibold mb-3 text-fg-soft">
        <span :title="estimated ? estimateMark('estimate').title : undefined">{{ estimated ? estimateMark('estimate').prefix : '' }}Cost per day</span>
      </h2>
      <p v-if="costError" role="alert" class="text-sm text-danger-text">
        {{ costError }}
      </p>
      <p v-else-if="!perDay.length" class="text-sm text-fg-mute">
        No cost data in this period.
      </p>
      <template v-else>
        <div class="flex items-end gap-0.5 h-32" aria-hidden="true">
          <div
            v-for="d in perDay"
            :key="d.day"
            data-testid="day-bar"
            class="flex-1 bg-accent rounded-t min-h-px"
            :style="{ height: `${maxDay ? (d.costUsd / maxDay) * 100 : 0}%` }"
            :title="`${d.day}: ${formatCost(d.costUsd)}`"
          />
        </div>
        <ChartDataTable
          caption="Cost per day"
          :columns="[{ key: 'day', label: 'Day' }, { key: 'cost', label: 'Cost' }]"
          :rows="tableRows"
        />
      </template>
    </AppCard>
  </div>
</template>
