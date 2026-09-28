<script setup lang="ts">
defineProps<{
  icon: string
  label: string
  active: boolean
  expanded: boolean
}>()
defineEmits<{ select: [] }>()
</script>

<template>
  <button
    type="button"
    class="flex items-center gap-3 w-full overflow-hidden rounded-lg px-2.5 min-h-[40px] text-[13px] transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
    :class="active
      ? 'bg-accent-soft text-accent font-semibold'
      : 'text-fg-mute hover:text-fg hover:bg-raised'"
    :aria-current="active ? 'page' : undefined"
    :title="!expanded ? label : undefined"
    @click="$emit('select')"
  >
    <span class="text-[16px] w-5 shrink-0 text-center" aria-hidden="true">{{ icon }}</span>
    <span
      class="truncate transition-opacity duration-150 motion-reduce:transition-none"
      :class="expanded ? 'opacity-100 delay-75' : 'opacity-0 delay-0'"
    >{{ label }}</span>
    <span
      class="ml-auto whitespace-nowrap transition-opacity duration-150 motion-reduce:transition-none"
      :class="expanded ? 'opacity-100 delay-75' : 'opacity-0 delay-0'"
    ><slot name="badge" /></span>
  </button>
</template>
