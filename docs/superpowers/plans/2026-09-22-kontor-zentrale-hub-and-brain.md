# Zentrale Hub and Brain (slices 6–8) plus the slice 1–5 follow-ups — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the Zentrale's hub tile from a list into the zoomable orbit-plus-knowledge-graph the spec describes, give every tile the new frame, and close every follow-up the slices 1–5 run left open.

**Architecture:** A new `hub` feature holds pure geometry, camera and launcher modules (TDD), a camera composable, and DOM components (core, agents, sectors, launchers, controls, minimap, list, cards) layered over one Canvas 2D element that draws the notes and links. The server gains `GET /api/obsidian/graph` (two JsonLogic searches against the Obsidian Local REST API, confined to `obsidian.vaultRoot`, cached 60 s with one shared rebuild, gated by `memory.read`) and `POST /api/obsidian/open`. The widget registry loads widget components lazily, which also brings the index chunk back under the CI bundle budget.

**Tech Stack:** Vue 3 + TypeScript (Vite, pnpm, Vitest, Playwright, Tailwind v4, @vueuse/core), Go 1.26 (chi, ent, `golang.org/x/sync/singleflight` — already in `server/go.mod`).

**Spec:** `docs/superpowers/specs/2026-09-21-kontor-zentrale-design.md` (extends `docs/superpowers/specs/2026-09-20-composable-workspace-design.md`). Visual reference: `docs/superpowers/specs/2026-09-21-kontor-zentrale-prototype.html` — its JavaScript is the reference implementation for the camera, the orbit and the label culling; port from it, do not invent.

## Global Constraints

- No new dependency, frontend or Go. The camera is hand-rolled (spec "Rendering"). `golang.org/x/sync` is already a direct dependency of `server/go.mod`.
- Notes and links render on **Canvas 2D**; core, agents, launchers, sector names, queue and cards are **DOM** on the same camera (spec "Rendering").
- Colours come from theme tokens in `src/styles/main.css`, so the hub follows light and dark mode (spec "Rendering").
- All motion (flights, pulse, Kontor growth) is dropped under `prefers-reduced-motion` (spec "Accessibility").
- The camera is per-window state and is never written to the server layout (spec "Navigation").
- Semantic levels switch at hard thresholds `rel < 1.8` overview, `1.8 ≤ rel < 4` topics, `rel ≥ 4` notes; the scale is clamped to `0.6×–14×` of fit (spec "Semantic zoom", "Navigation").
- Freshness radius: `r = r0 + (rMax − r0) · √(min(age, 730 d) / 730 d)`; sector angle ∝ √(note count) with a 24° floor; a note's position within its sector is a stable hash of its path (spec "One coordinate system").
- Agents never come closer to the core than 116 px on screen, 88 px when waiting (spec "One coordinate system").
- The graph endpoint answers `{ configured: false }` when Obsidian is not configured; results are confined to `obsidian.vaultRoot`; cached in memory 60 s with concurrent requests sharing one rebuild; gated by `memory.read`; no note body is read (spec "Server").
- Server binds `127.0.0.1` only (project rule). Obsidian settings stay `ApplyRestart` — the client is built once at boot (`server/serverapp/di_obsidian.go:25`).
- Cross-feature imports go through a feature's `index.ts` barrel (ESLint `boundary/feature-internals`).
- English for everything that ships; comments only when one of the four comment categories applies (event/lifecycle timing, external contract, non-obvious edge case, case→effect mapping) — one short line.
- SSOT: every constant used in more than one place lives in exactly one place; new canonical locations get a row in `.agent-context/layer2-project-core.md`.
- Docs current in the same branch: `README.md`, `CHANGELOG.md` (Keep a Changelog), `docs/guides/security.md` for new endpoints, the spec's status line.
- Gates (paste raw output): frontend `pnpm lint && pnpm typecheck && pnpm test`; Go `go vet ./...` in `server/` and `go test ./internal/<touched>/...`; then `git checkout -- server/internal/db/ent/` before committing. After `pnpm build` restore `git checkout HEAD -- server/frontend/dist/.gitkeep`.
- Git in a shared worktree: implementers never run `git stash`, `git reset`, `git checkout <branch>`, `git restore` on files they did not change, or `git clean`. To demonstrate a red test, edit the guard out by hand and back in.

## Findings this plan answers (from reading the code and the live vault, 2026-09-22)

- The live Obsidian REST API (plugin 5.1.0) exposes `POST /open/{filename}`, which opens a note in the Obsidian UI. *Open in Obsidian* therefore needs no vault name and no new setting (spec open question 1).
- JsonLogic search returns `[{ "filename": "<vault path>", "result": <value> }]`, only for non-falsy results. `{"var":"stat.mtime"}` → epoch milliseconds (5,693 notes); `{"var":"links"}` → array of vault paths (193 notes). Link targets can point outside `vaultRoot` or at files that are not notes — both are dropped.
- The top level of the operator's vault is `Privat` 5,371 · `claude-memory` 306 · `Arbeit` 10 · `_src` 3 · root 2 · `Test` 1. With √-weighting and the 24° floor, `Privat` gets about 213° — usable, so the spec's one-level rule stays (spec open question 2).
- `pathUnderRoot` (`server/internal/apps/obsidian/client.go:438`) never matches an empty root, and `buildObsidianClient` refuses a partial configuration, so a configured client always has a non-empty `vaultRoot`.
- `memory.Gate.Authorize` records a rate-limit use on every call (see `server/internal/memory/authorize.go`). One graph request is one use, so the client fetches the graph on hub mount and on window focus at most once per 60 s — never on a timer.
- `App.vue:99` counts pending capability decisions in the attention badge, while `rankNextThings` (and so the queue and `document.title`) ignores them.
- The index chunk is 120 KB gzip against a 100 KB budget (`scripts/check-bundle-size.sh:24`, CI `.github/workflows/ci.yml:330-338`). `src/features/workspace/index.ts` re-exports the widget registry, and the registry imports every widget component statically, so every widget lands in the index chunk.

## File structure

New feature `src/features/hub/`:

| File | Responsibility |
| --- | --- |
| `hubGeometry.ts` | Pure: freshness radius, stable hash, sectors (weights, floor), note and agent positions, sector keys from note paths |
| `hubCamera.ts` | Pure: camera type, fit scale, clamp, zoom at a point, screen↔world, levels, fly interpolation |
| `hubLaunchers.ts` | Pure: the up-to-eight launchers from nav items and pages |
| `hubCanvas.ts` | Pure: visible notes, label priority, greedy label culling, note hit-testing |
| `graphApi.ts` | The graph response type (hand-kept parity with the Go handler) |
| `composables/useHubCamera.ts` | Camera state, resize, wheel, drag, flights |
| `composables/useObsidianGraph.ts` | Module-level graph state, fetch policy, derived notes |
| `composables/useHubFocus.ts` | Module-level "fly to this note/agent" request, used by Spotlight and the memory tile |
| `components/HubWidget.vue` | The tile: stage, layers, docked queue, keyboard |
| `components/HubOrbit.vue` | DOM overlay: core, agents, sector names, ring labels |
| `components/HubLaunchers.vue` | Launcher ring / dock |
| `components/HubControls.vue` | Zoom, fit, wide, list, level buttons |
| `components/HubMinimap.vue` | Minimap |
| `components/HubList.vue` | List view (`L`) |
| `components/HubAgentCard.vue`, `components/HubNoteCard.vue` | Cards |
| `components/HubBrainCanvas.vue` | Canvas 2D notes and links |
| `index.ts` | Barrel |

Server: `server/internal/apps/obsidian/client.go` (+`SearchJSONLogic`, `OpenNote`), new `server/internal/apps/obsidian/graph.go`, `server/internal/api/obsidian/handler.go` (+ graph and open routes), new `server/internal/api/obsidian/graph_cache.go`.

---

## Wave 0 — follow-ups that unblock the hub

### Task 1: Lazy widget registry, `WidgetId` union, bundle budget green

**Files:**
- Modify: `src/features/workspace/widgetSpecs.ts`
- Modify: `src/features/workspace/widgetRegistry.ts`
- Test: `src/features/workspace/widgetRegistry.test.ts`

**Interfaces:**
- Produces: `export const WIDGET_IDS` (readonly tuple), `export type WidgetId = typeof WIDGET_IDS[number]`, `WIDGET_SPECS: Record<WidgetId, WidgetSpec>`, `WIDGETS: Record<WidgetId, WidgetDef>` whose `component` is a `defineAsyncComponent` loader, `widgetIds(): WidgetId[]`, `isWidgetId(id: string): id is WidgetId`.
- Consumes: nothing new.

Why: the registry's static imports put all nine widgets (and the cockpit, mission and analytics code they pull in) into the index chunk; making each widget an async component moves them into their own chunks, and also removes the static import edge `workspace → mission/hub`, which Task 14 needs because the hub imports `@/features/workspace`.

- [ ] **Step 1: Measure the budget red (baseline evidence)**

Run: `pnpm build && bash scripts/check-bundle-size.sh server/frontend/dist/assets; echo exit=$?; git checkout HEAD -- server/frontend/dist/.gitkeep`
Expected: `exit=1`, the `index-*.js` line over 100 KB. Paste it into the report.

- [ ] **Step 2: Write the failing tests**

Add to `src/features/workspace/widgetRegistry.test.ts`:

```ts
import { isWidgetId, WIDGET_IDS, WIDGET_SPECS, WIDGETS, widgetIds } from './index'

describe('widget ids', () => {
  it('has one spec and one widget per id, and nothing else', () => {
    expect(Object.keys(WIDGET_SPECS).sort()).toEqual([...WIDGET_IDS].sort())
    expect(widgetIds().sort()).toEqual([...WIDGET_IDS].sort())
  })

  it('recognises known ids and rejects module and unknown ids', () => {
    expect(isWidgetId('hub')).toBe(true)
    expect(isWidgetId('github__prs')).toBe(false)
    expect(isWidgetId('nope')).toBe(false)
  })

  it('loads widget components lazily', () => {
    for (const id of WIDGET_IDS)
      expect((WIDGETS[id].component as { __asyncLoader?: unknown }).__asyncLoader, id).toBeTypeOf('function')
  })
})
```

Keep the existing tests; if an existing test imports a widget component through the registry and mounts it synchronously, change it to `await flushPromises()` after mount.

- [ ] **Step 3: Run the tests to see them fail**

Run: `pnpm vitest run src/features/workspace/widgetRegistry.test.ts`
Expected: FAIL — `WIDGET_IDS`/`isWidgetId` not exported, `__asyncLoader` undefined.

- [ ] **Step 4: Implement**

`src/features/workspace/widgetSpecs.ts` — replace the id-keyed build with a tuple:

```ts
export const WIDGET_IDS = ['kontor', 'hub', 'live-work', 'agents', 'pipeline', 'routines', 'github', 'memory', 'cost-today'] as const
export type WidgetId = typeof WIDGET_IDS[number]

export function isWidgetId(id: string): id is WidgetId {
  return (WIDGET_IDS as readonly string[]).includes(id)
}

export interface WidgetSpec {
  id: WidgetId
  title: string
  defaultColSpan: number
  defaultRowSpan: number
  minColSpan: number
  minRowSpan: number
}

function spec(id: WidgetId, title: string, def: [number, number], min: [number, number]): WidgetSpec {
  return { id, title, defaultColSpan: def[0], defaultRowSpan: def[1], minColSpan: min[0], minRowSpan: min[1] }
}

// Spans from the Zentrale spec's widget table. Plain data on purpose: the layout
// rules import this without pulling in a single component.
export const WIDGET_SPECS: Record<WidgetId, WidgetSpec> = {
  'kontor': spec('kontor', 'Kontor', [6, 1], [4, 1]),
  'hub': spec('hub', 'Zentrale', [6, 11], [6, 6]),
  'live-work': spec('live-work', 'Live work', [3, 5], [3, 3]),
  'agents': spec('agents', 'Agents', [3, 3], [3, 2]),
  'pipeline': spec('pipeline', 'Pipeline', [3, 3], [3, 2]),
  'routines': spec('routines', 'Routines', [3, 4], [3, 2]),
  'github': spec('github', 'GitHub', [3, 3], [3, 2]),
  'memory': spec('memory', 'Memory', [3, 3], [3, 2]),
  'cost-today': spec('cost-today', 'Today', [3, 3], [2, 2]),
}
```

`src/features/workspace/widgetRegistry.ts`:

```ts
import type { Component } from 'vue'
import type { WidgetId, WidgetSpec } from './widgetSpecs'
import { defineAsyncComponent } from 'vue'
import { WIDGET_IDS, WIDGET_SPECS } from './widgetSpecs'

export type WidgetDef = WidgetSpec & { component: Component }

// Each widget is its own chunk: a static import here would put every widget, and
// every feature it imports, into the index chunk that App.vue loads first.
const LOADERS: Record<WidgetId, () => Promise<Component>> = {
  'kontor': () => import('@/features/mission').then(m => m.KontorWidget),
  'hub': () => import('@/features/mission').then(m => m.HubWidget),
  'live-work': () => import('@/features/mission').then(m => m.LiveWorkWidget),
  'agents': () => import('@/features/cockpit').then(m => m.AgentsPanel),
  'pipeline': () => import('@/features/cockpit').then(m => m.PipelinePanel),
  'routines': () => import('@/features/cockpit').then(m => m.RoutinesPanel),
  'github': () => import('@/features/cockpit').then(m => m.GitHubPanel),
  'memory': () => import('@/features/cockpit').then(m => m.MemoryPanel),
  'cost-today': () => import('@/features/analytics').then(m => m.CostTodayWidget),
}

export const WIDGETS: Record<WidgetId, WidgetDef> = Object.fromEntries(
  WIDGET_IDS.map(id => [id, { ...WIDGET_SPECS[id], component: defineAsyncComponent(LOADERS[id]) }]),
) as Record<WidgetId, WidgetDef>

export function widgetIds(): WidgetId[] {
  return [...WIDGET_IDS]
}
```

Callers that index `WIDGETS[id]` / `WIDGET_SPECS[id]` with a plain `string` (layout.ts, WorkspaceGrid.vue, WorkspaceEditBar.vue) now need `isWidgetId(id) ? WIDGETS[id] : undefined` — fix each type error `pnpm typecheck` reports that way; do not widen the records back to `Record<string, …>`.

- [ ] **Step 5: Run tests, typecheck, and measure the budget**

Run: `pnpm vitest run src/features/workspace && pnpm typecheck`
Expected: PASS.

Run: `pnpm build && bash scripts/check-bundle-size.sh server/frontend/dist/assets; echo exit=$?; git checkout HEAD -- server/frontend/dist/.gitkeep`
Expected: `exit=0`, index chunk under 100 KB.

If it is still over: find the largest modules in the index chunk with `pnpm vite build --sourcemap` and a one-off Node script that sums `sourcesContent` lengths per source from `server/frontend/dist/assets/index-*.js.map` (do not commit the script). Move the largest non-shell module behind an existing lazy boundary (`defineAsyncComponent`/dynamic `import()`), repeat, and list what you moved in the report. Do not raise the budget.

- [ ] **Step 6: Run the full frontend gate and the Zentrale e2e**

Run: `pnpm lint && pnpm typecheck && pnpm test`
Run: `pnpm exec playwright test tests/e2e/zentrale.spec.ts --reporter=line`
Expected: all PASS (lazy widgets still render; the specs wait for widget test ids).

- [ ] **Step 7: Commit**

```bash
git add src/features/workspace/widgetSpecs.ts src/features/workspace/widgetRegistry.ts src/features/workspace/widgetRegistry.test.ts <other files typecheck made you touch>
git commit -m "perf: load widget components lazily so the index chunk fits the bundle budget"
```

### Task 2: Correctness batch — title, stale comment, parity, view fallback, error-page attributes

**Files:**
- Modify: `index.html`, `src/App.vue:105-109`
- Modify: `server/internal/api/settings/handler.go:27-29`
- Modify: `server/internal/settings/workspace_layout.go` (+ `workspace_layout_test.go`)
- Modify: `src/composables/useViewState.ts` (+ its test file)
- Modify: `src/components/shell/PageLoadError.vue`

