# A Kontor Session in the Mission Tile — Design

Status: approved by the operator, 2026-09-21
Date: 2026-09-21
Amended: 2026-09-21 — the session runs in `auto` permission mode and the tile
shows it as a chat (the agent modal's pane) instead of its terminal, at the
operator's request.

## Why

The Mission Control input is labelled "Or ask for anything — tasks, this
interface, the system itself" (`src/features/mission/components/MissionInput.vue:56-60`),
but `submit()` (`MissionInput.vue:20-50`) can only navigate, reject a slash
command, or capture. Capture turns the text into a backlog item in
`projects[0]` (`src/composables/useCapture.ts:30`), and `/api/projects` sorts by
creation date (`server/internal/db/repo/project_repo.go:90-93`). Every capture
therefore lands in the oldest project — on this installation a client project,
with the client repository as its working directory. Spotlight does the same when
nothing matches (`src/components/SpotlightSearch.vue:127-143`).

What the operator wants from that input is a conversation: a Claude session that
can read and steer Kontor, sparring first and creating tasks only when asked.
Nothing like it exists. "+ New Agent" starts an interactive `claude`, but its MCP
config carries only the channel bridge (`server/internal/api/agents/spawn.go:387`
passes no task API to `WriteTempConfig`, `server/internal/channelconfig/channelconfig.go:104`).
The task API itself already exposes the controls — tasks, refinement, approvals,
permissions, schedules, memory, Obsidian, GitHub (`server/internal/mcp/auth.go:20-53`).
What is missing is a way to start such a session, a short-lived credential for
it, a briefing, and a place in the UI.

## Decisions taken with the operator

1. **The session lives in the Mission tile**, not only as an agent card. The
   centre column shows "Needs you" at the top, sized to its content, and the
   Kontor tile below it takes the remaining height.
2. **Capture goes away.** The Mission input always talks to Kontor. When
   Spotlight matches nothing, Enter hands the text to the Kontor tile. The
   session chooses the project from context and asks when it cannot tell.
3. **The session key carries every scope except `keys:manage`**, so a session
   can never mint itself a credential that outlives it — a real boundary only
   under JWT auth. Under the documented local-trust mode (`auth.mode=none`),
   the session's `auto` permission mode is the boundary instead: Claude Code's
   classifier, not a human prompt, decides each write. The tile has no
   terminal to answer a prompt in.
4. **One session per installation, living until the operator ends or renews
   it.** It survives a view switch, a reload and a server restart.

## Non-goals

- A terminal in the tile. The session's AskUserQuestion screens surface in
  `NextThing` above the tile, like any agent's; its terminal stays reachable
  from its agent card.
- Several Kontor sessions at once.
- An idle timeout.
- Registering the tile as a composable-workspace widget. The component is built
  so that it can be registered later without changes.
- The first start in a new session dir shows Claude Code's one-time
  folder-trust dialog (default "No, exit"); the operator confirms it once.

## Behaviour

| Input in the tile | No session running | Session running |
| --- | --- | --- |
| An exact view name | Navigates, as today | Navigates, as today |
| A `/command` | Refused with the current message | Sent to the session |
| Anything else | Starts a session; the text is its first prompt | Sent to the session |

Text is delivered to a running session through the existing message endpoint
(`POST /api/agents/{pid}/message`, `server/internal/api/router.go:530`).

Once the scanner lists the session's pid, the tile shows it through
`AgentSessionPane` — the same header, context, transcript and prompt the agent
modal uses — titled "Kontor", with **New** (end the current session and start
a fresh one) and **End** in its header. The pane's prompt replaces the tile
input; navigation stays in Spotlight. Until the pid is listed, the tile keeps
its own header, state and input from the table above. "Nothing needs you"
shrinks to a single faint line so the tile gets the height.

The reading badge under the input (`src/features/mission/composables/useReading.ts`)
loses its `capture` reading and gains `ask`, labelled START KONTOR or SEND TO
KONTOR depending on whether a session is running.

## Server

A new service, `kontorsession`, owns the session. Its HTTP surface:

| Route | Effect |
| --- | --- |
| `GET /api/kontor-session` | `{ "pid": n }` of the live session, or `{ "pid": null }` |
| `POST /api/kontor-session` | Starts a session with `{ prompt }`, returning `{ "pid": n, "started": true }`; if one is already running, returns it unchanged as `{ "pid": n, "started": false }` |
| `POST /api/kontor-session/renew` | Ends the running session, then starts one with `{ prompt }`; the prompt may be empty |
| `DELETE /api/kontor-session` | Ends the running session; ending none is not an error |

Start, renew and end are serialised by one mutex in the service, so two browser
windows cannot start two sessions.

### The spawned process

A native `claude` from the global default spawner, with:

- `--permission-mode auto`, replacing whatever permission flags the default spawner carries, so no write waits on a prompt nobody can see.
- `--append-system-prompt <briefing>`. Not `--system-prompt`, which the
  existing spawn path uses (`spawn.go:312`) and which replaces Claude Code's own
  system prompt.
- `-n Kontor`, so the session is recognisable in `/resume` and on its agent card.
- `--mcp-config <file> --strict-mcp-config`. The file carries `kontor-tasks`
  with the session key, the channel bridge, and the operator's other user-scope
  MCP servers, the same way stage runs carry them. Strict mode keeps a stale
  user-scope `kontor-tasks` registration out.
- `--allowedTools` listing the tools of every `:read` scope, derived the way
  `StageRunAllowedTools` derives its list (`server/internal/mcp/stagekey.go:39`).
  Every other Kontor tool is left to auto mode.

The briefing is a Markdown file embedded in the service package. It says what
Kontor is, which tools exist, that the session creates no backlog item unless
asked, and that it picks a task's project from context and asks when unsure. It
also says the session runs in auto mode, so it confirms in chat before granting
a permission, approving a plan or pending requests, merging, or creating
anything unasked.

### The working directory

`os.UserCacheDir()/kontor/session` (`~/Library/Caches/kontor/session` on macOS,
`~/.cache/kontor/session` on Linux), created on first start. It cannot sit next
to the module data under `~/.claude/kontor/`: the spawn policy always refuses a
cwd under `~/.claude` or `~/.config` (`server/internal/services/spawn_policy.go:207-213`).
The directory is registered as an explicit extra root of the spawn policy, so
the policy stays the one gate every spawn passes through. Transcripts do not
live here — Claude writes them under its own config directory — so the directory
holds nothing worth keeping.

Every start first removes `<Dir>/.claude`, so a `settings.local.json` an
earlier session wrote there — Claude Code's own "don't ask again" answers —
never silently applies to the next one. The session always runs under the pty
host, the transport that delivers messages live and whose `<pid>.pty.json`
file the agent card's terminal needs to attach.

## The credential is the session record

A new key kind, `ApiKeyKindKontorSession`, joins `user`, `stage_run` and
`module` (`server/internal/db/repo/api_key_repo.go:17-21`), and the `api_key`
table gains an optional `session_pid`. The active row of that kind *is* the
session: its pid is what `GET` returns, and revoking it is what ending means.
There is no second table, so there is no second copy of the state that could
disagree with the key.

The key's scopes are derived: every scope that `ToolScopeMap` assigns, minus
`keys:manage`. A scope added later reaches the session without an edit here.

### Ending

Every way a session can end calls one function, `end(reason)`, which stops the
process if it is still alive, revokes the key, removes the MCP config file and
audits the reason. Its callers:

| Trigger | Caller |
| --- | --- |
| The operator presses End or New | `DELETE` / `renew` |
| The process exits while the server runs | The pty host's exit watch plus `WaitForExit` in `SpawnSession` (`spawn_session.go`) |
| The server restarts while the process runs | Reconcile at boot re-arms the exit watcher for the recorded pid |
| The process died while the server was down | Reconcile at boot finds the pid dead and calls `end` |

A key that cannot be minted means no spawn. A spawn that fails revokes the key
it was given before the error is returned.

## Client

- `useKontorSession` holds the shared state — `pid`, `status`, `start(text)`,
  `send(text)`, `end()`, `renew(text)` — so the tile and Spotlight act on the
  same session.
- `KontorTile.vue` renders `AgentSessionPane` for a listed session, and its own
  header and input otherwise. It does not know about `MissionControlView`.
- `MissionControlView.vue` stacks `NextThing` and `KontorTile` in the centre
  column. `NextThing`'s empty state becomes one line.
- `useCapture.ts` is deleted. Spotlight's "nothing matched" hint and handler
  change from capture to handing the text to Kontor and switching to Mission.

## Failure handling

| Situation | Result |
| --- | --- |
| Minting the key fails | Nothing is spawned; the tile shows the error |
| The spawn fails | The key is revoked; the tile shows the error |
| Two windows start a session at once | The mutex lets one through; the other receives the same pid |
| The process exits on its own (`/exit`) | The watcher ends the session; the tile returns to its empty state |
| The server restarts | The session keeps running; the tile reattaches through `GET` |
| Delivering a message fails | The text stays in the input and the error is shown |

## Testing

- **Go:** service tests through the spawn DI seam, never a real agent — one
  session at a time; renew ends before it starts; reconcile revokes a dead pid
  and re-arms a live one; a failed spawn revokes the key. An MCP auth test that
  a session key is refused `create_api_key`. A scope test that the derived set
  lacks `keys:manage` and contains every other scope. The `session_pid`
  migration's down path.
- **Vitest:** `useReading` without capture; `useKontorSession`; the tile's
  empty, starting, running and failed states; Spotlight's hand-off.
- **Playwright:** the Mission view against a stubbed `/api`.
- **In the running app:** build, restart the desktop app, print the new
  artifact's mtime, then enter "Lies das Handoff für Phase 4 im Dashboard und
  starte mit der Umsetzung bzw. deren Planung". Expected: a Kontor session in
  the tile and no new backlog item.

## Documentation

README (the Mission input and the Kontor session), CHANGELOG (Added: Kontor
session; Removed: capture from the Mission input and Spotlight).
