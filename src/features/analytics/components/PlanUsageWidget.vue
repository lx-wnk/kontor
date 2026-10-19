<script setup lang="ts">
import type { PanelState } from '@/features/cockpit'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { CockpitPanel } from '@/features/cockpit'
import { formatRelativeActivity, formatUptime, secondsSince } from '@/utils/format'

interface UsageWindow { usedPct: number, resetsAt: string }
interface Sample { fiveHour?: UsageWindow | null, sevenDay?: UsageWindow | null, sampledAt: string }
interface Summary { accounts: Record<string, Sample> | null, staleMinutes: number }

const WINDOWS = [{ key: 'fiveHour', label: '5h' }, { key: 'sevenDay', label: 'Week' }] as const
const EMPTY_MESSAGE = 'No plan usage data. Wire the statusline hook to see rate limits.'
const WARN_PCT = 90
const TICK_MS = 60_000

const summary = ref<Summary | null>(null)
const failed = ref(false)
const now = ref(Date.now())

const rows = computed(() => Object.entries(summary.value?.accounts ?? {}).map(([dir, sample]) => {
  const age = secondsSince(sample.sampledAt, now.value)
  return {
    dir,
    name: dir.split('/').filter(Boolean).pop() ?? dir,
    asOf: formatRelativeActivity(age),
    stale: age !== null && age > (summary.value?.staleMinutes ?? Infinity) * 60,
    windows: WINDOWS.map(({ key, label }) => {
      const w = sample[key]
      if (!w)
        return { key, label, present: false as const }
      const remaining = Math.max(0, Math.floor((Date.parse(w.resetsAt) - now.value) / 1000))
      return {
        key,
        label,
        present: true as const,
        pct: Math.round(w.usedPct),
        width: Math.min(100, Math.max(0, w.usedPct)),
        warn: w.usedPct >= WARN_PCT,
        countdown: formatUptime(remaining),
      }
    }),
  }
}))

const state = computed<PanelState>(() => {
  if (summary.value === null)
    return failed.value ? 'failed' : 'loading'
  return rows.value.length ? 'ready' : 'empty'
})

async function fetchSummary() {
  if (summary.value)
    return
  try {
    const res = await fetch('/api/plan-usage')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    summary.value = await res.json()
  }
  catch {
    failed.value = true
  }
}

let source: EventSource | null = null
let timer: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  source = new EventSource('/api/plan-usage/stream')
  source.onmessage = (e: MessageEvent<string>) => {
    const msg = JSON.parse(e.data)
    if (msg.type === 'plan_usage_updated') {
      summary.value = msg.payload
      failed.value = false
    }
  }
  source.onerror = fetchSummary
  timer = setInterval(() => (now.value = Date.now()), TICK_MS)
})

onBeforeUnmount(() => {
  source?.close()
  clearInterval(timer)
})
</script>

<template>
  <CockpitPanel
    id="plan-usage"
    title="Plan usage"
    icon="%"
    :state="state"
    :message="state === 'empty' ? EMPTY_MESSAGE : undefined"
  >
    <ul class="flex flex-col gap-3">
      <li
        v-for="row in rows"
        :key="row.dir"
        data-testid="plan-usage-row"
        class="flex flex-col gap-1 text-[12px]"
        :class="{ 'stale opacity-60': row.stale }"
      >
        <div class="flex items-baseline justify-between gap-2">
          <span class="truncate font-medium text-fg" :title="row.dir">{{ row.name }}</span>
          <span class="shrink-0 text-fg-mute" data-testid="plan-usage-asof">as of {{ row.asOf }}</span>
        </div>
        <div
          v-for="w in row.windows"
          :key="w.key"
          class="flex items-center gap-2"
          :data-testid="`plan-usage-${w.key}`"
        >
          <span class="w-8 shrink-0 text-fg-mute">{{ w.label }}</span>
          <template v-if="w.present">
            <div
              class="h-1.5 min-w-8 flex-1 overflow-hidden rounded-full bg-raised"
              role="progressbar"
              :aria-label="`${row.name} ${w.label} usage`"
              aria-valuemin="0"
              aria-valuemax="100"
              :aria-valuenow="w.pct"
            >
              <div class="h-full rounded-full" :class="w.warn ? 'bg-warning' : 'bg-accent'" :style="{ width: `${w.width}%` }" />
            </div>
            <span class="w-9 shrink-0 text-right tabular-nums text-fg" data-testid="plan-usage-pct">{{ w.pct }}%</span>
            <span class="shrink-0 tabular-nums text-fg-mute" data-testid="plan-usage-countdown">{{ w.countdown }}</span>
          </template>
          <span v-else class="text-fg-mute">—</span>
        </div>
      </li>
    </ul>
  </CockpitPanel>
</template>