**Interfaces:** Produces nothing new. `resolveView` keeps its signature.

- [ ] **Step 1: Title.** `index.html` `<title>` → `Kontor`. In `src/App.vue` replace the hard-coded `BASE_TITLE` with the document's own title, read once, so `index.html` is the one place the name lives:

```ts
const BASE_TITLE = document.title
```

Update any unit test asserting the old title string (`grep -rn "Claude Code agent monitor" src tests`).

- [ ] **Step 2: Stale comment.** `server/internal/api/settings/handler.go:27-29` claims callers gate `MountWrite` behind admin authorization; `server/internal/api/router.go:355-358` mounts it in the ordinary session-authenticated group. Replace the comment with what is true (one line, external-contract category), e.g.:

```go
// MountWrite registers PATCH /api/settings/{key}; the router mounts it behind session auth only (local-trust posture).
```

- [ ] **Step 3: Go/TS empty-value parity.** TS `parseLayout` treats `raw.trim() === ''` as "no stored layout"; the Go validator checks `raw == ""`. Make Go use `strings.TrimSpace(raw) == ""` for the same decision and add a test case `"   "` → accepted as empty, mirroring the TS behaviour.

- [ ] **Step 4: View fallback — failing tests first** in the `useViewState` test file:

```ts
it('maps page:zentrale to the core zentrale view', () => {
  expect(resolveView('page:zentrale', ['zentrale', 'morning'])).toBe('zentrale')
})
it('falls back when a stored page id does not match the id pattern', () => {
  expect(resolveView('page:Not Valid!', ['Not Valid!'])).toBe('zentrale')
})
```

Run: `pnpm vitest run src/composables` → FAIL.

- [ ] **Step 5: Implement** in `useViewState.ts` (import `PAGE_ID_PATTERN`, `ZENTRALE_PAGE_ID` from `@/features/workspace`):

```ts
export function resolveView(v: ActiveView, pageIds: string[]): ActiveView {
  const id = pageIdOf(v)
  if (id === null)
    return v
  if (id === ZENTRALE_PAGE_ID || !PAGE_ID_PATTERN.test(id) || !pageIds.includes(id))
    return 'zentrale'
  return v
}
```

If importing `@/features/workspace` from `src/composables/useViewState.ts` creates an import cycle that breaks tests (the workspace barrel → `WorkspacePage.vue` → … → `useViewState`), import from `@/features/workspace/layout` is not allowed for features but *is* allowed here (`src/composables` is not a feature) — use `@/features/workspace/layout` then.

Run: `pnpm vitest run src/composables` → PASS.

- [ ] **Step 6: PageLoadError attributes.** Verify the hypothesis first: mount `PageLoadError` via `defineAsyncComponent({ loader: () => Promise.reject(new Error('x')), errorComponent: PageLoadError })` with `class="foo" page-id="p"` and assert the root `<div>` has no `page-id` attribute. If it has (hypothesis confirmed), add `defineOptions({ inheritAttrs: false })` and declare `defineProps<{ error?: Error }>()`; if it has not, write that in the report and leave the component unchanged. Commit the test either way.

- [ ] **Step 7: Gate and commit**

Run: `pnpm lint && pnpm typecheck && pnpm test` and `(cd server && go vet ./... && go test ./internal/settings/... ./internal/api/settings/...) ; git checkout -- server/internal/db/ent/`

```bash
git commit -m "fix: name the page Kontor, fold page:zentrale into the core view, and align the empty-layout check"
```

### Task 3: Layout helpers — frozen default, fail-fast removal, `pageWithWidget`, `widenedTiles`

**Files:**
- Modify: `src/features/workspace/layout.ts`
- Test: `src/features/workspace/layout.test.ts`
- Modify: `src/components/SpotlightSearch.vue` (use `pageWithWidget`), `src/components/SpotlightSearch.test.ts` (reset in `afterEach`)

**Interfaces:**
- Produces:
  - `pageWithWidget(layout: WorkspaceLayout, widget: string): WorkspacePage | null` — the Zentrale page if it holds the widget, else the first own page that does, else `null`.
  - `viewForPage(page: WorkspacePage): ActiveView` is **not** added; callers write `page.id === ZENTRALE_PAGE_ID ? 'zentrale' : \`page:${page.id}\`` via the existing helper if one exists in `layout.ts` (grep `page:`); if none exists add `export function pageView(id: string): ActiveView` here and use it in Spotlight.
  - `widenedTiles(tiles: readonly Tile[], widget: string): Tile[]` (see step 4).
  - `removeTile` throws `RangeError` on an out-of-range index.
  - `DEFAULT_LAYOUT` deep-frozen.

- [ ] **Step 1: Failing tests** in `layout.test.ts`:

```ts
describe('pageWithWidget', () => {
  it('prefers the zentrale page, then own pages in order, else null', () => {
    const l = { version: 1, pages: [
      { id: 'zentrale', title: 'Zentrale', tiles: [{ widget: 'hub', col: 1, row: 1, colSpan: 6, rowSpan: 6 }] },
      { id: 'a', title: 'A', tiles: [{ widget: 'kontor', col: 1, row: 1, colSpan: 6, rowSpan: 1 }] },
      { id: 'b', title: 'B', tiles: [{ widget: 'kontor', col: 1, row: 1, colSpan: 6, rowSpan: 1 }] },
    ] } as WorkspaceLayout
    expect(pageWithWidget(l, 'hub')?.id).toBe('zentrale')
    expect(pageWithWidget(l, 'kontor')?.id).toBe('a')
    expect(pageWithWidget(l, 'github')).toBeNull()
  })
})

describe('removeTile', () => {
  it('throws on an index outside the page', () => {
    expect(() => removeTile(DEFAULT_LAYOUT.pages[0].tiles, 99)).toThrow(RangeError)
  })
})

describe('DEFAULT_LAYOUT', () => {
  it('is frozen down to its tiles', () => {
    expect(Object.isFrozen(DEFAULT_LAYOUT)).toBe(true)
    expect(Object.isFrozen(DEFAULT_LAYOUT.pages[0].tiles[0])).toBe(true)
  })
})

describe('widenedTiles', () => {
  const t = (widget: string, col: number, row: number, colSpan: number, rowSpan: number) => ({ widget, col, row, colSpan, rowSpan })
  it('stretches the target and the tiles inside its column band, hides tiles sharing their rows', () => {
    const out = widenedTiles(DEFAULT_LAYOUT.pages[0].tiles, 'hub')
    expect(out.map(x => x.widget).sort()).toEqual(['hub', 'kontor'])
    expect(out.every(x => x.col === 1 && x.colSpan === 12)).toBe(true)
  })
  it('keeps tiles whose rows are free', () => {
    const tiles = [t('hub', 4, 1, 6, 6), t('github', 1, 8, 3, 2)]
    expect(widenedTiles(tiles, 'hub')).toEqual([t('hub', 1, 1, 12, 6), t('github', 1, 8, 3, 2)])
  })
  it('keeps only the first of two band tiles that would overlap once stretched', () => {
    const tiles = [t('hub', 4, 1, 6, 6), t('kontor', 4, 7, 3, 1), t('memory', 7, 7, 3, 1)]
    expect(widenedTiles(tiles, 'hub').map(x => x.widget)).toEqual(['hub', 'kontor'])
  })
  it('returns the tiles unchanged when the widget is not on the page', () => {
    const tiles = [t('github', 1, 1, 3, 2)]
    expect(widenedTiles(tiles, 'hub')).toEqual(tiles)
  })
})
```

Adjust the literal `WorkspaceLayout` shape (field names such as `version`) to the real type in `layout.ts`.

Run: `pnpm vitest run src/features/workspace/layout.test.ts` → FAIL.

- [ ] **Step 2: Implement** in `layout.ts`:

```ts
export function pageWithWidget(layout: WorkspaceLayout, widget: string): WorkspacePage | null {
  const zentrale = layout.pages.find(p => p.id === ZENTRALE_PAGE_ID)
  if (zentrale?.tiles.some(t => t.widget === widget))
    return zentrale
  return layout.pages.find(p => p.id !== ZENTRALE_PAGE_ID && p.tiles.some(t => t.widget === widget)) ?? null
}

// A wide widget spans the whole page over its own rows; tiles sharing those rows give way for this window only.
export function widenedTiles(tiles: readonly Tile[], widget: string): Tile[] {
  const target = tiles.find(t => t.widget === widget)
  if (!target)
    return [...tiles]
  const first = target.col
  const last = target.col + target.colSpan - 1
  const rowsOf = (t: Tile) => [t.row, t.row + t.rowSpan - 1] as const
  const overlaps = (a: Tile, b: Tile) => rowsOf(a)[0] <= rowsOf(b)[1] && rowsOf(b)[0] <= rowsOf(a)[1]
  const stretched: Tile[] = []
  for (const t of readingOrder(tiles)) {
    if (t.col < first || t.col + t.colSpan - 1 > last)
      continue
    const wide = { ...t, col: 1, colSpan: GRID_COLUMNS }
    if (!stretched.some(s => overlaps(s, wide)))
      stretched.push(wide)
  }
  const kept = tiles.filter(t => (t.col < first || t.col + t.colSpan - 1 > last) && !stretched.some(s => overlaps(s, t)))
  return [...stretched, ...kept]
}
```

Use the existing `readingOrder` helper (it exists in `layout.ts`; match its signature). Make sure `target` itself is first in `stretched` — reading order puts it first in the default layout; if a band tile above the target could come first, sort `stretched` so the target wins (add a test for that ordering if you change it).

`removeTile`: throw `new RangeError(\`no tile at index ${index}\`)` when `index < 0 || index >= tiles.length`.

`DEFAULT_LAYOUT`: wrap in a small `deepFreeze` (recursive `Object.freeze` over arrays and plain objects) at definition.

- [ ] **Step 3: Spotlight uses `pageWithWidget`.** Replace `kontorTargetView()` in `SpotlightSearch.vue:48-54` with `pageWithWidget(layout.value, 'kontor')` mapped to its view. Move the layout reset in `SpotlightSearch.test.ts` (the Morning-page test around lines 119-128) into an `afterEach`.

- [ ] **Step 4: Gate and commit**

Run: `pnpm lint && pnpm typecheck && pnpm test`

```bash
git commit -m "refactor: share the page lookup for a widget and add the wide-tile layout rule"
```

### Task 4: One ranked needs-you list, capability decisions included

**Files:**
- Modify: `src/features/mission/composables/useNextThing.ts` (+ `src/features/mission/__tests__/useNextThing.test.ts`)
- Modify: `src/composables/openTask.ts` (new key `NEEDS_YOU`)
- Modify: `src/App.vue` (rank once, provide)
- Modify: `src/features/mission/components/NeedsYouQueue.vue` (+ its test)

**Interfaces:**
- Produces:
  - `NextKind = 'permission' | 'question' | 'capability' | 'plan'`; `KIND_RANK = { permission: 0, question: 1, capability: 2, plan: 3 }`; `WHY.capability = 'A run is paused until you allow or deny this capability.'`.
  - `NextThing.decision?: PendingCapabilityDecision` (from `@/types` / `@/sdk.generated`).
  - `rankNextThings(items, tasks, agents = [], decisions: PendingCapabilityDecision[] = []): NextThing[]`.
  - `export const NEEDS_YOU: InjectionKey<ComputedRef<NextThing[]>>` in `src/composables/openTask.ts`.
- Consumes: `pendingCapabilityDecisions` from `useAgents` (`src/features/agents/composables/useAgents.ts:23`).

Fixes three final-review minors at once: the ranking ran twice per SSE tick (App and each queue), every page switch re-fetched tasks (`NeedsYouQueue.vue` `onMounted(refetch)`), and the two injections behaved differently when missing.

- [ ] **Step 1: Failing ranking test** in `useNextThing.test.ts`:

```ts
it('ranks a pending capability decision after questions and before plans', () => {
  const decision = { id: 'd1', capability: 'net.fetch', value: 'api.github.com', context: 'routine:nightly', reason: 'not granted', requestedAt: '2026-09-22T10:00:00Z' }
  const planTask = { ...taskFixture({ id: 't-plan', currentStage: 'plan_review' }) }
  const ranked = rankNextThings([], [planTask], [], [decision])
  expect(ranked.map(n => n.kind)).toEqual(['capability', 'plan'])
  expect(ranked[0]).toMatchObject({ kind: 'capability', decision, title: 'net.fetch(api.github.com)', why: WHY.capability })
})
```

Use the file's existing task fixture helper (whatever builds a `PipelineTask` there); a plan item needs whatever makes the existing plan branch fire.

Run: `pnpm vitest run src/features/mission/__tests__/useNextThing.test.ts` → FAIL.

- [ ] **Step 2: Implement** in `useNextThing.ts`: extend the union, `KIND_RANK`, `WHY`, add the parameter, and push one item per decision before the plan loop:

```ts
for (const decision of decisions) {
  out.push({
    kind: 'capability',
    taskId: '',
    taskTitle: '',
    projectName: '',
    stage: '',
    decision,
    title: decision.value ? `${decision.capability}(${decision.value})` : decision.capability,
    why: WHY.capability,
  })
}
```

Keep the final sort by `KIND_RANK` the file already applies (read it; if the function relies on push order instead of a sort, insert this loop between the question and plan loops).

- [ ] **Step 3: Rank once in App, provide it.** In `openTask.ts`:

```ts
import type { ComputedRef } from 'vue'
import type { NextThing } from '@/features/mission'
// App.vue ranks once per tick and provides the list; every queue and the hub core read it.
export const NEEDS_YOU: InjectionKey<ComputedRef<NextThing[]>> = Symbol('needsYou')
```

(export `NextThing` from the mission barrel if it is not yet exported). In `App.vue`:

```ts
const needsYou = computed(() => rankNextThings(permissionItems.value, tasks.value, agents.value, pendingCapabilityDecisions.value))
provide(NEEDS_YOU, needsYou)
const needsYouCount = computed(() => needsYou.value.length)
```

Rename App's existing `needsYou` placement computed (`App.vue:282`) to `needsYouPlace` to avoid the clash, and update its template uses.

- [ ] **Step 4: Queue consumes the ranked list.** In `NeedsYouQueue.vue` drop `useTasks`, `useAgents`, `rankNextThings` and `onMounted(refetch)`; inject `NEEDS_YOU` and throw when missing, and make `OPEN_TASK` throw when missing too (both are provided by App for every place a queue renders):

```ts
const needsYou = inject(NEEDS_YOU)
const openTask = inject(OPEN_TASK)
if (!needsYou || !openTask)
  throw new Error('NeedsYouQueue requires NEEDS_YOU and OPEN_TASK from App.vue')
const ranked = computed(() => props.kinds ? needsYou.value.filter(t => props.kinds!.includes(t.kind)) : needsYou.value)
```

Keep the `PENDING_PERMISSIONS` injection only if the queue still calls its `refresh`/`decide` (it passes actions to `NextThing`); otherwise remove it. Update `NeedsYouQueue.test.ts` (and any other test mounting the queue, e.g. `HubWidget.test.ts`, the App tests) to provide `NEEDS_YOU` with a `computed(() => [...])`.

- [ ] **Step 5: Gate and commit**

Run: `pnpm lint && pnpm typecheck && pnpm test`

```bash
git commit -m "feat: count pending capability decisions in the needs-you queue and rank it once"
```

### Task 5: Capability card in the queue, strip semantics

**Files:**
- Modify: `src/features/mission/components/NextThing.vue` (+ `src/features/mission/__tests__/NextThing.test.ts`)
- Modify: `src/features/mission/components/NeedsYouQueue.vue` (strip `role`/`aria-live`)

**Interfaces:**
- Consumes: `NextThing.kind === 'capability'` with `decision` (Task 4); the resolve call `AgentTriageBand.vue` already uses for capability cards (`src/features/agents/components/AgentTriageBand.vue:829-883`, `useCapabilityDecisions().resolve` → `POST /api/capabilities/decisions/respond`).
- Produces: a capability card with the same actions as the triage band's capability card, reached through the agents barrel (export `useCapabilityDecisions` from `@/features/agents` if it is not exported).

