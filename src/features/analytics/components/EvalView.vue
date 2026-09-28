<script setup lang="ts">
import type { DriftAlert, EvalMetricSnapshot, MetricKey } from '@/types'
import { max } from 'd3-array'
import { axisBottom, axisLeft } from 'd3-axis'
import { scaleLinear, scalePoint } from 'd3-scale'
import { select } from 'd3-selection'
import { curveMonotoneX, line as d3line } from 'd3-shape'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { toast } from '@/composables/useToast'
import { useEvalMetrics } from '@/features/analytics/composables/useEvalMetrics'
import { EVAL_RANGE_OPTIONS, formatWindowLabel, METRIC_KEYS, metricLabel } from '@/utils/evalMetrics'

const { snapshots, openAlerts, isLoading, error, hours, lastDataAt, acknowledge, runScan, setHours, start } = useEvalMetrics()

// Toast is a transient nudge; `error` itself drives the persistent banner
// below so a broken backend doesn't look like legitimate emptiness once the
// toast fades.
watch(error, (msg) => {
  if (msg)
    toast.error(msg)
})

const windowLabel = computed(() => formatWindowLabel(hours.value))

// Shared across all nine cards — the window is one setting, not per-metric.
// Never implies "Run scan now" can back-fill a period nothing ran in: it
// plots snapshot history by recordedAt and cannot reconstruct the past.
const emptyReason = computed(() => {
  const base = `No stage runs in the last ${windowLabel.value}.`
  return lastDataAt.value
    ? `${base} Last recorded ${new Date(lastDataAt.value).toLocaleDateString()}.`
    : base
})

const isScanning = ref(false)

async function handleRunScan() {
  if (isScanning.value)
    return
  isScanning.value = true
  try {
    await runScan()
  }
  catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Scan failed')
  }
  finally {
    isScanning.value = false
  }
}

async function handleAcknowledge(id: string) {
  try {
    await acknowledge(id)
  }
  catch {
    // non-fatal — alert stays visible
  }
}

// Group alerts by dimension string for display
const groupedAlerts = computed(() => {
  const groups = new Map<string, DriftAlert[]>()
  for (const alert of openAlerts.value) {
    const key = `${alert.spawnerId} / ${alert.model} / ${alert.stage}`
    if (!groups.has(key))
      groups.set(key, [])
    groups.get(key)!.push(alert)
  }
  return groups
})

// One SVG ref per metric key
const svgRefs = ref<Record<string, SVGSVGElement | null>>(
  Object.fromEntries(METRIC_KEYS.map(k => [k, null])),
)

function setSvgRef(key: MetricKey, el: Element | null) {
  svgRefs.value[key] = el as SVGSVGElement | null
}

function renderChart(key: MetricKey, data: EvalMetricSnapshot[]) {
  const svg = svgRefs.value[key]
  if (!svg)
    return

  const sel = select(svg)
  sel.selectAll('*').remove()

  if (data.length === 0)
    return

  const sorted = [...data].sort((a, b) => a.recordedAt.localeCompare(b.recordedAt))

  const margin = { top: 12, right: 16, bottom: 36, left: 56 }
  const W = (svg.clientWidth || 400) - margin.left - margin.right
  const H = 160 - margin.top - margin.bottom

  sel.attr('height', H + margin.top + margin.bottom)
  const g = sel.append('g').attr('transform', `translate(${margin.left},${margin.top})`)

  const x = scalePoint<string>()
    .domain(sorted.map(d => d.recordedAt))
    .range([0, W])
    .padding(0.4)

  const yMax = max(sorted, d => d.value) ?? 0
  const y = scaleLinear().domain([0, yMax * 1.1 || 1]).nice().range([H, 0])

  const line = d3line<EvalMetricSnapshot>()
    .x(d => x(d.recordedAt) ?? 0)
    .y(d => y(d.value))
    .curve(curveMonotoneX)

  g.append('path')
    .datum(sorted)
    .attr('fill', 'none')
    .attr('stroke', '#10b981')
    .attr('stroke-width', 2)
    .attr('d', line)

  g.append('g').selectAll('circle').data(sorted).join('circle').attr('cx', d => x(d.recordedAt) ?? 0).attr('cy', d => y(d.value)).attr('r', 3).attr('fill', '#10b981').append('title').text(d => `${new Date(d.recordedAt).toLocaleString()}: ${d.value}`)

  const tickEvery = Math.max(1, Math.ceil(sorted.length / 6))
  g.append('g')
    .attr('transform', `translate(0,${H})`)
    .call(
      axisBottom(x)
        .tickValues(sorted.filter((_, i) => i % tickEvery === 0).map(d => d.recordedAt))
        .tickFormat(d => new Date(String(d)).toLocaleDateString()),
    )
    .selectAll('text')
    .attr('font-size', '10px')
    .attr('transform', 'rotate(-24)')
    .attr('text-anchor', 'end')

  g.append('g')
    .call(axisLeft(y).ticks(4))
    .selectAll('text')
    .attr('font-size', '10px')
}

