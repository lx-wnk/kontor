<script setup lang="ts">
import type { OpResult, PlacedTile, WorkspacePage } from '../layout'
import type { WidgetDef } from '../widgetRegistry'
import { useEventListener, useResizeObserver } from '@vueuse/core'
import { computed, ref } from 'vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { cellAt } from '../gridGeometry'
import { fitsMinimum, moveTile, readingOrder, removeTile, resizeTile, rowsUsed, swapTile, validatePlacement } from '../layout'
import { widgetIds, WIDGETS } from '../widgetRegistry'
import { isWidgetId } from '../widgetSpecs'

const props = defineProps<{ page: WorkspacePage, editing: boolean }>()
const emit = defineEmits<{ change: [page: WorkspacePage], refuse: [reason: string] }>()

// DOM order is reading order — what the single-column layout below md shows and what a screen reader announces; from md up tiles are placed explicitly, so it stops mattering.
const ordered = computed(() => readingOrder(props.page.tiles).map(o => ({ ...o, widget: widgetOf(o.tile) })))
const rows = computed(() => rowsUsed(props.page.tiles))

const GAP = 12 // matches .workspace-grid gap
const gridEl = ref<HTMLElement | null>(null)
const drag = ref<null | { index: number, mode: 'move' | 'resize', grabCol: number, grabRow: number, target: PlacedTile }>(null)
let gridRect: DOMRect

function measureGrid() {
  gridRect = gridEl.value!.getBoundingClientRect()
}

// The rect is viewport-relative, so a resize (window or sidebar) and a scroll both invalidate it; re-measuring per pointermove would force a layout on every frame of the drag.
useResizeObserver(gridEl, () => {
  if (drag.value)
    measureGrid()
})
useEventListener(window, 'scroll', () => {
  if (drag.value)
    measureGrid()
}, { capture: true, passive: true })

function cellOf(e: PointerEvent) {
  return cellAt(gridRect, e.clientX, e.clientY, rows.value, GAP)
}

function startDrag(e: PointerEvent, index: number, mode: 'move' | 'resize') {
  if (!props.editing || e.button !== 0 || (e.target as HTMLElement).closest('input, button:not([data-resize])'))
    return
  const t = props.page.tiles[index]
  measureGrid()
  const c = cellOf(e)
  drag.value = { index, mode, grabCol: c.col - t.col, grabRow: c.row - t.row, target: { ...t } }
  ;(e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId)
  e.stopPropagation()
}

function moveDrag(e: PointerEvent) {
  const d = drag.value
  if (!d)
    return
  const t = props.page.tiles[d.index]
  const c = cellOf(e)
  d.target = d.mode === 'move'
    ? { ...t, col: Math.max(1, Math.min(13 - t.colSpan, c.col - d.grabCol)), row: Math.max(1, c.row - d.grabRow) }
    : { ...t, colSpan: Math.max(1, c.col - t.col + 1), rowSpan: Math.max(1, c.row - t.row + 1) }
}

function endDrag() {
  const d = drag.value
  drag.value = null
  if (!d)
    return
  const t = props.page.tiles[d.index]
  if (d.target.col === t.col && d.target.row === t.row && d.target.colSpan === t.colSpan && d.target.rowSpan === t.rowSpan)
    return
  apply(d.mode === 'move'
    ? moveTile(props.page, d.index, d.target.col, d.target.row)
    : resizeTile(props.page, d.index, d.target.colSpan, d.target.rowSpan))
}

const ghostInvalid = computed(() => {
  const d = drag.value
  if (!d)
    return false
  return (validatePlacement(props.page.tiles, d.target, d.index) ?? fitsMinimum(d.target.widget, d.target.colSpan, d.target.rowSpan)) !== null
})

function placement(t: PlacedTile): Record<string, number> {
  return { '--col': t.col, '--col-span': t.colSpan, '--row': t.row, '--row-span': t.rowSpan }
}

function widgetOf(tile: PlacedTile): WidgetDef | undefined {
  return isWidgetId(tile.widget) ? WIDGETS[tile.widget] : undefined
}

function widgetTitle(tile: PlacedTile): string {
  return widgetOf(tile)?.title ?? tile.widget
}

function tileAriaLabel(tile: PlacedTile): string {
  return `${widgetTitle(tile)}, column ${tile.col}, row ${tile.row}, ${tile.colSpan} by ${tile.rowSpan}`
}

function apply(r: OpResult<WorkspacePage>) {
  if (r.ok)
    emit('change', r.value)
  else
    emit('refuse', r.reason)
}

const MOVES: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }

function onKey(e: KeyboardEvent, index: number) {
  if (!props.editing)
    return
  // Shift+arrows is the one exemption from the resize handle otherwise leaving keys to the tile it sits on (mirrors startDrag's own carve-out).
  const onResizeArrow = e.key in MOVES && e.shiftKey && (e.target as HTMLElement).hasAttribute('data-resize')
  if (e.target !== e.currentTarget && !onResizeArrow)
    return
  const t = props.page.tiles[index]
  if (e.key === 'Delete' || e.key === 'Backspace') {
    e.preventDefault()
    emit('change', removeTile(props.page, index))
    return
  }
  const d = MOVES[e.key]
  if (!d)
    return
  e.preventDefault()
  apply(e.shiftKey
    ? resizeTile(props.page, index, t.colSpan + d[0], t.rowSpan + d[1])
    : moveTile(props.page, index, t.col + d[0], t.row + d[1]))
}

