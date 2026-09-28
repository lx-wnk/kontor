<script setup lang="ts">
import type { PanelState } from '@/features/cockpit'
import { computed } from 'vue'
import { CockpitPanel } from '@/features/cockpit'
import { useTasks } from '@/features/pipeline'
import LiveWorkRail from './LiveWorkRail.vue'

const { tasks, isLoading } = useTasks({ autoStart: false })

// Named positively on purpose. The first version excluded done, backlog and
// ready, which silently let 'cancelled' and 'on_hold' through — ten cancelled
// tasks rendered under the heading "10 running". A list of what counts cannot
// grow a hole when a stage is added; a list of what does not, can.
const RUNNING_STAGES = new Set(['plan_review', 'implementation', 'self_review', 'finalization'])
const running = computed(() => tasks.value.filter(t => RUNNING_STAGES.has(t.currentStage)))
const state = computed<PanelState>(() => (isLoading.value ? 'loading' : 'ready'))
</script>

<template>
  <CockpitPanel id="live-work" title="Live work" icon="▶" :state="state">
    <template #figure>
      <span data-testid="live-work-count">{{ running.length }}</span>
      <span class="ml-1.5 text-[12px] font-normal text-fg-mute">running</span>
    </template>
    <LiveWorkRail :tasks="running" />
  </CockpitPanel>
</template>
