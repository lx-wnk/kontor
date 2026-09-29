<script setup lang="ts">
import type { HubNote } from '../composables/useObsidianGraph'
import type { Camera, HubLevel } from '../hubCamera'
import type { LabelCandidate, LabelObstacle } from '../hubCanvas'
import type { HubEdge } from '../hubEdges'
import type { Leaf, Sector } from '../hubGeometry'
import type { BundleRoute, Point } from '../hubLinks'
import type { NoteTouchKind } from '@/types'
import { useMutationObserver } from '@vueuse/core'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { toScreen } from '../hubCamera'
import { cullLabels, isToday, NOTE_LABEL_OFFSET_PX, noteLabelBox, notePriority } from '../hubCanvas'
import { DAY_MS, leafShade, NOTE_KIND, SESSIONS_KIND, shadeMix } from '../hubGeometry'
import { bundlePoints, isKnowHowLink, linksOf, traceBundle } from '../hubLinks'

const props = defineProps<{
  cam: Camera
  size: { width: number, height: number }
  level: HubLevel
  points: ReadonlyArray<[number, number]>
  colours: ReadonlyArray<number>
  notes: ReadonlyArray<HubNote>
  links: ReadonlyArray<[number, number]>
  hubNotes: ReadonlySet<number>
  selected: number | null
  edges: ReadonlyArray<HubEdge>
  sectors: ReadonlyArray<Sector>
  leaves: ReadonlyArray<Leaf>
  noteLeaf: ReadonlyArray<string>
  hoveredNote: number | null
  // Aggregated category (level 0) or leaf (level 1) links, routed and sampled once in HubWidget so
  // the canvas only maps world→screen and strokes; empty at level 2.
  linkArcs: ReadonlyArray<{ a: string, b: string, count: number, points: readonly Point[], line: readonly Point[] }>
  // The drawn project-name boxes (HubWidget's leafNames, filtered to namedLeaves): the legend a
  // note title must yield to, since the project names are the map's legend.
  legendBoxes?: ReadonlyArray<LabelObstacle>
  // A health lens' note set: members draw at full alpha with a halo, everyone else dims. Null/absent leaves rendering unchanged.
  highlighted?: ReadonlySet<number> | null
}>()

const NOTE_RADIUS_PX: Record<HubLevel, number> = { 0: 2.1, 1: 3.4, 2: 4.6 }
const LINK_WIDTH_PX = 0.7
const LINK_THIN_WIDTH_PX = 0.4
const LINK_AGG_BASE_PX = 0.8
const LINK_AGG_STEP_PX = 0.25
const LINK_AGG_MAX_PX = 5
const LINK_AGG_ALPHA = 0.6
const LINK_CROSS_ALPHA = 0.6
const LINK_INTRA_ALPHA = 0.18
// Level 1 only: individual know-how links within one leaf, visibly lighter than the leaf's own arc.
const LINK_KNOWHOW_ALPHA = 0.35
const LINK_HOVER_ALPHA = 0.95
const LINK_HOVER_WIDTH_SCALE = 2
const LINK_DIM_ALPHA = 0.06
const EDGE_WIDTH_PX = 1.2
const EDGE_DASH: Record<NoteTouchKind, number[]> = { read: [4, 3], write: [1, 3] }
const NOTE_ALPHA = 0.75
const HUB_NOTE_SCALE = 1.9
const HALO_SCALE = 2.6
const HALO_ALPHA = 0.8
const HALO_WIDTH_PX = 1.2
const LENS_DIM_ALPHA = 0.18
const SELECTION_SCALE = 3.2
const SELECTION_WIDTH_PX = 1.5
const VIEWPORT_MARGIN_PX = 20
const LABEL_FONT = '11px system-ui, sans-serif'
const HUB_LABEL_FONT = `600 ${LABEL_FONT}`
const LABEL_STROKE_PX = 3
const FULL_CIRCLE = Math.PI * 2
const HOLE_SCALE = 0.4
const RING_WIDTH_SCALE = 0.45
const RING_WIDTH_MIN_PX = 1

interface Scene {
  ctx: CanvasRenderingContext2D
  screen: Array<[number, number]>
  onStage: boolean[]
  visible: number[]
  radius: number
  now: number
  token: (name: string) => string
}

const canvas = ref<HTMLCanvasElement | null>(null)
let frame: number | null = null

const sectorByKey = computed(() => new Map(props.sectors.map(s => [s.key, s])))
const leafByKey = computed(() => new Map(props.leaves.map(l => [l.key, l])))

// moveTo opens a new sub-path, so circles batched into one path are not joined by lines.
function addCircle(ctx: CanvasRenderingContext2D, [x, y]: [number, number], r: number) {
  ctx.moveTo(x + r, y)
  ctx.arc(x, y, r, 0, FULL_CIRCLE)
}

