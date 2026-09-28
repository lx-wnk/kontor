<script setup lang="ts">
import type { HubLevel } from '../hubCamera'
import type { LabelBox, LabelSize } from '../hubCanvas'
import type { Sector } from '../hubGeometry'
import type { Launcher } from '../hubLaunchers'
import type { WidgetId } from '@/features/workspace'
import type { Agent } from '@/types'
import { useEventListener, useNow } from '@vueuse/core'
import { computed, inject, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { NEEDS_YOU, OPEN_SETTINGS } from '@/composables/openTask'
import { useSidebar } from '@/composables/useSidebar'
import { pageView, useViewState } from '@/composables/useViewState'
import { useAgents } from '@/features/agents'
import { NeedsYouQueue, useKontorAgent, useKontorSession } from '@/features/mission'
import { failedWidgets, HUB_WIDGET, pageWithWidget, useWorkspace, ZENTRALE_PAGE_ID } from '@/features/workspace'
import { attentionFor } from '@/utils/attention'
import { isTypingTarget } from '@/utils/isTypingTarget'
import { NAV_ITEMS } from '@/utils/navConfig'
import { agentDisplayStatus } from '@/utils/statusColors'
import { lastHubView, useHubCamera } from '../composables/useHubCamera'
import { hubFocusRequest } from '../composables/useHubFocus'
import { useObsidianGraph } from '../composables/useObsidianGraph'
import { launchersDocked, LEVEL_TARGETS, toScreen } from '../hubCamera'
import { agentDotBox, agentLabelBox, agentLabelDirection, agentLabelKey, agentPriority, boxesOverlap, cullLabels, hitNote, hubNoteSet, inwardUnit, sectorLabelBox, sectorLabelKey } from '../hubCanvas'
import { agentNoteRows, liveEdges } from '../hubEdges'
import { agentAngles, agentRadius, agentRingPx, agentSectorRingPx, DAY_MS, notePoint, planSectors, polar, radiusForAge, RINGS, sectorColour, sectorLabelRadius, sectorMid, wedgePath } from '../hubGeometry'
import { GRAPH_NOTICES } from '../hubGraphNotices'
import { launcherBox, launchersFor } from '../hubLaunchers'
import HubAgentCard from './HubAgentCard.vue'
import HubBrainCanvas from './HubBrainCanvas.vue'
import HubControls from './HubControls.vue'
import HubLaunchers from './HubLaunchers.vue'
import HubList from './HubList.vue'
import HubMinimap from './HubMinimap.vue'
import HubNoteCard from './HubNoteCard.vue'
import HubOrbit from './HubOrbit.vue'

const needsYou = inject(NEEDS_YOU)
if (!needsYou)
  throw new Error('HubWidget requires NEEDS_YOU from App.vue')
const openSettings = inject(OPEN_SETTINGS)
if (!openSettings)
  throw new Error('HubWidget requires OPEN_SETTINGS from App.vue')

const hub = ref<HTMLElement | null>(null)
const stage = ref<HTMLElement | null>(null)
const { cam, k0, size, rel, level, dragging, zoomBy, panBy, flyTo, fit, centreWorld } = useHubCamera(stage, { onTap: tapNote })
const { status: graphStatus, message: graphMessage, notes, refresh: refreshGraph, recentNotes, noteByPath } = useObsidianGraph()
const { agents } = useAgents({ autoStart: false })
const { ask, overlayOpen } = useKontorSession()
const kontorAgent = useKontorAgent()
const { activeView } = useViewState()
const { layout, wide } = useWorkspace()
const { requestNewPage } = useSidebar()
const listOpen = ref(false)
type HubCard = { kind: 'agent', pid: number } | { kind: 'note', path: string }
const openCard = ref<HubCard | null>(null)
let cameraBeforeCard: [number, number, number] | null = null
// Runs after useHubCamera's own unmount hook, so it overrides the zoomed-in card view it saved.
onUnmounted(() => {
  if (cameraBeforeCard) {
    const [wx, wy, r] = cameraBeforeCard
    lastHubView.value = { wx, wy, rel: r }
  }
})

const SECTOR_FLY_RADIUS = 260
const SECTOR_FLY_REL = 2.6
const AGENT_FLY_REL = 3
const NOTE_FLY_REL = 2.6
const CHIP_FLY_MIN_REL = 3
const LIST_NOTE_FLY_REL = 5
const LIST_NOTE_COUNT = 14
const MINIMAP_FLY_MIN_REL = 2
const ZOOM_STEP = 1.4
const PAN_STEP_PX = 60
const SLOT_KEY = /^\d$/
const KONTOR_WIDGET: WidgetId = 'kontor'
const KONTOR_LOAD_FAILED = 'Kontor could not load — reload the app'
// One condition, one wording per control: each names the action it would have taken, so a tooltip
// never describes something other than the button it sits on.
const KONTOR_BLOCKED: Record<'failed' | 'absent', { open: string, ask: string }> = {
  failed: { open: KONTOR_LOAD_FAILED, ask: KONTOR_LOAD_FAILED },
  absent: {
    open: 'Add the Kontor tile to a page to open it here',
    ask: 'Add the Kontor tile to a page to ask Kontor here',
  },
}

// Only the blocking kinds: needsAttention() is also true for every non-working agent ('yourTurn').
function blocksOnOperator(agent: Agent): boolean {
  const kind = attentionFor(agent, null)?.kind
  return kind === 'question' || kind === 'permission'
}

const live = computed(() => agents.value.filter(a => a.status !== 'finished').sort((a, b) => a.pid - b.pid))
// Equal joined names return the same array reference, so a tick that only changes an agent's status does not retrigger planSectors.
const liveProjectNames = computed<string[]>((previous) => {
  const names = live.value.map(a => a.projectName)
  return previous && names.join('\n') === previous.join('\n') ? previous : names
})
// A refetch passes through 'loading'; a failed refetch keeps the last good notes too, so the brain and an open note card don't blank.
const vaultNotes = computed(() => graphStatus.value === 'ready' || graphStatus.value === 'loading' || graphStatus.value === 'failed' ? notes.value : [])
const plan = computed(() => planSectors(vaultNotes.value.map(n => n.path), liveProjectNames.value))
const graphNotice = computed(() => GRAPH_NOTICES[graphStatus.value])

// A note the sector plan does not place is drawn nowhere: off stage, so the canvas skips it and it
// cannot be hit. A gap in the brain, rather than a TypeError inside a render function.
const OFF_MAP: [number, number] = [Number.NaN, Number.NaN]

const brain = computed(() => {
  const { sectors, sectorOfNote } = plan.value
  const slotOf = new Map<string | undefined, { sector: Sector, colour: number }>(sectors.map(sector => [sector.key, { sector, colour: sectorColour(sector.key) }]))
  const now = Date.now()
  const slots = vaultNotes.value.map(n => slotOf.get(sectorOfNote.get(n.path)))
  return {
    points: vaultNotes.value.map((n, i) => {
      const slot = slots[i]
      return slot ? notePoint(n.path, slot.sector, (now - n.mtimeMs) / DAY_MS) : OFF_MAP
    }),
    colours: slots.map(s => s?.colour ?? 0),
    links: vaultNotes.value.flatMap(n => n.links.map((to): [number, number] => [n.index, to])),
    hubNotes: hubNoteSet(vaultNotes.value, n => sectorOfNote.get(n.path) ?? ''),
  }
})

const cardNote = computed(() => {
  const card = openCard.value
  return card?.kind === 'note' ? vaultNotes.value.find(n => n.path === card.path) ?? null : null
})

// Empty when the plan has no sector for the note: a blank label beside it, not a throw.
function sectorLabel(path: string): string {
  const { sectors, sectorOfNote } = plan.value
  return sectors.find(s => s.key === sectorOfNote.get(path))?.label ?? ''
}

const listNotes = computed(() => vaultNotes.value.length ? recentNotes(LIST_NOTE_COUNT).map(n => ({ ...n, sector: sectorLabel(n.path) })) : [])

// A denied refetch keeps `notes` but drops them from the map, so an index resolved through `notes`
// can miss; the request is dropped and the graph notice says why.
function openNote(index: number) {
  const note = vaultNotes.value[index]
  if (note)
    showCard({ kind: 'note', path: note.path })
}

function showCard(card: HubCard) {
  if (!openCard.value)
    cameraBeforeCard = [...centreWorld(), rel.value]
  openCard.value = card
}

function closeCard() {
  openCard.value = null
  if (cameraBeforeCard)
    flyTo(...cameraBeforeCard)
  cameraBeforeCard = null
}

function flyToNote(index: number, relTarget: number) {
  const point = brain.value.points[index]
  if (!point)
    return
  openNote(index)
  flyTo(...point, relTarget)
}

function tapNote(sx: number, sy: number) {
  const hit = hitNote(brain.value.points, cam.value, sx, sy)
  if (hit < 0)
    return
  if (level.value === 0)
    flyTo(...brain.value.points[hit], NOTE_FLY_REL)
  else
    openNote(hit)
}

onMounted(() => refreshGraph())
useEventListener(window, 'focus', () => refreshGraph())
const stagePx = computed(() => Math.min(size.value.width, size.value.height))
const ringPx = computed(() => agentRingPx(live.value.length, stagePx.value))
const baseRingPx = computed(() => agentRadius(k0.value, false, ringPx.value) * k0.value)
const ringOnScreenPx = computed(() => baseRingPx.value * rel.value)

const placed = computed(() => {
  const k = k0.value
  const { sectors, sectorOfProject } = plan.value
  return sectors.flatMap((sector) => {
    const members = live.value.filter(a => sectorOfProject.get(a.projectName) === sector.key)
    const angles = agentAngles(members.length, sector)
    return members.map((agent, i) => {
      const needsOperator = blocksOnOperator(agent)
      const ring = agentSectorRingPx(ringPx.value, i, members.length, stagePx.value)
      const [x, y] = polar(agentRadius(k, needsOperator, ring), angles[i])
      return { agent, x, y, state: agentDisplayStatus(agent), needsOperator }
    })
  })
})

// How far the agents actually reach: a sector's agents are staggered outward tier by tier, so the
// legend and the launchers must clear the outermost tier in use, not the base ring.
const outerRingBasePx = computed(() => Math.max(baseRingPx.value, ...placed.value.map(p => Math.hypot(p.x, p.y) * k0.value)))
const sectorNameRadius = computed(() => sectorLabelRadius(k0.value, outerRingBasePx.value))
const docked = computed(() => launchersDocked(rel.value, k0.value, outerRingBasePx.value, stagePx.value))

const otherPages = computed(() => layout.value.pages.filter(p => p.id !== ZENTRALE_PAGE_ID))
const launchers = computed(() => launchersFor(NAV_ITEMS, otherPages.value, activeView.value))
const listLaunchers = computed(() => launchersFor(NAV_ITEMS, otherPages.value, activeView.value, Infinity))
const launcherBoxes = computed(() => launchers.value.map((_, i) => launcherBox(i, docked.value, cam.value, k0.value, outerRingBasePx.value)))
// A launcher is opaque chrome. The ring clears the map by construction; the docked rail is fixed to
// the screen while the map pans under it, so whatever it covers is unreachable and stays undrawn.
function coveredByRail(box: LabelBox): boolean {
  return launcherBoxes.value.some(b => boxesOverlap(box, b))
}

const placedScreen = computed(() => placed.value.map(p => ({ p, screen: toScreen(cam.value, p.x, p.y) })))
// An agent under the rail is left undrawn rather than drawn unclickable: the dot would look
// interactive and swallow every press. It keeps its row in the list view (L) and its dot on the
// minimap, neither of which the rail covers.
const drawnAgents = computed(() => new Set(placedScreen.value
  .filter(({ screen: [sx, sy] }) => !coveredByRail(agentDotBox(sx, sy)))
  .map(({ p }) => p.agent.pid)))

const EDGE_CLOCK_MS = 30_000
// Edges fade and expire on this clock too: an idle agent sends no SSE tick to redraw them.
const edgeNow = useNow({ interval: EDGE_CLOCK_MS })
const notesByPath = computed(() => new Map(vaultNotes.value.map(n => [n.path, n])))
const edges = computed(() => liveEdges(placed.value, drawnAgents.value, notesByPath.value, edgeNow.value.getTime()))
const listAgents = computed(() => placed.value.map(p => ({ ...p, notes: agentNoteRows(p.agent, notesByPath.value, edgeNow.value.getTime()) })))

const showSectorNames = computed(() => vaultNotes.value.length > 0)

// Measured by HubOrbit from the rendered DOM, keyed by label text; a text not in here yet has no
// box, so its label is drawn for the one tick before its size arrives.
const labelSizes = shallowRef<ReadonlyMap<string, LabelSize>>(new Map())

// Greedy label culling (Ruling R23): the stagger only separates dots, a crowded sector still needs
// a subset of labels drawn. A culled label stays reachable via hover/focus (HubOrbit.vue's CSS) and
// the agent's full aria-label; the list view (L) is unaffected since it reads `placed`, not this set.
//
// The obstacle set a label must clear is seeded before any label is placed: every OTHER agent's dot
// (own dot excluded, or a label could never sit next to its own agent) and, when they are actually
// drawn (HubOrbit.vue only shows them below level 2 and with showSectorNames), every sector name —
// the map's legend, which never yields to an agent label.
// The legend as HubOrbit draws it: one entry per name on screen, with the box it occupies.
const sectorNames = computed(() => level.value >= 2 || !showSectorNames.value
  ? []
  : plan.value.sectors.map((sector) => {
      const [wx, wy] = polar(sectorNameRadius.value, sectorMid(sector))
      const [sx, sy] = toScreen(cam.value, wx, wy)
      return { key: sector.key, box: sectorLabelBox(sx, sy, labelSizes.value.get(sectorLabelKey(sector.label, sector.weight))) }
    }))

// Half a sector name reads as a shorter, wrong one, so the legend yields to the rail as well.
const namedSectors = computed(() => new Set(sectorNames.value.filter(s => !coveredByRail(s.box)).map(s => s.key)))

const labels = computed(() => {
  const sizes = labelSizes.value
  const drawn = placedScreen.value.filter(({ p }) => drawnAgents.value.has(p.agent.pid))
  const candidates = drawn.map(({ p, screen: [sx, sy] }) => ({
    index: p.agent.pid,
    sx,
    sy,
    text: agentLabelKey(p.agent.projectName, p.state),
    priority: agentPriority(p.needsOperator, p.state === 'working'),
  }))
  const dotObstacles = drawn.map(({ p, screen: [sx, sy] }) => ({ box: agentDotBox(sx, sy), ownerIndex: p.agent.pid }))
  const obstacles = [...dotObstacles, ...sectorNames.value]
  const directions = new Map(candidates.map((c, i) => [
    c.index,
    agentLabelDirection(c, sizes.get(c.text), inwardUnit(drawn[i].p.x, drawn[i].p.y), obstacles),
  ]))
  return { directions, kept: cullLabels(candidates, c => agentLabelBox(c, sizes.get(c.text), directions.get(c.index)), obstacles) }
})

const running = computed(() => placed.value.filter(p => p.state === 'working' || p.state === 'active').length)
const waiting = computed(() => placed.value.filter(p => p.needsOperator).length)
const kontorState = computed(() => kontorAgent.value ? agentDisplayStatus(kontorAgent.value) : 'off')
const kontorPage = computed(() => pageWithWidget(layout.value, KONTOR_WIDGET))
// Why Kontor cannot be reached, or null when it can. A 404'd chunk (the server was rebuilt under an
// open tab) renders PageLoadError where the tile is and can take no prompt, so every control that
// would send one says so instead of handing it to no-one.
const kontorBlock = computed<keyof typeof KONTOR_BLOCKED | null>(() => {
  if (failedWidgets.has(KONTOR_WIDGET))
    return 'failed'
  return kontorPage.value ? null : 'absent'
})
const coreTitle = computed(() => kontorBlock.value ? KONTOR_BLOCKED[kontorBlock.value].open : `Open Kontor (${kontorState.value})`)
const askBlocked = computed(() => kontorBlock.value ? KONTOR_BLOCKED[kontorBlock.value].ask : undefined)

const cardAgent = computed(() => {
  const card = openCard.value
  return card?.kind === 'agent' ? live.value.find(a => a.pid === card.pid) ?? null : null
})

// A finished agent or a note gone after a refetch closes its card for good.
watch(() => openCard.value !== null && !cardAgent.value && !cardNote.value, (gone) => {
  if (gone)
    openCard.value = null
})

function openKontor(prefill?: string) {
  if (!kontorPage.value || kontorBlock.value)
    return
  const current = layout.value.pages.find(p => pageView(p.id) === activeView.value)
  if (!current?.tiles.some(t => t.widget === KONTOR_WIDGET))
    activeView.value = pageView(kontorPage.value.id)
  ask(prefill)
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

// Pre-flush: the closing layer is still in the DOM here, so this is the last chance to see whether it held focus.
let closingLayerHadFocus = false
watch([listOpen, openCard], () => {
  closingLayerHadFocus = !!hub.value?.contains(document.activeElement)
}, { flush: 'pre' })

// Post-flush: only reclaim focus for the hub itself, never steal it from elsewhere (sidebar, topbar, Kontor overlay).
watch([listOpen, openCard], () => {
  const active = document.activeElement
  const nothingFocused = active === document.body || !active
  if (closingLayerHadFocus || nothingFocused)
    stage.value?.focus()
}, { flush: 'post' })

function escape() {
  if (openCard.value)
    closeCard()
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

// Shift stays allowed: '+' needs it on most layouts.
function ignored(e: KeyboardEvent): boolean {
  return e.ctrlKey || e.metaKey || e.altKey || isTypingTarget(e.target)
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

// On the hub root so Escape from the list and card reaches it too; the Kontor overlay handles its own Escape via a window listener.
function onEscape(e: KeyboardEvent) {
  if (ignored(e) || overlayOpen.value)
    return
  e.preventDefault()
  escape()
}

function flyToAgent(agent: Agent) {
  showCard({ kind: 'agent', pid: agent.pid })
  const hit = placed.value.find(p => p.agent.pid === agent.pid)
  if (hit)
    flyTo(hit.x, hit.y, AGENT_FLY_REL)
}

function pickFromList(agent: Agent) {
  listOpen.value = false
  flyToAgent(agent)
}

function pickNoteFromList(path: string) {
  listOpen.value = false
  const note = noteByPath(path)
  if (note)
    flyToNote(note.index, LIST_NOTE_FLY_REL)
}

function launchFromList(launcher: Launcher) {
  listOpen.value = false
  launch(launcher)
}

// An unresolved target (deleted note, finished agent) is dropped silently: clearing the request either way stops a stale one from firing on the next mount.
watch(hubFocusRequest, (target) => {
  if (!target)
    return
  if (target.kind === 'note') {
    const note = noteByPath(target.path)
    if (note)
      flyToNote(note.index, LIST_NOTE_FLY_REL)
  }
  else {
    const agent = live.value.find(a => a.pid === target.pid)
    if (agent)
      flyToAgent(agent)
  }
  hubFocusRequest.value = null
}, { immediate: true, flush: 'post' })
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
            v-for="sector in plan.sectors"
            :key="sector.key"
            :d="wedgePath(sector.start, sector.end)"
            fill-opacity="0.035"
            :style="{ fill: `var(--sector-${sectorColour(sector.key)})` }"
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
      <HubBrainCanvas
        :cam="cam"
        :size="size"
        :level="level"
        :points="brain.points"
        :colours="brain.colours"
        :notes="vaultNotes"
        :links="brain.links"
        :edges="edges"
        :hub-notes="brain.hubNotes"
        :selected="cardNote?.index ?? null"
      />
      <HubOrbit
        :cam="cam"
        :sectors="plan.sectors"
        :agents="placed"
        :level="level"
        :running="running"
        :waiting="waiting"
        :needs-you="needsYou.length"
        :core-title="coreTitle"
        :core-disabled="!!kontorBlock"
        :agent-ring-px="ringOnScreenPx"
        :sector-name-radius="sectorNameRadius"
        :show-sector-names="showSectorNames"
        :labelled-agents="labels.kept"
        :label-directions="labels.directions"
        :named-sectors="namedSectors"
        :drawn-agents="drawnAgents"
        @core="openKontor"
        @agent="flyToAgent"
        @sector="sector => flyTo(...polar(SECTOR_FLY_RADIUS, sectorMid(sector)), SECTOR_FLY_REL)"
        @measure="sizes => labelSizes = sizes"
      />
      <HubLaunchers :launchers="launchers" :cam="cam" :k0="k0" :docked="docked" :agent-ring-px="outerRingBasePx" @launch="launch" />
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
    <div class="pointer-events-none absolute left-1/2 top-2.5 z-10 flex max-h-[45%] w-[min(560px,calc(100%-120px))] -translate-x-1/2 flex-col items-start gap-1.5">
      <NeedsYouQueue
        variant="docked"
        data-hub-layer
        class="pointer-events-auto min-h-0 w-full overflow-y-auto rounded-[10px] border bg-card/95 px-2.5 py-2 shadow-lg"
        :class="needsYou.length > 0 ? 'border-warning-line' : 'border-line'"
      />
      <div
        v-if="graphNotice"
        data-hub-layer
        data-testid="hub-graph-notice"
        :title="graphStatus === 'denied' ? graphMessage : undefined"
        class="pointer-events-auto flex shrink-0 flex-col gap-0.5 rounded-lg border border-line bg-card/95 px-2.5 py-1.5 text-[12px] text-fg-mute shadow"
      >
        <p class="flex items-center gap-2">
          {{ graphNotice }}
          <button
            v-if="graphStatus === 'unconfigured'"
            type="button"
            class="cursor-pointer text-accent underline-offset-2 hover:underline"
            @click="openSettings()"
          >
            Open settings
          </button>
        </p>
        <p v-if="graphStatus === 'denied'" data-testid="hub-graph-notice-detail" class="text-[11px] text-fg-faint">
          {{ graphMessage }}
        </p>
      </div>
    </div>
    <HubList
      v-if="listOpen"
      :agents="listAgents"
      :notes="listNotes"
      :graph-status="graphStatus"
      :graph-message="graphMessage"
      :launchers="listLaunchers"
      @agent="pickFromList"
      @note="pickNoteFromList"
      @launch="launchFromList"
      @close="listOpen = false"
    />
    <HubAgentCard v-if="cardAgent" :agent="cardAgent" @close="closeCard" />
    <HubNoteCard
      v-if="cardNote"
      :key="cardNote.path"
      :note="cardNote"
      :notes="vaultNotes"
      :sector-label="sectorLabel(cardNote.path)"
      :kontor-blocked="askBlocked"
      @fly="index => flyToNote(index, Math.max(rel, CHIP_FLY_MIN_REL))"
      @ask="openKontor"
      @close="closeCard"
    />
  </section>
</template>
