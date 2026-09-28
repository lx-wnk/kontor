<script setup lang="ts">
import { max } from 'd3-array'
import { axisBottom, axisLeft } from 'd3-axis'
import { scaleBand, scaleLinear, scaleOrdinal, scalePoint } from 'd3-scale'
import { select } from 'd3-selection'
import { area, curveMonotoneX, line, stack } from 'd3-shape'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import AppCard from '@/components/ui/AppCard.vue'
import { useHistoryImport } from '@/composables/useHistoryImport'
import { useTheme } from '@/composables/useTheme'
import { toast } from '@/composables/useToast'
import { useCostAnalytics } from '@/features/analytics/composables/useCostAnalytics'
import { chartColors, chartPalette } from '@/utils/chartColors'
import { formatCost, formatTokens } from '@/utils/format'

const { summary, isLoading, error, from, to, setRange, start, refresh } = useCostAnalytics()

// Surface async load failures as toasts; the view keeps its empty/loading state.
watch(error, (msg) => {
  if (msg)
    toast.error(msg)
})

const { theme } = useTheme()

// --- Historical data rescan ---
const { isImporting, importStatus, start: startRescan } = useHistoryImport({
  onDone: () => void refresh(),
})

const stackedRef = ref<SVGSVGElement | null>(null)
const trendRef = ref<SVGSVGElement | null>(null)

const hasData = computed(() => summary.value.byDay.length > 0 || summary.value.byWeek.length > 0)
const updatedAtLabel = computed(() => {
  if (!summary.value.updatedAt)
    return ''
  return new Date(summary.value.updatedAt).toLocaleString()
})

const totalTokens = computed(() => {
  const s = summary.value
  return (s.totalInputTokens ?? 0) + (s.totalOutputTokens ?? 0)
})

const projectMaxCost = computed(() =>
  Math.max(0, ...summary.value.byProject.map(p => p.costUsd)),
)

// --- Time-range presets ---
type Preset = '7d' | '30d' | '90d' | 'all'

const activePreset = ref<Preset | null>('30d')

function toDateStr(d: Date): string {
  return d.toISOString().slice(0, 10)
}

function todayStr(): string {
  return toDateStr(new Date())
}

function daysAgoStr(n: number): string {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return toDateStr(d)
}

function applyPreset(preset: Preset) {
  activePreset.value = preset
  const today = todayStr()
  if (preset === '7d') {
    setRange(daysAgoStr(7), today)
  }
  else if (preset === '30d') {
    setRange(daysAgoStr(30), today)
  }
  else if (preset === '90d') {
    setRange(daysAgoStr(90), today)
  }
  else {
    // 'all'
    setRange('2000-01-01', today)
  }
}

// Custom date range fields (separate from reactive from/to in composable)
const customFrom = ref('')
const customTo = ref('')

function applyCustomRange() {
  if (!customFrom.value || !customTo.value)
    return
  activePreset.value = null
  setRange(customFrom.value, customTo.value)
}

/**
 * Reshape the flat byDay rows into one row per day with one column per model
 * — the input shape stack() expects.
 */
function buildDayMatrix(): { rows: Record<string, number | string>[], models: string[] } {
  const byDay = summary.value.byDay
  const dayMap = new Map<string, Record<string, number | string>>()
  const modelSet = new Set<string>()
  for (const p of byDay) {
    modelSet.add(p.model)
    if (!dayMap.has(p.day))
      dayMap.set(p.day, { day: p.day })
    const row = dayMap.get(p.day)!
    row[p.model] = ((row[p.model] as number | undefined) ?? 0) + p.costUsd
  }
  const rows = Array.from(dayMap.values()).sort((a, b) => String(a.day).localeCompare(String(b.day)))
  const models = Array.from(modelSet).sort()
  // Ensure every row has every model key (zero-fill) so stack() yields
  // contiguous segments with no NaN holes.
  for (const row of rows) {
    for (const m of models) {
      if (row[m] === undefined)
        row[m] = 0
    }
  }
  return { rows, models }
}

