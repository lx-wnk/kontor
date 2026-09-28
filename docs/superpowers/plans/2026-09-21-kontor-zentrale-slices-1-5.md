# Zentrale, slices 1-5 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mission control and the cockpit become one page, the Zentrale, assembled from widgets on an anchored grid the operator edits in the UI — with the needs-you queue in the page shell, the Kontor session as a collapsible tile, a first hub widget, and pages of the operator's own.

**Architecture:** Plain data describes what can be placed (`widgetSpecs.ts`) and where it sits (`layout.ts`, pure functions, no Vue). A registry maps widget ids to components. The layout is one server setting, `workspace.layout`, validated on both sides with the same rules. `WorkspacePage` renders a page from the layout through `WorkspaceGrid`; edit operations are pure functions whose result is saved.

**Tech Stack:** Vue 3 + TypeScript (Vite, pnpm, Vitest, Playwright), Tailwind v4, CSS Grid, `@vueuse/core` (`onKeyStroke`), Go settings registry (`server/internal/settings`).

**Spec:** `docs/superpowers/specs/2026-09-21-kontor-zentrale-design.md`, extending `docs/superpowers/specs/2026-09-20-composable-workspace-design.md`. This plan supersedes `docs/superpowers/plans/2026-09-20-composable-workspace.md`. Visual reference: `docs/superpowers/specs/2026-09-21-kontor-zentrale-prototype.html`.

## Global Constraints

- Twelve columns. `col` 1-12, `col + colSpan − 1 ≤ 12`, `row ≥ 1`, `rowSpan ≥ 1`. A placement overlapping another tile is refused; nothing is displaced.
- Rows split the page height: `grid-template-rows: repeat(N, minmax(56px, 1fr))`, N = `max(row + rowSpan − 1)` over the page's tiles.
- Below the `md` breakpoint: one column, tiles in reading order (row, then col), spans ignored.
- A tile naming an unknown widget stays in the layout and renders as a placeholder.
- An unreadable stored layout shows the built-in one, locks editing, and is never overwritten except by an explicit *Reset to default*.
- Client (`src/features/workspace/layout.ts`) and server (`server/internal/settings/workspace_layout.go`) enforce the same layout rules, kept in parity by hand — there is no shared module (`.agent-context/layer2-project-core.md`, SSOT section).
- No new dependency.
- Gate, chained with `&&`, exit code read directly, never through a trailing filter: `pnpm lint && pnpm typecheck && pnpm test`; for Go changes also `task test && task lint` and `go vet ./...` in `server/`. After `task test`, `git checkout -- server/internal/db/ent/` unless the regeneration is the change.
- Every guard (validation, refusal) gets a test that goes red when the guard is removed; demonstrate once by removing it, paste the red output, restore.
- Everything written is English. Commits are Conventional Commits describing behaviour; no slice or task numbers in messages.
- Work on branch `feat/kontor-zentrale` (worktree `.claude/worktrees/kontor-zentrale`); `develop` is fast-forwarded from it.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `src/features/workspace/widgetSpecs.ts` | id → title, default span, minimum span. Plain data, no Vue |
| `src/features/workspace/widgetRegistry.ts` | id → component, joined with the specs |
| `src/features/workspace/layout.ts` | Layout types, the built-in layout, validation, tile and page operations, parsing |
| `src/features/workspace/gridGeometry.ts` | Pointer position → grid cell |
| `src/features/workspace/useWorkspace.ts` | Loads and saves `workspace.layout`; shared state (layout, lock, save error, editing) |
| `src/features/workspace/components/WorkspaceGrid.vue` | Renders one page's tiles on the anchored grid; edit chrome when editing |
| `src/features/workspace/components/WorkspaceEditBar.vue` | Add tile, notices, reset, done |
| `src/features/workspace/components/WorkspacePage.vue` | A page by id: grid + edit bar + lock/save notices |
| `src/features/workspace/index.ts` | Public exports |
| `src/features/mission/components/LiveWorkWidget.vue` | Live work as a prop-less widget |
| `src/features/mission/components/NeedsYouQueue.vue` | The shell's queue, docked or strip |
| `src/features/mission/components/KontorWidget.vue` | Collapsed Kontor row + expanded overlay |
| `src/features/mission/components/HubWidget.vue` | Hub v0: docked queue + agent list |
| `src/features/analytics/components/CostTodayWidget.vue` | Today's spend as a tile |
| `src/composables/openTask.ts` | Injection key through which widgets open a task |
| `server/internal/settings/workspace_layout.go` | Server-side layout validation |

---

### Task 1: Widget specs and registry; the cockpit renders from it

**Files:**
- Create: `src/features/workspace/widgetSpecs.ts`, `src/features/workspace/widgetRegistry.ts`, `src/features/workspace/widgetRegistry.test.ts`, `src/features/workspace/index.ts`, `src/features/mission/components/LiveWorkWidget.vue`, `src/features/analytics/components/CostTodayWidget.vue`
- Modify: `src/features/cockpit/components/CockpitView.vue`, `src/features/mission/MissionControlView.vue`

**Interfaces:**
- Produces: `WidgetSpec { id, title, defaultColSpan, defaultRowSpan, minColSpan, minRowSpan }`, `WIDGET_SPECS: Record<string, WidgetSpec>`, `WidgetDef = WidgetSpec & { component: Component }`, `WIDGETS: Record<string, WidgetDef>`, `widgetIds(): string[]`.

- [ ] **Step 1: Write the failing test** — `src/features/workspace/widgetRegistry.test.ts`

```ts
import { describe, expect, it } from 'vitest'
import { WIDGETS, widgetIds } from './widgetRegistry'
import { WIDGET_SPECS } from './widgetSpecs'

describe('widget registry', () => {
  // Both sides are read: a spec without a component, or a component without a
  // spec, is the drift this registry exists to rule out.
  it('has a component for every spec and a spec for every component', () => {
    expect(widgetIds().sort()).toEqual(Object.keys(WIDGET_SPECS).sort())
  })

  it('carries the cockpit panels, live work and today\'s cost', () => {
    for (const id of ['agents', 'pipeline', 'routines', 'memory', 'github', 'live-work', 'cost-today'])
      expect(widgetIds()).toContain(id)
  })

  it('gives every widget a title and spans that fit twelve columns', () => {
    for (const id of widgetIds()) {
      const w = WIDGETS[id]
      expect(w.title.length).toBeGreaterThan(0)
      expect(w.minColSpan).toBeGreaterThanOrEqual(1)
      expect(w.minColSpan).toBeLessThanOrEqual(w.defaultColSpan)
      expect(w.defaultColSpan).toBeLessThanOrEqual(12)
      expect(w.minRowSpan).toBeGreaterThanOrEqual(1)
      expect(w.minRowSpan).toBeLessThanOrEqual(w.defaultRowSpan)
    }
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/widgetRegistry.test.ts`
Expected: FAIL — `./widgetRegistry` does not exist.

- [ ] **Step 3: Write the specs** — `src/features/workspace/widgetSpecs.ts`

```ts
export interface WidgetSpec {
  id: string
  title: string
  defaultColSpan: number
  defaultRowSpan: number
  minColSpan: number
  minRowSpan: number
}

function spec(id: string, title: string, def: [number, number], min: [number, number]): WidgetSpec {
  return { id, title, defaultColSpan: def[0], defaultRowSpan: def[1], minColSpan: min[0], minRowSpan: min[1] }
}

// Spans from the Zentrale spec's widget table. Plain data on purpose: the layout
// rules import this without pulling in a single component.
export const WIDGET_SPECS: Record<string, WidgetSpec> = Object.fromEntries([
  spec('live-work', 'Live work', [3, 5], [3, 3]),
  spec('agents', 'Agents', [3, 3], [3, 2]),
  spec('pipeline', 'Pipeline', [3, 3], [3, 2]),
  spec('routines', 'Routines', [3, 4], [3, 2]),
  spec('github', 'GitHub', [3, 3], [3, 2]),
  spec('memory', 'Memory', [3, 3], [3, 2]),
  spec('cost-today', 'Today', [3, 3], [2, 2]),
].map(s => [s.id, s]))
```

- [ ] **Step 4: Write the two new widgets**

`src/features/mission/components/LiveWorkWidget.vue` — takes over the running-tasks filter that lives in `MissionControlView.vue:21-24` today, so the widget needs no props:

```vue
<script setup lang="ts">
import { computed } from 'vue'
import { useTasks } from '@/features/pipeline'
import LiveWorkRail from './LiveWorkRail.vue'

const { tasks } = useTasks({ autoStart: false })

// Named positively on purpose. The first version excluded done, backlog and
// ready, which silently let 'cancelled' and 'on_hold' through — ten cancelled
// tasks rendered under the heading "10 running". A list of what counts cannot
// grow a hole when a stage is added; a list of what does not, can.
const RUNNING_STAGES = new Set(['plan_review', 'implementation', 'self_review', 'finalization'])
const running = computed(() => tasks.value.filter(t => RUNNING_STAGES.has(t.currentStage)))
</script>

<template>
  <LiveWorkRail :tasks="running" />
</template>
```

`src/features/analytics/components/CostTodayWidget.vue`:

```vue
<script setup lang="ts">
import type { PanelState } from '@/features/cockpit'
import { computed } from 'vue'
import { useTodayCost } from '@/composables/useTodayCost'
import CockpitPanel from '@/features/cockpit/components/CockpitPanel.vue'
import { formatCost } from '@/utils/format'

const { todayUsd } = useTodayCost()
const state = computed<PanelState>(() => (todayUsd.value === null ? 'loading' : 'ready'))
</script>

<template>
  <CockpitPanel id="cost-today" title="Today" :state="state">
    <p data-testid="cost-today-value" class="text-[24px] font-semibold leading-none text-fg">
      {{ formatCost(todayUsd ?? 0) }}
    </p>
  </CockpitPanel>
</template>
```

Check that `useTodayCost()` fetches on its own (read `src/composables/useTodayCost.ts`); if it only exposes a `fetch`/`refresh` function, call it in `onMounted`.

- [ ] **Step 5: Write the registry** — `src/features/workspace/widgetRegistry.ts`

```ts
import type { Component } from 'vue'
import type { WidgetSpec } from './widgetSpecs'
import CostTodayWidget from '@/features/analytics/components/CostTodayWidget.vue'
import AgentsPanel from '@/features/cockpit/components/AgentsPanel.vue'
import GitHubPanel from '@/features/cockpit/components/GitHubPanel.vue'
import MemoryPanel from '@/features/cockpit/components/MemoryPanel.vue'
import PipelinePanel from '@/features/cockpit/components/PipelinePanel.vue'
import RoutinesPanel from '@/features/cockpit/components/RoutinesPanel.vue'
import LiveWorkWidget from '@/features/mission/components/LiveWorkWidget.vue'
import { WIDGET_SPECS } from './widgetSpecs'

export type WidgetDef = WidgetSpec & { component: Component }

const COMPONENTS: Record<string, Component> = {
  'live-work': LiveWorkWidget,
  'agents': AgentsPanel,
  'pipeline': PipelinePanel,
  'routines': RoutinesPanel,
  'github': GitHubPanel,
  'memory': MemoryPanel,
  'cost-today': CostTodayWidget,
}

export const WIDGETS: Record<string, WidgetDef> = Object.fromEntries(
  Object.entries(COMPONENTS).map(([id, component]) => [id, { ...WIDGET_SPECS[id], component }]),
)

export function widgetIds(): string[] {
  return Object.keys(WIDGETS)
}
```

`src/features/workspace/index.ts`:

```ts
export * from './widgetRegistry'
export * from './widgetSpecs'
```

- [ ] **Step 6: Run the test again**

Run: `pnpm exec vitest run src/features/workspace/widgetRegistry.test.ts`
Expected: PASS.

- [ ] **Step 7: Render the cockpit from the registry, and Mission's live work from the widget**

`CockpitView.vue` keeps `data-testid="cockpit"` and its classes and renders the five panels in today's order:

```vue
<script setup lang="ts">
import { WIDGETS } from '@/features/workspace'

const COCKPIT = ['agents', 'pipeline', 'routines', 'memory', 'github'] as const
</script>

<template>
  <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3" data-testid="cockpit">
    <component :is="WIDGETS[id].component" v-for="id in COCKPIT" :key="id" />
  </div>
</template>
```

