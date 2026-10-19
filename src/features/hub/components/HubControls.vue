<script setup lang="ts">
import type { HubLevel } from '../hubCamera'
import { CONTROLS_BUTTON_PX, CONTROLS_GAP_PX, CONTROLS_RIGHT_INSET_PX } from '../hubLaunchers'

defineProps<{ level: HubLevel, wide: boolean }>()
const emit = defineEmits<{ zoomIn: [], zoomOut: [], fit: [], wide: [], list: [], level: [level: HubLevel] }>()

const LEVELS: ReadonlyArray<{ level: HubLevel, label: string }> = [
  { level: 0, label: 'Overview' },
  { level: 1, label: 'Topics' },
  { level: 2, label: 'Notes' },
]
const BUTTON = 'size-(--controls-button) cursor-pointer rounded-[7px] border bg-card hover:border-accent'
const IDLE = 'border-line-strong text-fg'
const COLUMN_STYLE = { 'right': `${CONTROLS_RIGHT_INSET_PX}px`, 'gap': `${CONTROLS_GAP_PX}px`, '--controls-button': `${CONTROLS_BUTTON_PX}px` }
</script>

<template>
  <div class="absolute top-1/2 z-[2] flex -translate-y-1/2 flex-col" :style="COLUMN_STYLE">
    <button type="button" :class="[BUTTON, IDLE]" aria-label="Zoom in" title="Zoom in (+)" @click="emit('zoomIn')">
      +
    </button>
    <button type="button" :class="[BUTTON, IDLE]" aria-label="Zoom out" title="Zoom out (−)" @click="emit('zoomOut')">
      −
    </button>
    <button type="button" :class="[BUTTON, IDLE]" aria-label="Show all" title="Show all (0)" @click="emit('fit')">
      ⌂
    </button>
    <button
      type="button"
      aria-label="Widen"
      title="Widen (F)"
      :aria-pressed="wide"
      :class="[BUTTON, wide ? 'border-accent text-accent' : IDLE]"
      @click="emit('wide')"
    >
      ⤢
    </button>
    <button type="button" :class="[BUTTON, IDLE]" aria-label="List" title="List (L)" @click="emit('list')">
      ☰
    </button>
  </div>
  <div role="group" aria-label="Zoom level" class="absolute bottom-2.5 left-2.5 z-[2] flex overflow-hidden rounded-[7px] border border-line-strong bg-card">
    <button
      v-for="option in LEVELS"
      :key="option.level"
      type="button"
      :aria-pressed="option.level === level"
      class="cursor-pointer px-2.5 py-1 text-[12px]"
      :class="option.level === level ? 'bg-accent-soft text-fg' : 'text-fg-mute'"
      @click="emit('level', option.level)"
    >
      {{ option.label }}
    </button>
  </div>
</template>
