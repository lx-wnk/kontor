<script setup lang="ts">
import type { Camera, HubLevel } from '../hubCamera'
import type { Sector } from '../hubGeometry'
import type { Agent } from '@/types'
import type { AgentDisplayStatus } from '@/utils/statusColors'
import { computed } from 'vue'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { statusLabel } from '@/utils/statusColors'
import { toScreen } from '../hubCamera'
import { polar, radiusForAge, sectorLabelRadius, sectorMid, visibleRingLabels } from '../hubGeometry'

const props = defineProps<{
  cam: Camera
  sectors: Sector[]
  agents: ReadonlyArray<{ agent: Agent, x: number, y: number, state: AgentDisplayStatus, needsOperator: boolean }>
  level: HubLevel
  running: number
  waiting: number
  needsYou: number
  coreTitle: string
  agentRingPx: number
  showSectorNames: boolean
}>()

defineEmits<{ core: [], agent: [agent: Agent], sector: [sector: Sector] }>()

const RING_LABEL_DEG = -128

const ringLabels = computed(() => visibleRingLabels(props.cam.k, props.agentRingPx))
const sectorNameRadius = computed(() => sectorLabelRadius(props.cam.k, props.agentRingPx))

const DOT_CLASS: Record<AgentDisplayStatus, string> = {
  working: 'bg-info-dot',
  active: 'bg-success-dot',
  waiting: 'bg-warning-dot',
  idle: 'bg-fg-faint',
  finished: 'bg-fg-faint',
}

function at(x: number, y: number) {
  const [sx, sy] = toScreen(props.cam, x, y)
  return { transform: `translate(${sx}px, ${sy}px)` }
}

function atPolar(radius: number, deg: number) {
  return at(...polar(radius, deg))
}
</script>

<template>
  <div class="pointer-events-none absolute inset-0 [&>*]:absolute [&>*]:left-0 [&>*]:top-0">
    <button
      type="button"
      data-testid="hub-core"
      :title="coreTitle"
      class="pointer-events-auto flex size-20 -translate-1/2 cursor-pointer flex-col items-center justify-center rounded-full border border-accent bg-card shadow-[0_0_40px_color-mix(in_oklch,var(--accent)_18%,transparent)]"
      :style="at(0, 0)"
      @click="$emit('core')"
    >
      <b class="text-[13px] text-fg">Kontor</b>
      <span class="text-[10px] text-fg-mute">{{ running }} running · {{ waiting }} need you</span>
      <span
        v-if="needsYou > 0"
        data-testid="hub-core-needs-you"
        class="absolute -right-1 -top-1 rounded-full border border-warning-line bg-warning-soft px-1.5 text-[10px] font-semibold text-warning-text"
      >{{ needsYou }}<span class="sr-only"> in the needs-you queue</span></span>
    </button>

    <button
      v-for="{ agent, x, y, state, needsOperator } in agents"
      :key="agent.pid"
      type="button"
      :data-testid="`hub-agent-${agent.pid}`"
      :aria-label="`${friendlyProjectName(agent.projectName)}, ${statusLabel(state)}${needsOperator ? ', needs you' : ''}`"
      :title="friendlyProjectName(agent.projectName)"
      class="pointer-events-auto flex -translate-x-1/2 -translate-y-[9px] cursor-pointer flex-col items-center gap-[3px]"
      :style="at(x, y)"
      @click="$emit('agent', agent)"
    >
      <span
        class="size-[18px] rounded-full border-[3px] border-app"
        :class="[DOT_CLASS[state], needsOperator && 'outline-2 outline-warning motion-safe:animate-pulse']"
      />
      <span
        class="flex gap-1 whitespace-nowrap rounded-md border bg-card/85 px-1.5 text-[10.5px] text-fg"
        :class="needsOperator ? 'border-warning' : 'border-line'"
      >
        <span data-testid="hub-label-name" class="max-w-[14ch] truncate">{{ friendlyProjectName(agent.projectName) }}</span>
        <em class="not-italic" :class="needsOperator ? 'text-warning-text' : 'text-fg-mute'">{{ statusLabel(state) }}</em>
      </span>
    </button>

    <template v-if="level < 2">
      <template v-if="showSectorNames">
        <button
          v-for="(sector, i) in sectors"
          :key="sector.key"
          type="button"
          :data-testid="`hub-sector-${i}`"
          class="pointer-events-auto -translate-1/2 cursor-pointer whitespace-nowrap rounded px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-widest hover:bg-fg/5"
          :class="level === 1 && 'opacity-55'"
          :style="{ ...atPolar(sectorNameRadius, sectorMid(sector)), color: `var(--sector-${i % 8})` }"
          @click="$emit('sector', sector)"
        >
          {{ sector.label }}<small class="ml-1 font-normal normal-case tracking-normal text-fg-mute">{{ sector.weight }}</small>
        </button>
      </template>
      <span
        v-for="ring in ringLabels"
        :key="ring.label"
        data-testid="hub-ring-label"
        class="-translate-1/2 text-[9px] text-fg-faint"
        :style="atPolar(radiusForAge(ring.days), RING_LABEL_DEG)"
      >{{ ring.label }}</span>
    </template>
  </div>
</template>
