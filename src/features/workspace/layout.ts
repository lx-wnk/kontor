import type { WidgetSpec } from './widgetSpecs'
import { isWidgetId, WIDGET_SPECS } from './widgetSpecs'

// Tiles are persisted as plain strings, so every widget lookup here is guarded (see validateLayout below).
function specOf(widget: string): WidgetSpec | undefined {
  return isWidgetId(widget) ? WIDGET_SPECS[widget] : undefined
}

export interface PlacedTile { widget: string, col: number, row: number, colSpan: number, rowSpan: number }
export interface WorkspacePage { id: string, title: string, tiles: PlacedTile[] }
export interface WorkspaceLayout { version: 1, pages: WorkspacePage[] }
export type OpResult<T> = { ok: true, value: T } | { ok: false, reason: string }

// These rules are mirrored in server/internal/settings/workspace_layout.go.
// Change both together: the settings API accepts a PATCH from any loopback
// caller, so a rule only the browser applies is not a rule.
export const GRID_COLUMNS = 12
export const ZENTRALE_PAGE_ID = 'zentrale'
export const PAGE_ID_PATTERN = /^[a-z0-9-]{1,40}$/
export const WIDGET_ID_PATTERN = /^[a-z0-9_-]{1,64}$/
const MAX_PAGES = 50
const MAX_TILES = 100
const MAX_ROW = 500
const MAX_TITLE = 80

function t(widget: string, col: number, row: number, colSpan: number, rowSpan: number): PlacedTile {
  return { widget, col, row, colSpan, rowSpan }
}

function deepFreeze<T>(value: T): T {
  if (Array.isArray(value))
    value.forEach(deepFreeze)
  else if (value !== null && typeof value === 'object')
    Object.values(value as object).forEach(deepFreeze)
  Object.freeze(value)
  return value
}

// Default Zentrale, 12 × 12 — frozen so no caller can mutate the shared default in place.
export const DEFAULT_LAYOUT: WorkspaceLayout = deepFreeze({
  version: 1,
  pages: [{
    id: ZENTRALE_PAGE_ID,
    title: 'Zentrale',
    tiles: [
      t('live-work', 1, 1, 3, 5),
      t('agents', 1, 6, 3, 3),
      t('routines', 1, 9, 3, 4),
      t('hub', 4, 1, 6, 11),
      t('kontor', 4, 12, 6, 1),
      t('github', 10, 1, 3, 3),
      t('pipeline', 10, 4, 3, 3),
      t('memory', 10, 7, 3, 3),
      t('cost-today', 10, 10, 3, 3),
    ],
  }],
})

const ok = <T>(value: T): OpResult<T> => ({ ok: true, value })
const fail = <T>(reason: string): OpResult<T> => ({ ok: false, reason })

function overlaps(a: PlacedTile, b: PlacedTile): boolean {
  return a.col < b.col + b.colSpan && b.col < a.col + a.colSpan
    && a.row < b.row + b.rowSpan && b.row < a.row + a.rowSpan
}

export function validatePlacement(tiles: PlacedTile[], c: PlacedTile, ignoreIndex?: number): string | null {
  if (![c.col, c.row, c.colSpan, c.rowSpan].every(Number.isInteger))
    return 'Positions and spans must be whole numbers.'
  if (c.colSpan < 1 || c.rowSpan < 1)
    return 'A tile must span at least one column and one row.'
  if (c.col < 1 || c.col + c.colSpan - 1 > GRID_COLUMNS)
    return `A tile must stay within ${GRID_COLUMNS} columns.`
  if (c.row < 1 || c.row + c.rowSpan - 1 > MAX_ROW)
    return `A tile must start at row 1 or below and end by row ${MAX_ROW}.`
  const other = tiles.findIndex((o, i) => i !== ignoreIndex && overlaps(o, c))
  if (other !== -1)
    return `That spot would overlap ${specOf(tiles[other].widget)?.title ?? tiles[other].widget}.`
  return null
}

export function fitsMinimum(widget: string, colSpan: number, rowSpan: number): string | null {
  const s = specOf(widget)
  if (!s || (colSpan >= s.minColSpan && rowSpan >= s.minRowSpan))
    return null
  return `${s.title} needs at least ${s.minColSpan} × ${s.minRowSpan}.`
}

