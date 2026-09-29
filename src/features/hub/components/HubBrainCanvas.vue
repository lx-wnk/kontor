<script setup lang="ts">
import type { HubNote } from '../composables/useObsidianGraph'
import type { Camera, HubLevel } from '../hubCamera'
import type { LabelCandidate } from '../hubCanvas'
import type { HubEdge } from '../hubEdges'
import type { Leaf, Sector } from '../hubGeometry'
import type { BundleRoute, Point } from '../hubLinks'
import type { NoteTouchKind } from '@/types'
import { useMutationObserver } from '@vueuse/core'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { toScreen } from '../hubCamera'
import { cullLabels, isToday, NOTE_LABEL_OFFSET_PX, noteLabelBox, notePriority } from '../hubCanvas'
import { DAY_MS, leafShade, NOTE_KIND, polar, R0, R_MAX, sectorMid, SESSIONS_KIND, shadeMix } from '../hubGeometry'
import { aggregateLinks, bundlePoints, linksOf, traceBundle } from '../hubLinks'

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
}>()

const NOTE_RADIUS_PX: Record<HubLevel, number> = { 0: 2.1, 1: 3.4, 2: 4.6 }
const LINK_WIDTH_PX = 0.7
const LINK_THIN_WIDTH_PX = 0.4
// Anchor radius for bundled category/leaf arcs (level 0/1): further out than the leaf/category
// anchors in hubLinks so the arcs read as a separate, coarser layer above the note-level bundles.
const LINK_AGG_ANCHOR_R = R0 + 0.55 * (R_MAX - R0)
const LINK_AGG_BASE_PX = 0.8
const LINK_AGG_STEP_PX = 0.25
const LINK_AGG_MAX_PX = 5
const LINK_AGG_ALPHA = 0.6
const LINK_CROSS_ALPHA = 0.6
const LINK_INTRA_ALPHA = 0.18
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

function categoryKeyOf(i: number): string {
  return leafByKey.value.get(props.noteLeaf[i])?.parent ?? ''
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

// Level 0/1: one arc per (category, category) or (leaf, leaf) pair, width growing with the count of
// links it represents; a pair sharing one group is not a link and draws nothing.
function drawAggregatedLinks<T extends Sector>(
  ctx: CanvasRenderingContext2D,
  accent: string,
  groupOf: (i: number) => string,
  anchorOf: (key: string) => T | undefined,
  routeOf: (a: T, b: T) => BundleRoute,
) {
  for (const { a, b, count } of aggregateLinks(props.links, groupOf)) {
    const sa = anchorOf(a)
    const sb = anchorOf(b)
    if (!sa || !sb)
      continue
    const from = polar(LINK_AGG_ANCHOR_R, sectorMid(sa))
    const to = polar(LINK_AGG_ANCHOR_R, sectorMid(sb))
    const width = Math.min(LINK_AGG_MAX_PX, LINK_AGG_BASE_PX + count * LINK_AGG_STEP_PX)
    strokeBundle(ctx, from, to, routeOf(sa, sb), width, LINK_AGG_ALPHA, accent)
  }
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
  if (props.level === 0) {
    drawAggregatedLinks(ctx, accent, i => categoryKeyOf(i), key => sectorByKey.value.get(key), (a, b) => ({ fromLeaf: a, toLeaf: b, fromCategory: a, toCategory: b }))
    return
  }
  if (props.level === 1) {
    drawAggregatedLinks(ctx, accent, i => props.noteLeaf[i] ?? '', key => leafByKey.value.get(key), (a, b) => ({ fromLeaf: a, toLeaf: b, fromCategory: categoryOf(a), toCategory: categoryOf(b) }))
    return
  }
  drawIndividualLinks(ctx, accent, token('--line-strong'), onStage)
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

// A hovered note's linked neighbours join today's touched notes in the halo pass, reusing its style.
function strokeHalos({ ctx, screen, visible, radius, now, token }: Scene) {
  if (props.level === 0)
    return
  const neighbours = props.level === 2 && props.hoveredNote !== null
    ? new Set(linksOf(props.links, props.hoveredNote).flatMap(idx => props.links[idx]))
    : null
  ctx.globalAlpha = HALO_ALPHA
  ctx.strokeStyle = token('--halo')
  ctx.lineWidth = HALO_WIDTH_PX
  ctx.beginPath()
  for (const i of visible) {
    if (isToday(props.notes[i].mtimeMs, now) || neighbours?.has(i))
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
    const key = props.level === 0 ? String(props.colours[i]) : `${props.colours[i]}\u0000${props.noteLeaf[i]}`
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
    ctx.globalAlpha = hub ? 1 : NOTE_ALPHA
    for (const members of groupByColour(filled).values()) {
      ctx.fillStyle = noteColour(members[0], token)
      ctx.beginPath()
      for (const i of members) addCircle(ctx, screen[i], r)
      ctx.fill()
    }
    for (const members of groupByColour(holed).values()) {
      ctx.fillStyle = noteColour(members[0], token)
      ctx.beginPath()
      for (const i of members) addCircle(ctx, screen[i], r)
      ctx.fill()
    }
    if (holed.length > 0) {
      ctx.fillStyle = token('--app')
      ctx.beginPath()
      for (const i of holed) addCircle(ctx, screen[i], r * HOLE_SCALE)
      ctx.fill()
    }
    for (const members of groupByColour(ringed).values()) {
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

// Topics level labels the hub notes only; the notes level labels whatever cullLabels keeps.
function drawLabels({ ctx, screen, visible, now, token }: Scene) {
  if (props.level === 0)
    return
  const candidates = visible
    .filter(i => props.level === 2 || props.hubNotes.has(i))
    .map(i => labelCandidate(i, screen[i], now))
  const kept = props.level === 2 ? cullLabels(candidates, c => noteLabelBox(c, labelWidth(ctx, c.index, c.text))) : null
  ctx.globalAlpha = 1
  ctx.textBaseline = 'middle'
  ctx.lineJoin = 'round'
  ctx.lineWidth = LABEL_STROKE_PX
  ctx.strokeStyle = token('--app')
  ctx.fillStyle = token('--fg-soft')
  for (const c of candidates) {
    if (kept && !kept.has(c.index))
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