In `MissionControlView.vue` replace `<LiveWorkRail :tasks="running" />` with `<LiveWorkWidget />` and delete `RUNNING_STAGES`, `running` and the `LiveWorkRail` import there — the widget is now their single owner. Keep `useTasks()` and `onMounted(refetch)`: `NextThing` still reads `tasks`.

- [ ] **Step 8: Prove it changed nothing visible**

Run: `pnpm exec vitest run src/features/cockpit src/features/mission && pnpm test:e2e tests/e2e/cockpit.spec.ts tests/e2e/kontor-session.spec.ts`
Expected: PASS, unchanged.

- [ ] **Step 9: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add src/features/workspace src/features/mission src/features/analytics/components/CostTodayWidget.vue src/features/cockpit/components/CockpitView.vue
git commit -m "refactor: the cockpit renders its panels from a widget registry"
```

---

### Task 2: The layout model and its operations

**Files:**
- Create: `src/features/workspace/layout.ts`, `src/features/workspace/layout.test.ts`, `src/features/workspace/gridGeometry.ts`, `src/features/workspace/gridGeometry.test.ts`
- Modify: `src/features/workspace/index.ts` (export both)

**Interfaces:**
- Consumes: `WIDGET_SPECS` (Task 1).
- Produces:
  - Types `PlacedTile { widget, col, row, colSpan, rowSpan }`, `WorkspacePage { id, title, tiles }`, `WorkspaceLayout { version: 1, pages }`, `OpResult<T> = { ok: true, value: T } | { ok: false, reason: string }`.
  - Constants `GRID_COLUMNS = 12`, `ZENTRALE_PAGE_ID = 'zentrale'`, `DEFAULT_LAYOUT`, `PAGE_ID_PATTERN = /^[a-z0-9-]{1,40}$/`, `WIDGET_ID_PATTERN = /^[a-z0-9_-]{1,64}$/`.
  - Functions `validatePlacement(tiles, candidate, ignoreIndex?): string | null`, `fitsMinimum(widget, colSpan, rowSpan): string | null`, `moveTile(page, index, col, row)`, `resizeTile(page, index, colSpan, rowSpan)`, `swapTile(page, index, widget)`, `addTile(page, widget)` (all `OpResult<WorkspacePage>`), `removeTile(page, index): WorkspacePage`, `firstFreeSpot(tiles, colSpan, rowSpan): { col, row }`, `rowsUsed(tiles): number`, `readingOrder(tiles): Array<{ tile, index }>`, `validateLayout(value: unknown): string | null`, `parseLayout(raw: string): { layout: WorkspaceLayout, unreadable: boolean }`, `serializeLayout(layout): string`, `addPage(layout, title, newId?)`: `OpResult<{ layout, pageId }>`, `renamePage(layout, id, title)`: `OpResult<WorkspaceLayout>`, `removePage(layout, id)`: `OpResult<WorkspaceLayout>`, `replacePage(layout, page): WorkspaceLayout`.
  - `cellAt(rect: { left, top, width, height }, x, y, rows, gap): { col, row }` in `gridGeometry.ts`.

- [ ] **Step 1: Write the failing tests** — `src/features/workspace/layout.test.ts`

```ts
import type { PlacedTile, WorkspacePage } from './layout'
import { describe, expect, it } from 'vitest'
import {
  addPage, addTile, DEFAULT_LAYOUT, firstFreeSpot, fitsMinimum, moveTile, parseLayout, readingOrder,
  removePage, removeTile, resizeTile, rowsUsed, serializeLayout, swapTile, validateLayout, validatePlacement,
} from './layout'

const tile = (widget: string, col: number, row: number, colSpan = 3, rowSpan = 2): PlacedTile =>
  ({ widget, col, row, colSpan, rowSpan })
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

  it('removes a tile', () => {
    expect(removeTile(page(tile('agents', 1, 1), tile('github', 4, 1)), 0).tiles).toEqual([tile('github', 4, 1)])
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
})
```

`src/features/workspace/gridGeometry.test.ts`:

```ts
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
```

- [ ] **Step 2: Run them and watch them fail**

Run: `pnpm exec vitest run src/features/workspace/layout.test.ts src/features/workspace/gridGeometry.test.ts`
Expected: FAIL — the modules do not exist.

- [ ] **Step 3: Write the model** — `src/features/workspace/layout.ts`

```ts
import { WIDGET_SPECS } from './widgetSpecs'

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

const t = (widget: string, col: number, row: number, colSpan: number, rowSpan: number): PlacedTile =>
  ({ widget, col, row, colSpan, rowSpan })

// The spec's default Zentrale, 12 × 12.
export const DEFAULT_LAYOUT: WorkspaceLayout = {
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
}

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
    return `That spot would overlap ${WIDGET_SPECS[tiles[other].widget]?.title ?? tiles[other].widget}.`
  return null
}

export function fitsMinimum(widget: string, colSpan: number, rowSpan: number): string | null {
  const s = WIDGET_SPECS[widget]
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
    return fail(`${WIDGET_SPECS[widget]?.title ?? widget} is already on this page.`)
  return replaceTile(page, index, { ...page.tiles[index], widget })
}

export function removeTile(page: WorkspacePage, index: number): WorkspacePage {
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
  const s = WIDGET_SPECS[widget]
  if (!s)
    return fail(`Unknown widget ${widget}.`)
  if (page.tiles.some(tile => tile.widget === widget))
    return fail(`${s.title} is already on this page.`)
  const spot = firstFreeSpot(page.tiles, s.defaultColSpan, s.defaultRowSpan)
  return ok({ ...page, tiles: [...page.tiles, { widget, ...spot, colSpan: s.defaultColSpan, rowSpan: s.defaultRowSpan }] })
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
    if (title.length === 0 || title.length > MAX_TITLE)
      return `Page ${p.id} needs a title of 1 to ${MAX_TITLE} characters.`
    if (!Array.isArray(p.tiles) || p.tiles.length > MAX_TILES)
      return `Page ${p.id} holds at most ${MAX_TILES} tiles.`
    const placed: PlacedTile[] = []
    for (const tile of p.tiles) {
      if (!isRecord(tile) || typeof tile.widget !== 'string' || !WIDGET_ID_PATTERN.test(tile.widget))
        return `Page ${p.id} has a tile without a valid widget id.`
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
  catch {}
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
  return trimmed.length === 0 || trimmed.length > MAX_TITLE
    ? fail(`A page title has 1 to ${MAX_TITLE} characters.`)
    : ok(trimmed)
}

export function addPage(layout: WorkspaceLayout, title: string, newId = `p-${Date.now().toString(36)}`): OpResult<{ layout: WorkspaceLayout, pageId: string }> {
  const t = cleanTitle(title)
  if (!t.ok)
    return t
  if (layout.pages.length >= MAX_PAGES)
    return fail(`At most ${MAX_PAGES} pages.`)
  return ok({ layout: { ...layout, pages: [...layout.pages, { id: newId, title: t.value, tiles: [] }] }, pageId: newId })
}

export function renamePage(layout: WorkspaceLayout, id: string, title: string): OpResult<WorkspaceLayout> {
  const t = cleanTitle(title)
  if (!t.ok)
    return t
  return ok({ ...layout, pages: layout.pages.map(p => (p.id === id ? { ...p, title: t.value } : p)) })
}

export function removePage(layout: WorkspaceLayout, id: string): OpResult<WorkspaceLayout> {
  if (id === ZENTRALE_PAGE_ID)
    return fail('The Zentrale cannot be removed.')
  return ok({ ...layout, pages: layout.pages.filter(p => p.id !== id) })
}
```

`src/features/workspace/gridGeometry.ts`:

```ts
import { GRID_COLUMNS } from './layout'

// The grid cell under a pointer. Columns clamp to the grid; rows may run one
// past the last, which is how a dragged tile reaches a new row.
export function cellAt(rect: { left: number, top: number, width: number, height: number }, x: number, y: number, rows: number, gap: number): { col: number, row: number } {
  const colWidth = (rect.width - gap * (GRID_COLUMNS - 1)) / GRID_COLUMNS
  const rowHeight = (rect.height - gap * (rows - 1)) / rows
  const col = Math.floor((x - rect.left) / (colWidth + gap)) + 1
  const row = Math.floor((y - rect.top) / (rowHeight + gap)) + 1
  return { col: Math.min(GRID_COLUMNS, Math.max(1, col)), row: Math.min(rows + 1, Math.max(1, row)) }
}
```

- [ ] **Step 4: Run the tests again**

Run: `pnpm exec vitest run src/features/workspace`
Expected: PASS.

- [ ] **Step 5: Red-test the guards**

Delete the `overlaps` check in `validatePlacement` (the `findIndex` lines), run `pnpm exec vitest run src/features/workspace/layout.test.ts`, paste the failing `refuses an overlap` / `refuses a move onto another tile` output, restore. Same once for the `fitsMinimum` call in `replaceTile` (the resize and swap tests go red).

- [ ] **Step 6: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add src/features/workspace
git commit -m "feat: a workspace layout model that refuses overlaps and undersized tiles"
```

---

### Task 3: `workspace.layout` on the server

**Files:**
- Create: `server/internal/settings/workspace_layout.go`, `server/internal/settings/workspace_layout_test.go`
- Modify: `server/internal/settings/registry.go` (one entry), `src/features/settings/components/AppSettings.vue` (hide the category), its test (create `src/features/settings/components/AppSettings.test.ts` if none exists)

**Interfaces:**
- Produces: setting key `workspace.layout`, `Type: TypeString`, `Default: ""` (= built-in layout), `Apply: ApplyLive`, `Category: "workspace"`.

- [ ] **Step 1: Write the failing test** — `server/internal/settings/workspace_layout_test.go`

```go
package settings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const zentraleOnly = `{"version":1,"pages":[{"id":"zentrale","title":"Zentrale","tiles":[` +
	`{"widget":"agents","col":1,"row":1,"colSpan":3,"rowSpan":3},` +
	`{"widget":"obsidian__recent","col":4,"row":1,"colSpan":6,"rowSpan":2}]}]}`

func TestWorkspaceLayout_RegisteredAsLiveString(t *testing.T) {
	d, ok := Lookup("workspace.layout")
	require.True(t, ok, "workspace.layout is not in the registry")
	require.Equal(t, TypeString, d.Type)
	require.Equal(t, ApplyLive, d.Apply)
	require.Equal(t, "", d.Default)
}

func TestWorkspaceLayout_Validation(t *testing.T) {
	d, _ := Lookup("workspace.layout")

	// Empty means "the built-in layout".
	require.NoError(t, d.Validate(""))
	// An unknown widget id is kept: a deactivated module must not cost the layout.
	require.NoError(t, d.Validate(zentraleOnly))

	// The settings API accepts a PATCH from anything on loopback, so the rules
	// the browser applies must hold here too.
	bad := map[string]string{
		"not json":         `{`,
		"wrong version":    strings.Replace(zentraleOnly, `"version":1`, `"version":2`, 1),
		"unknown field":    strings.Replace(zentraleOnly, `"version":1`, `"version":1,"extra":true`, 1),
		"overlap":          strings.Replace(zentraleOnly, `"col":4,"row":1`, `"col":3,"row":2`, 1),
		"past column 12":   strings.Replace(zentraleOnly, `"col":4,"row":1,"colSpan":6`, `"col":8,"row":1,"colSpan":6`, 1),
		"row below one":    strings.Replace(zentraleOnly, `"col":4,"row":1`, `"col":4,"row":0`, 1),
		"span below one":   strings.Replace(zentraleOnly, `"colSpan":6,"rowSpan":2`, `"colSpan":6,"rowSpan":0`, 1),
		"bad widget id":    strings.Replace(zentraleOnly, `obsidian__recent`, `Bad Widget`, 1),
		"no zentrale":      strings.Replace(zentraleOnly, `"id":"zentrale"`, `"id":"morning"`, 1),
		"duplicate page":   strings.Replace(zentraleOnly, `]}]}`, `]},{"id":"zentrale","title":"Again","tiles":[]}]}`, 1),
		"empty title":      strings.Replace(zentraleOnly, `"title":"Zentrale"`, `"title":"  "`, 1),
		"uppercase pageid": strings.Replace(zentraleOnly, `]}]}`, `]},{"id":"Morning","title":"M","tiles":[]}]}`, 1),
	}
	for name, raw := range bad {
		require.Error(t, d.Validate(raw), name)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd server && go test ./internal/settings/ -run TestWorkspaceLayout -v`
Expected: FAIL — `workspace.layout is not in the registry`.

