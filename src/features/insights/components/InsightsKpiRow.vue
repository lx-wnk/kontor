<script setup lang="ts">
import type { InsightsSummary, InsightsTotals } from '../insightsApi'
import { computed } from 'vue'
import AppCard from '@/components/ui/AppCard.vue'
import { formatCost, formatTokens, formatUptime } from '@/utils/format'
import { estimateMark } from '../estimateMark'

const props = defineProps<{ summary: InsightsSummary | null }>()

type Metric = 'costUsd' | 'agentMs' | 'tokens' | 'sessions'

const legacyCount = computed(() => (props.summary?.current.legacy ?? 0) + (props.summary?.previous?.legacy ?? 0))
const SKEWED_BY_LEGACY: Metric[] = ['tokens', 'agentMs']

function delta(metric: Metric): string {
  if (legacyCount.value > 0 && SKEWED_BY_LEGACY.includes(metric))
    return ''
  const prev: InsightsTotals | null | undefined = props.summary?.previous
  if (!prev || !prev[metric])
    return ''
  const pct = Math.round(((props.summary!.current[metric] - prev[metric]) / prev[metric]) * 100)
  return `${pct >= 0 ? '▲' : '▼'}${Math.abs(pct)}%`
}

const mark = computed(() => estimateMark((props.summary?.current.estimated ?? 0) > 0 ? 'estimate' : ''))

const tiles = computed(() => {
  const c = props.summary?.current
  return [
    { key: 'cost', label: 'Cost', metric: 'costUsd' as const, marked: true, value: c ? formatCost(c.costUsd) : '—' },
    { key: 'agent', label: 'Agent time', metric: 'agentMs' as const, marked: true, value: c ? formatUptime(Math.floor(c.agentMs / 1000)) : '—' },
    { key: 'tokens', label: 'Tokens', metric: 'tokens' as const, marked: false, value: c ? formatTokens(c.tokens) : '—' },
    { key: 'sessions', label: 'Sessions', metric: 'sessions' as const, marked: false, value: c ? String(c.sessions) : '—' },
  ]
})
</script>

<template>
  <div>
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <AppCard v-for="t in tiles" :key="t.key" :data-testid="`kpi-${t.key}`" class="p-3">
        <div class="text-xs text-fg-mute">
          {{ t.label }}
        </div>
        <div class="text-xl font-semibold text-fg tabular-nums" :title="t.marked ? mark.title : undefined">
          {{ t.marked ? mark.prefix : '' }}<span v-if="t.marked && mark.srLabel" class="sr-only">{{ mark.srLabel }}</span>{{ t.value }}
        </div>
        <div v-if="delta(t.metric)" class="text-xs text-fg-mute tabular-nums">
          {{ delta(t.metric) }} vs previous period
        </div>
      </AppCard>
    </div>
    <p v-if="summary && summary.current.estimated > 0" class="mt-2 text-xs text-fg-mute">
      {{ summary.current.estimated }} of {{ summary.current.sessions }} sessions estimated
    </p>
    <p v-if="legacyCount > 0" class="mt-1 text-xs text-fg-mute">
      {{ legacyCount }} {{ legacyCount === 1 ? 'session' : 'sessions' }} recorded before per-session tracking
    </p>
  </div>
</template>
