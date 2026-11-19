<script setup lang="ts">
import type { FleetFilter } from '@/utils/agentGroup'
import { FLEET_FILTERS } from '@/utils/agentGroup'

defineProps<{
  modelValue: FleetFilter
  counts: Record<FleetFilter, number>
}>()
const emit = defineEmits<{ 'update:modelValue': [value: FleetFilter] }>()
</script>

<template>
  <div role="group" aria-label="Filter agents by state" class="mb-3 flex flex-wrap gap-2">
    <button
      v-for="filter in FLEET_FILTERS"
      :key="filter.value"
      type="button"
      :data-testid="`fleet-chip-${filter.value}`"
      :aria-pressed="filter.value === modelValue"
      class="cursor-pointer rounded-full border px-3 py-1 text-[12px] focus-visible:outline-2 focus-visible:outline-accent"
      :class="filter.value === modelValue ? 'border-accent bg-accent-soft text-fg' : 'border-line-strong text-fg-mute'"
      @click="emit('update:modelValue', filter.value)"
    >
      {{ filter.label }}
      <span class="ml-1 tabular-nums">{{ counts[filter.value] }}</span>
    </button>
  </div>
</template>