- [ ] **Step 3: Write the validator** — `server/internal/settings/workspace_layout.go`

```go
package settings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The workspace layout rules, mirrored in src/features/workspace/layout.ts.
// Change both together: the settings API accepts a PATCH from any loopback
// caller, so a rule only the browser applies is not a rule.
const (
	workspaceColumns  = 12
	workspaceMaxPages = 50
	workspaceMaxTiles = 100
	workspaceMaxRow   = 500
	workspaceMaxTitle = 80
	workspaceZentrale = "zentrale"
)

var (
	workspacePageID   = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	workspaceWidgetID = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
)

type workspaceTile struct {
	Widget  string `json:"widget"`
	Col     int    `json:"col"`
	Row     int    `json:"row"`
	ColSpan int    `json:"colSpan"`
	RowSpan int    `json:"rowSpan"`
}

type workspacePage struct {
	ID    string          `json:"id"`
	Title string          `json:"title"`
	Tiles []workspaceTile `json:"tiles"`
}

type workspaceLayout struct {
	Version int             `json:"version"`
	Pages   []workspacePage `json:"pages"`
}

func validWorkspaceLayout(raw string) error {
	if raw == "" {
		return nil
	}
	var l workspaceLayout
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return fmt.Errorf("workspace.layout: not a layout: %w", err)
	}
	if l.Version != 1 {
		return fmt.Errorf("workspace.layout: version must be 1")
	}
	if len(l.Pages) == 0 || len(l.Pages) > workspaceMaxPages {
		return fmt.Errorf("workspace.layout: between 1 and %d pages", workspaceMaxPages)
	}
	seen := make(map[string]bool, len(l.Pages))
	for _, p := range l.Pages {
		if err := validWorkspacePage(p, seen); err != nil {
			return fmt.Errorf("workspace.layout: %w", err)
		}
	}
	if !seen[workspaceZentrale] {
		return fmt.Errorf("workspace.layout: the %s page is missing", workspaceZentrale)
	}
	return nil
}

func validWorkspacePage(p workspacePage, seen map[string]bool) error {
	if !workspacePageID.MatchString(p.ID) {
		return fmt.Errorf("page id %q: lowercase letters, digits and dashes only", p.ID)
	}
	if seen[p.ID] {
		return fmt.Errorf("page %s appears twice", p.ID)
	}
	seen[p.ID] = true
	if n := utf8.RuneCountInString(strings.TrimSpace(p.Title)); n == 0 || n > workspaceMaxTitle {
		return fmt.Errorf("page %s: title needs 1 to %d characters", p.ID, workspaceMaxTitle)
	}
	if len(p.Tiles) > workspaceMaxTiles {
		return fmt.Errorf("page %s: at most %d tiles", p.ID, workspaceMaxTiles)
	}
	for i, t := range p.Tiles {
		if err := validWorkspaceTile(t); err != nil {
			return fmt.Errorf("page %s tile %d: %w", p.ID, i, err)
		}
		for j := range i {
			if workspaceTilesOverlap(t, p.Tiles[j]) {
				return fmt.Errorf("page %s: tile %d overlaps tile %d", p.ID, i, j)
			}
		}
	}
	return nil
}

func validWorkspaceTile(t workspaceTile) error {
	switch {
	case !workspaceWidgetID.MatchString(t.Widget):
		return fmt.Errorf("widget id %q is not valid", t.Widget)
	case t.ColSpan < 1 || t.RowSpan < 1:
		return fmt.Errorf("spans must be at least 1")
	case t.Col < 1 || t.Col+t.ColSpan-1 > workspaceColumns:
		return fmt.Errorf("must stay within %d columns", workspaceColumns)
	case t.Row < 1 || t.Row+t.RowSpan-1 > workspaceMaxRow:
		return fmt.Errorf("rows must lie between 1 and %d", workspaceMaxRow)
	}
	return nil
}

func workspaceTilesOverlap(a, b workspaceTile) bool {
	return a.Col < b.Col+b.ColSpan && b.Col < a.Col+a.ColSpan &&
		a.Row < b.Row+b.RowSpan && b.Row < a.Row+a.RowSpan
}
```

`for j := range i` needs Go 1.22+; the module is on 1.26.

Add to the `list` in `registry.go`, after the `onboarding.completed` entry:

```go
		// workspace.layout is the operator's arrangement of pages and tiles, as
		// JSON. Empty means the built-in layout. Edited on the page itself, never
		// in the settings list (AppSettings hides the category).
		{Key: "workspace.layout", Type: TypeString, Default: "", Apply: ApplyLive, Category: "workspace", validate: validWorkspaceLayout},
```

- [ ] **Step 4: Run the test again, then red-test the overlap guard**

Run: `cd server && go test ./internal/settings/ -run TestWorkspaceLayout -v`
Expected: PASS. Then delete the `workspaceTilesOverlap` loop, rerun, paste the red `overlap` case, restore.

- [ ] **Step 5: Hide the category from the generic settings list**

In `AppSettings.vue`, filter before grouping:

```ts
// Categories edited on their own surface. A raw JSON field for the workspace
// here would be a second editor that bypasses the page's placement rules.
const HIDDEN_CATEGORIES = new Set(['workspace'])
```

and in `groups`: `for (const item of items.value.filter(i => !HIDDEN_CATEGORIES.has(i.category)))`. Add a component test that mounts `AppSettings` with a mocked `useSettings` returning one `workspace` and one `sse` item and asserts the text `workspace.layout` is absent while `sse.intervalMs` is present (follow the mocking style of an existing settings test: `ls src/features/settings/components/*.test.ts`).

- [ ] **Step 6: Gate, check the live API accepts the write, commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
(cd server && task test && task lint && go vet ./...)
git checkout -- server/internal/db/ent/
```

Build and start the server from this worktree on a scratch port and confirm a PATCH is accepted without an admin session (the route is `MountWrite`, `server/internal/api/settings/handler.go:27-29`; the installation runs `auth.mode=none`). Use the start command from `Taskfile.yml` (`grep -n "serve" Taskfile.yml`), then:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X PATCH -H 'Content-Type: application/json' -H 'Origin: http://127.0.0.1:<port>' \
  --data '{"value":""}' http://127.0.0.1:<port>/api/settings/workspace.layout
```

Expected: `200`. If it is `403`, stop and report — the layout cannot be saved from the UI and the spec's persistence decision needs revisiting.

```bash
git add server/internal/settings src/features/settings
git commit -m "feat: the server stores a workspace layout and refuses an invalid one"
```

---

### Task 4: Loading and saving the workspace

**Files:**
- Create: `src/features/workspace/useWorkspace.ts`, `src/features/workspace/useWorkspace.test.ts`
- Modify: `src/features/workspace/index.ts`

**Interfaces:**
- Consumes: `parseLayout`, `serializeLayout`, `DEFAULT_LAYOUT`, `WorkspaceLayout`, `WorkspacePage` (Task 2).
- Produces: `useWorkspace(): { layout: Ref<WorkspaceLayout>, loaded: Ref<boolean>, locked: Ref<string | null>, saveError: Ref<string | null>, editing: Ref<boolean>, load(): Promise<void>, save(next: WorkspaceLayout): Promise<void>, reset(): Promise<void>, page(id: string): WorkspacePage | undefined }`. State is module-level: every caller shares one workspace.

- [ ] **Step 1: Write the failing test** — `src/features/workspace/useWorkspace.test.ts`

```ts
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_LAYOUT, serializeLayout } from './layout'

function settingsResponse(value: string) {
  return new Response(JSON.stringify([{ key: 'workspace.layout', value }]), { status: 200 })
}

async function fresh() {
  vi.resetModules()
  return (await import('./useWorkspace')).useWorkspace()
}

describe('useWorkspace', () => {
  beforeEach(() => vi.restoreAllMocks())

  it('shows the built-in layout when nothing is stored', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse(''))
    const ws = await fresh()
    await ws.load()
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toBeNull()
  })

  // A parse bug must not destroy what the operator built: editing locks, so
  // nothing writes over the stored value until they choose Reset.
  it('locks editing when the stored layout is unreadable', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse('{broken'))
    const ws = await fresh()
    await ws.load()
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toMatch(/could not be read/i)
  })

  it('saves with a PATCH and keeps the change on screen when the save fails', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockResolvedValueOnce(new Response('{"error":"bad request"}', { status: 400 }))
    const ws = await fresh()
    await ws.load()
    const next = { ...DEFAULT_LAYOUT, pages: [{ ...DEFAULT_LAYOUT.pages[0], tiles: [] }] }
    await ws.save(next)
    expect(fetch).toHaveBeenLastCalledWith('/api/settings/workspace.layout', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ value: serializeLayout(next) }),
    }))
    expect(ws.layout.value).toEqual(next)
    expect(ws.saveError.value).toMatch(/not saved/i)
  })

  it('refuses to save while locked', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse('{broken'))
    const ws = await fresh()
    await ws.load()
    await ws.save(DEFAULT_LAYOUT)
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/useWorkspace.test.ts`
Expected: FAIL — module missing.

- [ ] **Step 3: Write the composable** — `src/features/workspace/useWorkspace.ts`

```ts
import type { WorkspaceLayout, WorkspacePage } from './layout'
import { ref } from 'vue'
import { DEFAULT_LAYOUT, parseLayout, serializeLayout } from './layout'

const SETTING = 'workspace.layout'

const layout = ref<WorkspaceLayout>(DEFAULT_LAYOUT)
const loaded = ref(false)
const locked = ref<string | null>(null)
const saveError = ref<string | null>(null)
const editing = ref(false)
let loading: Promise<void> | null = null
// Saves run one after another, so an older layout can never land after a newer one.
let saving: Promise<void> = Promise.resolve()

async function load(): Promise<void> {
  loading ??= (async () => {
    try {
      const res = await fetch('/api/settings')
      if (!res.ok)
        throw new Error(`HTTP ${res.status}`)
      const items = await res.json() as Array<{ key: string, value: string }>
      const parsed = parseLayout(items.find(i => i.key === SETTING)?.value ?? '')
      layout.value = parsed.layout
      locked.value = parsed.unreadable
        ? 'The saved layout could not be read, so the built-in one is shown. Editing is locked until you reset it.'
        : null
    }
    catch {
      locked.value = 'The saved layout could not be loaded, so the built-in one is shown. Editing is locked until it loads.'
    }
    finally {
      loaded.value = true
    }
  })()
  return loading
}

async function write(next: WorkspaceLayout): Promise<void> {
  layout.value = next
  saveError.value = null
  saving = saving.then(async () => {
    try {
      const res = await fetch(`/api/settings/${SETTING}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ value: serializeLayout(next) }),
      })
      if (!res.ok)
        throw new Error(`HTTP ${res.status}`)
    }
    catch (e) {
      saveError.value = `Not saved (${e instanceof Error ? e.message : 'network error'}). The change stays on screen; the next edit tries again.`
    }
  })
  return saving
}

async function save(next: WorkspaceLayout): Promise<void> {
  if (locked.value)
    return
  return write(next)
}

async function reset(): Promise<void> {
  locked.value = null
  return write(DEFAULT_LAYOUT)
}

function page(id: string): WorkspacePage | undefined {
  return layout.value.pages.find(p => p.id === id)
}

export function useWorkspace() {
  return { layout, loaded, locked, saveError, editing, load, save, reset, page }
}
```

- [ ] **Step 4: Run the test again**

Expected: PASS.

- [ ] **Step 5: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add src/features/workspace
git commit -m "feat: the workspace loads from and saves to the server, and locks when unreadable"
```

---

### Task 5: Rendering a page on the anchored grid

**Files:**
- Create: `src/features/workspace/components/WorkspaceGrid.vue`, `src/features/workspace/components/WorkspacePage.vue`, `src/features/workspace/components/WorkspaceGrid.test.ts`
- Modify: `src/features/workspace/index.ts` (export `WorkspacePage`)

**Interfaces:**
- Consumes: `WIDGETS` (Task 1), `readingOrder`, `rowsUsed`, `WorkspacePage` type (Task 2), `useWorkspace` (Task 4).
- Produces: `<WorkspaceGrid :page="WorkspacePage" :editing="boolean" @change="(page: WorkspacePage) => void" @refuse="(reason: string) => void" />`; `<WorkspacePage page-id="string" />`. Test ids: `workspace-grid`, `workspace-tile-<widget>`, `workspace-unknown`.

- [ ] **Step 1: Write the failing test** — `src/features/workspace/components/WorkspaceGrid.test.ts`

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

