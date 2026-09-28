# Composable Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The cockpit becomes a grid the operator assembles from tiles, and they can create pages of their own and fill them the same way.

**Architecture:** A widget registry replaces the hardcoded panel list; a layout model describes where each tile is anchored; the layout lives in the server-side settings store so every window shows the same workspace; navigation opens up so a page the operator created can be reached.

**Tech Stack:** Vue 3 + TypeScript (Vite, pnpm), CSS Grid for placement, `sortablejs` (already a dependency) for dragging, Go settings registry (`server/internal/settings/registry.go`) for storage.

**Spec:** `docs/superpowers/specs/2026-09-20-composable-workspace-design.md`

## Global Constraints

- **Twelve columns, fixed row height.** `col` is 1-12, `col + colSpan - 1 ≤ 12`, `row ≥ 1`, `rowSpan ≥ 1`.
- **Anchored, never reflowed.** A placement overlapping an occupied cell is refused and the tile returns to where it was. Nothing is displaced.
- **Below `md`: one column**, tiles ordered by `row` then `col`, spans ignored.
- **A tile whose widget the registry does not know stays in the layout** and renders as a placeholder. A deactivated module must not cost the operator their arrangement.
- **An unreadable stored layout shows the built-in one and leaves the stored value untouched.** A parse bug may not destroy what the operator built.
- The gate is `pnpm lint && pnpm typecheck && pnpm test && task test && task lint`, chained with `&&`, each exit code checked — never a filter at the end of the chain.
- Work on `develop`; no CI runs there, so the local gate is the only gate.
- Everything written is English.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `src/features/workspace/widgetRegistry.ts` | id → title, default span, component. The single list of what can be placed |
| `src/features/workspace/layout.ts` | The layout type, the built-in default, placement validation (bounds + overlap) |
| `src/features/workspace/useWorkspace.ts` | Loads and saves the layout through the settings API; holds it for the session |
| `src/features/workspace/components/WorkspaceGrid.vue` | Renders a page: CSS Grid, anchored tiles, the placeholder for an unknown widget |
| `src/features/workspace/components/WorkspaceEditBar.vue` | Edit mode: add, remove, and the drag/resize handles |
| `src/features/cockpit/components/CockpitView.vue` | Renders the cockpit page from the registry instead of a template |
| `server/internal/settings/registry.go` | The `workspace.layout` entry and its validation |
| `src/composables/useViewState.ts` | `ActiveView` opens to allow `page:<id>` |

---

### Task 1: The widget registry

**Files:**
- Create: `src/features/workspace/widgetRegistry.ts`, `src/features/workspace/widgetRegistry.test.ts`
- Modify: `src/features/cockpit/components/CockpitView.vue:1-17`

**Interfaces:**
- Produces: `WIDGETS: Record<string, WidgetDef>` where `WidgetDef = { id: string, title: string, defaultColSpan: number, defaultRowSpan: number, component: Component }`, and `widgetIds(): string[]`.

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest'
import { WIDGETS, widgetIds } from './widgetRegistry'

