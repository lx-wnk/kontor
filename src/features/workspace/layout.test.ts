import type { PlacedTile, WorkspaceLayout, WorkspacePage } from './layout'
import { describe, expect, it } from 'vitest'
import {
  addPage,
  addTile,
  DEFAULT_LAYOUT,
  firstFreeSpot,
  fitsMinimum,
  moveTile,
  pageWithWidget,
  parseLayout,
  readingOrder,
  removePage,
  removeTile,
  resizeTile,
  rowsUsed,
  serializeLayout,
  swapTile,
  validateLayout,
  validatePlacement,
  widenedTiles,
} from './layout'

function tile(widget: string, col: number, row: number, colSpan = 3, rowSpan = 2): PlacedTile {
  return { widget, col, row, colSpan, rowSpan }
}
const page = (...tiles: PlacedTile[]): WorkspacePage => ({ id: 'zentrale', title: 'Zentrale', tiles })

describe('placement', () => {
  it('accepts a tile that fits', () => {
    expect(validatePlacement([tile('agents', 1, 1)], tile('github', 4, 1))).toBeNull()
  })

  // Anchoring means two tiles can be asked to occupy one cell. The rule is to
  // refuse, never to displace: a tile the operator did not touch must not move.
  it('refuses an overlap', () => {
    expect(validatePlacement([tile('agents', 1, 1, 4, 2)], tile('github', 3, 2))).toMatch(/overlap/i)
  })

  it('refuses a span running past the twelfth column', () => {
    expect(validatePlacement([], tile('agents', 10, 1, 4))).toMatch(/column/i)
  })

  it('refuses a row or span below one, and non-integers', () => {
    expect(validatePlacement([], tile('agents', 1, 0))).toMatch(/row/i)
    expect(validatePlacement([], tile('agents', 1, 1, 0, 1))).toMatch(/span/i)
    expect(validatePlacement([], tile('agents', 1.5, 1))).toMatch(/whole/i)
  })

  it('lets a tile be moved onto its own cells', () => {
    expect(validatePlacement([tile('agents', 1, 1, 4, 2)], tile('agents', 1, 1, 4, 3), 0)).toBeNull()
  })

  it('refuses a span below the widget\'s minimum, and ignores unknown widgets', () => {
    expect(fitsMinimum('live-work', 2, 3)).toMatch(/3 × 3/)
    expect(fitsMinimum('obsidian__recent', 1, 1)).toBeNull()
  })
})

