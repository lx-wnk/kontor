# The Zentrale — Design

Status: slices 1-8 implemented on feat/kontor-zentrale (2026-09-22); slices 9-11 open
Date: 2026-09-21
Extends: `2026-09-20-composable-workspace-design.md` — the anchored grid, the
collision rule, edit mode, own pages and the server-side layout store stay exactly
as specified there. This document adds what the grid is filled with.
Prototype: `2026-09-21-kontor-zentrale-prototype.html` — open it in a browser. It
runs on synthetic data and is the visual reference for every slice below.

## Why

Two pages answer one question today. Mission control
(`src/features/mission/MissionControlView.vue`) is three fixed columns: live work,
the next decision plus the Kontor session, GitHub and memory. The cockpit
(`src/features/cockpit/components/CockpitView.vue:9-17`) is five panels in a
flowing grid that leaves half the page empty. Both are arranged in a template, and
both already share parts — Mission imports `GitHubPanel` and `MemoryPanel` from
the cockpit.

The operator wants one start page with a centre worth looking at first — a hub
that shows the running system and the knowledge it draws on — and side tiles they
change, swap and maintain themselves.

## Decisions taken with the operator

1. **"Tile tagging" means docking.** It is the anchored grid of the 2026-09-20
   spec, unchanged. No tags, filters or per-tile parameters.
2. **Mission and cockpit become one page, the Zentrale.**
3. **The centre is a zoomable hub** combining a live orbit of agents with the
   Obsidian knowledge graph ("option D": the orbit of option B with the second
   brain of option C).
4. **The Kontor session is a tile of its own.** Collapsed it shows the latest
   output; opened it grows for input.
5. **Own palette.** The RUBRIC Agentic OS screenshot the operator shared was a
   reference for the arrangement — a central hub between two tile columns — not
   for its colours.
6. **Every tile, the side tiles included, is an ordinary widget**: moved, resized,
   added, removed and swapped in the UI.

## Non-goals

- Per-tile configuration (filters, tags, projects per tile).
- A force-directed layout. Positions in the hub are deterministic (see below).
- Editing notes from the hub, or showing note bodies beyond a preview line.
- Layouts shared between machines or exported.
- A second Kontor session.

## The Zentrale page

`ActiveView` loses `mission` and `cockpit` and gains `zentrale`. A stored
`agent-active-view` of `mission` or `cockpit` reads as `zentrale`, so nobody lands
on the fallback after the update. This change lands together with the base spec's
Task 4, which opens the same union for `page:<id>` — one edit to the riskiest
place in the design, not two.

### Rows share the window height

The base spec left the row height open (its question 1). The hub has to fill the
window the way the Kontor tile fills Mission control today (`flex-1`), so the
answer is: **rows split the page height equally** —
`grid-template-rows: repeat(N, minmax(56px, 1fr))`, where N is the lowest row any
tile on the page reaches (`max(row + rowSpan − 1)`). Every row
still has one height, which is what the base spec's argument for fixed rows needs;
only the source of the number changes. Below 56px per row the page scrolls instead
of squeezing.

### Default layout (12 columns × 12 rows)

| Widget | col | row | colSpan | rowSpan |
| --- | --- | --- | --- | --- |
| `live-work` | 1 | 1 | 3 | 5 |
| `agents` | 1 | 6 | 3 | 3 |
| `routines` | 1 | 9 | 3 | 4 |
| `hub` | 4 | 1 | 6 | 11 |
| `kontor` | 4 | 12 | 6 | 1 |
| `github` | 10 | 1 | 3 | 3 |
| `pipeline` | 10 | 4 | 3 | 3 |
| `memory` | 10 | 7 | 3 | 3 |
| `cost-today` | 10 | 10 | 3 | 3 |

## Widgets

`WidgetDef` from the base plan gains a minimum span:
`{ id, title, defaultColSpan, defaultRowSpan, minColSpan, minRowSpan, component }`.