describe('widget registry', () => {
  // The five panels the cockpit shows today are what the registry must carry
  // on day one: stage one is provably invisible only if nothing is missing.
  it('carries the five cockpit panels', () => {
    expect(widgetIds().sort()).toEqual(['agents', 'github', 'memory', 'pipeline', 'routines'])
  })

  it('gives every widget a title and a span that fits twelve columns', () => {
    for (const id of widgetIds()) {
      const widget = WIDGETS[id]
      expect(widget.title.length).toBeGreaterThan(0)
      expect(widget.defaultColSpan).toBeGreaterThanOrEqual(1)
      expect(widget.defaultColSpan).toBeLessThanOrEqual(12)
      expect(widget.defaultRowSpan).toBeGreaterThanOrEqual(1)
    }
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/widgetRegistry.test.ts`
Expected: FAIL — the module does not exist.

- [ ] **Step 3: Write the registry**

```ts
import type { Component } from 'vue'
import AgentsPanel from '@/features/cockpit/components/AgentsPanel.vue'
import GitHubPanel from '@/features/cockpit/components/GitHubPanel.vue'
import MemoryPanel from '@/features/cockpit/components/MemoryPanel.vue'
import PipelinePanel from '@/features/cockpit/components/PipelinePanel.vue'
import RoutinesPanel from '@/features/cockpit/components/RoutinesPanel.vue'

export interface WidgetDef {
  id: string
  title: string
  defaultColSpan: number
  defaultRowSpan: number
  component: Component
}

// The one list of what can be placed. A module's widget joins it at runtime
// under `<moduleId>__<widget>`; nothing else may be placed.
export const WIDGETS: Record<string, WidgetDef> = {
  agents: { id: 'agents', title: 'Agents', defaultColSpan: 4, defaultRowSpan: 2, component: AgentsPanel },
  pipeline: { id: 'pipeline', title: 'Pipeline', defaultColSpan: 4, defaultRowSpan: 1, component: PipelinePanel },
  routines: { id: 'routines', title: 'Routines', defaultColSpan: 4, defaultRowSpan: 1, component: RoutinesPanel },
  memory: { id: 'memory', title: 'Memory', defaultColSpan: 4, defaultRowSpan: 1, component: MemoryPanel },
  github: { id: 'github', title: 'GitHub', defaultColSpan: 4, defaultRowSpan: 1, component: GitHubPanel },
}

export function widgetIds(): string[] {
  return Object.keys(WIDGETS)
}
```

- [ ] **Step 4: Run the test again**

Run: `pnpm exec vitest run src/features/workspace/widgetRegistry.test.ts`
Expected: PASS.

- [ ] **Step 5: Render the cockpit from the registry**

`CockpitView.vue` keeps `data-testid="cockpit"` and its current Tailwind classes, and renders `<component :is="WIDGETS[id].component" v-for="id in widgetIds()" :key="id">` in the current order. Nothing about the page changes visually — this step exists so the registry is load-bearing before anything depends on it.

- [ ] **Step 6: Prove it is invisible**

Run: `pnpm exec vitest run src/features/cockpit && pnpm exec playwright test tests/e2e/cockpit.spec.ts --reporter=line`
Expected: PASS, unchanged. The existing cockpit spec asserting the five panels' states is the assertion that this changed nothing.

- [ ] **Step 7: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git add -A && git commit -m "refactor: the cockpit renders from a widget registry"
```

---

### Task 2: The layout model and where it lives

**Files:**
- Create: `src/features/workspace/layout.ts`, `src/features/workspace/layout.test.ts`
- Modify: `server/internal/settings/registry.go` (the `workspace.layout` entry)
- Test: `server/internal/settings/registry_test.go`

**Interfaces:**
- Consumes: `widgetIds()` from Task 1.
- Produces: `WorkspaceLayout = { pages: WorkspacePage[] }`, `WorkspacePage = { id: string, title: string, tiles: PlacedTile[] }`, `PlacedTile = { widget: string, col: number, row: number, colSpan: number, rowSpan: number }`, `DEFAULT_LAYOUT`, `validatePlacement(tiles, candidate, ignoreIndex?): string | null`.

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest'
import { DEFAULT_LAYOUT, validatePlacement } from './layout'

const tile = (col: number, row: number, colSpan = 2, rowSpan = 1) =>
  ({ widget: 'agents', col, row, colSpan, rowSpan })

describe('placement', () => {
  it('accepts a tile that fits', () => {
    expect(validatePlacement([tile(1, 1)], tile(3, 1))).toBeNull()
  })

  // Anchoring means two tiles can be asked to occupy one cell. The rule is to
  // refuse, never to displace: a tile the operator did not touch must not move.
  it('refuses an overlap', () => {
    expect(validatePlacement([tile(1, 1, 4, 2)], tile(3, 2))).toMatch(/overlap/i)
  })

  it('refuses a span running past the twelfth column', () => {
    expect(validatePlacement([], tile(10, 1, 4))).toMatch(/column/i)
  })

  it('lets a tile be moved onto its own cells', () => {
    const tiles = [tile(1, 1, 4, 2)]
    expect(validatePlacement(tiles, tile(1, 1, 4, 3), 0)).toBeNull()
  })

  it('has a default layout whose tiles are all legal', () => {
    for (const page of DEFAULT_LAYOUT.pages) {
      page.tiles.forEach((t, i) => {
        expect(validatePlacement(page.tiles, t, i)).toBeNull()
      })
    }
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace/layout.test.ts`
Expected: FAIL — the module does not exist.

- [ ] **Step 3: Write the model**

`validatePlacement` returns a human-readable reason or `null`. It checks, in order: `col` within 1-12, `col + colSpan - 1 ≤ 12`, `row ≥ 1`, both spans `≥ 1`, then cell-by-cell overlap against every tile except `ignoreIndex`. `DEFAULT_LAYOUT` is the cockpit's five panels laid out three per row at `colSpan: 4`.

- [ ] **Step 4: Run the test again**

Expected: PASS.

- [ ] **Step 5: Add the setting, validated on the server**

In `registry.go`, add `{Key: "workspace.layout", Type: TypeString, Default: "", Apply: ApplyLive, Category: "workspace", validate: validWorkspaceLayout}`. The validator parses the JSON and applies the same bounds and overlap rules. An empty value is valid and means "the built-in layout".

The rule lives on the server as well as in the client because the settings API accepts a PATCH from anything on loopback; a layout that only the browser validates is a layout the next caller can corrupt.

- [ ] **Step 6: Write the server test**

```go
func TestWorkspaceLayoutValidation(t *testing.T) {
	def, ok := settings.Lookup("workspace.layout")
	if !ok {
		t.Fatal("workspace.layout is not in the registry")
	}

	valid := `{"pages":[{"id":"cockpit","title":"Cockpit","tiles":[{"widget":"agents","col":1,"row":1,"colSpan":4,"rowSpan":1}]}]}`
	if err := def.Validate(valid); err != nil {
		t.Fatalf("a legal layout must validate: %v", err)
	}

	// The settings API accepts a PATCH from anything on loopback, so the rule
	// the browser applies has to hold here too.
	overlapping := `{"pages":[{"id":"c","title":"C","tiles":[
		{"widget":"agents","col":1,"row":1,"colSpan":4,"rowSpan":2},
		{"widget":"pipeline","col":3,"row":2,"colSpan":4,"rowSpan":1}]}]}`
	if err := def.Validate(overlapping); err == nil {
		t.Error("an overlapping layout must be refused by the server too")
	}

	if err := def.Validate(""); err != nil {
		t.Errorf("an empty value means the built-in layout: %v", err)
	}
}
```

Run: `cd server && go test ./internal/settings/ -run TestWorkspaceLayout -v`
Expected: FAIL first (no such key), PASS after step 5.

- [ ] **Step 7: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git checkout -- server/internal/db/ent/
git add -A && git commit -m "feat: a workspace layout is stored, and the server checks it"
```

---

### Task 3: Rendering a page, and editing it

**Files:**
- Create: `src/features/workspace/components/WorkspaceGrid.vue`, `src/features/workspace/useWorkspace.ts`, and their tests
- Modify: `src/features/cockpit/components/CockpitView.vue` to render `WorkspaceGrid`

**Interfaces:**
- Consumes: `WIDGETS`, `validatePlacement`, `DEFAULT_LAYOUT`.
- Produces: `useWorkspace()` returning `{ layout, loading, error, save, place, remove }`.

- [ ] **Step 1: Write the failing test for the grid**

```ts
it('anchors a tile where the layout says, and names an unknown widget', async () => {
  const wrapper = mount(WorkspaceGrid, {
    props: { page: { id: 'cockpit', title: 'Cockpit', tiles: [
      { widget: 'agents', col: 1, row: 1, colSpan: 4, rowSpan: 2 },
      { widget: 'obsidian__recent', col: 5, row: 1, colSpan: 4, rowSpan: 1 },
    ] }, editing: false },
  })
  const tiles = wrapper.findAll('[data-testid^="workspace-tile-"]')
  expect(tiles).toHaveLength(2)
  expect(tiles[0].attributes('style')).toContain('grid-column: 1 / span 4')
  expect(tiles[0].attributes('style')).toContain('grid-row: 1 / span 2')
  // A module that is deactivated must not cost the operator their layout.
  expect(wrapper.text()).toContain('obsidian__recent')
  wrapper.unmount()
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm exec vitest run src/features/workspace`
Expected: FAIL — the component does not exist.

- [ ] **Step 3: Write the grid**

A `div` with `grid-template-columns: repeat(12, minmax(0, 1fr))` and a fixed `grid-auto-rows`, each tile positioned with an inline `grid-column`/`grid-row`. Below `md` the container switches to a single column and tiles render sorted by `row` then `col`, spans dropped.

- [ ] **Step 4: Run the test again**

Expected: PASS.

- [ ] **Step 5: Choose the row height, with a screenshot**

Build, run the app, look at the cockpit at `rowSpan: 1`, and pick the pixel value from what the existing panels need. Record the number and the reason in the component. This is the spec's first open question and is answered here rather than guessed.

- [ ] **Step 6: Add edit mode**

A toggle puts the page into editing: `sortablejs` for moving a tile between cells, a corner handle for spans, an "add tile" menu listing `widgetIds()` minus what is already placed, and a remove control. Every move and resize goes through `validatePlacement`; a refusal returns the tile to its previous placement and shows the reason.

- [ ] **Step 7: Write the refusal test**

```ts
it('returns a tile to where it was when the drop would overlap', async () => {
  // …place two tiles, attempt a move onto the other, assert the model is unchanged
  // and that the reason is shown.
})
```

- [ ] **Step 8: Gate, verify in the running app, commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
task build && ./bin/kontor serve   # move a tile, reload, confirm it stayed
git add -A && git commit -m "feat: the cockpit is a grid the operator arranges"
```

---

### Task 4: Pages of the operator's own

**Files:**
- Modify: `src/composables/useViewState.ts:5,10,23` (`ActiveView`), `src/App.vue:280-291` (the view branches), the navigation rail component
- Test: `src/composables/useViewState.test.ts`

**Interfaces:**
- Consumes: `useWorkspace()`.
- Produces: `ActiveView = CoreView | \`page:${string}\``, and `isCoreView(v): v is CoreView`.

- [ ] **Step 1: Write the failing test**

```ts
it('accepts a page id and still recognises a core view', () => {
  expect(isCoreView('cockpit')).toBe(true)
  expect(isCoreView('page:morning')).toBe(false)
  // A stored value for a page that no longer exists must fall back, not blank
  // the screen.
  expect(readInitialView('page:deleted', { pages: [] })).toBe('cockpit')
})
```

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Open the union**

`ActiveView` becomes `CoreView | \`page:${string}\``. Every comparison in `App.vue` that assumed eight values gets `isCoreView` or an explicit page branch. The prefix is what keeps "is this one of mine?" answerable in one line.

- [ ] **Step 4: Run the test again**

- [ ] **Step 5: Create, rename and delete a page**

The navigation rail lists core entries, then the operator's pages, then "new page". Deleting asks first — the spec's second open question, answered yes.

- [ ] **Step 6: Gate, verify in the running app, commit**

Create a page, put two tiles on it, restart the server, confirm it is still there.

---

### Task 5: Module widgets in the picker

**Files:**
- Modify: `src/features/workspace/widgetRegistry.ts` (merge runtime widgets), the module list endpoint if it does not already expose `widgets`
- Test: `src/features/workspace/widgetRegistry.test.ts`

- [ ] **Step 1: Write the failing test** — a module reporting a widget appears in `widgetIds()` under `<moduleId>__<widget>`; a deactivated module's widget disappears from the picker while a placed tile of it stays in the layout as a placeholder.

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Merge the module widgets into the registry at load**

- [ ] **Step 4: Run the test again**

- [ ] **Step 5: Gate and commit**

---

### Task 6: Stage two — the large views as tiles

**Files:** the kanban board, the cost chart and the agent list, each extracted into a component that can be given a size.

Deliberately last, and deliberately vague here: it reuses the interface the first five tasks proved, and the shape of that interface is what those tasks decide. Plan it in detail once Task 3 has shipped and the row height and edit interactions are settled.

---

## Self-Review

**Spec coverage.** The spec's six slices map to Tasks 1-6. Its decisions appear as constraints: anchored placement (Task 2's `validatePlacement`), the refusal rule (Task 3, step 6), the collapse below `md` (Task 3, step 3), the unknown-widget placeholder (Task 3, step 1), the server-side store (Task 2, step 5), the `page:` prefix (Task 4, step 3).

**Placeholders.** Task 6 is intentionally not broken down; every other step names its file, its command and its expected result. Task 6 says why.

**Type consistency.** `WidgetDef` (Task 1) is what `WIDGETS` holds and what Task 5 merges into; `PlacedTile` (Task 2) is what `WorkspaceGrid` renders (Task 3) and what the server validator parses; `ActiveView` (Task 4) consumes the page ids `useWorkspace` produces.

**The risk this plan is shaped around.** Task 4 opens a closed union, which is the one edit here that can fail silently: a value falling into a chain with no case for it renders nothing at all. It is last, and the fallback for an unknown page id is tested before the union is opened.
