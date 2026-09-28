<script setup lang="ts">
import type { Camera, HubLevel } from '../hubCamera'
import type { LabelSize } from '../hubCanvas'
import type { Sector } from '../hubGeometry'
import type { Agent } from '@/types'
import type { AgentDisplayStatus, ChipTone } from '@/utils/statusColors'
import { computed, onMounted, ref, shallowRef, watch } from 'vue'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { agentStatusTone, statusLabel } from '@/utils/statusColors'
import { toScreen } from '../hubCamera'
import { agentLabelKey, agentLabelOffset, inwardUnit, sectorLabelKey } from '../hubCanvas'
import { polar, radiusForAge, sectorColour, sectorMid, visibleRingLabels } from '../hubGeometry'

const props = defineProps<{
  cam: Camera
  sectors: Sector[]
  agents: ReadonlyArray<{ agent: Agent, x: number, y: number, state: AgentDisplayStatus, needsOperator: boolean }>
  level: HubLevel
  running: number
  waiting: number
  needsYou: number
  coreTitle: string
  coreDisabled: boolean
  agentRingPx: number
  // World radius the caller placed the legend on; it clears the outermost agent tier, which the
  // base ring above does not, and the culler judges the sector-name boxes at this same radius.
  sectorNameRadius: number
  showSectorNames: boolean
  // Omitted shows every label (used by callers that don't cull, e.g. tests); the dot is never gated.
  labelledAgents?: ReadonlySet<number>
  // Sector keys whose name is drawable; omitted draws them all.
  namedSectors?: ReadonlySet<string>
  // Pids whose dot is reachable; omitted draws them all. One the docked rail covers is left
  // undrawn instead of drawn under opaque chrome that swallows its clicks.
  drawnAgents?: ReadonlySet<number>
  // The direction the culler placed each label in; omitted hangs every label toward the core.
  labelDirections?: ReadonlyMap<number, readonly [number, number]>
}>()

const emit = defineEmits<{ core: [], agent: [agent: Agent], sector: [sector: Sector], measure: [sizes: ReadonlyMap<string, LabelSize>] }>()

const RING_LABEL_DEG = -128

const root = ref<HTMLElement | null>(null)
const ringLabels = computed(() => visibleRingLabels(props.cam.k, props.agentRingPx))
const sectorNamesShown = computed(() => props.level < 2 && props.showSectorNames)

// Label sizes come from the DOM, never from a character count: an estimate has twice placed labels
// over what they must clear. A size depends on the text and the font only — the camera scales
// neither — so each distinct text is measured once and reused across every pan and zoom.
const sizes = shallowRef<ReadonlyMap<string, LabelSize>>(new Map())

// Joined, so a camera tick — which hands us new objects for the same labels — is not a change.
const labelKeys = computed(() => [
  ...props.agents.map(a => agentLabelKey(a.agent.projectName, a.state)),
  ...(sectorNamesShown.value ? props.sectors.map(s => sectorLabelKey(s.label, s.weight)) : []),
].join('\n'))

function measure() {
  const grown = new Map(sizes.value)
  for (const el of root.value?.querySelectorAll<HTMLElement>('[data-label-key]') ?? []) {
    const key = el.dataset.labelKey!
    if (grown.has(key))
      continue
    const { width, height } = el.getBoundingClientRect()
    // A label without a layout box measures 0×0, and boxesOverlap reads an empty box as covering
    // nothing: cached, that text would opt out of collision avoidance until the page reloads.
    if (width <= 0 || height <= 0)
      continue
    grown.set(key, { w: width, h: height })
  }
  if (grown.size === sizes.value.size)
    return
  sizes.value = grown
  emit('measure', grown)
}

onMounted(measure)
// Post-flush and not immediate: the labels of a text that has just appeared must be in the DOM first.
watch(labelKeys, measure, { flush: 'post' })

// Tailwind needs the full literal class name, so the tone still maps to a fixed string per component.
const TONE_DOT_CLASS: Partial<Record<ChipTone, string>> = {
  success: 'bg-success-dot',
  info: 'bg-info-dot',
  warning: 'bg-warning-dot',
  neutral: 'bg-fg-faint',
}

function dotClass(state: AgentDisplayStatus): string {
  return TONE_DOT_CLASS[agentStatusTone(state)] ?? 'bg-fg-faint'
}