function renderStackedBar() {
  const svg = stackedRef.value
  if (!svg)
    return

  const sel = select(svg)
  sel.selectAll('*').remove()

  const { rows, models } = buildDayMatrix()
  if (rows.length === 0)
    return

  const margin = { top: 12, right: 16, bottom: 50, left: 56 }
  const W = (svg.clientWidth || 700) - margin.left - margin.right
  const H = 260 - margin.top - margin.bottom

  sel.attr('height', H + margin.top + margin.bottom)
  const g = sel.append('g').attr('transform', `translate(${margin.left},${margin.top})`)

  const x = scaleBand<string>()
    .domain(rows.map(r => String(r.day)))
    .range([0, W])
    .padding(0.15)

  const stackGen = stack<Record<string, number | string>, string>()
    .keys(models)
    .value((d, key) => (d[key] as number) ?? 0)
  const series = stackGen(rows)

  const yMax = max(series, layer => max(layer, d => d[1])) ?? 0
  const y = scaleLinear()
    .domain([0, yMax * 1.05 || 1])
    .nice()
    .range([H, 0])

  const palette = chartPalette()
  const color = scaleOrdinal<string>()
    .domain(models)
    .range(palette)

  // bars
  g.append('g')
    .selectAll('g')
    .data(series)
    .join('g')
    .attr('fill', d => color(d.key))
    .selectAll('rect')
    .data(d => d)
    .join('rect')
    .attr('x', d => x(String(d.data.day)) ?? 0)
    .attr('y', d => y(d[1]))
    .attr('height', d => Math.max(0, y(d[0]) - y(d[1])))
    .attr('width', x.bandwidth())
    .append('title')
    .text((d) => {
      const key = (d as unknown as { key?: string }).key
      const val = d[1] - d[0]
      return `${d.data.day}${key ? ` — ${key}` : ''}: ${formatCost(val)}`
    })

  // axes
  const xTickEvery = Math.max(1, Math.ceil(rows.length / 10))
  g.append('g')
    .attr('transform', `translate(0,${H})`)
    .call(axisBottom(x).tickValues(rows.map((r, i) => i % xTickEvery === 0 ? String(r.day) : '').filter(Boolean)).tickFormat(d => String(d).slice(5)))
    .selectAll('text')
    .attr('font-size', '10px')
    .attr('transform', 'rotate(-32)')
    .attr('text-anchor', 'end')

  g.append('g')
    .call(axisLeft(y).ticks(5).tickFormat(d => `$${Number(d).toFixed(2)}`))
    .selectAll('text')
    .attr('font-size', '10px')

  // legend
  const legend = sel.append('g')
    .attr('transform', `translate(${margin.left},${H + margin.top + 36})`)
  let xOffset = 0
  for (const m of models) {
    const group = legend.append('g').attr('transform', `translate(${xOffset},0)`)
    group.append('rect').attr('width', 10).attr('height', 10).attr('fill', color(m)).attr('y', -8)
    group.append('text').attr('x', 14).attr('y', 1).attr('font-size', '10px').attr('fill', 'currentColor').text(m)
    xOffset += 14 + Math.max(60, m.length * 6.2)
  }
}

