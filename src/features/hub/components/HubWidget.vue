<script setup lang="ts">
import type { HubLevel } from '../hubCamera'
import type { Launcher } from '../hubLaunchers'
import type { WidgetId } from '@/features/workspace'
import type { Agent } from '@/types'
import { computed, inject, ref, watch } from 'vue'
import { NEEDS_YOU } from '@/composables/openTask'
import { useSidebar } from '@/composables/useSidebar'
import { useViewState } from '@/composables/useViewState'
import { useAgents } from '@/features/agents'
import { NeedsYouQueue, useKontorAgent, useKontorSession } from '@/features/mission'
import { pageView, pageWithWidget, useWorkspace, ZENTRALE_PAGE_ID } from '@/features/workspace'
import { attentionFor } from '@/utils/attention'
import { NAV_ITEMS } from '@/utils/navConfig'
import { agentDisplayStatus } from '@/utils/statusColors'
import { useHubCamera } from '../composables/useHubCamera'
import { launchersDocked, LEVEL_TARGETS } from '../hubCamera'
import { agentAngles, agentRadius, agentRingPx, planSectors, polar, radiusForAge, RINGS, sectorMid, wedgePath } from '../hubGeometry'
import { launchersFor } from '../hubLaunchers'
import HubAgentCard from './HubAgentCard.vue'
import HubControls from './HubControls.vue'
import HubLaunchers from './HubLaunchers.vue'
import HubList from './HubList.vue'
import HubMinimap from './HubMinimap.vue'
import HubOrbit from './HubOrbit.vue'

const needsYou = inject(NEEDS_YOU)
if (!needsYou)
  throw new Error('HubWidget requires NEEDS_YOU from App.vue')

const hub = ref<HTMLElement | null>(null)
const stage = ref<HTMLElement | null>(null)
const { cam, size, rel, level, dragging, zoomBy, panBy, flyTo, fit, centreWorld } = useHubCamera(stage)
const { agents } = useAgents({ autoStart: false })
const { ask, overlayOpen } = useKontorSession()
const kontorAgent = useKontorAgent()
const { activeView } = useViewState()
const { layout, wide } = useWorkspace()
const { requestNewPage } = useSidebar()
const listOpen = ref(false)
const cardPid = ref<number | null>(null)

const SECTOR_FLY_RADIUS = 260
const SECTOR_FLY_REL = 2.6
const AGENT_FLY_REL = 3
const MINIMAP_FLY_MIN_REL = 2
const ZOOM_STEP = 1.4
const PAN_STEP_PX = 60
const SLOT_KEY = /^\d$/
const HUB_WIDGET: WidgetId = 'hub'
const KONTOR_WIDGET: WidgetId = 'kontor'

// Only the blocking kinds: needsAttention() is also true for every non-working agent ('yourTurn').
function blocksOnOperator(agent: Agent): boolean {
  const kind = attentionFor(agent, null)?.kind
  return kind === 'question' || kind === 'permission'
}

const live = computed(() => agents.value.filter(a => a.status !== 'finished').sort((a, b) => a.pid - b.pid))
const notePaths: readonly string[] = []
const plan = computed(() => planSectors(notePaths, live.value.map(a => a.projectName)))
const ringPx = computed(() => agentRingPx(live.value.length, Math.min(size.value.width, size.value.height)))
const ringOnScreenPx = computed(() => agentRadius(cam.value.k, false, ringPx.value) * cam.value.k)
const docked = computed(() => launchersDocked(rel.value, cam.value.k, ringOnScreenPx.value))

const placed = computed(() => {
  const k = cam.value.k
  const { sectors, sectorOfProject } = plan.value
  return sectors.flatMap((sector) => {
    const members = live.value.filter(a => sectorOfProject.get(a.projectName) === sector.key)
    const angles = agentAngles(members.length, sector)
    return members.map((agent, i) => {
      const needsOperator = blocksOnOperator(agent)
      const [x, y] = polar(agentRadius(k, needsOperator, ringPx.value), angles[i])
      return { agent, x, y, state: agentDisplayStatus(agent), needsOperator }
    })
  })
})

