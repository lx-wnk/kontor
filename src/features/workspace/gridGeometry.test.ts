import { describe, expect, it } from 'vitest'
import { cellAt } from './gridGeometry'

// 12 columns of 90px with 10px gaps = 1190px wide; 4 rows of 100px, 10px gaps.
const rect = { left: 0, top: 0, width: 1190, height: 430 }

describe('cellAt', () => {
  it('maps a point to its column and row', () => {
    expect(cellAt(rect, 5, 5, 4, 10)).toEqual({ col: 1, row: 1 })
    expect(cellAt(rect, 1185, 425, 4, 10)).toEqual({ col: 12, row: 4 })
    expect(cellAt(rect, 205, 115, 4, 10)).toEqual({ col: 3, row: 2 })
  })

  // Dragging below the last row is how a tile reaches a new row.
  it('clamps columns but allows one row past the end', () => {
    expect(cellAt(rect, -50, 5, 4, 10).col).toBe(1)
    expect(cellAt(rect, 5000, 5, 4, 10).col).toBe(12)
    expect(cellAt(rect, 5, 900, 4, 10).row).toBe(5)
  })
})