function renderWeeklyTrend() {
  const svg = trendRef.value
  if (!svg)
    return

  const sel = select(svg)
  sel.selectAll('*').remove()

  const data = summary.value.byWeek
  if (data.length === 0)
    return

  const margin = { top: 12, right: 16, bottom: 36, left: 56 }
  const W = (svg.clientWidth || 700) - margin.left - margin.right
  const H = 220 - margin.top - margin.bottom

  sel.attr('height', H + margin.top + margin.bottom)
  const g = sel.append('g').attr('transform', `translate(${margin.left},${margin.top})`)

  const x = scalePoint<string>()
    .domain(data.map(d => d.week))
    .range([0, W])
    .padding(0.4)

  const yMax = max(data, d => d.costUsd) ?? 0
  const y = scaleLinear().domain([0, yMax * 1.1 || 1]).nice().range([H, 0])

  const lineGen = line<WeekPoint>()
    .x(d => x(d.week) ?? 0)
    .y(d => y(d.costUsd))
    .curve(curveMonotoneX)

  const areaGen = area<WeekPoint>()
    .x(d => x(d.week) ?? 0)
    .y0(H)
    .y1(d => y(d.costUsd))
    .curve(curveMonotoneX)

  g.append('path')
    .datum(data)
    .attr('fill', chartColors().success)
    .attr('fill-opacity', 0.12)
    .attr('stroke', 'none')
    .attr('d', areaGen)

  g.append('path')
    .datum(data)
    .attr('fill', 'none')
    .attr('stroke', chartColors().success)
    .attr('stroke-width', 2)
    .attr('d', lineGen)

  g.append('g').selectAll('circle').data(data).join('circle').attr('cx', d => x(d.week) ?? 0).attr('cy', d => y(d.costUsd)).attr('r', 3).attr('fill', chartColors().success).append('title').text(d => `${d.week}: ${formatCost(d.costUsd)}`)

  const tickEvery = Math.max(1, Math.ceil(data.length / 8))
  g.append('g')
    .attr('transform', `translate(0,${H})`)
    .call(axisBottom(x).tickValues(data.filter((_, i) => i % tickEvery === 0).map(d => d.week)))
    .selectAll('text')
    .attr('font-size', '10px')
    .attr('transform', 'rotate(-24)')
    .attr('text-anchor', 'end')

  g.append('g')
    .call(axisLeft(y).ticks(5).tickFormat(d => `$${Number(d).toFixed(2)}`))
    .selectAll('text')
    .attr('font-size', '10px')
}

// Local alias to satisfy TS in the line generic above.
interface WeekPoint { week: string, costUsd: number }

onMounted(() => {
  start()
})

watch([summary, theme], () => {
  // nextTick is what the comment here always meant: a microtask can run before
  // Vue patches the DOM. These two charts survive that only because their
  // cards use v-show, so the refs exist from mount — the moment one becomes a
  // v-if they would go blank the way EvalView's did.
  void nextTick(() => {
    renderStackedBar()
    renderWeeklyTrend()
  })
}, { immediate: true })
</script>