const running = computed(() => placed.value.filter(p => p.state === 'working' || p.state === 'active').length)
const waiting = computed(() => placed.value.filter(p => p.needsOperator).length)
const kontorState = computed(() => kontorAgent.value ? agentDisplayStatus(kontorAgent.value) : 'off')
const kontorPage = computed(() => pageWithWidget(layout.value, KONTOR_WIDGET))
const coreTitle = computed(() => kontorPage.value ? `Open Kontor (${kontorState.value})` : 'Add the Kontor tile to a page to open it here')

const cardAgent = computed(() => live.value.find(a => a.pid === cardPid.value) ?? null)

const otherPages = computed(() => layout.value.pages.filter(p => p.id !== ZENTRALE_PAGE_ID))
const launchers = computed(() => launchersFor(NAV_ITEMS, otherPages.value, activeView.value))
const listLaunchers = computed(() => launchersFor(NAV_ITEMS, otherPages.value, activeView.value, Infinity))

function openKontor() {
  if (!kontorPage.value)
    return
  const current = layout.value.pages.find(p => pageView(p.id) === activeView.value)
  if (!current?.tiles.some(t => t.widget === KONTOR_WIDGET))
    activeView.value = pageView(kontorPage.value.id)
  ask()
}

function launch(launcher: Launcher) {
  if (launcher.kind === 'new-page')
    requestNewPage()
  else if (launcher.kind === 'more')
    listOpen.value = true
  else if (launcher.view)
    activeView.value = launcher.view
  else
    throw new Error(`Launcher ${launcher.id} has no view`)
}

function toggleWide() {
  wide.value = wide.value === HUB_WIDGET ? null : HUB_WIDGET
}

function toggleList() {
  listOpen.value = !listOpen.value
}

// Post-flush: a closed layer that held focus has unmounted by now and dropped focus to <body>.
watch([listOpen, cardPid], () => {
  if (!hub.value?.contains(document.activeElement))
    stage.value?.focus()
}, { flush: 'post' })

function escape() {
  if (cardAgent.value)
    cardPid.value = null
  else if (listOpen.value)
    toggleList()
  else
    fit()
}

function flyToLevel(target: HubLevel) {
  const [x, y] = target === 0 ? [0, 0] : centreWorld()
  flyTo(x, y, LEVEL_TARGETS[target])
}

const zoomIn = () => zoomBy(ZOOM_STEP)
const zoomOut = () => zoomBy(1 / ZOOM_STEP)

const KEY_ACTIONS: Record<string, () => void> = {
  'ArrowLeft': () => panBy(PAN_STEP_PX, 0),
  'ArrowRight': () => panBy(-PAN_STEP_PX, 0),
  'ArrowUp': () => panBy(0, PAN_STEP_PX),
  'ArrowDown': () => panBy(0, -PAN_STEP_PX),
  '+': zoomIn,
  '=': zoomIn,
  '-': zoomOut,
  '0': fit,
  'f': toggleWide,
  'F': toggleWide,
  'l': toggleList,
  'L': toggleList,
}

function isTyping(target: HTMLElement): boolean {
  return target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable
}

// Shift stays allowed: '+' needs it on most layouts.
function ignored(e: KeyboardEvent): boolean {
  return e.ctrlKey || e.metaKey || e.altKey || isTyping(e.target as HTMLElement)
}

function onKey(e: KeyboardEvent) {
  if (ignored(e))
    return
  const slot = SLOT_KEY.test(e.key) ? launchers.value[Number(e.key) - 1] : undefined
  const action = KEY_ACTIONS[e.key] ?? (slot && (() => launch(slot)))
  if (!action)
    return
  e.preventDefault()
  action()
}

// On the hub root so Escape from the list and card reaches it too; the Kontor overlay above
// the hub collapses on this same Escape from its window listener.
function onEscape(e: KeyboardEvent) {
  if (ignored(e) || overlayOpen.value)
    return
  e.preventDefault()
  escape()
}