// Swap candidates: everything not already on the page; ones whose minimum doesn't fit this tile are listed disabled with the reason.
function swapOptions(index: number) {
  const t = props.page.tiles[index]
  const placed = new Set(props.page.tiles.map(p => p.widget))
  return [
    { value: '', label: '⇄ Swap…', disabled: true },
    ...widgetIds().filter(id => !placed.has(id)).map((id) => {
      const reason = fitsMinimum(id, t.colSpan, t.rowSpan)
      return { value: id, label: reason ? `${WIDGETS[id].title} — ${reason}` : WIDGETS[id].title, disabled: !!reason }
    }),
  ]
}
</script>

<template>
  <div ref="gridEl" data-testid="workspace-grid" class="workspace-grid" :style="{ '--rows': rows }">
    <div
      v-for="{ tile, index, widget } in ordered"
      :key="tile.widget"
      :data-testid="`workspace-tile-${tile.widget}`"
      class="workspace-tile"
      :class="{ 'workspace-tile--editing': editing }"
      :style="placement(tile)"
      :tabindex="editing ? 0 : undefined"
      :aria-label="editing ? tileAriaLabel(tile) : undefined"
      @keydown="onKey($event, index)"
      @pointerdown="startDrag($event, index, 'move')"
      @pointermove="moveDrag"
      @pointerup="endDrag"
      @pointercancel="drag = null"
    >
      <div v-if="editing" class="workspace-chrome">
        <AppSelect
          model-value=""
          :options="swapOptions(index)"
          :data-testid="`workspace-swap-${tile.widget}`"
          :aria-label="`Swap ${widgetTitle(tile)} for`"
          size="compact"
          @update:model-value="apply(swapTile(page, index, $event))"
        />
        <button
          type="button"
          :data-testid="`workspace-remove-${tile.widget}`"
          :aria-label="`Remove ${widgetTitle(tile)}`"
          class="rounded-md border border-line-strong bg-card px-2 text-[12px]"
          @click="emit('change', removeTile(page, index))"
        >
          ✕
        </button>
      </div>
      <component :is="widget?.component" v-if="widget" />
      <div v-else data-testid="workspace-unknown" class="h-full rounded-xl border border-dashed border-line p-4 text-[12px] text-fg-mute">
        {{ tile.widget }} is not available — the module that provides it may be inactive.
      </div>
      <button
        v-if="editing"
        type="button"
        data-resize
        :data-testid="`workspace-resize-${tile.widget}`"
        :aria-label="`Resize ${widgetTitle(tile)} (Shift+arrows)`"
        class="workspace-resize"
        @pointerdown="startDrag($event, index, 'resize')"
      />
    </div>
    <div
      v-if="drag"
      data-testid="workspace-ghost"
      class="workspace-ghost"
      :class="{ 'workspace-ghost--invalid': ghostInvalid }"
      :style="placement(drag.target)"
    />
  </div>
</template>

<style scoped>
.workspace-grid { display: grid; gap: 12px; grid-template-columns: minmax(0, 1fr); }
.workspace-tile { position: relative; min-width: 0; }
.workspace-tile > :deep(:not(.workspace-chrome):not(.workspace-resize)) { height: 100%; }
.workspace-tile--editing { outline: 1px dashed var(--color-line-strong); outline-offset: 2px; border-radius: 12px; cursor: grab; }
.workspace-tile--editing:focus-visible { outline: 2px solid var(--color-accent); }
.workspace-tile--editing > :deep(:not(.workspace-chrome):not(.workspace-resize)) { pointer-events: none; }
.workspace-chrome { position: absolute; top: 6px; right: 6px; z-index: 2; display: flex; gap: 4px; }
.workspace-resize { position: absolute; right: 2px; bottom: 2px; z-index: 2; width: 14px; height: 14px; cursor: nwse-resize; border-right: 2px solid var(--color-accent); border-bottom: 2px solid var(--color-accent); border-radius: 0 0 8px 0; }
.workspace-ghost { pointer-events: none; border: 2px dashed var(--color-accent); border-radius: 12px; background: color-mix(in oklch, var(--color-accent) 10%, transparent); }
.workspace-ghost--invalid { border-color: var(--color-danger); background: color-mix(in oklch, var(--color-danger) 10%, transparent); }
@media (min-width: 768px) {
  .workspace-grid {
    height: 100%;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    grid-template-rows: repeat(var(--rows), minmax(56px, 1fr));
  }
  .workspace-tile {
    min-height: 0;
    grid-column: var(--col) / span var(--col-span);
    grid-row: var(--row) / span var(--row-span);
  }
  .workspace-ghost {
    grid-column: var(--col) / span var(--col-span);
    grid-row: var(--row) / span var(--row-span);
  }
}
</style>
