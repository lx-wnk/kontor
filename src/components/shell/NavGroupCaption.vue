<script setup lang="ts">
defineProps<{
  label: string
  showDivider: boolean
  expanded: boolean
}>()
</script>

<template>
  <div data-testid="nav-group-slot" class="relative h-7 shrink-0 overflow-hidden">
    <span
      class="absolute inset-0 flex items-center px-2 text-[9px] uppercase tracking-wider text-fg-faint font-bold whitespace-nowrap transition-opacity duration-150 motion-reduce:transition-none"
      :class="expanded ? 'opacity-100 delay-75' : 'opacity-0 delay-0'"
    >
      {{ label }}
    </span>
    <span
      v-if="showDivider"
      aria-hidden="true"
      data-testid="nav-group-divider"
      class="absolute inset-0 flex items-center justify-center transition-opacity duration-150 motion-reduce:transition-none"
      :class="expanded ? 'opacity-0 delay-0' : 'opacity-100 delay-75'"
    >
      <span class="h-px w-6 bg-line" />
    </span>
  </div>
</template>
