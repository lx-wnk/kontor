<script setup lang="ts">
import type { PanelState } from '../panelState'
import type { PipelineStage } from '@/types'
import { computed } from 'vue'
import { useTasks } from '@/features/pipeline'
import { STAGE_LABELS } from '@/utils/stageLabels'
import CockpitPanel from './CockpitPanel.vue'

// tasksByStageMap already exists on useTasks — do not re-derive the grouping
// here. autoStart: false, same singleton rule as the agents stream.
const { tasksByStageMap, isLoading, error } = useTasks({ autoStart: false })

// Object.entries widens the key to string (a known TS limitation), so it is
// cast back to PipelineStage here — the same pattern useTasks.ts itself uses
// for tasksByStageMap's own keys.
const byStage = computed(() =>
  (Object.entries(tasksByStageMap.value) as [PipelineStage, typeof tasksByStageMap.value[PipelineStage]][])
    .map(([stage, list]) => [stage, list?.length ?? 0] as const)
    .filter(([, count]) => count > 0)
    .sort((a, b) => b[1] - a[1]))

const state = computed<PanelState>(() => {
  if (error.value)
    return 'failed'
  if (isLoading.value)
    return 'loading'
  return byStage.value.length === 0 ? 'empty' : 'ready'
})
</script>

<template>
  <CockpitPanel id="pipeline" title="Pipeline" icon="▦" :state="state" :message="error ?? 'No task is in the pipeline.'">
    <ul class="flex flex-col gap-1.5">
      <li v-for="[stage, count] in byStage" :key="stage" class="flex items-center justify-between gap-2 text-[12px]" :data-testid="`cockpit-stage-${stage}`">
        <span class="truncate text-fg">{{ STAGE_LABELS[stage] ?? stage }}</span>
        <span class="shrink-0 text-fg-mute">{{ count }}</span>
      </li>
    </ul>
  </CockpitPanel>
</template>