function categoryOf(leaf: Leaf): Sector {
  return sectorByKey.value.get(leaf.parent) ?? leaf
}

function strokeBundle(ctx: CanvasRenderingContext2D, from: Point, to: Point, route: BundleRoute, width: number, alpha: number, colour: string) {
  ctx.globalAlpha = alpha
  ctx.strokeStyle = colour
  ctx.lineWidth = width
  ctx.beginPath()
  traceBundle(ctx, bundlePoints(from, to, route).map(([x, y]) => toScreen(props.cam, x, y)))
  ctx.stroke()
}

// Level 0/1: one arc per (category, category) or (leaf, leaf) pair, already routed and sampled by
// HubWidget; width grows with the count of links it represents.
function drawArcs(ctx: CanvasRenderingContext2D, accent: string) {
  for (const { count, points } of props.linkArcs) {
    ctx.globalAlpha = LINK_AGG_ALPHA
    ctx.strokeStyle = accent
    ctx.lineWidth = Math.min(LINK_AGG_MAX_PX, LINK_AGG_BASE_PX + count * LINK_AGG_STEP_PX)
    ctx.beginPath()
    traceBundle(ctx, points.map(([x, y]) => toScreen(props.cam, x, y)))
    ctx.stroke()
  }
}

// Level 1 only: individual bundled links within one leaf where at least one end is not a session
// (isKnowHowLink) — session-to-session links inside a leaf stay hidden.
function drawKnowHowLinks(ctx: CanvasRenderingContext2D, token: (name: string) => string) {
  props.links.forEach(([from, to]) => {
    const leafKey = props.noteLeaf[from]
    if (!leafKey || leafKey !== props.noteLeaf[to])
      return
    if (!isKnowHowLink(props.notes[from]?.kind ?? '', props.notes[to]?.kind ?? ''))
      return
    const leaf = leafByKey.value.get(leafKey)
    if (!leaf)
      return
    const route: BundleRoute = { fromLeaf: leaf, toLeaf: leaf, fromCategory: categoryOf(leaf), toCategory: categoryOf(leaf) }
    strokeBundle(ctx, props.points[from], props.points[to], route, LINK_THIN_WIDTH_PX, LINK_KNOWHOW_ALPHA, noteColour(from, token))
  })
}

// Level 2: every link on its own, faint within a leaf and accented where it crosses one; a hovered
// note's own links stand out brighter and wider, every other link fades.
function drawIndividualLinks(ctx: CanvasRenderingContext2D, accent: string, lineStrong: string, onStage: boolean[]) {
  const hovered = props.hoveredNote
  const hoveredLinks = hovered === null ? null : new Set(linksOf(props.links, hovered))
  props.links.forEach(([from, to], idx) => {
    if (!onStage[from] && !onStage[to])
      return
    const fromLeaf = leafByKey.value.get(props.noteLeaf[from])
    const toLeaf = leafByKey.value.get(props.noteLeaf[to])
    if (!fromLeaf || !toLeaf)
      return
    const crosses = fromLeaf.key !== toLeaf.key
    const isHovered = hoveredLinks?.has(idx) ?? false
    const dimmed = hoveredLinks !== null && !isHovered
    const alpha = isHovered ? LINK_HOVER_ALPHA : dimmed ? LINK_DIM_ALPHA : crosses ? LINK_CROSS_ALPHA : LINK_INTRA_ALPHA
    const width = (crosses ? LINK_WIDTH_PX : LINK_THIN_WIDTH_PX) * (isHovered ? LINK_HOVER_WIDTH_SCALE : 1)
    const colour = isHovered || crosses ? accent : lineStrong
    const route: BundleRoute = { fromLeaf, toLeaf, fromCategory: categoryOf(fromLeaf), toCategory: categoryOf(toLeaf) }
    strokeBundle(ctx, props.points[from], props.points[to], route, width, alpha, colour)
  })
}

function drawLinks({ ctx, onStage, token }: Scene) {
  const accent = token('--accent')
  if (props.level === 2) {
    drawIndividualLinks(ctx, accent, token('--line-strong'), onStage)
    return
  }
  drawArcs(ctx, accent)
  if (props.level === 1)
    drawKnowHowLinks(ctx, token)
}