- [ ] **Step 1: Failing component test** in `NextThing.test.ts`: render a `capability` item, assert the headline shows `net.fetch(api.github.com)`, the context line shows `routine:nightly` and the reason, and clicking the first action calls the mocked `resolve` with the decision id and the same decision value the triage band's primary button sends (read the band to get the exact argument). Also assert the 500 ms decision guard applies (a second click within 500 ms does not call `resolve` again) — reuse the guard the permission branch already uses.

- [ ] **Step 2: Implement** the `capability` branch in `NextThing.vue`, reusing the triage band's labels and resolve arguments (read them; do not invent new wording). Plain words for what is asked: *"{context} asks for {capability} on {value}"* followed by the reason.

- [ ] **Step 3: Strip semantics.** The strip variant of `NeedsYouQueue.vue` gets `role="status"` and `aria-live="polite"` on its root, matching the sibling warning boxes; add a one-line assertion to `NeedsYouQueue.test.ts`.

- [ ] **Step 4: Gate and commit**

Run: `pnpm lint && pnpm typecheck && pnpm test`

```bash
git commit -m "feat: answer capability decisions from the needs-you queue"
```

---

## Slice 6 — tokens and tile frame

### Task 6: Hub tokens and the tile frame

**Files:**
- Modify: `src/styles/main.css` (`:root` and `.dark` blocks)
- Modify: `src/features/cockpit/components/CockpitPanel.vue`
- Test: `src/features/cockpit/components/CockpitPanel.test.ts`

**Interfaces:**
- Produces:
  - CSS tokens `--hub-bg` (radial background of the hub stage), `--halo` (ring around notes touched today), `--sector-0` … `--sector-7` (categorical palette for hub sectors), in both themes. The hub reads them via `var(--…)` in templates and `getComputedStyle` on the canvas.
  - `CockpitPanel` props gain `icon?: string`; the header renders `icon` plus the title as an uppercase label, the existing `#action` slot on the right; new optional slot `#figure` renders above the body in `text-[24px] font-semibold leading-none text-fg` when the state is `ready`.
- Consumes: nothing.

Ruling carried in this plan: the existing surface tokens (`--app`, `--card`, `--line`, accent) stay. The prototype's dark values are within one shade of them (`--card #14171c` vs `#12151c`, `--line #242932` vs `#232a37`), the operator said colours do not matter, and retinting every surface of the app is a visual regression risk with no gain. Only the tokens the hub lacks are added.

- [ ] **Step 1: Tokens.** Add to `:root` (light):

```css
  --hub-bg: radial-gradient(circle at 50% 50%, var(--color-slate-100) 0, var(--app) 72%);
  --halo: var(--color-sky-500);
  --sector-0: #4f6bed;
  --sector-1: #16a36a;
  --sector-2: #e06c55;
  --sector-3: #8b67e0;
  --sector-4: #c9921a;
  --sector-5: #0f8fa3;
  --sector-6: #d0537f;
  --sector-7: #6b7a2a;
```