vi.mock('../widgetRegistry', () => ({
  WIDGETS: {
    agents: { id: 'agents', title: 'Agents', component: defineComponent({ render: () => h('p', 'agents body') }) },
  },
}))

const { default: WorkspaceGrid } = await import('./WorkspaceGrid.vue')

const page = {
  id: 'zentrale',
  title: 'Zentrale',
  tiles: [
    { widget: 'obsidian__recent', col: 5, row: 2, colSpan: 4, rowSpan: 1 },
    { widget: 'agents', col: 1, row: 1, colSpan: 4, rowSpan: 2 },
  ],
}

describe('WorkspaceGrid', () => {
  it('anchors each tile where the layout says, in reading order', () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: false } })
    const tiles = w.findAll('[data-testid^="workspace-tile-"]')
    expect(tiles.map(t => t.attributes('data-testid'))).toEqual(['workspace-tile-agents', 'workspace-tile-obsidian__recent'])
    const style = tiles[0].attributes('style')
    expect(style).toContain('--col: 1')
    expect(style).toContain('--col-span: 4')
    expect(style).toContain('--row: 1')
    expect(style).toContain('--row-span: 2')
    expect(w.get('[data-testid="workspace-grid"]').attributes('style')).toContain('--rows: 2')
    w.unmount()
  })

  // A module that is deactivated must not cost the operator their layout.
  it('renders an unknown widget as a placeholder naming it', () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: false } })
    expect(w.get('[data-testid="workspace-unknown"]').text()).toContain('obsidian__recent')
    expect(w.text()).toContain('agents body')
    w.unmount()
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/components`
Expected: FAIL — component missing.

- [ ] **Step 3: Write the grid** — `src/features/workspace/components/WorkspaceGrid.vue`

```vue
<script setup lang="ts">
import type { PlacedTile, WorkspacePage } from '../layout'
import { computed } from 'vue'
import { readingOrder, rowsUsed } from '../layout'
import { WIDGETS } from '../widgetRegistry'

const props = defineProps<{ page: WorkspacePage, editing: boolean }>()
defineEmits<{ change: [page: WorkspacePage], refuse: [reason: string] }>()

// DOM order is reading order, which is what the single-column layout below md
// shows; from md up every tile is placed explicitly, so DOM order stops mattering.
const ordered = computed(() => readingOrder(props.page.tiles))
const rows = computed(() => rowsUsed(props.page.tiles))

function placement(t: PlacedTile): Record<string, number> {
  return { '--col': t.col, '--col-span': t.colSpan, '--row': t.row, '--row-span': t.rowSpan }
}
</script>

<template>
  <div data-testid="workspace-grid" class="workspace-grid" :style="{ '--rows': rows }">
    <div
      v-for="{ tile, index } in ordered"
      :key="`${tile.widget}-${index}`"
      :data-testid="`workspace-tile-${tile.widget}`"
      class="workspace-tile"
      :style="placement(tile)"
    >
      <component :is="WIDGETS[tile.widget].component" v-if="WIDGETS[tile.widget]" />
      <div v-else data-testid="workspace-unknown" class="h-full rounded-xl border border-dashed border-line p-4 text-[12px] text-fg-mute">
        {{ tile.widget }} is not available — the module that provides it may be inactive.
      </div>
    </div>
  </div>
</template>

<style scoped>
.workspace-grid { display: grid; gap: 12px; grid-template-columns: minmax(0, 1fr); }
.workspace-tile { min-width: 0; min-height: 0; }
.workspace-tile > :deep(*) { height: 100%; }
@media (min-width: 768px) {
  .workspace-grid {
    height: 100%;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    grid-template-rows: repeat(var(--rows), minmax(56px, 1fr));
  }
  .workspace-tile {
    grid-column: var(--col) / span var(--col-span);
    grid-row: var(--row) / span var(--row-span);
  }
}
</style>
```

`src/features/workspace/components/WorkspacePage.vue`:

```vue
<script setup lang="ts">
import type { WorkspacePage as Page } from '../layout'
import { computed, onMounted } from 'vue'
import { replacePage } from '../layout'
import { useWorkspace } from '../useWorkspace'
import WorkspaceGrid from './WorkspaceGrid.vue'

const props = defineProps<{ pageId: string }>()
const { layout, load, save, locked, saveError, editing, page } = useWorkspace()
onMounted(load)

const current = computed(() => page(props.pageId))

function onChange(next: Page) {
  save(replacePage(layout.value, next))
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3" :data-testid="`workspace-page-${pageId}`">
    <p v-if="locked" role="alert" data-testid="workspace-locked" class="rounded-md bg-warning-soft px-3 py-2 text-[12.5px] text-warning-text">
      {{ locked }}
    </p>
    <p v-if="saveError" role="alert" data-testid="workspace-save-error" class="rounded-md bg-danger-soft px-3 py-2 text-[12.5px] text-danger-text">
      {{ saveError }}
    </p>
    <WorkspaceGrid v-if="current" class="min-h-0 flex-1" :page="current" :editing="editing" @change="onChange" />
  </div>
</template>
```

- [ ] **Step 4: Run the test again**

Expected: PASS.

- [ ] **Step 5: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add src/features/workspace
git commit -m "feat: a workspace page renders its tiles on an anchored twelve-column grid"
```

---

### Task 6: Edit mode — keyboard, buttons, add, remove, swap

**Files:**
- Create: `src/features/workspace/components/WorkspaceEditBar.vue`, `src/features/workspace/components/WorkspaceEdit.test.ts`
- Modify: `src/features/workspace/components/WorkspaceGrid.vue`, `src/features/workspace/components/WorkspacePage.vue`

**Interfaces:**
- Consumes: `moveTile`, `resizeTile`, `swapTile`, `removeTile`, `addTile`, `fitsMinimum`, `WIDGETS`, `widgetIds`, `useWorkspace().editing`, `reset`.
- Produces: in edit mode, each tile is focusable (`tabindex="0"`, `aria-label="<title>, column c, row r, c×r"`) and shows a chrome with `workspace-swap-<widget>` (a native `<select>`) and `workspace-remove-<widget>`. Keys on a focused tile: arrows move by one cell, Shift+arrows resize by one, Delete/Backspace remove. `WorkspaceEditBar` has `workspace-add` (native `<select>` + button `workspace-add-submit`), `workspace-refusal` (`aria-live="polite"`), `workspace-reset` (only while locked) and `workspace-done`.

- [ ] **Step 1: Write the failing test** — `src/features/workspace/components/WorkspaceEdit.test.ts`

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import WorkspaceGrid from './WorkspaceGrid.vue'

const page = {
  id: 'zentrale',
  title: 'Zentrale',
  tiles: [
    { widget: 'agents', col: 1, row: 1, colSpan: 3, rowSpan: 3 },
    { widget: 'github', col: 4, row: 1, colSpan: 3, rowSpan: 3 },
  ],
}

describe('edit mode', () => {
  it('moves a focused tile with the arrow keys', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowDown' })
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'agents', row: 2 }, { widget: 'github' }] })
    w.unmount()
  })

  // Refuse, never displace: the model is unchanged and the reason is said.
  it('refuses a move onto another tile and reports why', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    for (const _ of [1, 2, 3])
      await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowRight' })
    expect(w.emitted('change')?.length ?? 0).toBeLessThan(3)
    expect(w.emitted('refuse')?.[0]?.[0]).toMatch(/overlap/i)
    w.unmount()
  })

  it('swaps a tile in place from its chrome, keeping anchor and span', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    await w.get('[data-testid="workspace-swap-agents"]').setValue('pipeline')
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'pipeline', col: 1, row: 1, colSpan: 3, rowSpan: 3 }, { widget: 'github' }] })
    w.unmount()
  })

  it('removes a tile from its chrome', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    await w.get('[data-testid="workspace-remove-agents"]').trigger('click')
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'github' }] })
    w.unmount()
  })

  it('shows no chrome and ignores keys outside edit mode', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: false } })
    expect(w.find('[data-testid="workspace-remove-agents"]').exists()).toBe(false)
    await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowDown' })
    expect(w.emitted('change')).toBeUndefined()
    w.unmount()
  })
})
```

Add an `WorkspaceEditBar` case to the same file: mounting it with `page` and `editing: true`, selecting `pipeline` in `workspace-add` and clicking `workspace-add-submit` emits `change` with a third tile `{ widget: 'pipeline' }`; the option list excludes `agents` and `github` (already placed).

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/components/WorkspaceEdit.test.ts`
Expected: FAIL — no chrome, no key handling.

- [ ] **Step 3: Add the chrome and keys to `WorkspaceGrid.vue`**

Add to the script:

```ts
import type { OpResult } from '../layout'
import { fitsMinimum, moveTile, removeTile, resizeTile, swapTile } from '../layout'
import { widgetIds } from '../widgetRegistry'

const emit = defineEmits<{ change: [page: WorkspacePage], refuse: [reason: string] }>()

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

// Candidates for "swap": everything not already on the page; the ones whose
// minimum does not fit this tile are listed disabled with the reason.
function swapOptions(index: number) {
  const t = props.page.tiles[index]
  const placed = new Set(props.page.tiles.map(p => p.widget))
  return widgetIds().filter(id => !placed.has(id)).map(id => ({ id, title: WIDGETS[id].title, reason: fitsMinimum(id, t.colSpan, t.rowSpan) }))
}
```

Remove the earlier `defineEmits` line (the `const emit` replaces it). On each tile `div` add `:tabindex="editing ? 0 : undefined"`, `:aria-label="editing ? `${WIDGETS[tile.widget]?.title ?? tile.widget}, column ${tile.col}, row ${tile.row}, ${tile.colSpan} by ${tile.rowSpan}` : undefined"`, `:class="{ 'workspace-tile--editing': editing }"` and `@keydown="onKey($event, index)"`; inside it, before the widget:

```vue
<div v-if="editing" class="workspace-chrome">
  <select
    :data-testid="`workspace-swap-${tile.widget}`"
    :aria-label="`Swap ${WIDGETS[tile.widget]?.title ?? tile.widget} for`"
    class="rounded-md border border-line-strong bg-card px-1.5 text-[12px]"
    @change="apply(swapTile(page, index, ($event.target as HTMLSelectElement).value))"
  >
    <option value="" selected disabled>⇄ Swap…</option>
    <option v-for="o in swapOptions(index)" :key="o.id" :value="o.id" :disabled="!!o.reason">
      {{ o.title }}{{ o.reason ? ` — ${o.reason}` : '' }}
    </option>
  </select>
  <button type="button" :data-testid="`workspace-remove-${tile.widget}`" :aria-label="`Remove ${WIDGETS[tile.widget]?.title ?? tile.widget}`" class="rounded-md border border-line-strong bg-card px-2 text-[12px]" @click="emit('change', removeTile(page, index))">
    ✕
  </button>
</div>
```

Add to the style block:

```css
.workspace-tile { position: relative; }
.workspace-tile--editing { outline: 1px dashed var(--color-line-strong); outline-offset: 2px; border-radius: 12px; cursor: grab; }
.workspace-tile--editing:focus-visible { outline: 2px solid var(--color-accent); }
.workspace-tile--editing > :deep(:not(.workspace-chrome)) { pointer-events: none; }
.workspace-chrome { position: absolute; top: 6px; right: 6px; z-index: 2; display: flex; gap: 4px; }
```

Widgets stop taking clicks while editing (`pointer-events: none`), so a drag or a click cannot trigger a merge button or open an agent by accident.

- [ ] **Step 4: Write `WorkspaceEditBar.vue`**

