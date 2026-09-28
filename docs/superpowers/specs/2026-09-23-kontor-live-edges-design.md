# Kontor Zentrale, slice 9: live edges

Status: approved design, 2026-09-23. Replaces the "Live edges" section of
`2026-09-21-kontor-zentrale-design.md`.

## Goal

An edge in the hub answers one question: **which notes is this agent working
with right now?** A dashed line runs from an agent to each note it read in the
last ten minutes, a dotted one to each note it wrote. Edges are static and fade
with age; nothing animates.

## Data source: what the session logs show

Measured over 14 days of session JSONL on the operator's machine (2026-09-23):

| Access path | Calls | Path captured today? |
| --- | --- | --- |
| `curl …/vault/<path>` in a Bash call | ~830 | only inside the raw command string |
| Obsidian MCP (`mcp__obsidian__*`) | ~8 | no, the path sits in `input.path` |
| Read/Edit/Write on vault files | 0 | n/a |

Of the curl URLs, 680 use `…/vault/${OBSIDIAN_ROOT:-claude-memory}/…`, 71 a
literal root, the rest a shell variable (`$R`, `$F`, `$SCOPE`, …) whose value
only the shell knows. `-X PUT` is the common write; POST is rare; PATCH and
DELETE did not occur.

Consequences for the design:

- The parser's `lastTools` (last five calls, display-sanitised,
  `server/internal/parser/parser.go:297-331`) cannot carry a ten-minute window.
  A dedicated extraction is needed.
- Pipeline edges via `memory_injection` are **dropped**. Pipeline agents are
  Claude sessions whose own curl/MCP calls this extraction already covers; an
  injection happens at stage start and would fall out of a ten-minute window;
  and `memory_entry.source_ref` is not guaranteed to hold a note path
  (`server/internal/db/ent/schema/memory_entry.go:39-41`).

## Server

### Extraction in the parser

`ScanMessagesFrom` (`server/internal/parser/messages.go:108`) takes a callback
like `ScanMessages` already does, instead of hard-wiring token summation. The
incremental per-file cache entry behind `tokenUsageForFile`
(`server/internal/parser/parser.go:418`) gains a note window next to its
running token total: one pass over appended bytes feeds both collectors, and a
first sighting or inode change reseeds both from a full scan. No second cache,
no second notion of how far a file has been read.

Only `tool_use` blocks in assistant messages count, timestamped with the
message's `Timestamp`:

| Tool | Path from | Kind |
| --- | --- | --- |
| `mcp__obsidian__obsidian_read_note` | `input.path` | read |
| `mcp__obsidian__obsidian_create_note`, `mcp__obsidian__obsidian_edit_note` | `input.path` | write |
| `Bash` whose command contains `/vault/` | the URL segment after `/vault/` | write for `-X PUT/POST/PATCH` or `--request PUT/POST/PATCH`, otherwise read |

Every other tool, including search, list, move and delete, is ignored.

Rules for Bash commands:

1. Split the command into segments on `&&`, `||`, `;`, `|` and newlines. A
   method flag applies only to URLs in its own segment. A backslash-newline
   continuation is folded into a space first, so a wrapped line stays one
   segment.
2. Expand `${NAME:-default}` to `default`. The server cannot see the agent's
   environment, so the default is the only knowable value.
3. After expansion, a path that still contains `$` (a bare variable or a
   command substitution) is **dropped**, never guessed.
4. URL-decode the path. A path that does not end in `.md` (a directory
   listing) is dropped.

Output per session: `[]parser.NoteTouch{Path, Kind, At time.Time}` on
`SessionData.RecentNotes`, paths vault-relative as they appeared in the URL or
MCP argument. Per `(path, kind)` only the newest touch is
kept; touches older than ten minutes are pruned on every read; at most 50 are
kept per session, newest first, so a loop of PUTs cannot bloat the SSE payload.

Known gap: an agent that sets `OBSIDIAN_ROOT` to something other than the
default yields paths outside the configured root. They fail normalisation below
and are omitted — never drawn to a wrong note. A variable assigned in the same command (`URL=…; curl -X PUT "$URL"`, also
chained as `BASE=…/vault/$ROOT/dir`) is resolved before segments are split, and
the assignment itself counts as no touch; a variable set in an earlier Bash
call or by `export` in a sourced file stays unknown and its path is dropped. An
edge is drawn when the call is issued, not when it succeeds, so a denied or
still-pending write still shows as written.

### Contract

`sdk/types.go`, then `sdk.generated.ts` regenerated with `tygo`:

```go
type NoteTouchKind string // "read" | "write"

type NoteTouch struct {
    Path string        `json:"path"` // relative to obsidian.vaultRoot, the graph's form
    Kind NoteTouchKind `json:"kind"`
    At   string        `json:"at"`   // RFC 3339, same form as Agent.LastActivity
}

// on Agent:
RecentNotes []NoteTouch `json:"recentNotes,omitempty"`
```