function drawEdges({ ctx, screen, token }: Scene) {
  if (props.edges.length === 0)
    return
  ctx.strokeStyle = token('--accent')
  ctx.lineWidth = EDGE_WIDTH_PX
  for (const edge of props.edges) {
    ctx.globalAlpha = edge.alpha
    ctx.setLineDash(EDGE_DASH[edge.kind])
    ctx.lineCap = edge.kind === 'write' ? 'round' : 'butt'
    ctx.beginPath()
    ctx.moveTo(...toScreen(props.cam, ...edge.from))
    ctx.lineTo(...screen[edge.to])
    ctx.stroke()
  }
  ctx.setLineDash([])
  ctx.lineCap = 'butt'
}

// A hovered note's linked neighbours and a health lens' members join today's touched notes in the
// halo pass, reusing its style. A lens keeps the halo drawing at level 0 too, where it is otherwise skipped.
function strokeHalos({ ctx, screen, visible, radius, now, token }: Scene) {
  const highlight = props.highlighted
  if (props.level === 0 && !highlight)
    return
  const neighbours = props.level === 2 && props.hoveredNote !== null
    ? new Set(linksOf(props.links, props.hoveredNote).flatMap(idx => props.links[idx]))
    : null
  ctx.globalAlpha = HALO_ALPHA
  ctx.strokeStyle = token('--halo')
  ctx.lineWidth = HALO_WIDTH_PX
  ctx.beginPath()
  for (const i of visible) {
    if (isToday(props.notes[i].mtimeMs, now) || neighbours?.has(i) || highlight?.has(i))
      addCircle(ctx, screen[i], radius * HALO_SCALE)
  }
  ctx.stroke()
}

// Plain category colour at the overview; shaded by leaf from the topics level up, same as the wedges.
function noteColour(i: number, token: (name: string) => string): string {
  const base = token(`--sector-${props.colours[i]}`)
  return props.level === 0 ? base : shadeMix(base, leafShade(props.noteLeaf[i]))
}

function groupByColour(items: number[]): Map<string, number[]> {
  const groups = new Map<string, number[]>()
  for (const i of items) {
    const shade = props.level === 0 ? '' : props.noteLeaf[i]
    const hl = props.highlighted ? String(props.highlighted.has(i)) : ''
    const key = `${props.colours[i]}\u0000${shade}\u0000${hl}`
    const members = groups.get(key)
    if (members)
      members.push(i)
    else groups.set(key, [i])
  }
  return groups
}

// One path per (kind, colour): a note-kind dot is filled, a sessions-kind dot is a ring, any other
// kind is filled with a hole punched in the app background — plain notes first so hub notes sit on top.
function fillNotes({ ctx, screen, visible, radius, token }: Scene) {
  for (const hub of [false, true]) {
    const filled: number[] = []
    const holed: number[] = []
    const ringed: number[] = []
    for (const i of visible) {
      if (props.hubNotes.has(i) !== hub)
        continue
      const kind = props.notes[i].kind
      if (kind === SESSIONS_KIND)
        ringed.push(i)
      else if (kind === NOTE_KIND)
        filled.push(i)
      else
        holed.push(i)
    }
    const r = hub ? radius * HUB_NOTE_SCALE : radius
    const baseAlpha = hub ? 1 : NOTE_ALPHA
    const alphaOf = (i: number) => props.highlighted ? (props.highlighted.has(i) ? 1 : LENS_DIM_ALPHA) : baseAlpha
    for (const members of groupByColour(filled).values()) {
      ctx.globalAlpha = alphaOf(members[0])
      ctx.fillStyle = noteColour(members[0], token)
      ctx.beginPath()
      for (const i of members) addCircle(ctx, screen[i], r)
      ctx.fill()
    }
    for (const members of groupByColour(holed).values()) {
      ctx.globalAlpha = alphaOf(members[0])
      ctx.fillStyle = noteColour(members[0], token)
      ctx.beginPath()
      for (const i of members) addCircle(ctx, screen[i], r)
      ctx.fill()
    }
    for (const members of groupByColour(holed).values()) {
      ctx.globalAlpha = alphaOf(members[0])
      ctx.fillStyle = token('--app')
      ctx.beginPath()
      for (const i of members) addCircle(ctx, screen[i], r * HOLE_SCALE)
      ctx.fill()
    }
    for (const members of groupByColour(ringed).values()) {
      ctx.globalAlpha = alphaOf(members[0])
      ctx.strokeStyle = noteColour(members[0], token)
      ctx.lineWidth = Math.max(RING_WIDTH_MIN_PX, r * RING_WIDTH_SCALE)
      ctx.beginPath()
      for (const i of members) addCircle(ctx, screen[i], r)
      ctx.stroke()
    }
  }
}

function labelCandidate(i: number, [sx, sy]: [number, number], now: number): LabelCandidate {
  const note = props.notes[i]
  return {
    index: i,
    sx,
    sy,
    text: note.title,
    priority: notePriority({
      hub: props.hubNotes.has(i),
      touched: isToday(note.mtimeMs, now),
      fresh: now - note.mtimeMs < DAY_MS,
      linkCount: note.links.length + note.backlinks.length,
    }),
  }
}