```vue
<script setup lang="ts">
import type { WorkspacePage } from '../layout'
import { computed, ref } from 'vue'
import { addTile } from '../layout'
import { WIDGETS, widgetIds } from '../widgetRegistry'

const props = defineProps<{ page: WorkspacePage, refusal: string | null, locked: boolean }>()
const emit = defineEmits<{ change: [page: WorkspacePage], refuse: [reason: string], reset: [], done: [] }>()

const choice = ref('')
const addable = computed(() => widgetIds().filter(id => !props.page.tiles.some(t => t.widget === id)))

function add() {
  if (!choice.value)
    return
  const r = addTile(props.page, choice.value)
  if (r.ok)
    emit('change', r.value)
  else
    emit('refuse', r.reason)
  choice.value = ''
}
</script>

<template>
  <div data-testid="workspace-edit-bar" class="flex flex-wrap items-center gap-2 rounded-lg border border-line bg-card px-3 py-2 text-[12.5px]">
    <span class="font-medium text-fg">Editing {{ page.title }}</span>
    <span class="text-fg-mute">Arrows move · Shift+arrows resize · Delete removes</span>
    <span class="flex-grow" />
    <select v-model="choice" data-testid="workspace-add" aria-label="Tile to add" class="rounded-md border border-line-strong bg-app px-2 py-1">
      <option value="" disabled>
        Add a tile…
      </option>
      <option v-for="id in addable" :key="id" :value="id">
        {{ WIDGETS[id].title }}
      </option>
    </select>
    <button type="button" data-testid="workspace-add-submit" class="rounded-md border border-line-strong px-2.5 py-1" :disabled="!choice" @click="add">
      Add
    </button>
    <button v-if="locked" type="button" data-testid="workspace-reset" class="rounded-md border border-line-strong px-2.5 py-1" @click="emit('reset')">
      Reset to default
    </button>
    <button type="button" data-testid="workspace-done" class="rounded-md bg-accent px-2.5 py-1 text-accent-contrast" @click="emit('done')">
      Done
    </button>
    <p data-testid="workspace-refusal" aria-live="polite" class="basis-full text-warning-text empty:hidden">
      {{ refusal ?? '' }}
    </p>
  </div>
</template>
```

- [ ] **Step 5: Wire the bar into `WorkspacePage.vue`**

Add `const refusal = ref<string | null>(null)`, render `<WorkspaceEditBar v-if="editing && current" :page="current" :refusal="refusal" :locked="!!locked" @change="onChange" @refuse="r => (refusal = r)" @reset="reset" @done="editing = false" />` above the grid, pass `@refuse="r => (refusal = r)"` to the grid, and clear `refusal` inside `onChange`. While `locked`, keep `editing` false: `watch(locked, l => { if (l) editing.value = false })`, and render the lock notice with the *Reset to default* button (`workspace-reset`) even outside edit mode so the operator can leave the locked state.

- [ ] **Step 6: Run the tests again**

Run: `pnpm exec vitest run src/features/workspace`
Expected: PASS.

- [ ] **Step 7: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add src/features/workspace
git commit -m "feat: tiles are moved, resized, swapped, added and removed in an edit mode"
```

---

### Task 7: Dragging and resizing with the pointer

**Files:**
- Modify: `src/features/workspace/components/WorkspaceGrid.vue`
- Test: `src/features/workspace/components/WorkspaceEdit.test.ts` (extend)

**Interfaces:**
- Consumes: `cellAt` (Task 2), `moveTile`, `resizeTile`, `validatePlacement`.
- Produces: a move handle on each tile in edit mode (the tile itself), a resize handle `workspace-resize-<widget>` in the bottom-right corner, and a ghost `workspace-ghost` with class `workspace-ghost--invalid` while the target is refused.

- [ ] **Step 1: Write the failing test** (extend `WorkspaceEdit.test.ts`)

```ts
it('drops a dragged tile on the cell under the pointer', async () => {
  const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
  const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
  // 12 columns of 90px, 10px gaps; 3 rows of 100px.
  grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) })
  const tile = w.get('[data-testid="workspace-tile-agents"]')
  await tile.trigger('pointerdown', { clientX: 5, clientY: 5, pointerId: 1, button: 0 })
  await tile.trigger('pointermove', { clientX: 705, clientY: 5, pointerId: 1 })
  expect(w.find('[data-testid="workspace-ghost"]').exists()).toBe(true)
  await tile.trigger('pointerup', { clientX: 705, clientY: 5, pointerId: 1 })
  expect(w.emitted('change')?.at(-1)?.[0]).toMatchObject({ tiles: [{ widget: 'agents', col: 8, row: 1 }, { widget: 'github' }] })
  w.unmount()
})

it('snaps back and reports when the drop would overlap', async () => {
  const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
  const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
  grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) })
  const tile = w.get('[data-testid="workspace-tile-agents"]')
  await tile.trigger('pointerdown', { clientX: 5, clientY: 5, pointerId: 1, button: 0 })
  await tile.trigger('pointermove', { clientX: 405, clientY: 5, pointerId: 1 })
  expect(w.get('[data-testid="workspace-ghost"]').classes()).toContain('workspace-ghost--invalid')
  await tile.trigger('pointerup', { clientX: 405, clientY: 5, pointerId: 1 })
  expect(w.emitted('change')).toBeUndefined()
  expect(w.emitted('refuse')?.[0]?.[0]).toMatch(/overlap/i)
  w.unmount()
})
```

jsdom does not implement `setPointerCapture`; guard the call with `el.setPointerCapture?.(e.pointerId)`.

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/components/WorkspaceEdit.test.ts`
Expected: FAIL — no ghost, no change.

- [ ] **Step 3: Implement the drag** in `WorkspaceGrid.vue`

```ts
import { ref } from 'vue'
import { cellAt } from '../gridGeometry'
import { validatePlacement } from '../layout'

const GAP = 12 // matches .workspace-grid gap
const gridEl = ref<HTMLElement | null>(null)
const drag = ref<null | { index: number, mode: 'move' | 'resize', grabCol: number, grabRow: number, target: PlacedTile }>(null)

function cellOf(e: PointerEvent) {
  return cellAt(gridEl.value!.getBoundingClientRect(), e.clientX, e.clientY, rows.value, GAP)
}

function startDrag(e: PointerEvent, index: number, mode: 'move' | 'resize') {
  if (!props.editing || e.button !== 0 || (e.target as HTMLElement).closest('select, button:not([data-resize])'))
    return
  const t = props.page.tiles[index]
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

const ghostInvalid = computed(() => !!drag.value && validatePlacement(props.page.tiles, drag.value.target, drag.value.index) !== null)
```

Template: `ref="gridEl"` on the grid; on each tile `@pointerdown="startDrag($event, index, 'move')" @pointermove="moveDrag" @pointerup="endDrag" @pointercancel="drag = null"`; inside the chrome area of each tile in edit mode a resize handle:

```vue
<button
  v-if="editing"
  type="button"
  data-resize
  :data-testid="`workspace-resize-${tile.widget}`"
  :aria-label="`Resize ${WIDGETS[tile.widget]?.title ?? tile.widget} (Shift+arrows)`"
  class="workspace-resize"
  @pointerdown="startDrag($event, index, 'resize')"
/>
```

and after the tile loop:

```vue
<div
  v-if="drag"
  data-testid="workspace-ghost"
  class="workspace-ghost"
  :class="{ 'workspace-ghost--invalid': ghostInvalid }"
  :style="placement(drag.target)"
/>
```

Styles:

```css
.workspace-resize { position: absolute; right: 2px; bottom: 2px; z-index: 2; width: 14px; height: 14px; cursor: nwse-resize; border-right: 2px solid var(--color-accent); border-bottom: 2px solid var(--color-accent); border-radius: 0 0 8px 0; }
.workspace-ghost { pointer-events: none; border: 2px dashed var(--color-accent); border-radius: 12px; background: color-mix(in oklch, var(--color-accent) 10%, transparent); }
.workspace-ghost--invalid { border-color: var(--color-danger, #ff6b6b); background: color-mix(in oklch, #ff6b6b 10%, transparent); }
@media (min-width: 768px) { .workspace-ghost { grid-column: var(--col) / span var(--col-span); grid-row: var(--row) / span var(--row-span); } }
```

Check the danger token name in `src/styles/main.css` (`grep -n "danger" src/styles/main.css`) and use it instead of the literal if one exists.

- [ ] **Step 4: Run the tests again**

Expected: PASS.

- [ ] **Step 5: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add src/features/workspace
git commit -m "feat: tiles are dragged and resized with the pointer, and a refused drop snaps back"
```

---

### Task 8: The needs-you queue moves into the page shell

**Files:**
- Create: `src/features/mission/components/NeedsYouQueue.vue`, `src/features/mission/__tests__/NeedsYouQueue.test.ts`, `src/composables/openTask.ts`
- Modify: `src/features/mission/components/NextThing.vue` (drop `remaining`), `src/features/mission/__tests__/NextThing.test.ts`, `src/features/mission/MissionControlView.vue`, `src/App.vue`

**Interfaces:**
- Consumes: `rankNextThings` (`useNextThing.ts:50`), `usePendingPermissions`, `useTasks`, `useAgents`, `NextThing.vue`.
- Produces: `OPEN_TASK: InjectionKey<(taskId: string) => void>` in `src/composables/openTask.ts`, provided by `App.vue`. `<NeedsYouQueue variant="docked" | "strip" />` with test ids `needs-you`, `needs-you-position` ("1 of 3"), `needs-you-prev`, `needs-you-next`. `NextThing` props become `{ next: NextThing | null }`.

- [ ] **Step 1: Write the failing test** — `src/features/mission/__tests__/NeedsYouQueue.test.ts`

Mock `useTasks`, `usePendingPermissions`, `useAgents` and `rankNextThings` the way `KontorTile.test.ts:17-30` mocks its composables (`vi.mock` before a dynamic `import`), with `const items = ref<NextThing[]>([...])` holding three `NextThing` objects titled First, Second, Third, and `rankNextThings: () => items.value`, (`{ kind: 'permission', taskId: 't1', title: 'First', why: 'w', … }` — copy the shape from `useNextThing.ts:16`). Stub `NextThing.vue` with `{ props: ['next'], template: '<p data-testid="stub-next">{{ next ? next.title : "calm" }}</p>' }`.

```ts
it('shows the first item and its position, and pages with the arrows', async () => {
  const w = mount(NeedsYouQueue, { props: { variant: 'docked' } })
  expect(w.get('[data-testid="stub-next"]').text()).toBe('First')
  expect(w.get('[data-testid="needs-you-position"]').text()).toBe('1 of 3')
  await w.get('[data-testid="needs-you-next"]').trigger('click')
  expect(w.get('[data-testid="stub-next"]').text()).toBe('Second')
  await w.get('[data-testid="needs-you-prev"]').trigger('click')
  await w.get('[data-testid="needs-you-prev"]').trigger('click')
  expect(w.get('[data-testid="stub-next"]').text()).toBe('Third')
  w.unmount()
})

// Calm when empty: docked it says so in one line; a strip on another page
// would be noise on every screen, so it renders nothing.
it('is one calm line docked and nothing as a strip when empty', async () => {
  items.value = []
  const docked = mount(NeedsYouQueue, { props: { variant: 'docked' } })
  expect(docked.get('[data-testid="stub-next"]').text()).toBe('calm')
  const strip = mount(NeedsYouQueue, { props: { variant: 'strip' } })
  expect(strip.find('[data-testid="needs-you"]').exists()).toBe(false)
  docked.unmount()
  strip.unmount()
})