| Id | Component today | Min span |
| --- | --- | --- |
| `hub` | new | 6 × 6 |
| `kontor` | `KontorTile.vue`, reworked | 4 × 1 |
| `live-work` | `LiveWorkRail.vue` | 3 × 3 |
| `agents` | `AgentsPanel.vue` | 3 × 2 |
| `pipeline` | `PipelinePanel.vue` | 3 × 2 |
| `routines` | `RoutinesPanel.vue` | 3 × 2 |
| `github` | `GitHubPanel.vue` | 3 × 2 |
| `memory` | `MemoryPanel.vue` | 3 × 2 |
| `cost-today` | new; reads the value the status bar shows (`AppStatusBar.vue` `todayCostLabel`) | 2 × 2 |

`NextThing.vue` is not a widget; it becomes the shell's queue (next section).

### Changing, swapping and maintaining tiles

Edit mode is the base spec's, plus one verb: **swap**. Each tile in edit mode
offers ⇄; the picker lists the widgets whose minimum span fits the tile's current
span, and choosing one replaces the widget id in place — anchor and size unchanged.
A widget that does not fit is listed disabled with the reason. Resizing below a
widget's minimum span is refused like an overlap is.

"Maintained" means maintained in the UI: no file, no setting to edit by hand.

## Needs you — owned by the shell

One queue holds every permission request and every agent question, ranked by the
existing `rankNextThings` (`src/features/mission/composables/useNextThing.ts:50`).
It belongs to the page shell, not to a widget, because a pending decision must stay
visible whatever the operator does to their layout:

- On a page with a hub, the queue docks into the hub's top edge.
- On every other page — including one where the hub was removed — it renders as a
  strip above the grid.
- The count appears in `document.title` and on the hub's core.
- Empty, it is one calm line ("Nothing needs you right now").

Each item reads in plain words what is asked and why, with its actions: a
permission offers *Allow once*, *Deny* and *Go to agent*, plus *Allow for this
routine* when the request comes from a routine;
a question offers *Answer…* and *Go to agent*. The queue pages with ‹ › and says
"1 of N".

## The Kontor tile

- **Collapsed** (one row): the session's state in colour and word — working,
  waiting, idle — then its last assistant output (`Agent.lastOutput`,
  `src/sdk.generated.ts:466`) on one line, then the shortcut. No server work: both
  arrive on the agents stream already. Without a session it offers *Start Kontor*
  (`useKontorSession().start`).
- **Opened** by a click, or by `/` anywhere outside an input: the tile grows out of
  its own position towards the larger free side of the window, up to 64% of the page
  height, as an overlay above its neighbours — it does not re-flow the grid. The
  body is `AgentSessionPane`, as today. `Esc` collapses it and returns focus.
- The growth is one continuous transition from the collapsed row, so opening reads
  as zooming into the same object, not switching screens.

## The hub

### One coordinate system for agents and notes

| Axis | Meaning |
| --- | --- |
| Angle | the **sector**: which project or area a note or agent belongs to |
| Distance from the centre | **freshness**: `r = r0 + (rMax − r0) · √(min(age, 730 d) / 730 d)`, age from the note's `mtime` |
| Position within a sector | a stable hash of the note path, so a refresh never moves a note |

Sectors are the top-level folders under `obsidian.vaultRoot`. A folder whose name
equals an agent's `projectName` gets its own sector even when it is nested (e.g.
`claude-memory/private/agent-dashboard`). A sector's angle is proportional to the
square root of its note count, with a floor of 24°.

An agent sits on the angle of the sector matching its project; without one it
joins a sector named "Other". Its distance from the centre never drops below a
fixed on-screen radius (116 px, 88 px when it waits for the operator), so the core
and the agents never collide at any zoom. A waiting agent sits closer to the core
than a working one and pulses.

### Semantic zoom

`rel` is the camera scale relative to "fit all".

| Level | `rel` | What is drawn |
| --- | --- | --- |
| Overview | < 1.8 | core, agents with labels, launcher ring, sector names, notes as dots, freshness rings |
| Topics | 1.8 – 4 | notes larger, labels on hub notes (most backlinks), halos on notes touched today |
| Notes | ≥ 4 | note titles, with greedy label culling by priority: hub notes, notes an agent touches, fresh notes, link count |

