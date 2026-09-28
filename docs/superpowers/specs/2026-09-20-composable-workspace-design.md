# A Workspace the Operator Composes — Design

Status: approved by the operator 2026-09-20; extended by `2026-09-21-kontor-zentrale-design.md`, whose slice table replaces the one below
Date: 2026-09-20

## Why

The cockpit is five panels written into a template
(`src/features/cockpit/components/CockpitView.vue:9-17`). What it shows, in what
order, at what size, is a decision made in the source. The operator wants the
opposite: a surface they assemble, plus pages of their own for the groupings the
five panels do not cover.

Three things stand in the way today:

| Obstacle | Where |
| --- | --- |
| The panels are hardcoded in a Tailwind grid | `CockpitView.vue:9-17` |
| Navigation is a closed union of eight values plus `v-if` chains | `src/composables/useViewState.ts:5`, `src/App.vue:283-291` |
| Layout state lives in `localStorage`, so it is per browser | `useViewState.ts:129` — the desktop window and a browser tab would disagree |

The third is why this is not a frontend-only change: a workspace that differs
between the two windows the operator already uses is not a workspace.

## Decisions taken with the operator

1. **Tiles come in two generations.** The small cards first — the five cockpit
   panels and whatever modules contribute. The large views (the kanban board,
   the cost chart) follow in a second stage, through the interface the first
   stage proves.
2. **A layout belongs to the installation.** One set of pages, the same
   everywhere. Not per project: the operator's work spans many directories and
   a project switch rebuilding the workspace would be noise, not help.
3. **Tiles are anchored, not flowed.** A tile remembers its column *and* its
   row and stays there. Removing the tile above it leaves a gap; the gap is the
   operator's to fill. This is the more expensive of the options considered and
   was chosen deliberately over span-plus-order, which would have reflowed.

## Non-goals

- Reflowing or displacing tiles to make room. A drop that does not fit is
  refused, not resolved.
- Per-project layouts, and any precedence rule between scopes.
- Making the existing fixed views (pipeline, cost, sessions, eval) composable.
  They remain pages of their own.
- A layout editor for anything other than the operator's own machine — layouts
  are not shared or exported in v1.

## The model

A workspace is a list of pages. A page is a list of placed tiles.

```jsonc
{
  "pages": [
    {
      "id": "cockpit",                 // the built-in page, editable like any other
      "title": "Cockpit",
      "tiles": [
        { "widget": "agents",   "col": 1, "row": 1, "colSpan": 4, "rowSpan": 2 },
        { "widget": "pipeline", "col": 5, "row": 1, "colSpan": 4, "rowSpan": 1 },
        { "widget": "obsidian__recent", "col": 9, "row": 1, "colSpan": 4, "rowSpan": 3 }
      ]
    },
    { "id": "page:morning", "title": "Morning", "tiles": [] }
  ]
}
```

`col` is 1-12 and `col + colSpan - 1` may not exceed 12. `row` is 1-n, unbounded
upwards. `rowSpan` is 1-n. A tile naming a widget the registry does not know is
kept in the model and rendered as a placeholder — a module that is temporarily
deactivated must not cost the operator their layout.

### Why 12 columns and a fixed row height

Twelve divides into halves, thirds, quarters and sixths, which is the vocabulary
a person actually reaches for. The row height is a fixed pixel value
(`grid-auto-rows`), because a row span only means something if a row has a
height: with content-sized rows, "two rows tall" would depend on what the tile
happens to contain, and two tiles with `rowSpan: 2` would not match.

### Placement and collisions

CSS Grid places an anchored tile directly: `grid-column: <col> / span <colSpan>`
and `grid-row: <row> / span <rowSpan>`. The browser does the drawing; the
collision rule is ours, because anchoring means two tiles can be asked to occupy
one cell.

