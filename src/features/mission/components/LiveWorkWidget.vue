<script setup lang="ts">
import type { PanelState } from '@/features/cockpit'
import { useIntervalFn } from '@vueuse/core'
import { computed, ref, watch } from 'vue'
import { useAgents } from '@/features/agents'
import { CockpitPanel } from '@/features/cockpit'
import { byLatestActivity, LIVE_WORK_RESORT_MS, useTasks } from '@/features/pipeline'
import LiveWorkRail from './LiveWorkRail.vue'

const { tasks, isLoading } = useTasks({ autoStart: false })
const { agents } = useAgents({ autoStart: false })

// Named positively on purpose. The first version excluded done, backlog and
// ready, which silently let 'cancelled' and 'on_hold' through — ten cancelled
// tasks rendered under the heading "10 running". A list of what counts cannot
// grow a hole when a stage is added; a list of what does not, can.
const RUNNING_STAGES = new Set(['plan_review', 'implementation', 'self_review', 'finalization'])

// Dependency-blocked and unsatisfiable tasks are not doing work.
const running = computed(() =>
  tasks.value.filter(t =>
    RUNNING_STAGES.has(t.currentStage)
    && !t.isBlocked
    && !t.isUnsatisfiable,
  ),
)

function agentActivityMs(sessionId: string | null | undefined): number {
  if (!sessionId)
    return 0
  const agent = agents.value.find(a => a.sessionId === sessionId)
  return agent?.lastActivity ? new Date(agent.lastActivity).getTime() : 0
}

function activityOf(t: { activeSessionId?: string | null, updatedAt: string }): number {
  const agentMs = agentActivityMs(t.activeSessionId)
  const updatedMs = new Date(t.updatedAt).getTime()
  return Math.max(agentMs, updatedMs || 0)
}

// Sort keys are snapshotted so the order only changes on an explicit trigger,
// not on every heartbeat — cards must not jump under the pointer.
const sortKey = ref(new Map<string, number>())

function snapshotSortKeys() {
  const m = new Map<string, number>()
  for (const t of running.value)
    m.set(t.id, activityOf(t))
  sortKey.value = m
}

const fingerprint = computed(() =>
  running.value.map(t => `${t.id}:${t.currentStage}:${t.latestStageRunStatus ?? ''}`).join('|'),
)

// Stage/status changes reorder immediately; activity-only changes wait for the interval.
watch(fingerprint, snapshotSortKeys, { immediate: true })
useIntervalFn(snapshotSortKeys, LIVE_WORK_RESORT_MS)

const sortedRunning = computed(() => {
  const keys = sortKey.value
  return [...running.value].sort(byLatestActivity(t => keys.get(t.id) ?? 0))
})

const state = computed<PanelState>(() => (isLoading.value ? 'loading' : 'ready'))
</script>

<template>
  <CockpitPanel id="live-work" title="Live work" icon="▶" :state="state">
    <template #figure>
      <span data-testid="live-work-count">{{ sortedRunning.length }}</span>
      <span class="ml-1.5 text-[12px] font-normal text-fg-mute">running</span>
    </template>
    <LiveWorkRail :tasks="sortedRunning" />
  </CockpitPanel>
</template>