<template>
  <div class="cost-analytics-view p-6 flex flex-col gap-6">
    <header class="flex items-baseline justify-between flex-wrap gap-2">
      <h2 class="text-lg font-semibold text-fg">
        Cost Analytics
      </h2>
      <div class="text-xs text-fg-mute flex items-center gap-3">
        <span v-if="summary.totalUsd > 0">
          Total: <strong class="text-fg">{{ formatCost(summary.totalUsd) }}</strong>
          <span v-if="totalTokens > 0" class="ml-1">({{ formatTokens(totalTokens) }} tokens)</span>
        </span>
        <span v-if="updatedAtLabel">Updated {{ updatedAtLabel }}</span>
        <button
          v-if="hasData"
          type="button"
          :disabled="isImporting"
          class="text-xs px-2.5 py-1 rounded bg-raised border border-line text-fg-mute hover:text-fg hover:bg-raised/70 disabled:opacity-50 transition-colors"
          @click="startRescan"
        >
          {{ isImporting ? 'Scanning…' : 'Rescan now' }}
        </button>
      </div>
    </header>

    <!-- Time-range controls -->
    <div class="flex flex-wrap items-center gap-2 text-xs">
      <span class="text-fg-mute mr-1">Range:</span>
      <button
        v-for="preset in (['7d', '30d', '90d', 'all'] as const)"
        :key="preset"
        type="button"
        class="px-2.5 py-1 rounded border transition-colors" :class="[
          activePreset === preset
            ? 'bg-accent border-accent text-accent-contrast'
            : 'bg-raised border-line text-fg-mute hover:text-fg hover:bg-raised/70',
        ]"
        @click="applyPreset(preset)"
      >
        {{ preset === 'all' ? 'All' : preset }}
      </button>
      <span class="text-fg-mute mx-1">or</span>
      <input
        v-model="customFrom"
        type="date"
        class="px-2 py-1 rounded border border-line bg-raised text-fg text-xs focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent"
        aria-label="From date"
      >
      <span class="text-fg-mute">–</span>
      <input
        v-model="customTo"
        type="date"
        class="px-2 py-1 rounded border border-line bg-raised text-fg text-xs focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent"
        aria-label="To date"
      >
      <button
        type="button"
        :disabled="!customFrom || !customTo"
        class="px-2.5 py-1 rounded border border-line bg-raised text-fg-mute hover:text-fg hover:bg-raised/70 disabled:opacity-40 transition-colors"
        @click="applyCustomRange"
      >
        Apply
      </button>
      <span v-if="from && to" class="text-fg-mute ml-1">{{ from }} – {{ to }}</span>
    </div>

    <p v-if="isLoading && !hasData" class="text-sm text-fg-mute">
      Loading cost summary…
    </p>
    <div v-else-if="!hasData" class="flex flex-col gap-2">
      <p class="text-sm text-fg-mute">
        No cost data yet. Costs are imported automatically from your Claude sessions — the first scan may take a moment. You can also trigger a scan now.
      </p>
      <div class="flex items-center gap-3">
        <button
          type="button"
          :disabled="isImporting"
          class="text-sm px-3 py-1.5 rounded bg-accent text-accent-contrast hover:brightness-110 disabled:opacity-50 transition-colors"
          @click="startRescan"
        >
          {{ isImporting ? 'Scanning…' : 'Rescan now' }}
        </button>
        <span v-if="importStatus" class="text-xs text-fg-mute">{{ importStatus }}</span>
      </div>
    </div>
    <p v-if="importStatus && hasData" class="text-xs text-fg-mute">
      {{ importStatus }}
    </p>

    <AppCard v-if="summary.byModel.length > 0" class="p-4">
      <h3 class="text-sm font-semibold mb-3 text-fg-soft">
        Spend by Model
      </h3>
      <ul class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2 text-xs">
        <li
          v-for="m in summary.byModel"
          :key="m.model"
          class="flex items-center justify-between gap-2 px-3 py-2 bg-raised rounded-md"
        >
          <span class="font-mono truncate text-fg" :title="m.model">{{ m.model }}</span>
          <span class="font-mono text-success-text whitespace-nowrap">
            {{ formatCost(m.costUsd) }}
          </span>
          <span v-if="(m.inputTokens ?? 0) + (m.outputTokens ?? 0) > 0" class="text-fg-mute whitespace-nowrap">
            {{ formatTokens((m.inputTokens ?? 0) + (m.outputTokens ?? 0)) }}
          </span>
          <span class="text-fg-mute whitespace-nowrap">{{ m.sessions }} sess.</span>
        </li>
      </ul>
    </AppCard>

    <AppCard v-if="summary.byProject && summary.byProject.length > 0" class="p-4">
      <h3 class="text-sm font-semibold mb-3 text-fg-soft">
        Spend by Project
      </h3>
      <ul class="flex flex-col gap-2.5 text-xs">
        <li
          v-for="p in summary.byProject"
          :key="p.projectPath"
          class="flex items-center gap-2.5"
        >
          <span class="w-32 shrink-0 font-mono text-fg-soft truncate" :title="p.projectPath">{{ p.projectName }}</span>
          <div class="flex-1 h-3.5 bg-raised rounded-full overflow-hidden">
            <div
              class="h-full rounded-full bg-accent"
              :style="{ width: projectMaxCost > 0 ? `${(p.costUsd / projectMaxCost) * 100}%` : '0%' }"
            />
          </div>
          <span class="w-14 text-right font-mono text-success-text whitespace-nowrap shrink-0">{{ formatCost(p.costUsd) }}</span>
        </li>
      </ul>
    </AppCard>

    <AppCard v-show="summary.byDay.length > 0" class="p-4">
      <h3 class="text-sm font-semibold mb-2 text-fg-soft">
        Cost per Day (stacked by model)
      </h3>
      <svg ref="stackedRef" class="w-full text-fg" style="min-height: 220px;" />
    </AppCard>

    <AppCard v-show="summary.byWeek.length > 0" class="p-4">
      <h3 class="text-sm font-semibold mb-2 text-fg-soft">
        Weekly Trend
      </h3>
      <svg ref="trendRef" class="w-full text-fg" style="min-height: 200px;" />
    </AppCard>
  </div>
</template>