it('keeps a valid position when the list shrinks', async () => {
  const w = mount(NeedsYouQueue, { props: { variant: 'docked' } })
  await w.get('[data-testid="needs-you-next"]').trigger('click')
  await w.get('[data-testid="needs-you-next"]').trigger('click')
  items.value = items.value.slice(0, 1)
  await nextTick()
  expect(w.get('[data-testid="stub-next"]').text()).toBe('First')
  w.unmount()
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/mission/__tests__/NeedsYouQueue.test.ts`
Expected: FAIL — component missing.

- [ ] **Step 3: Write the injection key and the queue**

`src/composables/openTask.ts`:

```ts
import type { InjectionKey } from 'vue'

// How a widget opens a task: App.vue owns navigation and provides this.
export const OPEN_TASK: InjectionKey<(taskId: string) => void> = Symbol('openTask')
```

`src/features/mission/components/NeedsYouQueue.vue`:

```vue
<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from 'vue'
import { OPEN_TASK } from '@/composables/openTask'
import { usePendingPermissions } from '@/composables/usePendingPermissions'
import { useAgents } from '@/features/agents'
import { useTasks } from '@/features/pipeline'
import { rankNextThings } from '../composables/useNextThing'
import NextThing from './NextThing.vue'

const props = defineProps<{ variant: 'docked' | 'strip' }>()

const { tasks, refetch } = useTasks()
const { items: pending, refresh } = usePendingPermissions(tasks)
// autoStart: false — App.vue owns the stream.
const { agents } = useAgents({ autoStart: false })
const openTask = inject(OPEN_TASK, () => {})
onMounted(refetch)

const ranked = computed(() => rankNextThings(pending.value, tasks.value, agents.value))
const index = ref(0)
watch(() => ranked.value.length, (n) => {
  if (index.value >= n)
    index.value = 0
})
const current = computed(() => ranked.value[index.value] ?? null)

function step(by: number) {
  const n = ranked.value.length
  index.value = (index.value + by + n) % n
}
</script>

<template>
  <section
    v-if="variant === 'docked' || ranked.length > 0"
    data-testid="needs-you"
    :data-variant="variant"
    aria-label="Needs you"
    :class="variant === 'strip' ? 'rounded-xl border border-warning bg-warning-soft px-4 py-3' : ''"
  >
    <div v-if="ranked.length > 1" class="mb-2 flex items-center gap-2 text-[11px] uppercase tracking-widest text-warning-text">
      <span>Needs you</span>
      <span data-testid="needs-you-position">{{ index + 1 }} of {{ ranked.length }}</span>
      <span class="flex-grow" />
      <button type="button" data-testid="needs-you-prev" aria-label="Previous" class="rounded border border-line-strong px-1.5" @click="step(-1)">
        ‹
      </button>
      <button type="button" data-testid="needs-you-next" aria-label="Next" class="rounded border border-line-strong px-1.5" @click="step(1)">
        ›
      </button>
    </div>
    <NextThing :next="current" @resolved="refresh" @open="openTask" />
  </section>
</template>
```

Check the warning token names used above against `src/styles/main.css` (`grep -n "warning" src/styles/main.css`); `bg-warning-soft`/`text-warning-text` are already used by `CockpitPanel.vue`, `border-warning` may not exist — use `border-line-strong` if it does not.

- [ ] **Step 4: Drop `remaining` from `NextThing`**

In `NextThing.vue` change the props to `defineProps<{ next: NextThing | null }>()` and delete the `mission-remaining` span (`:71-73`); the queue owns the count now. Update `NextThing.test.ts` (remove `remaining` from every mount, delete assertions on `mission-remaining`) and confirm no e2e spec reads that test id: `grep -rn "mission-remaining" tests/e2e` must print nothing. In `MissionControlView.vue` replace the `NextThing` usage and its `ranked`/`next`/`remaining`/`pending` wiring with `<NeedsYouQueue variant="docked" />`.

- [ ] **Step 5: Provide `OPEN_TASK` and render the strip in `App.vue`**

```ts
import { provide } from 'vue'
import { OPEN_TASK } from '@/composables/openTask'
import NeedsYouQueue from '@/features/mission/components/NeedsYouQueue.vue'

provide(OPEN_TASK, taskId => navigateTo({ taskId }))

// The dashboard's triage band shows the same items (AgentTriageBand reads the
// same agents and permission items); the mission view docks the queue itself.
const showNeedsYouStrip = computed(() => activeView.value !== 'dashboard' && activeView.value !== 'mission')
```

Render `<NeedsYouQueue v-if="showNeedsYouStrip" variant="strip" class="mb-3" />` directly above the view `v-if` chain (`App.vue:303`), inside the same container. Put `provide(...)` after `navigateTo` is declared (`App.vue:227`). Replace the `@open-task` handler on `MissionControlView` only if the view no longer emits it (the queue opens tasks through `OPEN_TASK` now; delete the emit from `MissionControlView` if nothing else raises it).

- [ ] **Step 6: Count in the window title**

The title needs the count outside the queue component, so add `src/features/mission/composables/useNeedsYouCount.ts`:

```ts
import { computed } from 'vue'
import { usePendingPermissions } from '@/composables/usePendingPermissions'
import { useAgents } from '@/features/agents'
import { useTasks } from '@/features/pipeline'
import { rankNextThings } from './useNextThing'

export function useNeedsYouCount() {
  const { tasks } = useTasks({ autoStart: false })
  const { items } = usePendingPermissions(tasks)
  const { agents } = useAgents({ autoStart: false })
  return computed(() => rankNextThings(items.value, tasks.value, agents.value).length)
}
```

and in `App.vue`: `const needsYou = useNeedsYouCount()` plus `watchEffect(() => { document.title = needsYou.value ? `(${needsYou.value}) Kontor` : 'Kontor' })`. Check the base title in `index.html` (`grep -n "<title>" index.html`) and use it instead of the literal `Kontor` if it differs. Add a unit test for `useNeedsYouCount` mocking the three composables and `rankNextThings` (length 2 → value 2).

- [ ] **Step 7: Run the tests, gate and commit**

```bash
pnpm exec vitest run src/features/mission
pnpm lint && pnpm typecheck && pnpm test
pnpm test:e2e tests/e2e/question-band.spec.ts tests/e2e/kontor-session.spec.ts
git add src/composables/openTask.ts src/features/mission src/App.vue
git commit -m "feat: what needs you is a queue in the page shell, paged and counted in the title"
```

---

### Task 9: The Kontor tile collapses and grows

**Files:**
- Create: `src/features/mission/components/KontorWidget.vue`, `src/features/mission/__tests__/KontorWidget.test.ts`
- Modify: `src/features/mission/components/KontorTile.vue` (fill its container), `src/features/workspace/widgetSpecs.ts`, `src/features/workspace/widgetRegistry.ts`, `src/features/workspace/widgetRegistry.test.ts`

**Interfaces:**
- Consumes: `useKontorSession()` (`pid`, `status`), `useAgents()`, `KontorTile.vue`.
- Produces: widget `kontor` (spec `[6, 1]` default, `[4, 1]` minimum). Test ids `kontor-collapsed`, `kontor-collapsed-state`, `kontor-collapsed-last`, `kontor-expanded`, `kontor-collapse`.

- [ ] **Step 1: Write the failing test** — `src/features/mission/__tests__/KontorWidget.test.ts`

Mock `useKontorSession` (`{ pid: ref(42), status: ref('running') }`) and `useAgents` (`{ agents: ref([{ pid: 42, status: 'active', working: true, lastOutput: 'PR #467 has four red checks.' }]) }`) as `KontorTile.test.ts` does, and stub `KontorTile.vue` with `{ template: '<div data-testid="stub-kontor-tile" />' }`.

```ts
it('shows state and the last output collapsed', () => {
  const w = mount(KontorWidget, { attachTo: document.body })
  expect(w.get('[data-testid="kontor-collapsed-state"]').text()).toContain('working')
  expect(w.get('[data-testid="kontor-collapsed-last"]').text()).toBe('PR #467 has four red checks.')
  expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
  w.unmount()
})

it('grows on click and on "/", and collapses on Escape', async () => {
  const w = mount(KontorWidget, { attachTo: document.body })
  await w.get('[data-testid="kontor-collapsed"]').trigger('click')
  expect(document.querySelector('[data-testid="kontor-expanded"] [data-testid="stub-kontor-tile"]')).not.toBeNull()
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
  await nextTick()
  expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
  window.dispatchEvent(new KeyboardEvent('keydown', { key: '/' }))
  await nextTick()
  expect(document.querySelector('[data-testid="kontor-expanded"]')).not.toBeNull()
  w.unmount()
})

// "/" typed into a field is text, not a shortcut.
it('ignores "/" typed into an input', async () => {
  const w = mount(KontorWidget, { attachTo: document.body })
  const input = document.createElement('input')
  document.body.appendChild(input)
  input.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true }))
  await nextTick()
  expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
  input.remove()
  w.unmount()
})
```

Also extend `widgetRegistry.test.ts`'s id list with `kontor`.

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/mission/__tests__/KontorWidget.test.ts`
Expected: FAIL.

- [ ] **Step 3: Write the widget** — `src/features/mission/components/KontorWidget.vue`

```vue
<script setup lang="ts">
import { onKeyStroke } from '@vueuse/core'
import { computed, nextTick, ref } from 'vue'
import { useAgents } from '@/features/agents'
import { useKontorSession } from '../composables/useKontorSession'
import KontorTile from './KontorTile.vue'

const { pid, status } = useKontorSession()
const { agents } = useAgents({ autoStart: false })
const agent = computed(() => (pid.value === null ? null : agents.value.find(a => a.pid === pid.value) ?? null))

const state = computed(() => {
  const a = agent.value
  if (!a)
    return status.value === 'starting' ? 'starting' : 'no session'
  if (a.status === 'waiting')
    return 'waiting'
  return a.working ? 'working' : 'idle'
})
const last = computed(() => agent.value?.lastOutput ?? (agent.value ? '' : 'Start Kontor — ask for anything'))

const cell = ref<HTMLElement | null>(null)
const open = ref(false)
const box = ref<Record<string, string>>({})

// Grows out of its own cell towards the larger free side, up to 64% of the
// window, as an overlay: the grid underneath does not re-flow.
function place() {
  const r = cell.value!.getBoundingClientRect()
  const vh = window.innerHeight
  const up = r.top > vh - r.bottom
  const height = Math.min(vh * 0.64, (up ? r.bottom : vh - r.top) - 16)
  box.value = {
    left: `${r.left}px`,
    width: `${r.width}px`,
    height: `${height}px`,
    ...(up ? { bottom: `${vh - r.bottom}px` } : { top: `${r.top}px` }),
  }
}

function grow() {
  place()
  open.value = true
}

async function collapse() {
  open.value = false
  await nextTick()
  cell.value?.focus()
}

function typingIn(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)
}

onKeyStroke('/', (e) => {
  if (open.value || typingIn(e.target))
    return
  e.preventDefault()
  grow()
})
onKeyStroke('Escape', () => {
  if (open.value)
    collapse()
})
</script>

<template>
  <div
    ref="cell"
    data-testid="kontor-collapsed"
    role="button"
    tabindex="0"
    aria-label="Kontor — open the session"
    class="flex h-full min-w-0 cursor-pointer items-center gap-3 rounded-xl border border-line bg-card px-4"
    @click="grow"
    @keydown.enter.prevent="grow"
  >
    <span data-testid="kontor-collapsed-state" class="shrink-0 font-mono text-[11px]" :class="state === 'waiting' ? 'text-warning-text' : state === 'working' ? 'text-success-text' : 'text-fg-mute'">
      ● {{ state }}
    </span>
    <b class="shrink-0 text-[13px] text-fg">Kontor</b>
    <span data-testid="kontor-collapsed-last" class="min-w-0 flex-1 truncate text-[12.5px] text-fg-soft">{{ last }}</span>
    <kbd class="shrink-0 rounded border border-line-strong px-1 font-mono text-[10px] text-fg-mute">/</kbd>
  </div>

  <Teleport to="body">
    <Transition enter-from-class="opacity-0 translate-y-2" leave-to-class="opacity-0 translate-y-2" enter-active-class="transition duration-200 motion-reduce:transition-none" leave-active-class="transition duration-150 motion-reduce:transition-none">
      <div v-if="open" data-testid="kontor-expanded" class="fixed z-40 flex flex-col overflow-hidden rounded-xl border border-accent bg-card shadow-2xl" :style="box">
        <button type="button" data-testid="kontor-collapse" aria-label="Collapse Kontor (Esc)" class="absolute right-2 top-2 z-10 rounded border border-line-strong px-1.5 text-[12px] text-fg-mute" @click="collapse">
          ▾
        </button>
        <KontorTile class="h-full min-h-0 flex-1" />
      </div>
    </Transition>
  </Teleport>
</template>
```

Check `text-success-text` exists (`grep -n "success" src/styles/main.css`); otherwise use the token `LiveWorkRail.vue` uses for a running dot.

- [ ] **Step 4: Let `KontorTile` fill its container**

Replace `min-h-[420px]` in `KontorTile.vue`'s root section with `h-full min-h-0`. Mission control still renders `KontorTile` in a `flex-1` column (`MissionControlView.vue`), so it keeps its height there.

- [ ] **Step 5: Register the widget**

`widgetSpecs.ts`: `spec('kontor', 'Kontor', [6, 1], [4, 1])`. `widgetRegistry.ts`: `'kontor': KontorWidget`.

- [ ] **Step 6: Run the tests, gate and commit**

```bash
pnpm exec vitest run src/features/mission src/features/workspace
pnpm lint && pnpm typecheck && pnpm test
git add src/features/mission src/features/workspace
git commit -m "feat: the Kontor tile shows its last output in one row and grows for input"
```

---

### Task 10: The hub, first version

**Files:**
- Create: `src/features/mission/components/HubWidget.vue`, `src/features/mission/__tests__/HubWidget.test.ts`
- Modify: `src/features/workspace/widgetSpecs.ts`, `src/features/workspace/widgetRegistry.ts`, `src/features/workspace/widgetRegistry.test.ts`