function fontFor(index: number): string {
  return props.hubNotes.has(index) ? HUB_LABEL_FONT : LABEL_FONT
}

// A width depends on the text and the font only, and the notes level can offer over a thousand
// candidates per frame: each pair is measured once and reused for as long as the canvas lives.
const labelWidths = new Map<string, number>()

function labelWidth(ctx: CanvasRenderingContext2D, index: number, text: string): number {
  const font = fontFor(index)
  const key = `${font}\n${text}`
  const known = labelWidths.get(key)
  if (known !== undefined)
    return known
  ctx.font = font
  const { width } = ctx.measureText(text)
  labelWidths.set(key, width)
  return width
}

// Topics level labels the hub notes only, yielding to the project-name legend; the notes level
// labels whatever cullLabels keeps (legendBoxes is empty there — leaf names are hidden at level 2).
function drawLabels({ ctx, screen, visible, now, token }: Scene) {
  if (props.level === 0)
    return
  const candidates = visible
    .filter(i => props.level === 2 || props.hubNotes.has(i))
    .map(i => labelCandidate(i, screen[i], now))
  const kept = cullLabels(candidates, c => noteLabelBox(c, labelWidth(ctx, c.index, c.text)), props.legendBoxes ?? [])
  ctx.globalAlpha = 1
  ctx.textBaseline = 'middle'
  ctx.lineJoin = 'round'
  ctx.lineWidth = LABEL_STROKE_PX
  ctx.strokeStyle = token('--app')
  ctx.fillStyle = token('--fg-soft')
  for (const c of candidates) {
    if (!kept.has(c.index))
      continue
    ctx.font = fontFor(c.index)
    ctx.strokeText(c.text, c.sx + NOTE_LABEL_OFFSET_PX, c.sy)
    ctx.fillText(c.text, c.sx + NOTE_LABEL_OFFSET_PX, c.sy)
  }
}

function drawSelection({ ctx, screen, onStage, radius, token }: Scene) {
  const i = props.selected
  if (i === null || !onStage[i])
    return
  ctx.globalAlpha = 1
  ctx.strokeStyle = token('--fg')
  ctx.lineWidth = SELECTION_WIDTH_PX
  ctx.beginPath()
  addCircle(ctx, screen[i], radius * SELECTION_SCALE)
  ctx.stroke()
}

function draw() {
  frame = null
  const el = canvas.value
  if (!el)
    return
  const ctx = el.getContext('2d')
  if (!ctx)
    throw new Error('HubBrainCanvas needs a 2D canvas context')
  const { width, height } = props.size
  const dpr = window.devicePixelRatio || 1
  const bufferWidth = Math.round(width * dpr)
  const bufferHeight = Math.round(height * dpr)
  // Assigning either dimension reallocates and clears the backing store, even to the same value.
  if (el.width !== bufferWidth || el.height !== bufferHeight) {
    el.width = bufferWidth
    el.height = bufferHeight
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, width, height)
  const style = getComputedStyle(el)
  const screen = props.points.map(([x, y]) => toScreen(props.cam, x, y))
  const onStage = screen.map(([sx, sy]) =>
    sx >= -VIEWPORT_MARGIN_PX && sx <= width + VIEWPORT_MARGIN_PX && sy >= -VIEWPORT_MARGIN_PX && sy <= height + VIEWPORT_MARGIN_PX)
  const scene: Scene = {
    ctx,
    screen,
    onStage,
    visible: onStage.flatMap((on, i) => on ? [i] : []),
    radius: NOTE_RADIUS_PX[props.level],
    now: Date.now(),
    token: name => style.getPropertyValue(name).trim(),
  }
  drawLinks(scene)
  drawEdges(scene)
  strokeHalos(scene)
  fillNotes(scene)
  drawLabels(scene)
  drawSelection(scene)
}

function schedule() {
  frame ??= requestAnimationFrame(draw)
}

watch(() => Object.values(props), schedule)
// Colours are read from CSS tokens at draw time, so a theme switch (class on <html>) needs a redraw.
useMutationObserver(document.documentElement, schedule, { attributeFilter: ['class'] })
onMounted(schedule)
onUnmounted(() => {
  if (frame !== null)
    cancelAnimationFrame(frame)
})
</script>

<template>
  <canvas
    ref="canvas"
    aria-hidden="true"
    class="pointer-events-none absolute inset-0"
    :style="{ width: `${size.width}px`, height: `${size.height}px` }"
  />
</template>
