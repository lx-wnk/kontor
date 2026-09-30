<script setup lang="ts">
import type { Camera } from '../hubCamera'
import type { Circle } from '../hubPack'
import type { Agent } from '@/types'
import type { AgentDisplayStatus, ChipTone } from '@/utils/statusColors'
import { computed } from 'vue'
import { agentStatusTone } from '@/utils/statusColors'
import { MINIMAP_HALF, sectorColour } from '../hubGeometry'

const props = defineProps<{
  cam: Camera
  size: { width: number, height: number }
  categories: ReadonlyMap<string, Circle>
  agents: ReadonlyArray<{ agent: Agent, x: number, y: number, state: AgentDisplayStatus }>
}>()

const emit = defineEmits<{ fly: [wx: number, wy: number] }>()

const AGENT_DOT_R = 16

// Tailwind needs the full literal class name, so the tone still maps to a fixed string per component.
const TONE_DOT_FILL: Partial<Record<ChipTone, string>> = {
  success: 'fill-success-dot',
  info: 'fill-info-dot',
  warning: 'fill-warning-dot',
  neutral: 'fill-fg-faint',
}

function fillClass(state: AgentDisplayStatus): string {
  return TONE_DOT_FILL[agentStatusTone(state)] ?? 'fill-fg-faint'
}

const viewport = computed(() => {
  const { k, tx, ty } = props.cam
  return { x: -tx / k, y: -ty / k, width: props.size.width / k, height: props.size.height / k }
})

function onClick(e: MouseEvent) {
  const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
  emit('fly', (e.clientX - r.left) / r.width * 2 * MINIMAP_HALF - MINIMAP_HALF, (e.clientY - r.top) / r.height * 2 * MINIMAP_HALF - MINIMAP_HALF)
}
</script>

<template>
  <svg
    data-hub-layer
    :viewBox="`${-MINIMAP_HALF} ${-MINIMAP_HALF} ${2 * MINIMAP_HALF} ${2 * MINIMAP_HALF}`"
    aria-label="Overview map"
    aria-hidden="true"
    class="absolute bottom-2.5 right-2.5 z-[2] size-[108px] cursor-crosshair rounded-lg border border-line-strong bg-card/90"
    @click="onClick"
  >
    <circle
      v-for="[key, c] in categories"
      :key="key"
      :data-testid="`hub-category-${key}`"
      :cx="c.x"
      :cy="c.y"
      :r="c.r"
      fill-opacity="0.18"
      :style="{ fill: `var(--sector-${sectorColour(key)})` }"
    />
    <circle v-for="{ agent, x, y, state } in agents" :key="agent.pid" :cx="x" :cy="y" :r="AGENT_DOT_R" :class="fillClass(state)" />
    <rect v-bind="viewport" stroke-width="6" class="fill-accent/10 stroke-accent" />
  </svg>
</template>