describe('tile operations', () => {
  it('moves a tile and leaves the others where they were', () => {
    const r = moveTile(page(tile('agents', 1, 1), tile('github', 4, 1)), 0, 1, 3)
    expect(r.ok && r.value.tiles).toEqual([tile('agents', 1, 3), tile('github', 4, 1)])
  })

  it('refuses a move onto another tile and says why', () => {
    const r = moveTile(page(tile('agents', 1, 1), tile('github', 4, 1)), 0, 4, 1)
    expect(r.ok).toBe(false)
    expect(!r.ok && r.reason).toMatch(/overlap/i)
  })

  it('refuses a resize below the widget minimum', () => {
    expect(resizeTile(page(tile('live-work', 1, 1, 3, 5)), 0, 3, 2).ok).toBe(false)
  })

  // Swap is the operator's "exchange this tile": anchor and size stay.
  it('swaps a widget in place, keeping anchor and span', () => {
    const r = swapTile(page(tile('agents', 1, 1, 3, 3)), 0, 'github')
    expect(r.ok && r.value.tiles[0]).toEqual(tile('github', 1, 1, 3, 3))
  })

  it('refuses a swap to a widget that does not fit, or is already on the page', () => {
    expect(swapTile(page(tile('agents', 1, 1, 2, 2)), 0, 'live-work').ok).toBe(false)
    expect(swapTile(page(tile('agents', 1, 1), tile('github', 4, 1)), 0, 'github').ok).toBe(false)
  })

  it('adds a widget at the first free spot with its default span', () => {
    const r = addTile(page(tile('agents', 1, 1, 12, 2)), 'github')
    expect(r.ok && r.value.tiles[1]).toEqual(tile('github', 1, 3, 3, 3))
  })

  it('refuses to add a tile once the page holds 100 tiles', () => {
    const tiles = Array.from({ length: 100 }, (_, i) => tile(`w${i}`, (i % 12) + 1, Math.floor(i / 12) + 1, 1, 1))
    const r = addTile(page(...tiles), 'github')
    expect(r.ok).toBe(false)
    expect(!r.ok && r.reason).toMatch(/100/)
  })

  it('refuses to add a tile that would run past the last row', () => {
    const r = addTile(page(tile('x', 1, 1, 12, 500)), 'github')
    expect(r.ok).toBe(false)
    expect(!r.ok && r.reason).toMatch(/row/i)
  })

  it('removes a tile', () => {
    expect(removeTile(page(tile('agents', 1, 1), tile('github', 4, 1)), 0).tiles).toEqual([tile('github', 4, 1)])
  })

  it('throws on an index outside the page', () => {
    expect(() => removeTile(page(tile('agents', 1, 1)), 99)).toThrow(RangeError)
  })

  it('finds a free spot even on a full row', () => {
    expect(firstFreeSpot([tile('agents', 1, 1, 12, 1)], 3, 1)).toEqual({ col: 1, row: 2 })
  })

  it('counts rows and orders tiles for reading', () => {
    const tiles = [tile('github', 4, 1), tile('agents', 1, 3), tile('memory', 1, 1)]
    expect(rowsUsed(tiles)).toBe(4)
    expect(readingOrder(tiles).map(t => t.tile.widget)).toEqual(['memory', 'github', 'agents'])
  })
})

describe('the built-in layout', () => {
  it('holds only legal placements and passes the layout rules', () => {
    expect(validateLayout(DEFAULT_LAYOUT)).toBeNull()
  })

  it('is the Zentrale with the spec\'s nine widgets', () => {
    expect(DEFAULT_LAYOUT.pages.map(p => p.id)).toEqual(['zentrale'])
    expect(DEFAULT_LAYOUT.pages[0].tiles.map(t => t.widget).sort()).toEqual(
      ['agents', 'cost-today', 'github', 'hub', 'kontor', 'live-work', 'memory', 'pipeline', 'routines'],
    )
  })

  it('is frozen down to its tiles', () => {
    expect(Object.isFrozen(DEFAULT_LAYOUT)).toBe(true)
    expect(Object.isFrozen(DEFAULT_LAYOUT.pages[0].tiles[0])).toBe(true)
  })
})

describe('pageWithWidget', () => {
  it('prefers the zentrale page, then own pages in order, else null', () => {
    const l: WorkspaceLayout = {
      version: 1,
      pages: [
        { id: 'zentrale', title: 'Zentrale', tiles: [tile('hub', 1, 1, 6, 6)] },
        { id: 'a', title: 'A', tiles: [tile('kontor', 1, 1, 6, 1)] },
        { id: 'b', title: 'B', tiles: [tile('kontor', 1, 1, 6, 1)] },
      ],
    }
    expect(pageWithWidget(l, 'hub')?.id).toBe('zentrale')
    expect(pageWithWidget(l, 'kontor')?.id).toBe('a')
    expect(pageWithWidget(l, 'github')).toBeNull()
  })
})