function replaceTile(page: WorkspacePage, index: number, next: PlacedTile): OpResult<WorkspacePage> {
  const reason = validatePlacement(page.tiles, next, index) ?? fitsMinimum(next.widget, next.colSpan, next.rowSpan)
  if (reason)
    return fail(reason)
  return ok({ ...page, tiles: page.tiles.map((tile, i) => (i === index ? next : tile)) })
}

export function moveTile(page: WorkspacePage, index: number, col: number, row: number): OpResult<WorkspacePage> {
  return replaceTile(page, index, { ...page.tiles[index], col, row })
}

export function resizeTile(page: WorkspacePage, index: number, colSpan: number, rowSpan: number): OpResult<WorkspacePage> {
  return replaceTile(page, index, { ...page.tiles[index], colSpan, rowSpan })
}

export function swapTile(page: WorkspacePage, index: number, widget: string): OpResult<WorkspacePage> {
  if (page.tiles.some((tile, i) => i !== index && tile.widget === widget))
    return fail(`${specOf(widget)?.title ?? widget} is already on this page.`)
  return replaceTile(page, index, { ...page.tiles[index], widget })
}

export function removeTile(page: WorkspacePage, index: number): WorkspacePage {
  if (index < 0 || index >= page.tiles.length)
    throw new RangeError(`no tile at index ${index}`)
  return { ...page, tiles: page.tiles.filter((_, i) => i !== index) }
}

export function rowsUsed(tiles: PlacedTile[]): number {
  return Math.max(1, ...tiles.map(tile => tile.row + tile.rowSpan - 1))
}

export function firstFreeSpot(tiles: PlacedTile[], colSpan: number, rowSpan: number): { col: number, row: number } {
  // The row after the last used one is always free, so this terminates.
  for (let row = 1; row <= rowsUsed(tiles) + 1; row++) {
    for (let col = 1; col + colSpan - 1 <= GRID_COLUMNS; col++) {
      if (!validatePlacement(tiles, { widget: '', col, row, colSpan, rowSpan }))
        return { col, row }
    }
  }
  return { col: 1, row: rowsUsed(tiles) + 1 }
}

export function addTile(page: WorkspacePage, widget: string): OpResult<WorkspacePage> {
  const s = specOf(widget)
  if (!s)
    return fail(`Unknown widget ${widget}.`)
  if (page.tiles.some(tile => tile.widget === widget))
    return fail(`${s.title} is already on this page.`)
  if (page.tiles.length >= MAX_TILES)
    return fail(`At most ${MAX_TILES} tiles on a page.`)
  const spot = firstFreeSpot(page.tiles, s.defaultColSpan, s.defaultRowSpan)
  const candidate: PlacedTile = { widget, ...spot, colSpan: s.defaultColSpan, rowSpan: s.defaultRowSpan }
  const reason = validatePlacement(page.tiles, candidate)
  if (reason)
    return fail(reason)
  return ok({ ...page, tiles: [...page.tiles, candidate] })
}

