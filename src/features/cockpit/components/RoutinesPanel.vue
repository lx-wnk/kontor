<script setup lang="ts">
import type { PanelState } from '../panelState'
import { computed } from 'vue'
import { useResources } from '@/features/settings'
import CockpitPanel from './CockpitPanel.vue'

// A fresh useResources per panel: it is not a singleton, and each panel names
// its kind here so the composable's mount fetch asks for it directly.
const { resources, loading, error, denied } = useResources('routine')

// kind=routine is deliberately ungated server-side (api/resources/handler.go
// gates memory_space only), so `denied` is carried anyway rather than dropped:
// if that route is ever gated, this panel reports the refusal instead of
// drawing it as an empty list.
const state = computed<PanelState>(() => {
  if (loading.value)
    return 'loading'
  if (denied.value)
    return 'denied'
  if (error.value)
    return 'failed'
  return resources.value.length === 0 ? 'empty' : 'ready'
})
</script>

<template>
  <CockpitPanel
    id="routines"
    title="Routines"
    :state="state"
    :message="denied ?? error ?? 'No routine is scheduled.'"
  >
    <ul class="flex flex-col gap-1.5">
      <li v-for="r in resources.slice(0, 6)" :key="r.id" class="flex items-center justify-between gap-2 text-[12px] min-w-0" :data-testid="`cockpit-routine-${r.id}`">
        <span class="truncate text-fg">{{ r.name }}</span>
        <span class="shrink-0 text-fg-mute">{{ r.state }}</span>
      </li>
    </ul>
  </CockpitPanel>
</template>