and to `.dark` (the prototype's values, then four more at the same lightness):

```css
  --hub-bg: radial-gradient(circle at 50% 50%, #131821 0, var(--app) 72%);
  --halo: #9fe0ff;
  --sector-0: #7c9cff;
  --sector-1: #3ddc97;
  --sector-2: #f59e8b;
  --sector-3: #c4a7ff;
  --sector-4: #ffd479;
  --sector-5: #5fd4e8;
  --sector-6: #ff8fb8;
  --sector-7: #b9d46a;
```

- [ ] **Step 2: Failing frame test** in `CockpitPanel.test.ts`:

```ts
it('renders the icon, the uppercase label and the figure slot when ready', () => {
  const w = mount(CockpitPanel, {
    props: { id: 'x', title: 'Today', icon: '$', state: 'ready' },
    slots: { figure: '<span data-testid="fig">$4.20</span>', default: '<p>body</p>' },
  })
  const header = w.get('header')
  expect(header.text()).toContain('$')
  expect(header.get('h2').classes()).toContain('uppercase')
  expect(w.find('[data-testid="fig"]').exists()).toBe(true)
})

it('hides the figure while loading', () => {
  const w = mount(CockpitPanel, { props: { id: 'x', title: 'Today', state: 'loading' }, slots: { figure: '<span data-testid="fig">1</span>' } })
  expect(w.find('[data-testid="fig"]').exists()).toBe(false)
})
```

Run: `pnpm vitest run src/features/cockpit/components/CockpitPanel.test.ts` → FAIL.

- [ ] **Step 3: Implement** the header and figure in `CockpitPanel.vue`. The section gains `h-full min-h-0 overflow-hidden`, the ready body `min-h-0 overflow-y-auto`, so a tall list scrolls inside its tile:

```vue
    <header class="flex items-center gap-1.5">
      <span v-if="icon" aria-hidden="true" class="text-[11px] text-fg-mute">{{ icon }}</span>
      <h2 class="text-[10px] font-semibold uppercase tracking-[0.08em] text-fg-mute">
        {{ title }}
      </h2>
      <span class="flex-1" />
      <slot name="action" />
    </header>
    <div v-if="state === 'ready' && $slots.figure" class="text-[24px] font-semibold leading-none text-fg">
      <slot name="figure" />
    </div>
```

Existing tests that assert the title text keep passing (the text is unchanged; only its style changes). Update any snapshot.

- [ ] **Step 4: Gate and commit.** `pnpm lint && pnpm typecheck && pnpm test`; commit `feat: add hub tokens and give the tile frame an icon, label and key figure`.

### Task 7: Every tile in the new frame

**Files (mechanical batch):**
- Modify: `src/features/cockpit/components/AgentsPanel.vue`, `PipelinePanel.vue`, `RoutinesPanel.vue`, `GitHubPanel.vue`, `MemoryPanel.vue` — pass `icon`
- Modify: `src/features/analytics/components/CostTodayWidget.vue` — `icon="$"`, the value moves into `#figure`
- Modify: `src/features/mission/components/LiveWorkWidget.vue` — wrap the rail in `CockpitPanel` (`id="live-work"`, `title="Live work"`, `icon="▶"`, figure = running count)
- Test: update the tests of these components where a selector changed; add one assertion per changed component that the icon renders.

**Interfaces:**
- Consumes: `CockpitPanel` `icon` prop and `#figure` slot (Task 6).

Icons (same glyph family as `src/utils/navConfig.ts` and the prototype): agents `◉`, pipeline `▦`, routines `⟳`, github `⌂`, memory `✎`, cost-today `$`, live-work `▶`.

- [ ] **Step 1:** Pass `icon` in each panel's `<CockpitPanel …>` tag.
- [ ] **Step 2:** `CostTodayWidget.vue`: keep `data-testid="cost-today-value"` on the element now placed in `<template #figure>`.
- [ ] **Step 3:** `LiveWorkWidget.vue`:

```vue
<script setup lang="ts">
import type { PanelState } from '@/features/cockpit'
import { computed } from 'vue'
import { CockpitPanel } from '@/features/cockpit'
import { useTasks } from '@/features/pipeline'
import LiveWorkRail from './LiveWorkRail.vue'

const { tasks } = useTasks({ autoStart: false })

// (keep the existing RUNNING_STAGES comment and set unchanged)
const RUNNING_STAGES = new Set(['plan_review', 'implementation', 'self_review', 'finalization'])
const running = computed(() => tasks.value.filter(t => RUNNING_STAGES.has(t.currentStage)))
const state: PanelState = 'ready'
</script>

<template>
  <CockpitPanel id="live-work" title="Live work" icon="▶" :state="state">
    <template #figure>
      <span data-testid="live-work-count">{{ running.length }}</span>
      <span class="ml-1.5 text-[12px] font-normal text-fg-mute">running</span>
    </template>
    <LiveWorkRail :tasks="running" />
  </CockpitPanel>
</template>
```

If `useTasks` exposes a loading flag (read `src/features/pipeline`), map it to `'loading'` instead of the constant. `LiveWorkRail` renders its own heading today — remove that heading from the rail if it now duplicates the frame's label, and keep its empty-state line.

- [ ] **Step 4: Gate, e2e, commit.** `pnpm lint && pnpm typecheck && pnpm test`; `pnpm exec playwright test tests/e2e/zentrale.spec.ts tests/e2e/cockpit.spec.ts --reporter=line`; commit `feat: frame every Zentrale tile with its icon, label and key figure`.

---

## Kontor tile — follow-ups and the ask request

### Task 8: Kontor overlay follow-ups

**Files:**
- Modify: `src/features/mission/composables/useKontorSession.ts` (+ `useKontorAgent`)
- Modify: `src/features/mission/components/KontorWidget.vue`, `src/features/mission/components/KontorTile.vue`
- Modify: `src/features/mission/composables/useReading.ts` (narrow `view?` to `CoreView`)
- Test: `src/features/mission/__tests__/KontorWidget.test.ts`, `useKontorSession.test.ts`

**Interfaces:**
- Produces: `useKontorAgent(): ComputedRef<Agent | null>` exported from `useKontorSession.ts` — the agents-stream entry whose `pid` is the Kontor session's pid.

Four deferred findings in one area: the agent-by-pid lookup is duplicated in `KontorWidget.vue:9` and `KontorTile.vue:25` (the hub core in Task 14 would be the third copy); the overlay's `scroll` listener (`capture: true` on `window`) also fires for the session terminal's own scrolling and re-runs `getBoundingClientRect` each time; the bare `ease` class does not exist in Tailwind v4 (CSS already defaults `transition-timing-function` to `ease`); `useReading`'s `view?` can be narrowed.

- [ ] **Step 1: Verify the scroll hypothesis first.** In `KontorWidget.test.ts` mount the widget, open it, dispatch a `scroll` event whose target is an element **inside** the teleported overlay, and count calls to `getBoundingClientRect` on the cell (spy on it). Expected today: the count increases (hypothesis confirmed; paste the red run). Then assert the fixed behaviour: a scroll inside the overlay does not re-place; a scroll on `document` does.
- [ ] **Step 2: Implement.** Give the teleported overlay a template ref `overlay`; in the scroll listener return early when `overlay.value?.contains(e.target as Node)`. Remove `ease` from the overlay's class list. Replace both pid lookups with `useKontorAgent()`.
- [ ] **Step 3: Narrow `useReading`.** Type the navigate reading's `view` as `CoreView` (from `@/composables/useViewState`); fix what typecheck reports.
- [ ] **Step 4: Gate and commit.** `pnpm lint && pnpm typecheck && pnpm test`; commit `fix: stop re-placing the Kontor overlay on its own scrolling and share the Kontor agent lookup`.

### Task 9: Ask Kontor — open the tile with a prefilled prompt

**Files:**
- Modify: `src/features/mission/composables/useKontorSession.ts` (`ask`, `openRequested`, `pendingPrompt`, `takePendingPrompt`)
- Modify: `src/features/mission/components/KontorWidget.vue` (grow on request)
- Modify: `src/features/mission/components/KontorTile.vue` (consume the prompt)
- Modify: `src/features/agents/components/AgentSessionPane.vue`, `src/components/PromptInput.vue` (expose `prefill`)
- Modify: `src/features/mission/index.ts` (export `useKontorSession`, `useKontorAgent`)
- Test: `src/features/mission/__tests__/KontorWidget.test.ts`, `KontorTile.test.ts`

**Interfaces:**
- Produces (from `@/features/mission`):
  - `useKontorSession().ask(prefill?: string): void` — sets the pending prompt and requests the tile to open.
  - `useKontorSession().openRequested: Ref<boolean>`, `takePendingPrompt(): string | null`, `pendingPrompt: Ref<string | null>`.
  - `PromptInput` exposes `prefill(text: string): void` (sets the input text, focuses, caret at the end); `AgentSessionPane` exposes `prefill(text: string): void` forwarding to its `PromptInput`.
- Consumes: `useKontorAgent` (Task 8).

- [ ] **Step 1: Failing tests.**
  - `KontorWidget.test.ts`: after `useKontorSession().ask('[[notes/a]] ')`, the widget renders `kontor-expanded` without a click, and `openRequested` is back to `false`.
  - Mounting the widget **after** `ask()` was called also opens it (the request survives a page navigation).
  - `KontorTile.test.ts`: with no agent yet, the tile's own `kontor-input` holds `[[notes/a]] ` after `ask('[[notes/a]] ')`; with an agent, the stubbed `AgentSessionPane`'s exposed `prefill` is called with the text. The pending prompt is consumed once (`takePendingPrompt()` returns `null` afterwards).

Run: `pnpm vitest run src/features/mission` → FAIL.

- [ ] **Step 2: Implement** in `useKontorSession.ts` (module-level, next to the existing state):

```ts
const openRequested = ref(false)
const pendingPrompt = ref<string | null>(null)

function ask(prefill = ''): void {
  pendingPrompt.value = prefill
  openRequested.value = true
}

function takePendingPrompt(): string | null {
  const text = pendingPrompt.value
  pendingPrompt.value = null
  return text
}
```

Return them from `useKontorSession()`.

`KontorWidget.vue`:

```ts
const { openRequested } = useKontorSession()
watch(openRequested, (requested) => {
  if (!requested)
    return
  openRequested.value = false
  if (!open.value)
    grow()
}, { immediate: true, flush: 'post' })
```

(`flush: 'post'` so `cell` is mounted before `grow()` reads its rect.)

`KontorTile.vue`: `const paneRef = ref<InstanceType<typeof AgentSessionPane> | null>(null)`; `watch([pendingPrompt, agent], …, { immediate: true, flush: 'post' })` — when a prompt is pending, `const t = takePendingPrompt()`; if `agent.value` call `paneRef.value?.prefill(t)`, else set the tile's `text` ref to `t` and focus `#kontor-input`.

`PromptInput.vue`: `function prefill(t: string) { promptInput.value = t; nextTick(() => { focus(); …caret to end… }) }` and `defineExpose({ focus, prefill })`. Read how `focus` finds the input element and reuse it for the caret (`setSelectionRange(t.length, t.length)`).

`AgentSessionPane.vue`: `defineExpose({ prefill: (t: string) => promptInputRef.value?.prefill(t) })`.

- [ ] **Step 3: Gate and commit.** `pnpm lint && pnpm typecheck && pnpm test`; commit `feat: let any view open the Kontor tile with a prefilled prompt`.

### Task 10: Wide mode for a tile, per window

**Files:**
- Modify: `src/features/workspace/useWorkspace.ts` (`wide: Ref<string | null>`)
- Modify: `src/features/workspace/components/WorkspacePage.vue` (render `widenedTiles` while wide)
- Modify: `src/App.vue` (clear `wide` on view change and when editing starts)
- Test: `src/features/workspace/components/WorkspacePage.test.ts` (create if missing), `src/features/workspace/useWorkspace.test.ts`

**Interfaces:**
- Produces: `useWorkspace().wide: Ref<string | null>` — the widget id shown wide on the current page; never persisted, never sent to the server.
- Consumes: `widenedTiles` (Task 3).

- [ ] **Step 1: Failing tests.**
  - `WorkspacePage.test.ts`: with the default layout and `wide.value = 'hub'`, the grid receives only `hub` and `kontor`, both `col 1 / colSpan 12`; with `wide.value = null` all nine tiles render; with `editing` on, the stored tiles render even while `wide` is set.
  - `useWorkspace.test.ts`: `save()` never serialises `wide` (the PATCH body equals `serializeLayout(layout)`), and `wide` stays `null` after `load()`.
- [ ] **Step 2: Implement.** `const wide = ref<string | null>(null)` at module level in `useWorkspace.ts`, returned from `useWorkspace()`. In `WorkspacePage.vue` compute `tiles = !editing && wide ? widenedTiles(page.tiles, wide) : page.tiles` and pass those to the grid. In `App.vue`: `watch(activeView, () => { workspace.wide.value = null })` and `watch(() => workspace.editing.value, e => { if (e) workspace.wide.value = null })`.
- [ ] **Step 3: Gate and commit.** `pnpm lint && pnpm typecheck && pnpm test`; commit `feat: show one tile across the full page width for this window`.

---

## Slice 7 — Hub I: orbit, launchers, camera, minimap, list view (live data only)

### Task 11: Hub geometry (pure)

**Files:**
- Create: `src/features/hub/hubGeometry.ts`
- Test: `src/features/hub/hubGeometry.test.ts`

**Interfaces:**
- Produces (exact names; later tasks import them):
  - Constants `R0 = 110`, `R_MAX = 410`, `MAX_AGE_DAYS = 730`, `WORLD_RADIUS = 520`, `WEDGE_INNER = 100`, `WEDGE_OUTER = 440`, `SECTOR_LABEL_RADIUS = 392`, `LAUNCHER_RING_RADIUS = 470`, `SECTOR_FLOOR_DEG = 24`, `AGENT_FLOOR_PX = 116`, `AGENT_WAITING_FLOOR_PX = 88`, `OTHER_SECTOR_KEY = '__other__'`, `RINGS: ReadonlyArray<{ label: string, days: number }>` = today 1, week 7, month 30, year 365.
  - `radiusForAge(ageDays: number): number`
  - `hash01(text: string): number` — FNV-1a 32-bit, `[0, 1)`
  - `interface SectorInput { key: string, label: string, weight: number }`, `interface Sector extends SectorInput { start: number, end: number }` (degrees; 0° = east, clockwise because screen y grows downward; the first sector starts at −90°, i.e. north)
  - `buildSectors(inputs: SectorInput[]): Sector[]`
  - `sectorMid(s: Sector): number`
  - `polar(radius: number, deg: number): [number, number]`
  - `notePoint(path: string, sector: Sector, ageDays: number): [number, number]`
  - `agentAngles(count: number, sector: Sector): number[]`
  - `agentRadius(scale: number, waiting: boolean): number`
  - `sectorKeyFor(path: string, projects: readonly string[]): { key: string, label: string }`
  - `interface SectorPlan { sectors: Sector[], sectorOfNote: Map<string, string>, sectorOfProject: Map<string, string> }`
  - `planSectors(notePaths: readonly string[], projects: readonly string[]): SectorPlan`

- [ ] **Step 1: Failing tests** — `src/features/hub/hubGeometry.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import {
  agentAngles, agentRadius, AGENT_FLOOR_PX, AGENT_WAITING_FLOOR_PX, buildSectors, hash01, MAX_AGE_DAYS, notePoint,
  OTHER_SECTOR_KEY, planSectors, R0, R_MAX, radiusForAge, SECTOR_FLOOR_DEG, sectorKeyFor,
} from './hubGeometry'

const span = (s: { start: number, end: number }) => s.end - s.start

describe('radiusForAge', () => {
  it('starts at r0, grows with age and stops at rMax after two years', () => {
    expect(radiusForAge(0)).toBe(R0)
    expect(radiusForAge(1)).toBeGreaterThan(radiusForAge(0.5))
    expect(radiusForAge(365)).toBeGreaterThan(radiusForAge(30))
    expect(radiusForAge(MAX_AGE_DAYS)).toBeCloseTo(R_MAX)
    expect(radiusForAge(5000)).toBeCloseTo(R_MAX)
    expect(radiusForAge(-3)).toBe(R0)
  })
})

describe('hash01', () => {
  it('is stable and inside [0, 1)', () => {
    expect(hash01('a/b.md')).toBe(hash01('a/b.md'))
    expect(hash01('a/b.md')).not.toBe(hash01('a/c.md'))
    for (const p of ['', 'x', 'Privat/Reise Lissabon.md']) {
      expect(hash01(p)).toBeGreaterThanOrEqual(0)
      expect(hash01(p)).toBeLessThan(1)
    }
  })
})

describe('buildSectors', () => {
  it('sums to 360 degrees and starts at north', () => {
    const s = buildSectors([{ key: 'a', label: 'a', weight: 100 }, { key: 'b', label: 'b', weight: 9 }, { key: 'c', label: 'c', weight: 1 }])
    expect(s.reduce((sum, x) => sum + span(x), 0)).toBeCloseTo(360)
    expect(s[0].start).toBe(-90)
    for (let i = 1; i < s.length; i++) expect(s[i].start).toBeCloseTo(s[i - 1].end)
  })
  it('gives every sector at least the floor', () => {
    const s = buildSectors([{ key: 'privat', label: 'Privat', weight: 5371 }, { key: 'cm', label: 'claude-memory', weight: 306 }, { key: 'x', label: 'x', weight: 1 }, { key: 'y', label: 'y', weight: 2 }])
    for (const x of s) expect(span(x)).toBeGreaterThanOrEqual(SECTOR_FLOOR_DEG - 1e-9)
    expect(span(s.find(x => x.key === 'privat')!)).toBeGreaterThan(span(s.find(x => x.key === 'cm')!))
  })
  it('scales with the square root of the weight above the floor', () => {
    const s = buildSectors([{ key: 'a', label: 'a', weight: 400 }, { key: 'b', label: 'b', weight: 100 }])
    expect(span(s[0]) / span(s[1])).toBeCloseTo(2)
  })
  it('splits evenly when the floor cannot hold', () => {
    const s = buildSectors(Array.from({ length: 20 }, (_, i) => ({ key: `k${i}`, label: `k${i}`, weight: i + 1 })))
    for (const x of s) expect(span(x)).toBeCloseTo(18)
  })
  it('orders sectors by key, so a weight change never reorders them', () => {
    expect(buildSectors([{ key: 'b', label: 'b', weight: 1 }, { key: 'a', label: 'a', weight: 50 }]).map(x => x.key)).toEqual(['a', 'b'])
  })
  it('returns nothing for no input', () => {
    expect(buildSectors([])).toEqual([])
  })
})

describe('notePoint', () => {
  const [sector] = buildSectors([{ key: 'a', label: 'a', weight: 1 }, { key: 'b', label: 'b', weight: 1 }])
  it('is identical across two builds', () => {
    expect(notePoint('a/x.md', sector, 3)).toEqual(notePoint('a/x.md', sector, 3))
  })
  it('stays inside its sector and sits on its freshness radius', () => {
    const [x, y] = notePoint('a/x.md', sector, 30)
    const deg = Math.atan2(y, x) * 180 / Math.PI
    const norm = (d: number) => ((d - sector.start) % 360 + 360) % 360
    expect(norm(deg)).toBeLessThanOrEqual(sector.end - sector.start)
    expect(Math.hypot(x, y)).toBeCloseTo(radiusForAge(30))
  })
})

describe('agents', () => {
  it('spreads agents evenly inside their sector', () => {
    const [s] = buildSectors([{ key: 'a', label: 'a', weight: 1 }, { key: 'b', label: 'b', weight: 1 }])
    expect(agentAngles(1, s)).toEqual([(s.start + s.end) / 2])
    const two = agentAngles(2, s)
    expect(two[0]).toBeGreaterThan(s.start)
    expect(two[1]).toBeLessThan(s.end)
  })
  it('never comes closer to the core than the on-screen floor, at any zoom', () => {
    for (const k of [0.05, 0.3, 1, 2.5, 14]) {
      expect(agentRadius(k, false) * k).toBeGreaterThanOrEqual(AGENT_FLOOR_PX - 1e-9)
      expect(agentRadius(k, true) * k).toBeGreaterThanOrEqual(AGENT_WAITING_FLOOR_PX - 1e-9)
      expect(agentRadius(k, true)).toBeLessThanOrEqual(agentRadius(k, false))
    }
  })
})

describe('sectorKeyFor', () => {
  it('uses the top-level folder', () => {
    expect(sectorKeyFor('Privat/Reise.md', [])).toEqual({ key: 'Privat', label: 'Privat' })
  })
  it('gives a nested folder named like an agent project its own sector', () => {
    expect(sectorKeyFor('claude-memory/private/agent-dashboard/sessions/x.md', ['agent-dashboard']))
      .toEqual({ key: 'claude-memory/private/agent-dashboard', label: 'agent-dashboard' })
  })
  it('matches the project name case-insensitively', () => {
    expect(sectorKeyFor('work/Agent-Context/a.md', ['agent-context']).key).toBe('work/Agent-Context')
  })
  it('puts notes at the root into one root sector', () => {
    expect(sectorKeyFor('Inbox.md', [])).toEqual({ key: '', label: 'Notes' })
  })
})

describe('planSectors', () => {
  it('without notes, gives every agent project its own sector', () => {
    const p = planSectors([], ['kontor', 'shop', 'kontor'])
    expect(p.sectors.map(s => s.key)).toEqual(['kontor', 'shop'])
    expect(p.sectorOfProject.get('shop')).toBe('shop')
  })
  it('with notes, maps agents to a matching sector or to Other', () => {
    const p = planSectors(['Privat/a.md', 'Privat/b.md', 'claude-memory/x/kontor/c.md'], ['kontor', 'shop'])
    expect(p.sectorOfNote.get('Privat/a.md')).toBe('Privat')
    expect(p.sectorOfProject.get('kontor')).toBe('claude-memory/x/kontor')
    expect(p.sectorOfProject.get('shop')).toBe(OTHER_SECTOR_KEY)
    expect(p.sectors.map(s => s.key)).toContain(OTHER_SECTOR_KEY)
  })
  it('adds no Other sector when every agent has one', () => {
    expect(planSectors(['kontor/a.md'], ['kontor']).sectors.map(s => s.key)).toEqual(['kontor'])
  })
})
```

Run: `pnpm vitest run src/features/hub/hubGeometry.test.ts` → FAIL (module missing).

- [ ] **Step 2: Implement** `src/features/hub/hubGeometry.ts`:

```ts
// World units: the hub's "fit all" view shows a disc of about WORLD_RADIUS around the core.
export const R0 = 110
export const R_MAX = 410
export const MAX_AGE_DAYS = 730
export const WORLD_RADIUS = 520
export const WEDGE_INNER = 100
export const WEDGE_OUTER = 440
export const SECTOR_LABEL_RADIUS = 392
export const LAUNCHER_RING_RADIUS = 470
export const SECTOR_FLOOR_DEG = 24
export const AGENT_FLOOR_PX = 116
export const AGENT_WAITING_FLOOR_PX = 88
const AGENT_WORLD_MIN = 82
const AGENT_WAITING_WORLD_MIN = 60
export const OTHER_SECTOR_KEY = '__other__'
export const RINGS: ReadonlyArray<{ label: string, days: number }> = [
  { label: 'today', days: 1 },
  { label: 'week', days: 7 },
  { label: 'month', days: 30 },
  { label: 'year', days: 365 },
]

export function radiusForAge(ageDays: number): number {
  const age = Math.min(Math.max(ageDays, 0), MAX_AGE_DAYS)
  return R0 + (R_MAX - R0) * Math.sqrt(age / MAX_AGE_DAYS)
}

export function hash01(text: string): number {
  let h = 0x811C9DC5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0) / 4294967296
}

export interface SectorInput { key: string, label: string, weight: number }
export interface Sector extends SectorInput { start: number, end: number }

export function buildSectors(inputs: SectorInput[]): Sector[] {
  const sorted = [...inputs].sort((a, b) => a.key.localeCompare(b.key))
  const n = sorted.length
  if (n === 0)
    return []
  const spans = new Array<number>(n).fill(360 / n)
  const roots = sorted.map(s => Math.sqrt(Math.max(s.weight, 0)))
  if (n * SECTOR_FLOOR_DEG < 360 && roots.some(r => r > 0)) {
    const floored = new Set<number>()
    for (;;) {
      const free = 360 - floored.size * SECTOR_FLOOR_DEG
      const total = roots.reduce((sum, r, i) => floored.has(i) ? sum : sum + r, 0)
      let changed = false
      for (let i = 0; i < n; i++) {
        if (floored.has(i))
          continue
        const share = total > 0 ? free * roots[i] / total : free / (n - floored.size)
        if (share < SECTOR_FLOOR_DEG) {
          floored.add(i)
          changed = true
        }
        spans[i] = share
      }
      if (!changed)
        break
    }
    for (const i of floored) spans[i] = SECTOR_FLOOR_DEG
  }
  let at = -90
  return sorted.map((s, i) => {
    const sector = { ...s, start: at, end: at + spans[i] }
    at += spans[i]
    return sector
  })
}

export function sectorMid(s: Sector): number {
  return (s.start + s.end) / 2
}

export function polar(radius: number, deg: number): [number, number] {
  const r = deg * Math.PI / 180
  return [radius * Math.cos(r), radius * Math.sin(r)]
}

export function notePoint(path: string, sector: Sector, ageDays: number): [number, number] {
  const width = sector.end - sector.start
  const margin = Math.min(3, width / 4)
  return polar(radiusForAge(ageDays), sector.start + margin + hash01(path) * (width - 2 * margin))
}

export function agentAngles(count: number, sector: Sector): number[] {
  return Array.from({ length: count }, (_, i) => sector.start + (sector.end - sector.start) * (i + 1) / (count + 1))
}

// World radius that keeps the agent at least its floor away from the core on screen.
export function agentRadius(scale: number, waiting: boolean): number {
  return waiting
    ? Math.max(AGENT_WAITING_WORLD_MIN, AGENT_WAITING_FLOOR_PX / scale)
    : Math.max(AGENT_WORLD_MIN, AGENT_FLOOR_PX / scale)
}

export function sectorKeyFor(path: string, projects: readonly string[]): { key: string, label: string } {
  const folders = path.split('/').slice(0, -1)
  const wanted = new Set(projects.map(p => p.toLowerCase()))
  const hit = folders.findIndex(f => wanted.has(f.toLowerCase()))
  if (hit >= 0)
    return { key: folders.slice(0, hit + 1).join('/'), label: folders[hit] }
  return folders.length ? { key: folders[0], label: folders[0] } : { key: '', label: 'Notes' }
}

export interface SectorPlan { sectors: Sector[], sectorOfNote: Map<string, string>, sectorOfProject: Map<string, string> }

export function planSectors(notePaths: readonly string[], projects: readonly string[]): SectorPlan {
  const distinct = [...new Set(projects)]
  const sectorOfNote = new Map<string, string>()
  const sectorOfProject = new Map<string, string>()
  if (notePaths.length === 0) {
    for (const p of distinct) sectorOfProject.set(p, p)
    return { sectors: buildSectors(distinct.map(p => ({ key: p, label: p, weight: 1 }))), sectorOfNote, sectorOfProject }
  }
  const inputs = new Map<string, SectorInput>()
  for (const path of notePaths) {
    const { key, label } = sectorKeyFor(path, distinct)
    sectorOfNote.set(path, key)
    const input = inputs.get(key)
    if (input)
      input.weight++
    else inputs.set(key, { key, label, weight: 1 })
  }
  for (const p of distinct) {
    const match = [...inputs.values()].find(s => s.label.toLowerCase() === p.toLowerCase())
    sectorOfProject.set(p, match?.key ?? OTHER_SECTOR_KEY)
  }
  if ([...sectorOfProject.values()].includes(OTHER_SECTOR_KEY))
    inputs.set(OTHER_SECTOR_KEY, { key: OTHER_SECTOR_KEY, label: 'Other', weight: 1 })
  return { sectors: buildSectors([...inputs.values()]), sectorOfNote, sectorOfProject }
}
```

- [ ] **Step 3: Run the tests.** `pnpm vitest run src/features/hub/hubGeometry.test.ts` → PASS. If a test fails because the floor loop's result is off by float error, fix the implementation, not the tolerance.

- [ ] **Step 4: Commit.** `feat: add the hub's coordinate system as pure geometry`.

### Task 12: Hub camera and launchers (pure)

**Files:**
- Create: `src/features/hub/hubCamera.ts`, `src/features/hub/hubLaunchers.ts`
- Test: `src/features/hub/hubCamera.test.ts`, `src/features/hub/hubLaunchers.test.ts`

**Interfaces:**
- Produces from `hubCamera.ts`:
  - `interface Camera { k: number, tx: number, ty: number }` (screen = world · k + t)
  - `type HubLevel = 0 | 1 | 2`; constants `MIN_REL = 0.6`, `MAX_REL = 14`, `LEVEL_TOPICS = 1.8`, `LEVEL_NOTES = 4`, `DOCK_REL = 1.5`, `FLY_MS = 480`, `LEVEL_TARGETS: Record<HubLevel, number> = { 0: 1, 1: 2.4, 2: 5.5 }`
  - `fitScale(width: number, height: number): number`
  - `levelOf(rel: number): HubLevel`
  - `clampScale(k: number, k0: number): number`
  - `toScreen(cam: Camera, x: number, y: number): [number, number]`, `toWorld(cam: Camera, sx: number, sy: number): [number, number]`
  - `zoomAt(cam: Camera, factor: number, mx: number, my: number, k0: number): Camera`
  - `centredOn(wx: number, wy: number, k: number, width: number, height: number): Camera`
  - `easeInOut(p: number): number`
  - `flyFrame(from: Camera, to: { wx: number, wy: number, k: number }, width: number, height: number, p: number): Camera`
- Produces from `hubLaunchers.ts`:
  - `interface Launcher { id: string, label: string, icon: string, kind: 'view' | 'new-page' | 'more', view?: ActiveView }`
  - `MAX_LAUNCHERS = 8`
  - `launchersFor(items: ReadonlyArray<{ view: CoreView, label: string, icon: string }>, pages: ReadonlyArray<{ id: string, title: string }>, current: ActiveView): Launcher[]`
  - `launcherSlotDeg(index: number): number` — `-67.5 + index * 45`

- [ ] **Step 1: Failing tests.** `hubCamera.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { centredOn, clampScale, fitScale, flyFrame, levelOf, MAX_REL, MIN_REL, toScreen, toWorld, zoomAt } from './hubCamera'

describe('levelOf', () => {
  it('switches at 1.8 and 4', () => {
    expect(levelOf(1.79)).toBe(0)
    expect(levelOf(1.8)).toBe(1)
    expect(levelOf(3.99)).toBe(1)
    expect(levelOf(4)).toBe(2)
  })
})

describe('camera maths', () => {
  const cam = { k: 2, tx: 100, ty: 50 }
  it('round-trips screen and world', () => {
    const [sx, sy] = toScreen(cam, 12, -7)
    expect(toWorld(cam, sx, sy)).toEqual([12, -7])
  })
  it('keeps the point under the pointer fixed while zooming', () => {
    const before = toWorld(cam, 300, 200)
    const next = zoomAt(cam, 1.5, 300, 200, 1)
    const after = toWorld(next, 300, 200)
    expect(after[0]).toBeCloseTo(before[0])
    expect(after[1]).toBeCloseTo(before[1])
  })
  it('clamps to 0.6×–14× of fit', () => {
    expect(clampScale(0.01, 1)).toBe(MIN_REL)
    expect(clampScale(99, 1)).toBe(MAX_REL)
    expect(zoomAt({ k: 14, tx: 0, ty: 0 }, 2, 0, 0, 1).k).toBe(14)
  })
  it('fits the world disc into the stage', () => {
    expect(fitScale(1090, 1130)).toBeCloseTo(1)
    expect(fitScale(10, 10)).toBeGreaterThan(0)
  })
  it('flies from the start to exactly the target', () => {
    const from = { k: 1, tx: 0, ty: 0 }
    const end = flyFrame(from, { wx: 40, wy: -20, k: 3 }, 800, 600, 1)
    expect(end).toEqual(centredOn(40, -20, 3, 800, 600))
    const start = flyFrame(from, { wx: 40, wy: -20, k: 3 }, 800, 600, 0)
    expect(start.k).toBeCloseTo(1)
  })
})
```

`hubLaunchers.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { NAV_ITEMS } from '@/utils/navConfig'
import { launchersFor, MAX_LAUNCHERS } from './hubLaunchers'

describe('launchersFor', () => {
  it('lists the other core views and ends with New page', () => {
    const l = launchersFor(NAV_ITEMS, [], 'zentrale')
    expect(l.map(x => x.id)).toEqual([...NAV_ITEMS.filter(i => i.view !== 'zentrale').map(i => i.view), 'new-page'])
    expect(l.at(-1)!.kind).toBe('new-page')
  })
  it('adds own pages after the core views', () => {
    const l = launchersFor(NAV_ITEMS, [{ id: 'morning', title: 'Morning' }], 'zentrale')
    expect(l.at(-2)).toMatchObject({ id: 'page:morning', label: 'Morning', view: 'page:morning', kind: 'view' })
  })
  it('turns the eighth slot into More when there are more than eight', () => {
    const pages = [{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }, { id: 'c', title: 'C' }]
    const l = launchersFor(NAV_ITEMS, pages, 'zentrale')
    expect(l).toHaveLength(MAX_LAUNCHERS)
    expect(l.at(-1)).toMatchObject({ id: 'more', kind: 'more' })
  })
  it('leaves out the view the hub is on', () => {
    expect(launchersFor(NAV_ITEMS, [{ id: 'a', title: 'A' }], 'page:a').map(x => x.id)).not.toContain('page:a')
  })
})
```

Run: `pnpm vitest run src/features/hub` → FAIL.

- [ ] **Step 2: Implement** `hubCamera.ts`:

```ts
export interface Camera { k: number, tx: number, ty: number }
export type HubLevel = 0 | 1 | 2

export const MIN_REL = 0.6
export const MAX_REL = 14
export const LEVEL_TOPICS = 1.8
export const LEVEL_NOTES = 4
export const DOCK_REL = 1.5
export const FLY_MS = 480
export const LEVEL_TARGETS: Record<HubLevel, number> = { 0: 1, 1: 2.4, 2: 5.5 }

// The prototype's fit: the disc of radius 490 plus room for the controls (110 px) and the queue (150 px).
export function fitScale(width: number, height: number): number {
  return Math.max(0.05, Math.min(width - 110, height - 150) / (2 * 490))
}

export function levelOf(rel: number): HubLevel {
  return rel < LEVEL_TOPICS ? 0 : rel < LEVEL_NOTES ? 1 : 2
}

export function clampScale(k: number, k0: number): number {
  return Math.max(k0 * MIN_REL, Math.min(k0 * MAX_REL, k))
}

export function toScreen(cam: Camera, x: number, y: number): [number, number] {
  return [x * cam.k + cam.tx, y * cam.k + cam.ty]
}

export function toWorld(cam: Camera, sx: number, sy: number): [number, number] {
  return [(sx - cam.tx) / cam.k, (sy - cam.ty) / cam.k]
}

export function zoomAt(cam: Camera, factor: number, mx: number, my: number, k0: number): Camera {
  const k = clampScale(cam.k * factor, k0)
  const q = k / cam.k
  return { k, tx: mx - (mx - cam.tx) * q, ty: my - (my - cam.ty) * q }
}

export function centredOn(wx: number, wy: number, k: number, width: number, height: number): Camera {
  return { k, tx: width / 2 - wx * k, ty: height / 2 - wy * k }
}

export function easeInOut(p: number): number {
  return p < 0.5 ? 2 * p * p : 1 - (-2 * p + 2) ** 2 / 2
}

// Zoom interpolates in log space so a 1×→10× flight does not spend its first half barely moving.
export function flyFrame(from: Camera, to: { wx: number, wy: number, k: number }, width: number, height: number, p: number): Camera {
  if (p >= 1)
    return centredOn(to.wx, to.wy, to.k, width, height)
  const q = easeInOut(Math.max(0, p))
  const [cx, cy] = toWorld(from, width / 2, height / 2)
  const k = Math.exp(Math.log(from.k) + (Math.log(to.k) - Math.log(from.k)) * q)
  return centredOn(cx + (to.wx - cx) * q, cy + (to.wy - cy) * q, k, width, height)
}
```

`hubLaunchers.ts`:

```ts
import type { ActiveView, CoreView } from '@/composables/useViewState'

export interface Launcher { id: string, label: string, icon: string, kind: 'view' | 'new-page' | 'more', view?: ActiveView }

export const MAX_LAUNCHERS = 8

export function launchersFor(
  items: ReadonlyArray<{ view: CoreView, label: string, icon: string }>,
  pages: ReadonlyArray<{ id: string, title: string }>,
  current: ActiveView,
): Launcher[] {
  const views: Launcher[] = [
    ...items.filter(i => i.view !== current).map(i => ({ id: i.view, label: i.label, icon: i.icon, kind: 'view' as const, view: i.view })),
    ...pages
      .filter(p => `page:${p.id}` !== current)
      .map(p => ({ id: `page:${p.id}`, label: p.title, icon: '▣', kind: 'view' as const, view: `page:${p.id}` as ActiveView })),
  ]
  const newPage: Launcher = { id: 'new-page', label: 'New page', icon: '+', kind: 'new-page' }
  if (views.length + 1 <= MAX_LAUNCHERS)
    return [...views, newPage]
  return [...views.slice(0, MAX_LAUNCHERS - 1), { id: 'more', label: 'More…', icon: '…', kind: 'more' }]
}

export function launcherSlotDeg(index: number): number {
  return -67.5 + index * 45
}
```

`pages` passed by callers must already exclude the Zentrale page (`ZENTRALE_PAGE_ID`).

- [ ] **Step 3: Run tests** → PASS. **Step 4: Commit** `feat: add the hub camera and launcher rules as pure functions`.

### Task 13: `useHubCamera` composable

**Files:**
- Create: `src/features/hub/composables/useHubCamera.ts`
- Test: `src/features/hub/__tests__/useHubCamera.test.ts`

**Interfaces:**
- Produces:

```ts
export interface HubCameraOptions {
  /** A click that did not move the pointer more than 3 px, in stage coordinates. */
  onTap?: (sx: number, sy: number) => void
}
export function useHubCamera(stage: Ref<HTMLElement | null>, options?: HubCameraOptions): {
  cam: ShallowRef<Camera>
  k0: Ref<number>
  size: Readonly<Ref<{ width: number, height: number }>>
  rel: ComputedRef<number>
  level: ComputedRef<HubLevel>
  docked: ComputedRef<boolean>
  dragging: Ref<boolean>
  zoomBy: (factor: number, mx?: number, my?: number) => void
  panBy: (dx: number, dy: number) => void
  flyTo: (wx: number, wy: number, rel: number) => void
  fit: () => void
  centreWorld: () => [number, number]
}
```

- Consumes: `hubCamera.ts` (Task 12).

Behaviour (port from the prototype, `docs/superpowers/specs/2026-09-21-kontor-zentrale-prototype.html:382-415`):
- **Resize** (`useResizeObserver` from `@vueuse/core`): the first size centres the core at fit (`rel = 1`); later sizes keep the world point at the centre and the current `rel` (`k0 = fitScale(w, h)`, `k = k0 · rel`).
- **Wheel** on the stage (`passive: false`, `preventDefault`): `zoomBy(Math.exp(-deltaY * (ctrlKey ? 0.012 : 0.0016)), offsetX, offsetY)` — `ctrlKey` is how trackpad pinch arrives.
- **Drag** on the stage background: `pointerdown` ignored when the target is inside `button, input, a, [data-hub-layer]` (cards, queue, list, controls carry `data-hub-layer`); otherwise capture the pointer and pan with the movement; `dragging` true while held. On `pointerup` without a >3 px move call `onTap(offsetX, offsetY)`. `pointercancel` ends the drag without a tap.
- **flyTo**: animate with `requestAnimationFrame` over `FLY_MS` using `flyFrame`; a new flight or any manual zoom/pan cancels the running one; under `prefers-reduced-motion: reduce` (`usePreferredReducedMotion`) jump to the end frame at once. Target `k = clampScale(k0 · rel, k0)`.
- `fit()` = `flyTo(0, 0, 1)`. `centreWorld()` = `toWorld(cam, width / 2, height / 2)`.
- Cancel any running animation frame on unmount.

- [ ] **Step 1: Failing tests** (jsdom; stub `ResizeObserver` with a class whose `observe` records the callback, and drive it by calling that callback with `[{ contentRect: { width: 1090, height: 1130 } }]`; stub `matchMedia` for reduced motion):
  - after the first resize `rel.value === 1` and the core (world 0,0) is at the stage centre;
  - `zoomBy(2)` doubles `rel` and keeps the centre fixed; zooming beyond 14× clamps;
  - with reduced motion, `flyTo(100, 0, 3)` sets `rel` to 3 and centres (100, 0) synchronously;
  - a pointerdown → pointerup at the same spot calls `onTap` once; a pointerdown → move 20 px → up pans by 20 px and does not call `onTap`; a pointerdown on a `<button>` inside the stage neither pans nor taps; `pointercancel` does not tap;
  - after a second resize to half the width the centre world point and `rel` are unchanged.
- [ ] **Step 2: Implement** as specified.
- [ ] **Step 3: Gate and commit.** `pnpm lint && pnpm typecheck && pnpm test`; commit `feat: add the hub camera composable`.

### Task 14: The hub feature and the orbit

**Files:**
- Create: `src/features/hub/index.ts`, `src/features/hub/components/HubWidget.vue`, `src/features/hub/components/HubOrbit.vue`
- Move: `src/features/mission/components/HubWidget.vue` → delete; `src/features/mission/__tests__/HubWidget.test.ts` → `src/features/hub/__tests__/HubWidget.test.ts` (rewritten)
- Modify: `src/features/mission/index.ts` (drop `HubWidget`), `src/features/workspace/widgetRegistry.ts` (hub loader → `@/features/hub`)
- Test: `src/features/hub/__tests__/HubWidget.test.ts`, `src/features/hub/__tests__/HubOrbit.test.ts`

**Interfaces:**
- Produces: `HubWidget` exported from `@/features/hub`. `HubOrbit` props:

```ts
defineProps<{
  cam: Camera
  sectors: Sector[]
  agents: ReadonlyArray<{ agent: Agent, x: number, y: number, state: AgentDisplayStatus, needsOperator: boolean }>
  level: HubLevel
  running: number
  waiting: number
  needsYou: number
  kontorState: string
}>()
defineEmits<{ core: [], agent: [agent: Agent], sector: [sector: Sector] }>()
```

- Consumes: `hubGeometry` (Task 11), `hubCamera`/`useHubCamera` (Tasks 12–13), `NEEDS_YOU` (Task 4), `useKontorSession().ask`, `useKontorAgent` (Tasks 8–9), `useAgents` (`@/features/agents`), `NeedsYouQueue` (`@/features/mission`), `agentDisplayStatus` (`@/utils/statusColors`), `friendlyProjectName` (`@/utils/friendlyProjectName`).

What the tile shows at this task's end (live data only — no vault yet):
- A focusable stage (`data-testid="hub-stage"`, `tabindex="0"`, `role="application"`, `aria-roledescription="zoomable map"`, `aria-label="Zentrale. Arrow keys pan, plus and minus zoom, 0 shows all, F widens, L lists."`, `:data-level="level"`) with the background `var(--hub-bg)`.
- A world SVG layer (`aria-hidden`, `pointer-events: none`) transformed by the camera: sector wedges (`WEDGE_INNER`–`WEDGE_OUTER`, fill `var(--sector-i)` at 0.035 opacity) and the four freshness rings (dashed, `vector-effect: non-scaling-stroke`, stroke `var(--line)`).
- `HubOrbit` (DOM, screen space, constant size): the core (80 px disc, `Kontor`, `"{running} running · {waiting} waiting"`, plus a needs-you badge when `needsYou > 0`), each agent as a button (dot + label `friendlyProjectName(projectName)` + state word; `waiting` dot pulses with `motion-safe:` only; `aria-label="{name}, {state}"`), sector names at `SECTOR_LABEL_RADIUS` (buttons, coloured by their sector token, count badge; `opacity .55` at level 1, hidden at level 2), ring labels at −128° (hidden at level 2). Elements are positioned with `transform: translate(x, y)` from `toScreen`.
- Agents: `planSectors([], projects)` gives the sectors; each agent sits at `agentAngles` within its project's sector at `agentRadius(cam.k, needsOperator)`. The spec's "waits for the operator" means *needs you*, **not** the agent status `waiting` (that is only the 30 s–5 min quiet bucket from `server/internal/merger/merger.go`). So `needsOperator = !!(agent.pendingQuestion || agent.pendingConfirm || agent.pendingPermissions?.length)`; colour and word come from `agentDisplayStatus`, and needs-you agents additionally get the warning ring and the pulse.
- Clicking the core calls `useKontorSession().ask()` (opens the Kontor tile; Task 15 adds navigation when the page has no Kontor tile). Clicking an agent flies to it (`flyTo(x, y, 3)`); the agent card arrives in Task 16. Clicking a sector name flies to `polar(260, sectorMid)` at `rel 2.6`.
- The docked queue: `<NeedsYouQueue variant="docked" data-hub-layer class="absolute left-1/2 top-2.5 z-10 w-[min(560px,calc(100%-120px))] -translate-x-1/2 max-h-[45%] overflow-y-auto" />` — scrolls inside itself so a tall question's buttons stay reachable in a small hub (final-review minor M6).
- The old `data-testid="hub-list"` agent list is removed; the list view (`L`) replaces it in Task 16. Update `tests/e2e/zentrale.spec.ts` if it asserts `hub-list`/`hub-agent` — assert `hub-stage` and one `hub-agent-*` button instead (`data-testid="hub-agent-{pid}"` on each agent button).

- [ ] **Step 1: Failing tests.**
  - `HubOrbit.test.ts`: given two sectors, two agents (one needing the operator), level 0 → renders the core text `3 running · 1 waiting`, two agent buttons with their `aria-label`s, two sector buttons; at level 2 the sector buttons are not rendered (or hidden via `v-show`, assert `isVisible()` false); clicking the core emits `core`.
  - `HubWidget.test.ts` (provide `NEEDS_YOU`, `OPEN_TASK`, `PENDING_PERMISSIONS` as other tests do; stub `ResizeObserver`): renders `hub-stage` with `data-level="0"` after a resize; one `hub-agent-{pid}` per non-finished agent; the docked queue is present; pressing the core calls the mocked `ask`.
- [ ] **Step 2: Implement** `HubOrbit.vue` and `HubWidget.vue` as described (HubWidget owns `useHubCamera(stage)` and computes agent positions from `cam.k` every render, like the prototype's `placeAgents`). Create `src/features/hub/index.ts`:

```ts
export { default as HubWidget } from './components/HubWidget.vue'
```

- [ ] **Step 3: Move** — delete the mission HubWidget and its test, drop its export, point the registry's hub loader at `import('@/features/hub').then(m => m.HubWidget)`.
- [ ] **Step 4: Gate and commit.** `pnpm lint && pnpm typecheck && pnpm test`; `pnpm exec playwright test tests/e2e/zentrale.spec.ts --reporter=line`; commit `feat: draw the Zentrale hub as a live orbit of agents`.

### Task 15: Launchers, controls and keyboard

**Files:**
- Create: `src/features/hub/components/HubLaunchers.vue`, `src/features/hub/components/HubControls.vue`
- Modify: `src/features/hub/components/HubWidget.vue` (keys, wide toggle, launcher actions)
- Modify: `src/composables/useSidebar.ts` + `src/components/shell/AppSidebar.vue` (`requestNewPage`)
- Test: `src/features/hub/__tests__/HubLaunchers.test.ts`, `HubControls.test.ts`, `HubWidget.test.ts` (keys)

**Interfaces:**
- Produces:
  - `HubLaunchers` props `{ launchers: Launcher[], cam: Camera, docked: boolean }`, emits `launch: [launcher: Launcher]`. Ring: slot `i` at `polar(LAUNCHER_RING_RADIUS, launcherSlotDeg(i))` through the camera; docked (`rel > 1.5`): a column along the stage's left edge at `x = 30`, `y = 150 + i · 46`. Transition between the two only when `motion-safe`. Each is a `button` with `aria-label` = label, `title` = `"{label} ({i+1})"`, label shown on hover/focus.
  - `HubControls` props `{ level: HubLevel, wide: boolean }`, emits `zoomIn`, `zoomOut`, `fit`, `wide`, `list`, `level: [HubLevel]`. Buttons: `+` (Zoom in, `+`), `−` (Zoom out, `−`), `⌂` (Show all, `0`), `⤢` (Widen, `F`, `aria-pressed=wide`), `☰` (List, `L`); level switch `Overview` / `Topics` / `Notes` with `aria-pressed` on the current level.
  - `useSidebar().requestNewPage(): void` — `AppSidebar` watches it and runs its existing `startNewPage()` (focusing the input expands the rail).
- Consumes: `launchersFor`, `launcherSlotDeg` (Task 12), `NAV_ITEMS` (`@/utils/navConfig`), `useWorkspace` (`@/features/workspace`: `layout.pages` minus `ZENTRALE_PAGE_ID`, `wide`, `pageWithWidget`), `useViewState().activeView`.

Launcher actions: `view` → `activeView.value = launcher.view`; `new-page` → `useSidebar().requestNewPage()`; `more` → open the list view (Task 16 provides it; until then, open the list stub state `listOpen = true`).

Keys on the stage (ignore when `e.target` is an input/textarea/contenteditable, or when a modifier other than Shift is held): arrows pan 60 px, `+`/`=` zoom ×1.4, `-` zoom ÷1.4, `0` fit, `f`/`F` toggles wide (`wide.value = wide.value === 'hub' ? null : 'hub'`), `l`/`L` toggles the list, `1`–`8` fire the launcher in that slot, `Escape` closes the topmost layer (card → list) and otherwise fits. `preventDefault` for every handled key.

Core click (update from Task 14): if the current page has no `kontor` tile, navigate to `pageWithWidget(layout, 'kontor')` first, then `ask()`; if no page has one, the core's `title` says "Add the Kontor tile to a page to open it here" and the click does nothing.

- [ ] **Step 1: Failing tests** — launchers render in ring order with slot titles; docked moves them to the dock positions; clicking `new-page` calls `requestNewPage`; pressing `2` on the stage fires the second launcher; `+` raises `rel`, `0` returns to 1 (reduced motion stubbed); `F` toggles `wide` between `'hub'` and `null`; typing `+` inside an input in the stage does nothing.
- [ ] **Step 2: Implement.** **Step 3: Gate, e2e, commit** — `pnpm lint && pnpm typecheck && pnpm test`; `pnpm exec playwright test tests/e2e/zentrale.spec.ts --reporter=line`; commit `feat: navigate from the hub with launchers, controls and keys`.

### Task 16: Minimap, list view and agent card

**Files:**
- Create: `src/features/hub/components/HubMinimap.vue`, `src/features/hub/components/HubList.vue`, `src/features/hub/components/HubAgentCard.vue`
- Modify: `src/features/hub/components/HubWidget.vue`
- Test: `src/features/hub/__tests__/HubMinimap.test.ts`, `HubList.test.ts`, `HubAgentCard.test.ts`

**Interfaces:**
- Produces:
  - `HubMinimap` props `{ cam: Camera, size: { width: number, height: number }, sectors: Sector[], agents: ReadonlyArray<{ x: number, y: number, state: AgentDisplayStatus }> }`, emits `fly: [wx: number, wy: number]`. SVG 108×108, `viewBox="-540 -540 1080 1080"`, `aria-label="Overview map"`: sector wedges (fill the sector token at 0.12), agent dots, the viewport rect `x = -tx/k, y = -ty/k, w = width/k, h = height/k`. A click emits the world point under it. Notes are **not** drawn on the minimap (5,000+ SVG nodes for a 108 px map); the wedges carry the density.
  - `HubList` props `{ agents: ReadonlyArray<{ agent: Agent, state: AgentDisplayStatus }>, notes: ReadonlyArray<{ path: string, title: string, sector: string, mtimeMs: number }>, launchers: Launcher[] }`, emits `agent: [Agent]`, `note: [path: string]`, `launch: [Launcher]`, `close: []`. A panel (`data-hub-layer`, `role="dialog"`, `aria-label="Zentrale as a list"`) with sections *Agents* (name + state word), *Recently touched* (title, sector, `formatRelativeThenDate`), *Go to* (the launchers). Rows are buttons. Focus moves to the panel's first button on open and returns to the stage on close.
  - `HubAgentCard` props `{ agent: Agent }`, emits `close`. Shows name, state word (colour **and** word), `lastOutput` preview, *Open session* (`useAgents().selectAgent(agent)` — read how `App.vue` opens the agent modal and use the same call). `data-hub-layer`, `role="dialog"`.
- Consumes: Tasks 11–15.

In this task `notes` is always `[]` (the vault arrives in Task 20); the section renders "Connect Obsidian to see recently touched notes." with a button that opens settings (use the same call `AppTopbar`/`AppSidebar` use to open settings — read it; if settings are opened via an emit to App, emit `openSettings` from `HubWidget` through an injected callback `OPEN_SETTINGS` added to `src/composables/openTask.ts` and provided by App).

- [ ] **Step 1: Failing tests** — minimap viewport rect follows the camera; a click at the minimap centre emits `fly` near `(0, 0)`; the list lists agents with their state word and emits `agent` on click; Escape on the stage closes the list first, then fits; the agent card's *Open session* calls `selectAgent`.
- [ ] **Step 2: Implement.** **Step 3: Gate and commit** `feat: add the hub minimap, list view and agent card`.

---

## Slice 8 — Hub II: graph endpoint, canvas brain, semantic levels, note card, memory tile

### Task 17: Obsidian client — JsonLogic search, open, graph builder (Go)

**Files:**
- Modify: `server/internal/apps/obsidian/client.go` (`SearchJSONLogic`, `OpenNote`)
- Create: `server/internal/apps/obsidian/graph.go`
- Test: `server/internal/apps/obsidian/graph_test.go` (and client cases in the existing `client_test.go` if one exists; otherwise in `graph_test.go`)

**Interfaces:**
- Produces:

```go
// JSONLogicHit is one note a JsonLogic search matched, with the query's value for it.
type JSONLogicHit struct {
	Filename string          `json:"filename"`
	Result   json.RawMessage `json:"result"`
}
func (c *Client) SearchJSONLogic(ctx context.Context, logic string) ([]JSONLogicHit, error)
func (c *Client) OpenNote(ctx context.Context, notePath string) error // notePath relative to VaultRoot

type GraphNote struct {
	Path    string // relative to VaultRoot
	MtimeMs int64
}
type Graph struct {
	Notes []GraphNote // sorted by Path
	Links [][2]int    // indices into Notes, from → to, unique, no self-links
}
func (c *Client) Graph(ctx context.Context) (Graph, error)
```

- Consumes: `resolveVaultPath`, `pathUnderRoot` (both in `client.go`).

- [ ] **Step 1: Failing tests** in `graph_test.go` against an `httptest.NewTLSServer` or `httptest.NewServer` fake of the REST API (build the client the way the existing obsidian tests do — read them for the `Config` and TLS mode to use against `httptest`):
  - `POST /search/` with `Content-Type: application/vnd.olrapi.jsonlogic+json` and body `{"var":"stat.mtime"}` answers `[{"filename":"root/b.md","result":1700000000000},{"filename":"root/a.md","result":1700000001000},{"filename":"other/x.md","result":1},{"filename":"root/pic.png","result":5}]`; body `{"var":"links"}` answers `[{"filename":"root/a.md","result":["root/b.md","other/x.md","root/missing.md","root/a.md"]},{"filename":"other/x.md","result":["root/a.md"]}]`. With `VaultRoot: "root"`:
    - `Graph()` returns notes `[{a.md 1700000001000} {b.md 1700000000000}]` (sorted, confined, `.md` only) and links `[[0 1]]` (outside-root, missing and self links dropped; a link from outside the root dropped).
    - the fake asserts the `Authorization: Bearer <key>` header and the content type on both calls.
  - A non-200 search answer returns an error that does not contain the API key.
  - `OpenNote(ctx, "a.md")` sends `POST /open/root/a.md`; `OpenNote(ctx, "../etc.md")` fails before any request (fake records no call).

Run: `(cd server && go test ./internal/apps/obsidian/ -run 'Graph|OpenNote|JSONLogic' -count=1)` → FAIL.

- [ ] **Step 2: Implement** — `client.go`:

```go
// SearchJSONLogic runs a JsonLogic query against every note; the REST API returns only non-falsy results.
func (c *Client) SearchJSONLogic(ctx context.Context, logic string) ([]JSONLogicHit, error) {
	u := *c.baseURL
	u.Path = "/search/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(logic))
	if err != nil {
		return nil, fmt.Errorf("obsidian: build jsonlogic search: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/vnd.olrapi.jsonlogic+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("obsidian: jsonlogic search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("obsidian: jsonlogic search: unexpected status %d", resp.StatusCode)
	}
	var hits []JSONLogicHit
	if err := json.NewDecoder(resp.Body).Decode(&hits); err != nil {
		return nil, fmt.Errorf("obsidian: jsonlogic search: decode response: %w", err)
	}
	return hits, nil
}

// OpenNote asks Obsidian to show the note; the REST API creates a missing note, so callers pass only paths they know exist.
func (c *Client) OpenNote(ctx context.Context, notePath string) error {
	resolved, err := resolveVaultPath(c.vaultRoot, notePath)
	if err != nil {
		return err
	}
	u := *c.baseURL
	u.Path = "/open/" + resolved
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return fmt.Errorf("obsidian: build open request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("obsidian: open: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("obsidian: open: unexpected status %d", resp.StatusCode)
	}
	return nil
}
```

`graph.go`:

```go
package obsidian

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type GraphNote struct {
	Path    string
	MtimeMs int64
}

type Graph struct {
	Notes []GraphNote
	Links [][2]int
}

// Graph reads every note's mtime and resolved outgoing links in two searches; no note body is read.
func (c *Client) Graph(ctx context.Context) (Graph, error) {
	mtimes, err := c.SearchJSONLogic(ctx, `{"var":"stat.mtime"}`)
	if err != nil {
		return Graph{}, err
	}
	links, err := c.SearchJSONLogic(ctx, `{"var":"links"}`)
	if err != nil {
		return Graph{}, err
	}
	var g Graph
	for _, hit := range mtimes {
		rel, ok := pathUnderRoot(c.vaultRoot, hit.Filename)
		if !ok || !strings.HasSuffix(rel, ".md") {
			continue
		}
		var mtime float64
		if err := json.Unmarshal(hit.Result, &mtime); err != nil {
			return Graph{}, fmt.Errorf("obsidian: graph: mtime of %q: %w", rel, err)
		}
		g.Notes = append(g.Notes, GraphNote{Path: rel, MtimeMs: int64(mtime)})
	}
	sort.Slice(g.Notes, func(i, j int) bool { return g.Notes[i].Path < g.Notes[j].Path })
	index := make(map[string]int, len(g.Notes))
	for i, n := range g.Notes {
		index[n.Path] = i
	}
	seen := make(map[[2]int]bool)
	for _, hit := range links {
		fromRel, ok := pathUnderRoot(c.vaultRoot, hit.Filename)
		from, known := index[fromRel]
		if !ok || !known {
			continue
		}
		var targets []string
		if err := json.Unmarshal(hit.Result, &targets); err != nil {
			continue
		}
		for _, t := range targets {
			toRel, ok := pathUnderRoot(c.vaultRoot, t)
			to, known := index[toRel]
			if !ok || !known || to == from {
				continue
			}
			pair := [2]int{from, to}
			if !seen[pair] {
				seen[pair] = true
				g.Links = append(g.Links, pair)
			}
		}
	}
	return g, nil
}
```

(A links result that is not a string array is skipped rather than failing the graph: the plugin's `links` shape for a single malformed note must not blank the whole hub — this is the "non-obvious edge case" comment category if you add one.)

- [ ] **Step 3: Run** `(cd server && go vet ./... && go test ./internal/apps/obsidian/... -count=1) ; git checkout -- server/internal/db/ent/` → PASS. **Step 4: Commit** `feat: read the vault graph from Obsidian in two searches`.

### Task 18: `GET /api/obsidian/graph` and `POST /api/obsidian/open`

**Files:**
- Create: `server/internal/api/obsidian/graph_cache.go`
- Modify: `server/internal/api/obsidian/handler.go` (routes, handlers)
- Test: `server/internal/api/obsidian/handler_test.go` (extend with graph/open cases), `server/internal/api/obsidian/graph_cache_test.go`

**Interfaces:**
- Produces (HTTP, parity with `src/features/hub/graphApi.ts` in Task 19):
  - `GET /api/obsidian/graph` → `200 {"configured":false}` when the client is nil; `403 {"error": …}` when `memory.read` is refused; `502 {"error":"obsidian graph unavailable"}` on an upstream failure (never the upstream error text, which can carry a URL); else `200 {"configured":true,"notes":[["a.md",1700000001000],…],"links":[[0,1],…]}`.
  - `POST /api/obsidian/open` with JSON `{"path":"a.md"}` → `503` when not configured; `403` when `memory.read` is refused; `404` when the path is not a note of the (cached or freshly built) graph; `502` upstream failure; `204` on success.
  - `graphCache` with `get(ctx context.Context, build func(context.Context) (obsidianapp.Graph, error)) (obsidianapp.Graph, error)`: returns a cached graph younger than 60 s; otherwise one rebuild shared by all concurrent callers (`singleflight`), run on `context.WithoutCancel(ctx)` so one caller hanging up does not fail the others; a failed rebuild is not cached.
- Consumes: `Client.Graph`, `Client.OpenNote` (Task 17), `memory.Gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())` (the same call `server/internal/api/memory/handler.go:522` makes), `capability.ErrDenied`/`ErrAskRequired` → 403 (as `index` does).

Ruling carried in this plan: *Open in Obsidian* is gated by `memory.read` too. It shows the operator a note the graph already listed; the REST API's `/open` creates a missing file, so the handler only opens paths present in the graph — it can never create one.

- [ ] **Step 1: Failing tests.**
  - `graph_cache_test.go`: with an injected clock, two `get` calls within 60 s run `build` once; after 61 s it runs again; ten concurrent `get` calls while `build` blocks on a channel run `build` once and all receive its result; a `build` error is returned to all waiters and the next `get` retries.
  - `handler_test.go` (reuse `testDeps`, `grantCapability`, extend `newFakeVault` or add a second fake that answers the two JsonLogic searches and `/open/…`):
    - nil client → `GET /api/obsidian/graph` 200 `{"configured":false}`; `POST /api/obsidian/open` 503.
    - no `memory.read` grant → both 403 and the fake vault was never contacted.
    - with the grant → graph 200 with the confined notes and links from Task 17's fixture; a second request within 60 s does not reach the fake again.
    - open `{"path":"a.md"}` → 204 and the fake saw `POST /open/root/a.md`; open `{"path":"nope.md"}` → 404 and no `/open` call; open with a body that is not JSON → 400.
    - upstream 500 → graph 502 and the body does not contain the fake's URL.
- [ ] **Step 2: Implement** `graph_cache.go`:

```go
package obsidian

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
)

const graphTTL = 60 * time.Second

type graphCache struct {
	now   func() time.Time
	group singleflight.Group
	mu    sync.Mutex
	at    time.Time
	graph *obsidianapp.Graph
}

func (c *graphCache) get(ctx context.Context, build func(context.Context) (obsidianapp.Graph, error)) (obsidianapp.Graph, error) {
	c.mu.Lock()
	if c.graph != nil && c.now().Sub(c.at) < graphTTL {
		g := *c.graph
		c.mu.Unlock()
		return g, nil
	}
	c.mu.Unlock()
	v, err, _ := c.group.Do("graph", func() (any, error) {
		g, err := build(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.graph, c.at = &g, c.now()
		c.mu.Unlock()
		return g, nil
	})
	if err != nil {
		return obsidianapp.Graph{}, err
	}
	return v.(obsidianapp.Graph), nil
}
```

`handler.go`: add a `graphs graphCache` field (initialise `now: time.Now` in `NewHandler`), mount `r.Get("/api/obsidian/graph", …)` and `r.Post("/api/obsidian/open", …)`, and:

```go
func (h *Handler) authorizeRead(r *http.Request) error {
	if err := h.gate.Authorize(r.Context(), repo.CapabilityMemoryRead, "", repo.GlobalScope()); err != nil {
		return apierr.NewAppError(http.StatusForbidden, err.Error())
	}
	return nil
}

func (h *Handler) graph(w http.ResponseWriter, r *http.Request) error {
	if h.client == nil {
		apierr.WriteJSON(w, http.StatusOK, map[string]bool{"configured": false})
		return nil
	}
	if err := h.authorizeRead(r); err != nil {
		return err
	}
	g, err := h.graphs.get(r.Context(), h.client.Graph)
	if err != nil {
		slog.Warn("obsidian graph rebuild failed", "err", err)
		return apierr.NewAppError(http.StatusBadGateway, "obsidian graph unavailable")
	}
	notes := make([][2]any, len(g.Notes))
	for i, n := range g.Notes {
		notes[i] = [2]any{n.Path, n.MtimeMs}
	}
	links := g.Links
	if links == nil {
		links = [][2]int{}
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]any{"configured": true, "notes": notes, "links": links})
	return nil
}
```

`open`: decode `{"path": string}` with `DisallowUnknownFields` and a 4 KiB `http.MaxBytesReader` (400 on failure), authorize, get the graph through the cache, look the path up (exact match on `GraphNote.Path`), `404` if absent, `h.client.OpenNote`, `502` on its error (log it), `204` on success. Use whatever logger the package already uses (grep `slog` in `server/internal/api`); if the api packages use another logger, use that.

Update the package doc comment (it says the package only triggers indexing) and the `Handler` doc comment to name the three routes.

- [ ] **Step 3: Security doc.** Add both routes to `docs/guides/security.md` where the Obsidian/memory routes are described: gated by `memory.read`, confined to `obsidian.vaultRoot`, no note bodies, the open route refuses paths the graph does not list.
- [ ] **Step 4: Run** `(cd server && go vet ./... && go test ./internal/api/obsidian/... ./internal/apps/obsidian/... -count=1 -race) ; git checkout -- server/internal/db/ent/` → PASS. **Step 5: Commit** `feat: serve the vault graph and open notes in Obsidian from the hub`.

### Task 19: Graph state on the client and the canvas maths

**Files:**
- Create: `src/features/hub/graphApi.ts`, `src/features/hub/composables/useObsidianGraph.ts`, `src/features/hub/hubCanvas.ts`, `src/utils/fetchWithRateLimitRetry.ts` (moved out of `useWorkspace.ts`)
- Modify: `src/features/workspace/useWorkspace.ts` (import the moved helper)
- Test: `src/features/hub/__tests__/useObsidianGraph.test.ts`, `src/features/hub/hubCanvas.test.ts`

**Interfaces:**
- Produces:
  - `graphApi.ts`:

```ts
// Mirrors server/internal/api/obsidian/handler.go (graph); Go cannot share this type, keep the two in step by hand.
export interface GraphResponse {
  configured: boolean
  notes?: Array<[path: string, mtimeMs: number]>
  links?: Array<[from: number, to: number]>
}
```

  - `useObsidianGraph()` (module-level state; one fetch shared by every caller):

```ts
export type GraphStatus = 'idle' | 'loading' | 'ready' | 'unconfigured' | 'denied' | 'failed'
export interface HubNote { index: number, path: string, title: string, mtimeMs: number, links: number[], backlinks: number[] }
export function useObsidianGraph(): {
  status: Readonly<Ref<GraphStatus>>
  message: Readonly<Ref<string>>      // server's words for denied/failed
  notes: Readonly<ShallowRef<HubNote[]>>
  refresh: (force?: boolean) => Promise<void>
  recentNotes: (count: number) => HubNote[]
  noteByPath: (path: string) => HubNote | undefined
  openInObsidian: (path: string) => Promise<string | null> // error text or null
}
```

  - `hubCanvas.ts`:

```ts
export interface LabelCandidate { index: number, sx: number, sy: number, text: string, priority: number }
export function notePriority(n: { hub: boolean, touched: boolean, fresh: boolean, linkCount: number }): number
export function cullLabels(candidates: LabelCandidate[]): Set<number>
export function hitNote(points: ReadonlyArray<[number, number]>, cam: Camera, sx: number, sy: number, maxPx?: number): number
export function isToday(mtimeMs: number, nowMs: number): boolean
export function hubNoteSet(notes: ReadonlyArray<HubNote>, sectorOf: (n: HubNote) => string): Set<number>
```

- Consumes: `Camera`, `toScreen` (Task 12).

Fetch policy (`memory.Gate.Authorize` records a rate-limit use per request): `refresh()` does nothing within 60 s of the previous completed fetch unless `force`; `HubWidget` calls `refresh()` on mount and on `window` `focus`; nothing polls. `refresh` keeps the last good `notes` on `failed`. Status mapping: `200 configured:false` → `unconfigured`; `200` → `ready`; `403` → `denied` (message = body `error`); anything else or a network error → `failed`. A 429 is retried exactly like `useWorkspace` does it: move `fetchWithRateLimitRetry` (with `retryDelayMs`, `sleep` and its three constants) out of `src/features/workspace/useWorkspace.ts` into `src/utils/fetchWithRateLimitRetry.ts`, export it, import it in both places (second caller → one shared helper, no copy). `useWorkspace`'s existing tests must pass unchanged.

Titles: the file name without `.md`. `links` = outgoing indices, `backlinks` = incoming.

`notePriority`: hub 100 + touched 80 + fresh 40 + linkCount (the prototype's `prio`). `cullLabels`: sort by priority descending, place greedily, a label box is `{ x: sx + 6, y: sy - 8, w: text.length * 6.3 + 10, h: 16 }`, skip one that overlaps a placed box. `hitNote`: nearest point whose screen distance ≤ `maxPx` (default 8), else `-1`. `hubNoteSet`: per sector the notes with the most backlinks — at most 3 per sector and only notes with ≥ 2 backlinks. `isToday`: same local calendar day.

- [ ] **Step 1: Failing tests.**
  - `hubCanvas.test.ts`: `cullLabels` keeps the higher-priority label of two overlapping ones and both of two apart; `hitNote` returns the nearest within 8 px and `-1` beyond; `notePriority` orders hub > touched > fresh > links; `isToday` is false for yesterday 23:59 and true for today 00:01 (build timestamps with `new Date(y, m, d, h, min)`); `hubNoteSet` caps at 3 per sector and ignores notes with fewer than 2 backlinks.
  - `useObsidianGraph.test.ts` (mock `fetch`): `configured:false` → `unconfigured`; a 403 with `{"error":"memory.read denied"}` → `denied` + message; a ready graph builds titles, links and backlinks; a second `refresh()` within 60 s does not fetch, `refresh(true)` does; a failed refresh after a ready one keeps the notes and sets `failed`; `recentNotes(2)` returns the two newest; `openInObsidian` POSTs `{"path":…}` to `/api/obsidian/open` and returns the server's error text on a non-2xx.
- [ ] **Step 2: Implement.** **Step 3: Gate and commit** `feat: load the vault graph on the client and add the canvas maths`.

### Task 20: The brain on canvas

**Files:**
- Create: `src/features/hub/components/HubBrainCanvas.vue`
- Modify: `src/features/hub/components/HubWidget.vue` (sectors from the graph, notes layer, notices)
- Test: `src/features/hub/__tests__/HubBrainCanvas.test.ts`, `HubWidget.test.ts`

**Interfaces:**
- Produces: `HubBrainCanvas` props:

```ts
defineProps<{
  cam: Camera
  size: { width: number, height: number }
  level: HubLevel
  points: ReadonlyArray<[number, number]>   // world position per note index
  colours: ReadonlyArray<number>            // sector palette index (0–7) per note index
  notes: ReadonlyArray<HubNote>
  links: ReadonlyArray<[number, number]>
  hubNotes: ReadonlySet<number>
  selected: number | null
}>()
```

- Consumes: Tasks 11, 12, 19; `planSectors(notePaths, projects)`.

Rendering (Canvas 2D, port the prototype's `render()` for notes, edges, halos, labels and the selection ring, `prototype.html:344-381`):
- One `<canvas aria-hidden="true">` covering the stage, sized `width·dpr × height·dpr` with CSS size `width × height`; `ctx.setTransform(dpr, 0, 0, dpr, 0, 0)`.
- Draw only when an input changed, coalesced into one `requestAnimationFrame`.
- Colours read at the start of each draw from `getComputedStyle(canvas)`: `--sector-0…7`, `--line-strong`, `--halo`, `--fg-soft`, `--app` — so a theme switch recolours on the next draw; also redraw on a `MutationObserver` of `document.documentElement`'s `class` attribute.
- Links: straight lines between visible endpoints, `globalAlpha` 0.28 / 0.55 / 0.8 by level, colour `--line-strong`, 0.7 px.
- Notes: screen radius `[2.1, 3.4, 4.6][level]` px, hub notes ×1.9, fill the sector colour at alpha 0.75 (hub notes 1).
- Halos (level ≥ 1): notes touched today get a `--halo` ring at 2.6× the note radius.
- Labels: level 1 → hub notes only; level 2 → `cullLabels` over the notes inside the viewport; 11 px system font, `--fg-soft` fill with a 3 px `--app` stroke underneath (the prototype's `paint-order: stroke`).
- Selected note: a white 1.5 px ring at 3.2× the note radius.
- Skip notes outside the viewport (plus a 20 px margin) before drawing.

HubWidget changes:
- Sectors: `planSectors(ready ? notePaths : [], agentProjects)`; note points `notePoint(path, sectorOf(path), ageDays)`, `ageDays = (now − mtimeMs) / 86 400 000`; colours: the sector's index in `sectors` modulo 8.
- Tap (`useHubCamera` `onTap`): `hitNote(points, cam, sx, sy)`; at level 0 fly to the note at `rel 2.6`; otherwise select it (the note card arrives in Task 21).
- Notices (top-left, below the queue, `data-hub-layer`): `unconfigured` → "Connect Obsidian to see your notes here." + a button opening settings (the callback from Task 16); `denied` → "Memory reads are not granted, so your notes stay hidden." + the server's message in `title`; `failed` → "Your notes could not be loaded; retrying when you come back to this window." No notice while `loading` or `ready`.
- `refresh()` on mount and on `window` `focus`.

- [ ] **Step 1: Failing tests.**
  - `HubBrainCanvas.test.ts`: stub `HTMLCanvasElement.prototype.getContext` with a recording fake (arc, fillText, moveTo/lineTo, stroke calls); at level 0 with three notes it draws three arcs and no text; at level 2 it draws text for labels that `cullLabels` keeps; notes outside the viewport are not drawn; changing `cam` schedules exactly one redraw per frame (use fake timers and a rAF stub).
  - `HubWidget.test.ts`: with the graph composable mocked `ready` with notes in two folders, the sector buttons show both folder names; with `unconfigured`, the notice and its button render; a tap on a note's screen position at level 0 flies (the camera's `rel` rises to 2.6 with reduced motion stubbed).
- [ ] **Step 2: Implement.** **Step 3: Gate and commit** `feat: draw the Obsidian vault as the hub's brain`.

### Task 21: Note card and notes in the list view

**Files:**
- Create: `src/features/hub/components/HubNoteCard.vue`
- Modify: `src/features/hub/components/HubWidget.vue`, `src/features/hub/components/HubList.vue`
- Test: `src/features/hub/__tests__/HubNoteCard.test.ts`, `HubList.test.ts`

**Interfaces:**
- Produces: `HubNoteCard` props `{ note: HubNote, notes: ReadonlyArray<HubNote>, sectorLabel: string, kontorReachable: boolean }`, emits `fly: [index: number]`, `close: []`. `data-hub-layer`, `role="dialog"`, `aria-label` = the title.
- Consumes: `useObsidianGraph().openInObsidian`, `useKontorSession().ask` and `pageWithWidget` (for *Ask Kontor*), `formatRelativeThenDate`.

Card content (spec "The note card"): title; the vault path (monospace, `break-all`); "changed {formatRelativeThenDate(iso)} · {n} links · {m} backlinks"; link chips (outgoing, up to 8) and backlink chips (up to 8) that emit `fly(index)`; *Ask Kontor about this* → navigate to the Kontor page when the current page has no Kontor tile (same rule as the core click, Task 15), then `ask(\`[[${path without .md}]] \`)`; disabled with a `title` when no page has a Kontor tile; *Open in Obsidian* → `openInObsidian(path)`, and on an error show it inline under the buttons (`role="alert"`).

HubWidget: selecting a note (tap at level ≥ 1, a chip, a list row) opens the card and sets `selected`; a chip flies to the target at `max(rel, 3)` and opens its card; `Escape` closes the card first (Task 15's layer order: card → list → fit).

HubList: *Recently touched* now lists `recentNotes(14)` when the graph is ready; a row flies to the note at `rel 5` and opens its card.

- [ ] **Step 1: Failing tests** — the card shows title, path, counts; a link chip emits `fly` with its index; *Ask Kontor* calls `ask('[[Privat/Reise]] ')` for `Privat/Reise.md`; an `openInObsidian` error renders in the alert; the list's recent rows emit `note`.
- [ ] **Step 2: Implement.** **Step 3: Gate and commit** `feat: open a note card from the hub and ask Kontor about it`.

### Task 22: Fly to a note from anywhere — memory tile and command palette

**Files:**
- Create: `src/features/hub/composables/useHubFocus.ts`
- Modify: `src/features/hub/components/HubWidget.vue` (consume the request), `src/features/hub/index.ts` (export `useObsidianGraph`, `focusInHub`)
- Modify: `src/features/cockpit/components/MemoryPanel.vue` (recently touched notes)
- Modify: `src/components/SpotlightSearch.vue` (note entries)
- Test: `src/features/hub/__tests__/useHubFocus.test.ts`, `src/features/cockpit/components/resourcePanels.test.ts` (or the memory panel's own test), `src/components/SpotlightSearch.test.ts`

**Interfaces:**
- Produces:

```ts
export type HubTarget = { kind: 'note', path: string } | { kind: 'agent', pid: number }
export const hubFocusRequest: Ref<HubTarget | null>
// Navigates to the page holding the hub (pageWithWidget(layout, 'hub')) and asks it to fly there. Returns false when no page has a hub.
export function focusInHub(target: HubTarget): boolean
```

- Consumes: `pageWithWidget` (Task 3), `useWorkspace`, `useViewState`, `useObsidianGraph` (Task 19).

- HubWidget watches `hubFocusRequest` (`immediate`, `flush: 'post'`), resolves the target (note by path, agent by pid), flies (`rel 5` for a note, `3` for an agent), opens its card, and clears the request. An unknown target clears the request and does nothing.
- MemoryPanel: when the graph is `ready`, a second list *Recently touched* shows `recentNotes(5)` as buttons (`title` + relative time) calling `focusInHub({ kind: 'note', path })`; the panel calls `refresh()` on mount (the 60 s rule makes it free when the hub already fetched). Other states leave the panel as it is.
- Spotlight: when the graph is `ready`, the palette offers note entries (`id: \`note:${path}\``, label = title, hint = sector/folder) matched by the palette's existing matcher; running one calls `focusInHub`. Cap the entries fed to the matcher at the 2,000 most recent notes so a large vault does not slow typing; say so in the entry builder's name, not a comment (e.g. `recentNoteEntries`).

- [ ] **Step 1: Failing tests** — `focusInHub` navigates to the hub page and sets the request; returns `false` without a hub page; HubWidget consumes a request once; MemoryPanel lists recent notes only when ready and calls `focusInHub` on click; Spotlight shows a note entry for a matching title and runs `focusInHub`.
- [ ] **Step 2: Implement.** **Step 3: Gate and commit** `feat: jump to a note in the hub from the memory tile and the command palette`.

### Task 23: Hub end-to-end and the documentation

**Files:**
- Create: `tests/e2e/hub.spec.ts`
- Modify: `README.md`, `CHANGELOG.md`, `docs/superpowers/specs/2026-09-21-kontor-zentrale-design.md` (status line; open questions answered), `.agent-context/layer2-project-core.md` (SSOT rows)

**Interfaces:** Consumes everything above.

- [ ] **Step 1: E2E** (`tests/e2e/hub.spec.ts`; reuse the helpers of `tests/e2e/zentrale.spec.ts`; the Playwright server runs without Obsidian, so stub the graph with `page.route('**/api/obsidian/graph', r => r.fulfill({ json: FAKE_GRAPH }))` — 300 notes over three folders with mtimes spread over two years, a few dozen links):
  - focus the hub stage, press `+` until `data-level="2"` (at most 8 presses) — the notes level is reachable with the keyboard;
  - press `0` → `data-level="0"`;
  - press `l` → the list dialog lists the agents section and the recently touched notes; clicking the first note opens a note card with that title; `Escape` closes the card, `Escape` again closes the list;
  - with the graph route answering `{ "configured": false }` the *Connect Obsidian* notice renders;
  - press `f` → the hub tile spans the page width (its bounding box width ≥ 95 % of the grid's), press `f` again → back;
  - at a 1280×700 viewport with the hub resized to 6×6 via the stored layout, the docked queue's box stays inside the hub's box (the M6 check; a pending item is not needed — assert on the queue container).
- [ ] **Step 2: Docs.**
  - `README.md` Zentrale section: the hub (orbit, sectors, freshness rings, semantic levels, keys `+ − 0 F L 1–8 Esc`, minimap, list view), the brain (needs Obsidian configured and `memory.read` granted; *Connect Obsidian* otherwise), the note card, the memory tile's recent notes, notes in the command palette.
  - `CHANGELOG.md` (Unreleased): **Added** hub orbit and brain, `GET /api/obsidian/graph`, `POST /api/obsidian/open`, capability decisions in the needs-you queue, *Ask Kontor about this*; **Changed** tile frame (icon, label, key figure), widgets load lazily, page title *Kontor*; **Fixed** the follow-ups this plan closed (bundle budget, stale settings comment, `page:zentrale`, overlay scroll re-placement, …).
  - Spec: status line → "slices 1–8 implemented on feat/kontor-zentrale (2026-09-22); slices 9–11 open"; open question 1 → answered (REST `POST /open/{filename}`, no vault name needed); open question 2 → answered with the measured split (Privat ≈ 213°, one level stays).
  - `layer2-project-core.md` SSOT rows: hub geometry constants `src/features/hub/hubGeometry.ts`; camera thresholds `src/features/hub/hubCamera.ts`; graph response type (client, hand-kept parity with `server/internal/api/obsidian/handler.go`) `src/features/hub/graphApi.ts`.
- [ ] **Step 3: Gate.** `pnpm lint && pnpm typecheck && pnpm test`; `pnpm exec playwright test tests/e2e/hub.spec.ts tests/e2e/zentrale.spec.ts --reporter=line`. **Step 4: Commit** `test: cover the hub end to end and document the Zentrale hub`.

---

## Wave F — the remaining follow-ups of slices 1–5

### Task 24: Pages in the sidebar — focus, layout shift, duplication

**Files:**
- Modify: `src/components/shell/AppSidebar.vue`
- Test: `src/components/shell/AppSidebar.test.ts` (create if missing; otherwise extend)

**Interfaces:** none new.

Deferred findings (Task 13b review of slices 1–5):
1. Focus falls to `<body>` after creating and after deleting a page. After create, focus the new page's nav item; after a delete (the edit bar's delete navigates to the Zentrale), focus the Zentrale nav item. Find where the delete lands (`WorkspaceEditBar.vue`) and move focus via the nav item's id or `data-testid`.
2. The *Insights* group shifts up when the layout loads and with each new page, because the *New page* slot mounts only after load. Render the Pages group's slot from the first paint (disabled `+ New page` button until `workspace.loaded`), so nothing below moves when the load finishes.
3. The Pages caption markup duplicates the core groups' caption markup (second occurrence) — render the caption through the same markup path as the core groups (a small local component or a shared `v-for` over groups with pages appended), not a copy.
4. The input closes on success only as a side effect of `blur`. Close it explicitly (`creatingPage.value = false`) after a successful save, and keep the blur-before-unmount line (its comment explains a real focus event contract).
5. The "keeps the input open" test never asserts that the input exists — assert it.
6. The two-line template comment in `AppSidebar.vue` (introduced in slices 1–5): remove it unless it names one of the four comment categories; if it does, cut it to one line.
7. `badgeFor`/`badgeDanger` take `ActiveView` but only core views have badges — narrow the parameter to `CoreView`.

- [ ] **Step 1: Failing tests** for 1, 2 (the `+ New page` button exists, disabled, before `loaded`), 4 and 5.
- [ ] **Step 2: Implement.** **Step 3: Gate and commit** `fix: keep focus and layout steady when pages are created, loaded or deleted`.

### Task 25: Edit bar and edit-mode lifecycle

**Files:**
- Modify: `src/features/workspace/components/WorkspaceEditBar.vue` (+ test)
- Modify: `src/components/shell/AppTopbar.vue` and/or `src/App.vue` (one Done button; edit mode ends on navigation)
- Test: `src/features/workspace/components/WorkspaceEditBar.test.ts`, the App/topbar test that covers the edit toggle

Deferred findings:
1. Two *Done* buttons while editing (the topbar toggle and the edit bar). Keep the edit bar's *Done*; while editing, the topbar toggle hides (or reads *Editing…* and is inert — choose hiding, it is fewer states).
2. Edit mode survives navigating away and back (module state `editing`). End it on every `activeView` change (`App.vue` already watches `activeView` for Task 10's `wide`; extend that watch).
3. `confirmingDelete` stays armed across a rename — disarm it whenever the title input changes or the rename is saved.

- [ ] **Step 1: Failing tests** for 1–3. **Step 2: Implement.** **Step 3: Gate and commit** `fix: end edit mode on navigation and show one Done button`.

### Task 26: `useWorkspace` — listener lifecycle and missing tests

**Files:**
- Modify: `src/features/workspace/useWorkspace.ts`
- Modify: `src/App.vue` (install the listeners)
- Test: `src/features/workspace/useWorkspace.test.ts`, `src/utils/fetchWithRateLimitRetry.test.ts` (create)

**Interfaces:**
- Produces: `export function watchExternalChanges(): () => void` in `useWorkspace.ts` — installs the window `focus` and document `visibilitychange` listeners that refetch the layout, returns a disposer. The module no longer adds listeners at import time.
- Consumes: `fetchWithRateLimitRetry` from `src/utils/fetchWithRateLimitRetry.ts` (Task 19).

Deferred findings: the module adds `window`/`document` listeners at import (`useWorkspace.ts:88-89`), so tests that `vi.resetModules()` stack listeners and the focus tests only pass when run last; there is no test that two `useWorkspace()` calls share state; no unit test for a huge or negative `Retry-After`.

- [ ] **Step 1: Failing tests.**
  - `useWorkspace.test.ts`: without `watchExternalChanges()` a `focus` event does not refetch; with it, it does; after calling the disposer it does not. Each focus test installs and disposes in its own `beforeEach`/`afterEach`, and the file passes with `--sequence.shuffle`.
  - two `useWorkspace()` calls return the same `layout` ref (`toBe`).
  - `fetchWithRateLimitRetry.test.ts`: `Retry-After: 99999` waits the 5 s cap, `Retry-After: -5` and `Retry-After: soon` wait the 1 s default (fake timers; assert the second `fetch` fires at the expected time); three 429s then a 200 returns the 200; four 429s return the last 429.
- [ ] **Step 2: Implement.** `App.vue`: `onMounted(() => { stopWatching = watchExternalChanges() })`, `onUnmounted(() => stopWatching?.())`.
- [ ] **Step 3: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test` and `pnpm vitest run src/features/workspace --sequence.shuffle`; commit `fix: install the layout refetch listeners from the app, not at import`.

### Task 27: Grid tests and e2e helpers

**Files:**
- Modify: `src/features/workspace/components/WorkspaceGrid.test.ts`
- Modify: `tests/e2e/zentrale.spec.ts` (shared helper for the CDP cache switch and the PATCH wait; reload assertions)
- Modify (only if the comment check below says so): `src/features/workspace/components/WorkspaceGrid.vue`

Deferred findings: the move test uses grab offset 0 only; `pointercancel` is untested; the jsdom workaround comment in the test is three lines; the CDP-disable + wait-for-PATCH boilerplate is duplicated in two e2e tests; the rename/delete e2e never reloads, so persistence of rename and delete is unasserted; a two-line DOM-order comment in `WorkspaceGrid.vue`.

- [ ] **Step 1:** Add a move test that grabs a tile 2 cells right of its anchor and asserts the dropped anchor accounts for the offset; add a `pointercancel` test asserting nothing is saved and the ghost disappears; shorten the jsdom comment to one line.
- [ ] **Step 2:** Extract one e2e helper for "disable the HTTP cache via CDP and wait for the next non-429 PATCH of `workspace.layout`" and use it in both tests; after rename and after delete, reload and assert the new title / the missing page.
- [ ] **Step 3:** Read the DOM-order comment in `WorkspaceGrid.vue`: if it states a non-obvious contract (reading order = DOM order for the single-column breakpoint and screen readers), keep it at one line; otherwise delete it.
- [ ] **Step 4: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`, `pnpm exec playwright test tests/e2e/zentrale.spec.ts --reporter=line`; commit `test: cover grab offsets, cancelled drags and persisted page edits`.

### Task 28: The red and flaky e2e specs — reproduce, then fix or document

**Files:** decided by the investigation; expected candidates `tests/e2e/cold-start-rate-limit.spec.ts`, `server/internal/api/middleware.go` / `server/internal/api/router.go` (limiter burst), `tests/e2e/zentrale.spec.ts`, `tests/e2e/spawn-with-project.spec.ts`.

Use the `reproduce-first-debug` skill. One ledger per spec. For each: reproduce (numbers: N runs, M failures), state one hypothesis and the cheapest experiment that could disprove it, run it, and only then change code.

1. **`cold-start-rate-limit.spec.ts:22` fails** ("two clients opening at once are never throttled"). The limiter's burst of 120 was sized from a measured cold start of 21 requests per client (`server/internal/api/middleware.go:168-175`). Slices 1–5 added boot requests (`GET /api/settings` on every view, widget mount fetches). First establish whether it fails on the commit before the Zentrale work (`git log --format=%H -1 dd2f3973^` is the parent of the Zentrale plan commit — materialise that tree read-only with `git archive <sha> | tar -x -C <job tmp>/prebranch`, symlink `node_modules` from this worktree, and run the spec there; never switch this worktree's branch and never `git worktree add`) and on HEAD, then count the boot requests per client on both (Playwright `page.on('request')`). If HEAD's cold start grew past the limiter's sizing assumption, either cut the boot requests (preferred when a request is redundant, e.g. a widget refetching what App already streams) or re-measure and re-size the burst following the comment's own rule — state which and why in the ledger.
2. **`zentrale.spec.ts:110` ("/" opens the Kontor tile) flaked once in 63 full-suite runs**, never in 30 isolated runs. Hypothesis to test first: rate-limiter pressure in the full suite (same class as 1). Run the full suite 5× with a counter of 429 responses on that test's page; fix according to the result.
3. **`spawn-with-project` is flaky** (pre-existing). Reproduce with `--repeat-each=20`, read the failure's trace, find the race.

Deliverable: for each spec either a fix with a before/after run count, or a written finding (in the report and as a one-line entry in the plan's ledger) saying why it cannot be fixed here and what would.

- [ ] **Step 1–3:** one per spec as above. **Step 4: Gate** — `pnpm exec playwright test --reporter=line` (full suite) twice; paste both summaries. **Step 5: Commit** each fix separately with a `fix:` or `test:` subject naming the behaviour.

---

## Execution order and review notes

Tasks run in numeric order. Tasks 1–5 (wave 0) come first because Task 1 removes the static import edge the hub needs and Task 4 provides the ranked list the hub core reads. Task 10 must precede Task 15 (`wide`); Tasks 8–9 must precede Task 14 (`useKontorAgent`, `ask`); Task 19 must precede Task 26 (the moved retry helper).

Out of scope, recorded for later (spec slices 9–11): live edges between agents and notes, module widgets in the picker, the large views as tiles.