function flyToAgent(agent: Agent) {
  cardPid.value = agent.pid
  const hit = placed.value.find(p => p.agent.pid === agent.pid)
  if (hit)
    flyTo(hit.x, hit.y, AGENT_FLY_REL)
}

function pickFromList(agent: Agent) {
  listOpen.value = false
  flyToAgent(agent)
}

function launchFromList(launcher: Launcher) {
  listOpen.value = false
  launch(launcher)
}
</script>

<template>
  <section
    ref="hub"
    data-testid="hub"
    aria-label="Zentrale"
    class="relative h-full min-h-[34rem] overflow-hidden rounded-xl border border-line bg-card md:min-h-0"
    @keydown.escape="onEscape"
  >
    <div
      ref="stage"
      data-testid="hub-stage"
      tabindex="0"
      role="application"
      aria-roledescription="zoomable map"
      aria-label="Zentrale. Arrow keys pan, plus and minus zoom, 0 shows all, F widens, L lists."
      :data-level="level"
      class="absolute inset-0 touch-none overflow-hidden outline-none focus-visible:ring-2 focus-visible:ring-accent"
      :class="dragging ? 'cursor-grabbing' : 'cursor-grab'"
      style="background: var(--hub-bg)"
      @keydown="onKey"
    >
      <svg class="pointer-events-none absolute inset-0 size-full" aria-hidden="true">
        <g :transform="`translate(${cam.tx},${cam.ty}) scale(${cam.k})`">
          <path
            v-for="(sector, i) in plan.sectors"
            :key="sector.key"
            :d="wedgePath(sector.start, sector.end)"
            fill-opacity="0.035"
            :style="{ fill: `var(--sector-${i % 8})` }"
          />
          <circle
            v-for="ring in RINGS"
            :key="ring.label"
            :r="radiusForAge(ring.days)"
            fill="none"
            stroke-dasharray="3 5"
            vector-effect="non-scaling-stroke"
            style="stroke: var(--line)"
          />
        </g>
      </svg>
      <HubOrbit
        :cam="cam"
        :sectors="plan.sectors"
        :agents="placed"
        :level="level"
        :running="running"
        :waiting="waiting"
        :needs-you="needsYou.length"
        :core-title="coreTitle"
        :agent-ring-px="ringOnScreenPx"
        :show-sector-names="notePaths.length > 0"
        @core="openKontor"
        @agent="flyToAgent"
        @sector="sector => flyTo(...polar(SECTOR_FLY_RADIUS, sectorMid(sector)), SECTOR_FLY_REL)"
      />
      <HubLaunchers :launchers="launchers" :cam="cam" :docked="docked" @launch="launch" />
      <HubControls
        :level="level"
        :wide="wide === HUB_WIDGET"
        @zoom-in="zoomIn"
        @zoom-out="zoomOut"
        @fit="fit"
        @wide="toggleWide"
        @list="toggleList"
        @level="flyToLevel"
      />
      <HubMinimap
        :cam="cam"
        :size="size"
        :sectors="plan.sectors"
        :agents="placed"
        @fly="(x, y) => flyTo(x, y, Math.max(rel, MINIMAP_FLY_MIN_REL))"
      />
    </div>
    <NeedsYouQueue
      variant="docked"
      data-hub-layer
      class="absolute left-1/2 top-2.5 z-10 max-h-[45%] w-[min(560px,calc(100%-120px))] -translate-x-1/2 overflow-y-auto rounded-[10px] border bg-card/95 px-2.5 py-2 shadow-lg"
      :class="needsYou.length > 0 ? 'border-warning-line' : 'border-line'"
    />
    <HubList
      v-if="listOpen"
      :agents="placed"
      :notes="[]"
      :launchers="listLaunchers"
      @agent="pickFromList"
      @launch="launchFromList"
      @close="listOpen = false"
    />
    <HubAgentCard v-if="cardAgent" :agent="cardAgent" @close="cardPid = null" />
  </section>
</template>