function at(x: number, y: number) {
  const [sx, sy] = toScreen(props.cam, x, y)
  return { transform: `translate(${sx}px, ${sy}px)` }
}

function atPolar(radius: number, deg: number) {
  return at(...polar(radius, deg))
}

function showsLabel(pid: number): boolean {
  return !props.labelledAgents || props.labelledAgents.has(pid)
}

function showsSectorName(key: string): boolean {
  return !props.namedSectors || props.namedSectors.has(key)
}

function showsAgent(pid: number): boolean {
  return !props.drawnAgents || props.drawnAgents.has(pid)
}

// Centred on the dot, then pushed along the radial line — the offset the culler's box uses.
function labelStyle(pid: number, x: number, y: number, key: string) {
  const [dx, dy] = agentLabelOffset(props.labelDirections?.get(pid) ?? inwardUnit(x, y), sizes.value.get(key))
  return { transform: `translate(-50%, -50%) translate(${dx}px, ${dy}px)` }
}
</script>

<template>
  <div ref="root" class="pointer-events-none absolute inset-0 [&>*]:absolute [&>*]:left-0 [&>*]:top-0">
    <button
      type="button"
      data-testid="hub-core"
      :title="coreTitle"
      :aria-disabled="coreDisabled"
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

    <template v-if="sectorNamesShown">
      <button
        v-for="(sector, i) in sectors"
        :key="sector.key"
        type="button"
        :data-testid="`hub-sector-${i}`"
        :data-label-key="sectorLabelKey(sector.label, sector.weight)"
        class="pointer-events-auto -translate-1/2 cursor-pointer whitespace-nowrap rounded px-1.5 py-0.5 text-[10.5px] font-semibold uppercase tracking-widest hover:bg-fg/5"
        :class="[level === 1 && 'opacity-55', !showsSectorName(sector.key) && 'invisible']"
        :style="{ ...atPolar(sectorNameRadius, sectorMid(sector)), color: `var(--sector-${sectorColour(sector.key)})` }"
        @click="$emit('sector', sector)"
      >
        {{ sector.label }}<small class="ml-1 font-normal normal-case tracking-normal text-fg-mute">{{ sector.weight }}</small>
      </button>
    </template>
    <template v-if="level < 2">
      <span
        v-for="ring in ringLabels"
        :key="ring.label"
        data-testid="hub-ring-label"
        class="-translate-1/2 text-[9px] text-fg-faint"
        :style="atPolar(radiusForAge(ring.days), RING_LABEL_DEG)"
      >{{ ring.label }}</span>
    </template>

    <!-- After the sector names, so the agent layer paints and takes pointer events above the legend. -->
    <button
      v-for="{ agent, x, y, state, needsOperator } in agents"
      :key="agent.pid"
      type="button"
      :data-testid="`hub-agent-${agent.pid}`"
      :aria-label="`${friendlyProjectName(agent.projectName)}, ${statusLabel(state)}${needsOperator ? ', needs you' : ''}`"
      :title="friendlyProjectName(agent.projectName)"
      class="group pointer-events-auto relative flex -translate-x-1/2 -translate-y-[9px] cursor-pointer"
      :class="!showsAgent(agent.pid) && 'invisible'"
      :style="at(x, y)"
      @click="$emit('agent', agent)"
    >
      <span
        class="size-[18px] rounded-full border-[3px] border-app"
        :class="[dotClass(state), needsOperator && 'outline-2 outline-warning motion-safe:animate-pulse']"
      />
      <!-- Absolute, so a culled label neither grows the button's hit box nor covers a neighbour's dot. -->
      <span
        data-testid="hub-label"
        :data-label-key="agentLabelKey(agent.projectName, state)"
        class="absolute left-1/2 top-1/2 flex gap-1 whitespace-nowrap rounded-md border bg-card/85 px-1.5 text-[10.5px] text-fg"
        :class="[needsOperator ? 'border-warning' : 'border-line', !showsLabel(agent.pid) && 'invisible group-hover:visible group-focus-visible:visible']"
        :style="labelStyle(agent.pid, x, y, agentLabelKey(agent.projectName, state))"
      >
        <span data-testid="hub-label-name" class="max-w-[14ch] truncate">{{ friendlyProjectName(agent.projectName) }}</span>
        <em class="not-italic" :class="needsOperator ? 'text-warning-text' : 'text-fg-mute'">{{ statusLabel(state) }}</em>
      </span>
    </button>
  </div>
</template>