**Interfaces:**
- Consumes: `NeedsYouQueue` (Task 8), `useAgents`, `friendlyProjectName` (`src/utils/friendlyProjectName.ts`).
- Produces: widget `hub` (default `[6, 11]`, minimum `[6, 6]`). Test ids `hub`, `hub-list`, `hub-agent`. This is the list form the spec's hub keeps as its accessible view (key `L`); the zoomable canvas arrives in slice 7 on top of it.

- [ ] **Step 1: Write the failing test** — `HubWidget.test.ts`

Mock `useAgents` with three agents (`status: 'waiting'`, `'active'` + `working: true`, `'idle'`; distinct `pid`, `projectName`), stub `NeedsYouQueue.vue` with `{ props: ['variant'], template: '<div data-testid="stub-queue">{{ variant }}</div>' }`.

```ts
it('docks the queue and lists agents, waiting first', () => {
  const w = mount(HubWidget)
  expect(w.get('[data-testid="stub-queue"]').text()).toBe('docked')
  const rows = w.findAll('[data-testid="hub-agent"]').map(r => r.text())
  expect(rows[0]).toContain('waiting')
  expect(rows[1]).toContain('working')
  expect(rows[2]).toContain('idle')
  w.unmount()
})
```

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Write the widget**

```vue
<script setup lang="ts">
import type { Agent } from '@/types'
import { computed } from 'vue'
import { useAgents } from '@/features/agents'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import NeedsYouQueue from './NeedsYouQueue.vue'

const { agents } = useAgents({ autoStart: false })

function stateOf(a: Agent): 'waiting' | 'working' | 'idle' {
  if (a.status === 'waiting')
    return 'waiting'
  return a.working ? 'working' : 'idle'
}
const ORDER = { waiting: 0, working: 1, idle: 2 } as const
const rows = computed(() =>
  agents.value
    .filter(a => a.status !== 'finished')
    .map(a => ({ a, state: stateOf(a) }))
    .sort((x, y) => ORDER[x.state] - ORDER[y.state]),
)
</script>

<template>
  <section data-testid="hub" aria-label="Zentrale" class="flex h-full min-h-0 flex-col gap-4 overflow-hidden rounded-xl border border-line bg-card p-4">
    <NeedsYouQueue variant="docked" />
    <ul data-testid="hub-list" class="min-h-0 flex-1 overflow-y-auto">
      <li v-for="{ a, state } in rows" :key="a.pid" data-testid="hub-agent" class="flex items-center justify-between gap-3 border-b border-line py-1.5 text-[12.5px]">
        <span class="truncate text-fg">{{ friendlyProjectName(a.projectName) }}</span>
        <span :class="state === 'waiting' ? 'text-warning-text' : state === 'working' ? 'text-success-text' : 'text-fg-mute'">{{ state }}</span>
      </li>
    </ul>
  </section>
</template>
```

Check `friendlyProjectName`'s signature (`grep -n "export function friendlyProjectName" -A3 src/utils/friendlyProjectName.ts`) and pass what it takes.

- [ ] **Step 4: Register it** — `spec('hub', 'Zentrale', [6, 11], [6, 6])` and `'hub': HubWidget`; add `hub` to the registry test's list.

- [ ] **Step 5: Run the tests, gate and commit**

```bash
pnpm exec vitest run src/features/mission src/features/workspace
pnpm lint && pnpm typecheck && pnpm test
git add src/features/mission src/features/workspace
git commit -m "feat: a hub widget docks the needs-you queue above the agents by state"
```

---

### Task 11: The Zentrale replaces Mission control and the cockpit

**Files:**
- Modify: `src/composables/useViewState.ts`, `src/composables/useViewState.test.ts`, `src/utils/navConfig.ts`, `src/utils/navConfig.test.ts`, `src/App.vue`, `src/components/SpotlightSearch.vue`, `src/components/SpotlightSearch.test.ts`, `src/features/mission/__tests__/useReading.test.ts` and `KontorTile.test.ts` if they use `'mission'`
- Delete: `src/features/mission/MissionControlView.vue`, `src/features/cockpit/components/CockpitView.vue` (and their exports, if any)

**Interfaces:**
- Consumes: `WorkspacePage` (Task 5), `useWorkspace` (Task 4), `NeedsYouQueue` (Task 8).
- Produces: `ActiveView = 'zentrale' | 'dashboard' | 'workflows' | 'pipeline' | 'cost' | 'schedules' | 'eval'`, `ACTIVE_VIEWS` with `'zentrale'` first; the default view is `'zentrale'`; a stored `mission` or `cockpit` reads as `zentrale`. Test id `workspace-edit-toggle` on the topbar's *Edit layout* button.

- [ ] **Step 1: Write the failing tests**

In `useViewState.test.ts`: change the default expectation to `'zentrale'` and add

```ts
// The two views folded into the Zentrale. Someone whose last view was one of
// them must land on the Zentrale, not on the invalid-value fallback.
it.each(['mission', 'cockpit'])('reads a stored %s view as zentrale', async (old) => {
  localStorage.setItem('agent-active-view', old)
  const { useViewState } = await freshModule()
  expect(useViewState().activeView.value).toBe('zentrale')
  expect(localStorage.getItem('agent-active-view')).toBe('zentrale')
})
```

In `navConfig.test.ts`: replace the "mission is the first Monitor item" test with

```ts
it('the Zentrale is the first Monitor item and has a title', () => {
  expect(NAV_ITEMS[0].view).toBe('zentrale')
  expect(viewTitle('zentrale')).toBe('Zentrale')
})
```

In `SpotlightSearch.test.ts:116-122`: the free-text case expects `'zentrale'`.

- [ ] **Step 2: Run them and watch them fail**

Run: `pnpm exec vitest run src/composables/useViewState.test.ts src/utils/navConfig.test.ts src/components/SpotlightSearch.test.ts`
Expected: FAIL.

- [ ] **Step 3: Fold the union**

`useViewState.ts`:

```ts
export type ActiveView = 'zentrale' | 'dashboard' | 'workflows' | 'pipeline' | 'cost' | 'schedules' | 'eval'

export const ACTIVE_VIEWS: ActiveView[] = ['zentrale', 'dashboard', 'workflows', 'pipeline', 'cost', 'schedules', 'eval']

// Views folded into the Zentrale; a stored one reads as the Zentrale.
const FOLDED_INTO_ZENTRALE = new Set(['mission', 'cockpit'])
```

In `readInitial`, before the `ACTIVE_VIEWS.includes(stored)` check: `if (stored && FOLDED_INTO_ZENTRALE.has(stored)) { ls?.setItem('agent-active-view', 'zentrale'); return { view: 'zentrale', layout } }` — keep the returned `layout` computed above it. Replace the two fallback literals `'cockpit'` (`:25`, `:28`) with `'zentrale'`.

`navConfig.ts`: replace the `mission` and `cockpit` items with `{ view: 'zentrale', label: 'Zentrale', icon: '◎', group: 'Monitor' }`.

`SpotlightSearch.vue:137`: `activeView.value = 'zentrale'`.

`App.vue`:
- Import `WorkspacePage` from `@/features/workspace` and `useWorkspace`; delete the `MissionControlView` and `CockpitView` imports.
- Replace the two branches (`:310-314`) with `<WorkspacePage v-else-if="activeView === 'zentrale'" page-id="zentrale" />`.
- `:291` becomes `activeView === 'dashboard' || activeView === 'zentrale'`.
- Next to that button add the edit toggle, shown on the Zentrale while the layout is not locked:

```vue
<button
  v-if="activeView === 'zentrale' && !workspace.locked.value"
  type="button"
  data-testid="workspace-edit-toggle"
  :aria-pressed="workspace.editing.value"
  class="h-8 rounded-md border border-line-strong px-3 text-[12.5px] text-fg-soft"
  @click="workspace.editing.value = !workspace.editing.value"
>
  {{ workspace.editing.value ? 'Done' : 'Edit layout' }}
</button>
```

with `const workspace = useWorkspace()` in the script.
- The strip condition becomes: hidden on `dashboard`, and hidden on the Zentrale when its page holds a `hub` tile:

```ts
const pageHasHub = computed(() => activeView.value === 'zentrale' && !!workspace.page('zentrale')?.tiles.some(t => t.widget === 'hub'))
const showNeedsYouStrip = computed(() => activeView.value !== 'dashboard' && !pageHasHub.value)
```

- The view container must give the Zentrale the window's height (rows share it): make sure the element wrapping the `v-if` chain is a flex column with `min-h-0 flex-1`, as it was for Mission control (`grep -n "mission-control" -B3 src/App.vue` before deleting, and keep that wrapper's classes for the Zentrale).

Delete `MissionControlView.vue` and `CockpitView.vue`; remove any export of them (`grep -rn "MissionControlView\|CockpitView" src`). Then:

```bash
grep -rn "'mission'\|'cockpit'" src --include='*.ts' --include='*.vue' | grep -v FOLDED_INTO_ZENTRALE
```

must print only test fixtures that deliberately store the old values (the new `it.each` above).

- [ ] **Step 4: Run the tests again**

Run: `pnpm exec vitest run src/composables src/utils src/components src/features/mission src/features/workspace`
Expected: PASS.

- [ ] **Step 5: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test
git add -A src
git commit -m "feat: the Zentrale replaces Mission control and the cockpit as one editable start page"
```

---

### Task 12: End-to-end specs and docs follow the Zentrale

**Files:**
- Modify: `tests/e2e/cockpit.spec.ts`, `tests/e2e/csp-self-violations.spec.ts`, `tests/e2e/cold-start-rate-limit.spec.ts`, `tests/e2e/kontor-session.spec.ts`, `tests/e2e/sidebar-hover.spec.ts`, `tests/e2e/workflows.spec.ts`, `tests/e2e/dashboard.spec.ts`, `README.md`, `docs/guides/security.md`, `CHANGELOG.md`
- Create: `tests/e2e/zentrale.spec.ts`

- [ ] **Step 1: Write the new spec** — `tests/e2e/zentrale.spec.ts` (follow the fixture and `goto` conventions of `tests/e2e/cockpit.spec.ts`; `waitUntil: 'domcontentloaded'`, never `networkidle`)

```ts
test('the Zentrale is the default page with the nine widgets', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Zentrale')
  for (const id of ['live-work', 'agents', 'routines', 'hub', 'kontor', 'github', 'pipeline', 'memory', 'cost-today'])
    await expect(page.getByTestId(`workspace-tile-${id}`)).toBeVisible()
})

test('a moved tile stays moved after a reload', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('workspace-edit-toggle').click()
  await page.getByTestId('workspace-tile-cost-today').focus()
  await page.keyboard.press('Shift+ArrowUp') // 3×3 → 3×2 frees row 12
  await page.keyboard.press('ArrowDown')
  await page.getByTestId('workspace-edit-toggle').click()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-tile-cost-today')).toHaveAttribute('style', /--row: 11/)
})

