<script setup lang="ts">
import type { ActiveView } from '../../composables/useViewState'
import { computed } from 'vue'
import { useWorkspace } from '@/features/workspace'
import { viewTitle } from '../../utils/navConfig'
import OfflineBadge from '../OfflineBadge.vue'

const props = defineProps<{
  activeView: ActiveView
}>()

const { layout } = useWorkspace()
const title = computed(() => viewTitle(props.activeView, layout.value.pages))
</script>

<template>
  <header class="h-12 shrink-0 flex items-center gap-3 px-4 border-b border-line bg-card">
    <h1 class="text-[15px] font-semibold text-fg">
      {{ title }}
    </h1>
    <div class="ml-auto flex items-center gap-3">
      <slot name="cta" />
      <OfflineBadge />
    </div>
  </header>
</template>