The rule is the smallest one that keeps anchors honest: **a placement that would
overlap an occupied cell is refused.** The tile snaps back to where it was and
the target shows as invalid while dragging. Nothing is displaced, nothing is
pushed down, and the operator is never surprised by a tile moving that they did
not touch.

### Narrow windows

Below the `md` breakpoint the grid collapses to a single column and tiles render
in reading order: by `row`, then by `col`. Spans are ignored there; a tile keeps
its natural height. This is stated here because an anchored layout has no
automatic answer for a window narrower than its columns, and the alternative —
horizontal scrolling — makes a dashboard unusable on the device most likely to
be reaching for it.

## Where the layout lives

The workspace is one `app_setting` row under the global scope, holding the JSON
above. That store is already there, already server-side, already what settings
use — so the desktop window and a browser tab read the same workspace, which
`localStorage` cannot deliver.

Writes go through the settings API like any other setting. The existing
`agent-active-view` and `agent-dashboard-layout` keys in `localStorage` stay
where they are: which page you had open last is genuinely per-window state, and
moving it would be a different change.

## The widget registry

A widget is an id, a title, a default span, and a component. The five existing
panels register unchanged; the cockpit renders from the registry rather than
from a template, and looks identical on the first run.

Module widgets arrive under the id the module contract already reserves
(`widgets` in the manifest, `<moduleId>__<widget>`), which is why that key was
reserved before this design existed.

## Navigation

`ActiveView` opens up. Core views keep their current ids; a page the operator
creates is `page:<id>`.

The prefix is not decoration. A closed union is a promise to every `v-if` chain
and comparison in `App.vue` that there are exactly eight values; opening it means
each of those places must answer "is this one of mine?" again. With a prefix
that question stays answerable in one line, and an unknown value falls into the
page branch rather than into a chain that has no case for it.

The navigation rail renders the core entries, then the operator's pages.

## Error handling

| Situation | Behaviour |
| --- | --- |
| A tile names an unknown widget | Rendered as a placeholder naming the widget; the tile stays in the layout |
| A drop or resize would overlap | Refused; the tile returns to its previous placement |
| A span would run past column 12 | Clamped at the edge while dragging; never stored out of range |
| The stored layout is unreadable | The built-in cockpit layout is shown and the stored value is left untouched, so a parse bug cannot destroy what the operator built |
| The settings write fails | The change stays on screen with a notice that it was not saved; it is not silently reverted |

## Testing

- The registry renders the five panels identically to the current cockpit — a
  component test asserting the same five tiles, so stage one is provably
  invisible.
- Placement: a tile at `col: 9, colSpan: 4` is refused (it would exceed 12);
  overlapping placements are refused; a legal move is stored.
- Collapse: below the breakpoint tiles render ordered by row then column.
- Persistence: a layout written in one client is read by another — the property
  `localStorage` could not provide.
- An unknown widget id survives a round trip through save and load.

## Slices

| # | Slice | Why in this order |
| --- | --- | --- |
| 1 | Widget registry; the cockpit renders from it, unchanged | Nothing is visible yet, so the registry is proven before anything depends on it |
| 2 | The layout model and its server-side store, with the built-in cockpit as the default | The store is what makes the rest testable |
| 3 | Edit mode: move, resize, add, remove, with the collision rule | The first slice the operator can see |
| 4 | Pages of their own, and the navigation that reaches them | Needs the union opened, which is the riskiest edit in this design |
| 5 | Module widgets appear in the picker | Needs a module that offers one |
| 6 | Stage two: the large views become tiles | Deliberately last — it reuses the interface the first five slices proved |

## Open questions

1. What is the fixed row height in pixels? A number picked from how the five
   existing panels look at `rowSpan: 1` is the honest way to choose it, so this
   is answered in slice 2 with a screenshot rather than guessed here.
2. Does removing a page ask for confirmation? Recommendation: yes, and the page
   is recoverable until the next write, since a layout is cheap to keep and
   expensive to rebuild.