export function readingOrder(tiles: PlacedTile[]): Array<{ tile: PlacedTile, index: number }> {
  return tiles.map((tile, index) => ({ tile, index })).sort((a, b) => a.tile.row - b.tile.row || a.tile.col - b.tile.col)
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

export function validateLayout(value: unknown): string | null {
  if (!isRecord(value) || value.version !== 1 || !Array.isArray(value.pages))
    return 'Not a version 1 layout.'
  if (value.pages.length === 0 || value.pages.length > MAX_PAGES)
    return `A layout holds between 1 and ${MAX_PAGES} pages.`
  const seen = new Set<string>()
  for (const p of value.pages) {
    if (!isRecord(p) || typeof p.id !== 'string' || !PAGE_ID_PATTERN.test(p.id))
      return 'A page id must be lowercase letters, digits or dashes.'
    if (seen.has(p.id))
      return `Page ${p.id} appears twice.`
    seen.add(p.id)
    const title = typeof p.title === 'string' ? p.title.trim() : ''
    const titleLength = [...title].length
    if (titleLength === 0 || titleLength > MAX_TITLE)
      return `Page ${p.id} needs a title of 1 to ${MAX_TITLE} characters.`
    if (!Array.isArray(p.tiles) || p.tiles.length > MAX_TILES)
      return `Page ${p.id} holds at most ${MAX_TILES} tiles.`
    const placed: PlacedTile[] = []
    for (const tile of p.tiles) {
      if (!isRecord(tile) || typeof tile.widget !== 'string' || !WIDGET_ID_PATTERN.test(tile.widget))
        return `Page ${p.id} has a tile without a valid widget id.`
      if (placed.some(o => o.widget === tile.widget))
        return `Page ${p.id} places ${tile.widget} twice.`
      const reason = validatePlacement(placed, tile as unknown as PlacedTile)
      if (reason)
        return `Page ${p.id}: ${reason}`
      placed.push(tile as unknown as PlacedTile)
    }
  }
  if (!seen.has(ZENTRALE_PAGE_ID))
    return 'The Zentrale page is missing.'
  return null
}

export function parseLayout(raw: string): { layout: WorkspaceLayout, unreadable: boolean } {
  if (raw.trim() === '')
    return { layout: DEFAULT_LAYOUT, unreadable: false }
  try {
    const value: unknown = JSON.parse(raw)
    if (validateLayout(value) === null)
      return { layout: value as WorkspaceLayout, unreadable: false }
  }
  catch {
    // Malformed JSON falls through to the unreadable return below.
  }
  return { layout: DEFAULT_LAYOUT, unreadable: true }
}

export function serializeLayout(layout: WorkspaceLayout): string {
  return JSON.stringify(layout)
}

export function replacePage(layout: WorkspaceLayout, page: WorkspacePage): WorkspaceLayout {
  return { ...layout, pages: layout.pages.map(p => (p.id === page.id ? page : p)) }
}

function cleanTitle(title: string): OpResult<string> {
  const trimmed = title.trim()
  const length = [...trimmed].length
  return length === 0 || length > MAX_TITLE
    ? fail(`A page title has 1 to ${MAX_TITLE} characters.`)
    : ok(trimmed)
}

export function addPage(layout: WorkspaceLayout, title: string, newId = `p-${Date.now().toString(36)}`): OpResult<{ layout: WorkspaceLayout, pageId: string }> {
  const cleaned = cleanTitle(title)
  if (!cleaned.ok)
    return cleaned
  if (layout.pages.length >= MAX_PAGES)
    return fail(`At most ${MAX_PAGES} pages.`)
  return ok({ layout: { ...layout, pages: [...layout.pages, { id: newId, title: cleaned.value, tiles: [] }] }, pageId: newId })
}

export function renamePage(layout: WorkspaceLayout, id: string, title: string): OpResult<WorkspaceLayout> {
  const cleaned = cleanTitle(title)
  if (!cleaned.ok)
    return cleaned
  return ok({ ...layout, pages: layout.pages.map(p => (p.id === id ? { ...p, title: cleaned.value } : p)) })
}

export function removePage(layout: WorkspaceLayout, id: string): OpResult<WorkspaceLayout> {
  if (id === ZENTRALE_PAGE_ID)
    return fail('The Zentrale cannot be removed.')
  return ok({ ...layout, pages: layout.pages.filter(p => p.id !== id) })
}

// The Zentrale wins ties: a widget also placed on an own page still hands off there.
export function pageWithWidget(layout: WorkspaceLayout, widget: string): WorkspacePage | null {
  const zentrale = layout.pages.find(p => p.id === ZENTRALE_PAGE_ID)
  if (zentrale?.tiles.some(t => t.widget === widget))
    return zentrale
  return layout.pages.find(p => p.id !== ZENTRALE_PAGE_ID && p.tiles.some(t => t.widget === widget)) ?? null
}

function rowsOverlap(a: PlacedTile, b: PlacedTile): boolean {
  return a.row <= b.row + b.rowSpan - 1 && b.row <= a.row + a.rowSpan - 1
}

// A wide widget spans the whole page over its own rows; tiles sharing those rows give way for this window only.
export function widenedTiles(tiles: readonly PlacedTile[], widget: string): PlacedTile[] {
  const target = tiles.find(t => t.widget === widget)
  if (!target)
    return [...tiles]
  const first = target.col
  const last = target.col + target.colSpan - 1
  const inBand = (t: PlacedTile) => t.col >= first && t.col + t.colSpan - 1 <= last
  const stretched: PlacedTile[] = []
  for (const { tile } of readingOrder([...tiles])) {
    if (!inBand(tile))
      continue
    const wide = { ...tile, col: 1, colSpan: GRID_COLUMNS }
    if (!stretched.some(s => rowsOverlap(s, wide)))
      stretched.push(wide)
  }
  const kept = tiles.filter(t => !inBand(t) && !stretched.some(s => rowsOverlap(s, t)))
  return [...stretched, ...kept]
}