The levels switch at hard thresholds; they do not cross-fade. Core, agents,
launchers and labels keep a constant on-screen size at every level, so they act as
landmarks.

### Navigation

- Wheel or pinch zooms around the pointer; dragging pans; the scale is clamped to
  0.6×–14× of fit.
- Keys, while the hub has focus: arrows pan, `+`/`−` zoom, `0` fits all, `F`
  widens the hub to the full page width for this window, `L` opens the list view,
  `1`–`8` fire a launcher, `Esc` closes the topmost layer and otherwise fits all.
- A minimap in the bottom-right corner shows the whole space and the viewport; a
  click there flies to the spot.
- Clicking a sector name, a note, an agent, a "recently touched" entry or a search
  hit (`⌘K`) flies there.
- The camera is per-window state and is not written to the server layout.

### Launchers

Up to eight: the core views and the operator's own pages, derived from the same list
the navigation rail and the command palette read (`ACTIVE_VIEWS` plus the page
list), with *New page* last. More than eight turns the eighth slot into "…", which
opens the full list. The prototype's GitHub and Obsidian launchers stand in for
applications; an application joins the ring once it has a page. At overview they form a ring; beyond 1.5× fit they move to a
dock along the hub's left edge, so navigation never leaves the screen. Labels
appear on hover and focus.

### Live edges

Superseded by `2026-09-23-kontor-live-edges-design.md`: static dashed (read)
and dotted (write) edges from an agent to the notes it touched in the last ten
minutes, extracted from its curl and Obsidian MCP calls. Pipeline edges and
flowing animation were dropped there.

### The note card

Title, vault path, last change, link and backlink chips that fly to their note,
*Ask Kontor about this* — which opens the Kontor tile with `[[path]]` prefilled —
and *Open in Obsidian*.

### Rendering

- Notes and links: **Canvas 2D**. The operator's vault has 5,691 notes (measured
  2026-09-21); SVG stops being smooth around a thousand nodes, Canvas 2D holds
  roughly ten thousand, and WebGL's weak spot — text — is exactly what the notes
  level needs. Hit-testing is a linear scan of the visible notes at click time.
- Core, agents, launchers, sector names, queue, cards: **DOM** above the canvas, on
  the same camera — crisp text, real buttons, focus order and ARIA for the elements
  that change every few seconds.
- No new dependency. The camera is hand-rolled, as in the prototype; sigma.js 3
  (MIT) is the fallback if measurement shows Canvas 2D falling short.
- Colours come from the theme tokens, so the hub follows light and dark mode.

### Accessibility