`src/types.ts` re-exports `NoteTouch` and `NoteTouchKind`; no kinds list is
needed, the client keys its per-kind maps by the union type.

### Path normalisation

The graph lists paths relative to `obsidian.vaultRoot`; the parser emits
vault-relative ones. `server/internal/apps/obsidian` gains
`RootRelative(root, vaultPath string) (string, bool)` next to `NormalizeNotePath`
(`client.go:233`): it strips `vaultRoot/`, collapses `..` with the same logic as
`resolveVaultPath`, and rejects the root itself and anything outside it.

`RootRelative(root, vaultPath)` is a plain function, since the merger is built
before the Obsidian client exists in `server/serverapp/di.go`. The merger
receives it as `WithNotePathFn`, following `ScreenProbeFn` and `WithRegistry`
(`server/internal/merger/merger.go:264-282`). The window is re-applied there
against the current time, because the parser prunes only when a file changes
and an idle session's touches must still age out. `obsidian.vaultRoot` is `ApplyRestart`
(`server/internal/settings/registry.go:141`), so the function is fixed at
start. Without a configured root `RootRelative` rejects every path, so
`recentNotes` stays empty. It is applied at both places that copy session data onto an agent today:
`merger.go` (live) and `stale.go` (stale).

### Exposure

`Agent.recentNotes` rides `/api/agents/stream`, which sits behind the normal auth
(loopback host + JWT) but not `memory.read`, unlike the graph and open routes
(`server/internal/api/obsidian/handler.go:99`). This is new exposure:
`toolArgument` (`server/internal/parser/parser.go`, `pendingToolInput{Command,
FilePath}`) never read the Obsidian MCP tools' `input.path`, so those note paths
never reached the stream before, and curl paths that did were previously visible
only while among the last five `lastTools`, not for ten minutes. It ships
ungated: `memory.read` grants are scoped (global/project), not per viewer, so
gating a shared broadcast frame would exclude nobody who can already see the
stream's full shell commands. Worth revisiting once Kontor serves several people
with different rights.

## Client

### Edge computation

`src/features/hub/hubEdges.ts` (new) holds a pure
`liveEdges(placedAgents, drawnAgents, pathIndex, now)` returning
`{ from: [wx, wy], to: noteIndex, kind, alpha }[]`:

- An agent not in `drawnAgents` (under the docked rail,
  `src/features/hub/components/HubWidget.vue:197`) gets no edge.
- A path missing from the graph gets no edge.
- Nothing is keyed by array identity: every SSE tick delivers new arrays, and
  the canvas redraws every edge from scratch each frame.
- `alpha` falls linearly from 0.9 at age 0 to 0.2 at ten minutes.

### Drawing

`HubBrainCanvas.vue` gains a `drawEdges` pass between `drawLinks` and
`fillNotes`, so note dots sit on top of edges and the DOM agents above both.
Read: `setLineDash([4, 3])`. Write: `setLineDash([1, 3])` with round caps.
Colour from a theme token. Edges draw at every zoom level. The canvas redraws
on the existing triggers only (camera, props, theme) plus a 30-second clock
(`useNow`) that lets edges fade and expire between SSE ticks; no animation frame
loop.

Deliberately not built: hit-testing edges, edges on the minimap, note chips on
`HubAgentCard`.

### List view

The list view (`L`) holds the same content as the canvas. Under each agent row,
`HubList.vue` lists its recent notes — title, "read" or "wrote", relative time —
as focusable rows that fly to the note like the existing note rows.

## Testing

- **Parser (Go, table-driven, fixtures shaped like the logged commands):**
  default expansion, literal root, dropped variables and substitutions, method
  per segment, `--request`, URL decoding, non-`.md` dropped, MCP read/edit/create
  recognised and other MCP tools ignored, ten-minute pruning with an injected
  clock, newest-per-key dedupe, cap of 50, incremental append and inode reseed.
  The existing token tests stay green unchanged.
- **`RootRelative`:** prefix stripped; `..` escape, outside path and root itself
  rejected.
- **Merger:** the note function applies on the live and the stale path; absent
  function yields no notes.
- **`hubEdges.ts`:** undrawn agent, missing note, expired or unparsable
  timestamp, clock skew, alpha curve.
- **`HubBrainCanvas`:** a recording fake context sees `[4, 3]` for read and
  `[1, 3]` for write.
- **`HubList`:** sub-rows focusable and flying to their note.
- **E2E:** a complete agent fixture carrying `recentNotes` plus a mocked graph;
  asserts the list view rows.
- **In the app:** build, stop the running dashboard and desktop processes,
  print the new bundle's mtime, let a real agent read and write a vault note by
  curl, confirm a dashed and a dotted edge in the hub and their fade after a few
  minutes; screenshot as evidence.
- **Gates:** all four, run sequentially — `task test` regenerates
  `server/internal/db/ent/` and breaks a concurrent E2E server build.
