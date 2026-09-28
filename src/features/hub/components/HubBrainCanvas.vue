<script setup lang="ts">
import type { HubNote } from '../composables/useObsidianGraph'
import type { Camera, HubLevel } from '../hubCamera'
import type { LabelCandidate } from '../hubCanvas'
import type { HubEdge } from '../hubEdges'
import type { NoteTouchKind } from '@/types'
import { useMutationObserver } from '@vueuse/core'
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { toScreen } from '../hubCamera'
import { cullLabels, isToday, NOTE_LABEL_OFFSET_PX, noteLabelBox, notePriority } from '../hubCanvas'
import { DAY_MS } from '../hubGeometry'

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
}>()

const NOTE_RADIUS_PX: Record<HubLevel, number> = { 0: 2.1, 1: 3.4, 2: 4.6 }
const LINK_ALPHA: Record<HubLevel, number> = { 0: 0.28, 1: 0.55, 2: 0.8 }
const LINK_WIDTH_PX = 0.7
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

// moveTo opens a new sub-path, so circles batched into one path are not joined by lines.
function addCircle(ctx: CanvasRenderingContext2D, [x, y]: [number, number], r: number) {
  ctx.moveTo(x + r, y)
  ctx.arc(x, y, r, 0, FULL_CIRCLE)
}

function drawLinks({ ctx, screen, onStage, token }: Scene) {
  ctx.globalAlpha = LINK_ALPHA[props.level]
  ctx.strokeStyle = token('--line-strong')
  ctx.lineWidth = LINK_WIDTH_PX
  ctx.beginPath()
  for (const [from, to] of props.links) {
    if (!onStage[from] && !onStage[to])
      continue
    ctx.moveTo(...screen[from])
    ctx.lineTo(...screen[to])
  }
  ctx.stroke()
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

function strokeHalos({ ctx, screen, visible, radius, now, token }: Scene) {
  if (props.level === 0)
    return
  ctx.globalAlpha = HALO_ALPHA
  ctx.strokeStyle = token('--halo')
  ctx.lineWidth = HALO_WIDTH_PX
  ctx.beginPath()
  for (const i of visible) {
    if (isToday(props.notes[i].mtimeMs, now))
      addCircle(ctx, screen[i], radius * HALO_SCALE)
  }
  ctx.stroke()
}

// One path per (kind, colour); plain notes first so hub notes sit on top.
function fillNotes({ ctx, screen, visible, radius, token }: Scene) {
  for (const hub of [false, true]) {
    const byColour = new Map<number, number[]>()
    for (const i of visible) {
      if (props.hubNotes.has(i) !== hub)
        continue
      const members = byColour.get(props.colours[i])
      if (members)
        members.push(i)
      else
        byColour.set(props.colours[i], [i])
    }
    ctx.globalAlpha = hub ? 1 : NOTE_ALPHA
    for (const [colour, members] of byColour) {
      ctx.fillStyle = token(`--sector-${colour}`)
      ctx.beginPath()
      for (const i of members)
        addCircle(ctx, screen[i], hub ? radius * HUB_NOTE_SCALE : radius)
      ctx.fill()
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