function renderAllCharts() {
  for (const key of METRIC_KEYS)
    renderChart(key, snapshots.value[key] ?? [])
}

onMounted(() => {
  start()
})

// Two conditions gate the <svg> these charts draw into: the section is hidden
// while isLoading, and the card shows its empty reason until snapshots arrive.
// Watching snapshots alone fired while the element still did not exist, and
// nothing re-triggered the draw afterwards — nine blank cards until the 60s
// poll happened to replace snapshots again. nextTick rather than a microtask,
// because only nextTick is ordered after Vue has patched the DOM.
watch([snapshots, isLoading], () => {
  void nextTick(renderAllCharts)
}, { immediate: true })

onUnmounted(() => {
  // stop() is called by the composable's onUnmounted
})
</script>

<template>
  <div class="eval-view p-6 flex flex-col gap-6">
    <header class="flex items-baseline justify-between flex-wrap gap-2">
      <h2 class="text-lg font-semibold text-fg">
        Eval / Drift Detection
      </h2>
      <div class="flex items-center gap-3">
        <AppSelect
          id="eval-range-select"
          :model-value="hours"
          :options="EVAL_RANGE_OPTIONS"
          aria-label="Metric trend range"
          size="compact"
          @update:model-value="setHours"
        />
        <button
          type="button"
          :disabled="isScanning"
          class="text-xs px-3 py-1.5 rounded bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50 transition-colors"
          @click="handleRunScan"
        >
          {{ isScanning ? 'Scanning…' : 'Run scan now' }}
        </button>
      </div>
    </header>

    <div v-if="error" role="alert" class="rounded-md border border-danger-line bg-danger-soft text-danger-text text-sm px-3 py-2">
      Failed to load eval data: {{ error }}
    </div>

    <p v-if="isLoading" class="text-sm text-fg-mute">
      Loading eval metrics…
    </p>

    <!-- Metric trend charts -->
    <section v-if="!isLoading" class="flex flex-col gap-4">
      <h3 class="text-sm font-semibold text-fg-soft">
        Metric Trends (last {{ windowLabel }})
      </h3>
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        <div
          v-for="key in METRIC_KEYS"
          :key="key"
          class="bg-card border border-line rounded-md p-4"
        >
          <h4 class="text-xs font-semibold text-fg-soft mb-2">
            {{ metricLabel(key) }}
          </h4>
          <div v-if="!error && (snapshots[key] ?? []).length === 0" class="text-xs text-fg-mute py-4 text-center">
            {{ emptyReason }}
          </div>
          <svg
            v-else-if="(snapshots[key] ?? []).length > 0"
            :ref="el => setSvgRef(key as MetricKey, el as Element | null)"
            class="w-full text-fg"
            style="min-height: 140px;"
          />
        </div>
      </div>
    </section>

    <!-- Open drift alerts -->
    <section class="bg-card border border-line rounded-md p-4">
      <h3 class="text-sm font-semibold mb-3 text-fg-soft">
        Open drift alerts
        <span v-if="openAlerts.length > 0" class="ml-1.5 text-xs bg-red-600 text-white rounded-full px-1.5 py-0.5">
          {{ openAlerts.length }}
        </span>
      </h3>

      <p v-if="openAlerts.length === 0" class="text-sm text-fg-mute">
        No open drift alerts.
      </p>

      <div v-else class="flex flex-col gap-4">
        <div
          v-for="[dimension, alerts] in groupedAlerts"
          :key="dimension"
        >
          <div class="text-xs font-mono text-fg-mute mb-1.5">
            {{ dimension }}
          </div>
          <ul class="flex flex-col gap-1.5">
            <li
              v-for="alert in alerts"
              :key="alert.id"
              class="flex items-center justify-between gap-3 px-3 py-2 bg-raised rounded-md text-xs flex-wrap"
            >
              <span class="font-semibold text-fg">{{ metricLabel(alert.metricKey) }}</span>
              <span class="font-mono text-red-600 dark:text-red-400">
                {{ alert.direction === 'up' ? '▲' : '▼' }}
                {{ alert.baselineValue.toFixed(3) }} → {{ alert.recentValue.toFixed(3) }}
                (Δ {{ alert.delta >= 0 ? '+' : '' }}{{ alert.delta.toFixed(3) }})
              </span>
              <span class="text-fg-mute">n={{ alert.sampleCount }}</span>
              <button
                type="button"
                class="ml-auto px-2.5 py-1 rounded border border-line bg-raised text-fg-mute hover:text-fg hover:bg-raised/70 transition-colors shrink-0"
                @click="handleAcknowledge(alert.id)"
              >
                Acknowledge
              </button>
            </li>
          </ul>
        </div>
      </div>
    </section>
  </div>
</template>