test('"/" opens the Kontor tile and Escape closes it', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.locator('body').press('/')
  await expect(page.getByTestId('kontor-expanded')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('kontor-expanded')).toBeHidden()
})
```

Reset the stored layout between tests (a `PATCH /api/settings/workspace.layout` with `{"value":""}` in `test.afterEach`, sending the `Origin` header the API requires — see `.agent-context` lesson "Task API needs Origin header").

- [ ] **Step 2: Update the existing specs**

- `cockpit.spec.ts`: open the Zentrale (default view) instead of the cockpit; `getByTestId('cockpit')` → `getByTestId('workspace-page-zentrale')`; the panel state ids `cockpit-<panel>-<state>` stay. Delete the assertion that the cockpit sits beside the dashboard (`:96-101`); assert instead that the sidebar lists Zentrale then Dashboard.
- `csp-self-violations.spec.ts:25`, `cold-start-rate-limit.spec.ts:17`: `getByTestId('cockpit')` → `getByTestId('workspace-page-zentrale')`.
- `kontor-session.spec.ts:7`: store `'zentrale'`; before `:36-38` press `/` to open the tile, then use `mission-input` inside `kontor-expanded`.
- `sidebar-hover.spec.ts:20,34`: `NAV_LABELS = ['Zentrale', 'Dashboard']` followed by whatever labels it listed after `Dashboard`.
- `workflows.spec.ts:31`, `dashboard.spec.ts:145-148`: the default heading is `'Zentrale'`.

- [ ] **Step 3: Run the e2e suite**

Run: `pnpm test:e2e`
Expected: PASS. The two known flaky specs (`cold-start-rate-limit.spec.ts:22`, `spawn-with-project.spec.ts:66`) may need a rerun; report a rerun explicitly.

- [ ] **Step 4: Docs**

- `README.md:36`: the Zentrale is the default page — a hub with what needs you in the centre, live work and the agents on the left, GitHub, pipeline, memory and today's cost on the right, the Kontor session in a tile below; every tile can be moved, resized, swapped, added or removed with *Edit layout*, and the arrangement is stored on the server so the desktop window and a browser agree. Mention `/` for the Kontor tile.
- `README.md:194`: "the cockpit's **GitHub** panel" → "the **GitHub** tile".
- `docs/guides/security.md:456`: "The cockpit's **Merge**" → "The GitHub tile's **Merge**".
- `CHANGELOG.md` under `## [Unreleased]`, in the existing `### Added` / `### Changed` lists, in the file's `- **Bold sentence.** prose` form:
  - Added: **The Zentrale.** One start page assembled from tiles on a twelve-column grid; *Edit layout* moves, resizes, swaps, adds and removes tiles, by pointer or keyboard; the layout is stored as the `workspace.layout` setting and validated by the server.
  - Added: **The Kontor tile collapses.** It shows the session's state and last output in one row; a click or `/` grows it for input, Escape collapses it.
  - Changed: **Mission control and the cockpit are merged into the Zentrale.** A saved `mission` or `cockpit` view opens the Zentrale. What needs you is a queue in the page shell: docked in the Zentrale's hub, a strip above other pages, its count in the window title.

- [ ] **Step 5: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && pnpm test:e2e
git add tests/e2e README.md docs/guides/security.md CHANGELOG.md
git commit -m "test: end-to-end specs and docs describe the Zentrale"
```

---

### Task 13: Pages of the operator's own

**Files:**
- Modify: `src/composables/useViewState.ts`, `src/composables/useViewState.test.ts`, `src/App.vue`, `src/components/shell/AppSidebar.vue`, `src/components/SpotlightSearch.vue`, `src/features/workspace/components/WorkspaceEditBar.vue`
- Test: `src/components/shell/AppSidebar.test.ts` (create or extend), `tests/e2e/zentrale.spec.ts` (extend)

**Interfaces:**
- Consumes: `addPage`, `renamePage`, `removePage` (Task 2), `useWorkspace` (Task 4).
- Produces: `CoreView` (the former `ActiveView` union), `ActiveView = CoreView | \`page:${string}\``, `isCoreView(v: string): v is CoreView`, `pageIdOf(v: ActiveView): string | null`, `resolveView(v: ActiveView, pageIds: string[]): ActiveView`. Sidebar test ids `nav-pages`, `nav-page-<id>`, `nav-new-page`, `nav-new-page-input`. Edit bar test ids `workspace-rename`, `workspace-delete-page`, `workspace-delete-confirm`.

- [ ] **Step 1: Write the failing tests**

In `useViewState.test.ts`:

```ts
it('keeps a stored page view and recognises core views', async () => {
  localStorage.setItem('agent-active-view', 'page:p-abc')
  const { useViewState, isCoreView, pageIdOf, resolveView } = await freshModule()
  expect(useViewState().activeView.value).toBe('page:p-abc')
  expect(isCoreView('zentrale')).toBe(true)
  expect(isCoreView('page:p-abc')).toBe(false)
  expect(pageIdOf('page:p-abc')).toBe('p-abc')
  expect(pageIdOf('zentrale')).toBeNull()
  // A page that no longer exists falls back instead of blanking the screen.
  expect(resolveView('page:p-gone', ['zentrale'])).toBe('zentrale')
  expect(resolveView('page:p-abc', ['zentrale', 'p-abc'])).toBe('page:p-abc')
})
```

In `AppSidebar.test.ts`: with a mocked `useWorkspace` whose layout holds `zentrale` and `{ id: 'p-a', title: 'Morning', tiles: [] }`, the sidebar lists `nav-page-p-a` with the text `Morning` (not the Zentrale, which is a core item), clicking it sets `activeView` to `page:p-a`; clicking `nav-new-page`, typing `Evening` into `nav-new-page-input` and pressing Enter calls `save` with a layout that has a third page titled `Evening` and switches to it.

- [ ] **Step 2: Run them and watch them fail**

- [ ] **Step 3: Open the union** — `useViewState.ts`

```ts
export type CoreView = 'zentrale' | 'dashboard' | 'workflows' | 'pipeline' | 'cost' | 'schedules' | 'eval'
// A page the operator created. The prefix keeps "is this one of mine?"
// answerable in one line everywhere a view is compared.
export type ActiveView = CoreView | `page:${string}`

export const ACTIVE_VIEWS: CoreView[] = ['zentrale', 'dashboard', 'workflows', 'pipeline', 'cost', 'schedules', 'eval']

export function isCoreView(v: string): v is CoreView {
  return (ACTIVE_VIEWS as string[]).includes(v)
}

export function pageIdOf(v: ActiveView): string | null {
  return v.startsWith('page:') ? v.slice(5) : null
}

export function resolveView(v: ActiveView, pageIds: string[]): ActiveView {
  const id = pageIdOf(v)
  return id === null || pageIds.includes(id) ? v : 'zentrale'
}
```

In `readInitial`, accept a stored value that is a core view or starts with `page:`; existence is checked by `resolveView` once the workspace has loaded (it loads asynchronously, after this runs).

Typecheck now names every place that assumed the closed union (`pnpm typecheck`); `navConfig.ts`'s `NavItemConfig.view` becomes `CoreView`, and `viewTitle(view: ActiveView)` returns the page's title for a page view — pass the title in from `AppTopbar` via `useWorkspace().page(pageIdOf(view))?.title`, or make `viewTitle` take an optional pages list; keep `viewTitle`'s existing tests green. `SpotlightSearch.vue:25-31` maps `ACTIVE_VIEWS` (core views) and adds one entry per workspace page: `{ id: \`view:page:${p.id}\`, label: \`Go to ${p.title}\`, run: () => { activeView.value = \`page:${p.id}\` } }` for every page except `zentrale`.

- [ ] **Step 4: Render a page and fall back** — `App.vue`

After the core branches: `<WorkspacePage v-else-if="pageIdOf(activeView)" :key="activeView" :page-id="pageIdOf(activeView)!" />`. Once the workspace has loaded, `watch([activeView, () => workspace.layout.value], () => { if (workspace.loaded.value) activeView.value = resolveView(activeView.value, workspace.layout.value.pages.map(p => p.id)) })`. The edit toggle and the strip rule (`pageHasHub`) use the current page id: `const currentPageId = computed(() => activeView.value === 'zentrale' ? 'zentrale' : pageIdOf(activeView.value))`.

- [ ] **Step 5: Pages in the sidebar** — `AppSidebar.vue`

After the core groups, a "Pages" group (`data-testid="nav-pages"`) lists `workspace.layout.value.pages` except `zentrale`, each as the same nav item component with icon `▢`, `data-testid="nav-page-<id>"`, active when `activeView === \`page:${id}\``. Below it a `nav-new-page` button that swaps itself for an inline `<input data-testid="nav-new-page-input">`; Enter runs `addPage(layout, title)` and on success `save(r.value.layout)` then `activeView.value = \`page:${r.value.pageId}\`` and turns on edit mode for the new, empty page; Escape or an empty title cancels. No `window.prompt` — a browser dialog blocks automation and is not styled.

- [ ] **Step 6: Rename and delete in the edit bar** — `WorkspaceEditBar.vue`

For a page other than `zentrale`: an input `workspace-rename` bound to the title that runs `renamePage` on blur/Enter, and a two-step delete — `workspace-delete-page` ("Delete page") turns into `workspace-delete-confirm` ("Delete Morning and its tiles?") plus Cancel; confirming runs `removePage`, saves, and switches the view to `zentrale`. Emit these as `rename: [title]` and `remove: []` and handle them in `WorkspacePage.vue`, which owns `save` and has access to `useViewState`.

- [ ] **Step 7: Extend the e2e spec**

```ts
test('a page of my own survives a server restart', async ({ page, request }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('nav-new-page').click()
  await page.getByTestId('nav-new-page-input').fill('Morning')
  await page.getByTestId('nav-new-page-input').press('Enter')
  await page.getByTestId('workspace-add').selectOption('github')
  await page.getByTestId('workspace-add-submit').click()
  await page.getByTestId('workspace-done').click()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.getByText('Morning').click()
  await expect(page.getByTestId('workspace-tile-github')).toBeVisible()
})
```

A reload reads the layout from the server exactly as a restart does (the layout lives only in the settings table); the manual restart check is in Task 14.

- [ ] **Step 8: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && pnpm test:e2e tests/e2e/zentrale.spec.ts
git add -A src tests/e2e/zentrale.spec.ts
git commit -m "feat: the operator creates, names, fills and deletes pages of their own"
```

Then add one bullet to `CHANGELOG.md`'s `### Added`: **Pages of your own.** *+ New page* in the sidebar; a page is filled like the Zentrale, renamed or deleted in its edit mode — and commit it with `docs: note pages of one's own in the changelog`.

---

### Task 14: Verify in the running app

**Files:** none changed unless a defect is found (then: reproduce as a test first, fix, commit).

- [ ] **Step 1: Build and prove the artifact is fresh**

```bash
task build && ls -l --time-style=+%FT%T bin/kontor 2>/dev/null || stat -f '%Sm %N' bin/kontor
```

Stop any stale server this worktree started; do not touch the operator's running desktop instance on port 13120 unless the operator asked (`lsof -nP -iTCP:13120 -sTCP:LISTEN` identifies it). Start `./bin/kontor serve` from this worktree on a free port with a scratch data directory, so the operator's settings are not written by the test (check `./bin/kontor serve --help` for the port and data-dir flags).

- [ ] **Step 2: Walk the Zentrale with Playwright against that server**

Two browser contexts, same server: in the first, enter *Edit layout*, drag `cost-today` one row down with the pointer, click *Done*; reload the second context and assert the same `--row`. Open the Kontor tile with `/`, close it with Escape. Create a page, add a tile, stop and restart the server, open the page again. Take a screenshot of the Zentrale at 1440 × 900 and at 700 px wide (single column) and read both.

- [ ] **Step 3: Report**

Paste: the artifact mtime, the gate output of the last commit, the Playwright run output, and both screenshots' paths. Anything that did not hold is reported as `NOT DONE, open: …`.

---

## Self-Review

**Spec coverage.** Slices 1-5 of the Zentrale spec map to Tasks 1 (registry), 2-4 (layout model, server store, rows sharing height via Task 5's CSS), 5-7 (rendering and edit mode, swap included — the operator's condition that the side tiles be changeable), 11-13 (the Zentrale, the union fold, own pages) and 8-10 (the shell's queue, the Kontor tile, the hub's first form). The spec's error table: unknown widget → Task 5 placeholder; overlap → Tasks 2/6/7 refusal; unreadable layout → Tasks 2/4 lock plus reset; save failure → Task 4 notice; hub removed → Task 11 strip rule; no Kontor session → Task 9 collapsed text. Slices 6-11 (tokens, hub canvas, graph endpoint, live edges, module widgets, large views) are the next plan.

**Placeholder scan.** Steps that depend on a fact not verified while writing name the command that settles it (token names in `main.css`, `useTodayCost` fetching on its own, `friendlyProjectName`'s signature, the `index.html` title, the serve flags) instead of assuming.

**Type consistency.** `WidgetSpec`/`WidgetDef` (Task 1) feed `fitsMinimum` and the pickers (Tasks 2, 6); `PlacedTile`/`WorkspacePage`/`WorkspaceLayout`/`OpResult` (Task 2) are what `useWorkspace` stores (Task 4), `WorkspaceGrid` renders and emits (Tasks 5-7) and the Go validator parses (Task 3, same JSON field names: `version`, `pages`, `id`, `title`, `tiles`, `widget`, `col`, `row`, `colSpan`, `rowSpan`). `ActiveView` changes twice — closed with `zentrale` in Task 11, opened with `page:` in Task 13 — and `CoreView` exists only from Task 13 on.

**The risk this plan is shaped around.** Task 11 changes the one union every view comparison depends on; a value that falls into a chain with no case renders nothing. The fold is tested at the storage boundary (old values map, not fall back), the grep in Task 11 step 3 finds every literal left behind, and Task 13 opens the union only after the fold is green.
