import { GRID_COLUMNS } from './layout'

// Rows may run one past the last, which is how a dragged tile reaches a new row.
export function cellAt(rect: { left: number, top: number, width: number, height: number }, x: number, y: number, rows: number, gap: number): { col: number, row: number } {
  const colWidth = (rect.width - gap * (GRID_COLUMNS - 1)) / GRID_COLUMNS
  const rowHeight = (rect.height - gap * (rows - 1)) / rows
  const col = Math.floor((x - rect.left) / (colWidth + gap)) + 1
  const row = Math.floor((y - rect.top) / (rowHeight + gap)) + 1
  return { col: Math.min(GRID_COLUMNS, Math.max(1, col)), row: Math.min(rows + 1, Math.max(1, row)) }
}
