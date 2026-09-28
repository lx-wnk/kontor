<script setup lang="ts">
import type { PanelState } from '../panelState'
import { computed } from 'vue'
import { useResources } from '@/features/settings'
import CockpitPanel from './CockpitPanel.vue'

// A fresh useResources per panel: it is not a singleton, and each panel names
// its kind here so the composable's mount fetch asks for it directly.
const { resources, loading, error, denied } = useResources('memory_space')

// kind=memory_space gates on memory.read (api/resources/handler.go), so on a
// fresh install this panel is denied and must say so rather than reporting an
// empty store.
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
    id="memory"
    title="Memory"
    icon="✎"
    :state="state"
    :message="denied ?? error ?? 'No memory space is defined in this scope.'"
  >
    <ul class="flex flex-col gap-1.5">
      <li v-for="r in resources.slice(0, 6)" :key="r.id" class="flex items-center justify-between gap-2 text-[12px] min-w-0" :data-testid="`cockpit-memoryspace-${r.id}`">
        <span class="truncate text-fg">{{ r.name }}</span>
        <span class="shrink-0 text-fg-mute">{{ r.state }}</span>
      </li>
    </ul>
  </CockpitPanel>
</template>