The list view (`L`) holds the same content as the canvas — agents with their state,
recently touched notes — as focusable rows. All motion (flights, the pulse, the
Kontor tile's growth) is dropped under `prefers-reduced-motion`.

## Server: `GET /api/obsidian/graph`

Built from two JsonLogic searches against the Obsidian Local REST API
(`POST /search/`, `Content-Type: application/vnd.olrapi.jsonlogic+json`):

| Query | Returns | Measured 2026-09-21 |
| --- | --- | --- |
| `{"var":"stat.mtime"}` | every note with its mtime | 5,691 notes, 0.22 s, 709 KB |
| `{"var":"links"}` | every note with outgoing links, already resolved to paths | 193 notes, 0.06 s, 157 KB |

- Results are confined to `obsidian.vaultRoot`, the boundary `SearchUnderRoot`
  already enforces for the memory index, so the hub shows exactly what Kontor may
  read.
- The response is compact: `{ configured: true, notes: [[path, mtime], …],
  links: [[fromIndex, toIndex], …] }`. A title is the file name without `.md`; no
  note body is read.
- Cached in memory for 60 s, with concurrent requests sharing one rebuild.
- Gated by the capability that already guards the memory entries (`memory.read`).
- With Obsidian not configured — the state of the operator's installation on
  2026-09-21, `obsidian.baseURL` is empty — it answers `{ configured: false }`, and
  the hub shows the orbit alone with a *Connect Obsidian* hint that opens settings.

## Visual language

- Tokens in `src/styles/main.css`, for dark and light: the prototype's values for
  dark (`--bg #0d0f12`, `--card #14171c`, `--line #242932`, accent `#7c9cff`,
  ok `#3ddc97`, waiting `#f5b544`, danger `#ff6b6b`); light derived.
- The tile frame (`CockpitPanel.vue`) gets the prototype's header: icon, uppercase
  label, one action on the right. A tile with a key figure shows it large on top.
- State is always colour **and** word.

## Error handling

| Situation | Behaviour |
| --- | --- |
| Obsidian not configured | Orbit only, plus a *Connect Obsidian* hint |
| The graph request fails | Orbit only, a quiet notice in the hub; retried on the next refresh |
| An agent's project matches no sector | The agent joins "Other" |
| A layout with a tile below its widget's minimum span | Rendered at the stored size; edit mode flags it |
| The hub removed from a page | The queue renders as a strip above the grid |
| No Kontor session | The collapsed tile offers *Start Kontor* |

## Testing

- Component: the default Zentrale layout renders the nine widgets at their anchors.
- The queue renders on a page with no hub, and inside the hub when there is one.
- Swap keeps anchor and span; a widget whose minimum does not fit is refused.
- A stored `mission` or `cockpit` view reads as `zentrale`.
- Hub geometry, as pure functions: the radius grows with age; a note's position is
  identical across two builds; sector angles sum to 360° and respect the floor;
  agents never come closer to the core than the on-screen floor.
- The level thresholds switch at 1.8 and 4.
- Server: the graph handler against a fake REST API — confinement to `vaultRoot`,
  `configured: false` without a base URL, one upstream rebuild for concurrent
  requests.
- E2E: open the Kontor tile with `/`, close it with `Esc`; zoom into the hub with
  the keyboard and reach the notes level; the list view lists the agents.

## Slices

This order replaces the base spec's slice table.

| # | Slice | Why here |
| --- | --- | --- |
| 1 | Widget registry, mission widgets included; nothing visible changes | Proves the registry before anything depends on it |
| 2 | Layout model, server store, rows sharing the window height | The store makes the rest testable |
| 3 | Grid rendering and edit mode, swap included | The side tiles become changeable — the operator's condition |
| 4 | The Zentrale: mission and cockpit merged, default layout, `ActiveView` fold, own pages | One edit to the union, not two |
| 5 | The shell's queue and the Kontor tile | Both only need data that exists today |
| 6 | Tokens and tile frame | Cheap, and every later slice renders in it |
| 7 | Hub I: orbit, launchers, camera, minimap, list view — live data only | Proves the camera and landmarks without the vault |
| 8 | Hub II: graph endpoint, canvas brain, semantic levels, note card; the `memory` tile lists recently touched notes | Needs the camera from 7 |
| 9 | Live edges | Needs both layers; starts with the unverified tool-trail check |
| 10 | Module widgets in the picker | Base spec slice 5 |
| 11 | The large views as tiles | Base spec slice 6 |

## Open questions

1. *Open in Obsidian* needs the vault's name for an `obsidian://open` link. Derive
   it from the REST API or add a setting? **Answered:** neither — the REST API's
   own `POST /open/{filename}` (`OpenNote` in `server/internal/apps/obsidian/client.go`)
   opens a note by its vault-relative path alone; no vault name is needed.
2. Which folder depth makes a good sector for a vault whose top level is one large
   private folder (5,371 of 5,691 notes sit under `Privat/`)? The rule above keeps
   it one sector; slice 8 checks it on screen before anything more is built.
   **Answered** with the measured split on the operator's vault: `Privat/` holds
   its 5,371 notes as one sector (≈ 213° of the 360° ring, off the 24° floor), and
   the remaining folders share the rest — one level was enough, no deeper split
   was needed.
