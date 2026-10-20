<script setup lang="ts">
import { computed } from 'vue'
import { statusLabel } from '../../utils/statusColors'

type Variant = 'active' | 'working' | 'replied' | 'waiting' | 'idle' | 'finished' | 'completed' | 'error' | 'info'

const props = defineProps<{ variant: Variant, label?: string }>()

const dotClass: Record<Variant, string> = {
  active: 'bg-success-dot',
  working: 'bg-info-dot animate-pulse',
  replied: 'bg-accent',
  waiting: 'bg-warning-dot',
  idle: 'bg-slate-400 dark:bg-slate-500',
  finished: 'bg-slate-400 dark:bg-slate-500',
  completed: 'bg-slate-400 dark:bg-slate-500',
  error: 'bg-danger-dot',
  info: 'bg-info-dot',
}

const labelClass: Record<Variant, string> = {
  active: 'text-success-text',
  working: 'text-info-text',
  replied: 'text-accent',
  waiting: 'text-warning-text',
  idle: 'text-fg-mute',
  finished: 'text-fg-mute',
  completed: 'text-fg-mute',
  error: 'text-danger-text',
  info: 'text-info-text',
}

const displayLabel = computed(() => props.label ?? statusLabel(props.variant))
</script>

<template>
  <!-- UX-15: status conveyed via color dot + visible text label; sr-only provides AT fallback -->
  <span class="inline-flex items-center gap-1.5 text-xs">
    <span class="size-2 rounded-full flex-shrink-0" :class="dotClass[variant]" aria-hidden="true" />
    <span :class="labelClass[variant]" aria-hidden="true">{{ displayLabel }}</span>
    <span class="sr-only">{{ displayLabel }}</span>
  </span>
</template>
