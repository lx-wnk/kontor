<script setup lang="ts">
import type { Camera } from '../hubCamera'
import type { Sector } from '../hubGeometry'
import type { AgentDisplayStatus } from '@/utils/statusColors'
import { computed } from 'vue'
import { wedgePath } from '../hubGeometry'

const props = defineProps<{
  cam: Camera
  size: { width: number, height: number }
  sectors: Sector[]
  agents: ReadonlyArray<{ x: number, y: number, state: AgentDisplayStatus }>
}>()

const emit = defineEmits<{ fly: [wx: number, wy: number] }>()

const HALF = 540
const AGENT_DOT_R = 16

const DOT_FILL: Record<AgentDisplayStatus, string> = {
  working: 'fill-info-dot',
  active: 'fill-success-dot',
  waiting: 'fill-warning-dot',
  idle: 'fill-fg-faint',
  finished: 'fill-fg-faint',
}

const viewport = computed(() => {
  const { k, tx, ty } = props.cam
  return { x: -tx / k, y: -ty / k, width: props.size.width / k, height: props.size.height / k }
})

function onClick(e: MouseEvent) {
  const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
  emit('fly', (e.clientX - r.left) / r.width * 2 * HALF - HALF, (e.clientY - r.top) / r.height * 2 * HALF - HALF)
}
</script>

<template>
  <svg
    data-hub-layer
    :viewBox="`${-HALF} ${-HALF} ${2 * HALF} ${2 * HALF}`"
    aria-label="Overview map"
    class="absolute bottom-2.5 right-2.5 z-[2] size-[108px] cursor-crosshair rounded-lg border border-line-strong bg-card/90"
    @click="onClick"
  >
    <path
      v-for="(sector, i) in sectors"
      :key="sector.key"
      :d="wedgePath(sector.start, sector.end)"
      fill-opacity="0.12"
      :style="{ fill: `var(--sector-${i % 8})` }"
    />
    <circle v-for="(agent, i) in agents" :key="i" :cx="agent.x" :cy="agent.y" :r="AGENT_DOT_R" :class="DOT_FILL[agent.state]" />
    <rect v-bind="viewport" stroke-width="6" class="fill-accent/10 stroke-accent" />
  </svg>
</template>