describe('widenedTiles', () => {
  it('stretches the target and the tiles inside its column band, hides tiles sharing their rows', () => {
    const out = widenedTiles(DEFAULT_LAYOUT.pages[0].tiles, 'hub')
    expect(out.map(x => x.widget).sort()).toEqual(['hub', 'kontor'])
    expect(out.every(x => x.col === 1 && x.colSpan === 12)).toBe(true)
  })

  it('keeps tiles whose rows are free', () => {
    const tiles = [tile('hub', 4, 1, 6, 6), tile('github', 1, 8, 3, 2)]
    expect(widenedTiles(tiles, 'hub')).toEqual([tile('hub', 1, 1, 12, 6), tile('github', 1, 8, 3, 2)])
  })

  it('keeps only the first of two band tiles that would overlap once stretched', () => {
    const tiles = [tile('hub', 4, 1, 6, 6), tile('kontor', 4, 7, 3, 1), tile('memory', 7, 7, 3, 1)]
    expect(widenedTiles(tiles, 'hub').map(x => x.widget)).toEqual(['hub', 'kontor'])
  })

  it('returns the tiles unchanged when the widget is not on the page', () => {
    const tiles = [tile('github', 1, 1, 3, 2)]
    expect(widenedTiles(tiles, 'hub')).toEqual(tiles)
  })
})

describe('parsing', () => {
  it('reads an empty value as the built-in layout, readable', () => {
    expect(parseLayout('')).toEqual({ layout: DEFAULT_LAYOUT, unreadable: false })
  })

  // A parse bug must not destroy what the operator built: the caller gets the
  // built-in layout AND the flag that locks editing, so nothing overwrites it.
  it('flags an unreadable value instead of discarding it silently', () => {
    expect(parseLayout('{not json').unreadable).toBe(true)
    expect(parseLayout('{"version":1,"pages":[]}').unreadable).toBe(true)
  })

  it('keeps an unknown widget through a round trip', () => {
    const withModule = { version: 1 as const, pages: [page(tile('obsidian__recent', 1, 1))] }
    expect(parseLayout(serializeLayout(withModule))).toEqual({ layout: withModule, unreadable: false })
  })

  it('refuses a layout without the zentrale page, duplicate ids, or a bad id', () => {
    expect(validateLayout({ version: 1, pages: [{ id: 'other', title: 'O', tiles: [] }] })).toMatch(/zentrale/i)
    expect(validateLayout({ version: 1, pages: [page(), page()] })).toMatch(/twice/i)
    expect(validateLayout({ version: 1, pages: [page(), { id: 'Bad Id', title: 'B', tiles: [] }] })).toMatch(/id/i)
  })

  it('refuses the same widget twice on one page', () => {
    expect(validateLayout({ version: 1, pages: [page(tile('agents', 1, 1), tile('agents', 4, 1))] })).toBe('Page zentrale places agents twice.')
  })

  // Title length counts code points, matching server/internal/settings/workspace_layout.go
  // (utf8.RuneCountInString). Counting UTF-16 units instead would let the server accept a
  // title above U+FFFF (each 2 units) that the client then refuses to read.
  it('counts a title by code points, not UTF-16 units', () => {
    const emojiTitle = '😀'.repeat(41)
    expect(validateLayout({ version: 1, pages: [{ ...page(), title: emojiTitle }] })).toBeNull()
    expect(validateLayout({ version: 1, pages: [{ ...page(), title: 'a'.repeat(81) }] })).toMatch(/title/i)
  })
})

describe('pages', () => {
  it('adds a named page with the given id', () => {
    const r = addPage(DEFAULT_LAYOUT, '  Morning ', 'p-test')
    expect(r.ok && r.value.pageId).toBe('p-test')
    expect(r.ok && r.value.layout.pages.at(-1)).toEqual({ id: 'p-test', title: 'Morning', tiles: [] })
  })

  it('refuses an empty title and never removes the Zentrale', () => {
    expect(addPage(DEFAULT_LAYOUT, '   ').ok).toBe(false)
    expect(removePage(DEFAULT_LAYOUT, 'zentrale').ok).toBe(false)
  })

  it('counts an added page\'s title by code points, not UTF-16 units', () => {
    expect(addPage(DEFAULT_LAYOUT, '😀'.repeat(41), 'p-test').ok).toBe(true)
    expect(addPage(DEFAULT_LAYOUT, 'a'.repeat(81), 'p-test').ok).toBe(false)
  })
})
