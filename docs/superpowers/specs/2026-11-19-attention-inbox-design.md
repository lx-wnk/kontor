# Attention Inbox — Design (Roadmap Step 1 "Attention")

Date: 2026-11-19 · Grade: production · Plan: `docs/superpowers/plans/2026-11-19-attention-inbox.md`

## Goal

Make "what needs me" scannable, clearable and reachable from every view:

1. The needs-you queue becomes a stacked list of cards. Each card can be answered inline. Each card can be put away until the agent posts something new.
2. Attention clears on engagement. The cleared state is stored on the server in SQLite, so it survives a reload and syncs across browsers.
3. The single-key triage keys `n` / `a` / `d` / `⇧A` work on every view. The dashboard roster gets filter chips with live counts.

Pattern source: Agentastic / Orca inbox (unread until engaged, list not carousel, fleet filter chips).

## Current state (CODE-READ)

- `src/features/mission/components/NeedsYouQueue.vue:21-56` shows one `NextThing` at a time with `‹ ›` and "1 of n". Variants `strip` and `docked`.
- Callers: `src/App.vue:395` (strip, kinds from `needsYouPlacement`) and `src/features/hub/components/HubWidget.vue:771` (docked, scrolls inside the hub queue box `src/features/hub/hubLaunchers.ts:74-84`).
- `src/composables/needsYouPlacement.ts:12-18`: dashboard shows the strip with `kinds: ['plan']`, because the dashboard triage band (`AgentTriageBand`) covers the rest.
- The list is ranked once in `src/App.vue:114` by `rankNextThings` (`src/features/mission/composables/useNextThing.ts:57-147`) and provided as `NEEDS_YOU` (`src/composables/openTask.ts:14`). The title count and the hub core count read the same list.
- Kinds today: `permission | question | dialog | capability | plan` (`useNextThing.ts:16`). No `finished` or `failed` kind exists in the queue. The triage band uses a second classifier, `attentionFor` (`src/utils/attention.ts:18-80`), which has `error` ("Run failed") and `yourTurn`.
- `NextThing.vue:29-40` already derives a per-item key: `permission:<request.id>`, `capability:<decision.id>`, `<kind>:<pid>`, `task:<taskId>`.
- Keys: `src/App.vue:226-271`. The single-key branch returns when `activeView !== 'dashboard'` (`:242`). `n` cycles `attentionAgents`, `a`/`d` resolve the focused agent, `⇧A` approves all, `c` toggles the dashboard layout.
- Roster: `src/features/cockpit/components/DashboardView.vue:36-44` (project + spawner filter, sort, group). Toolbar: `src/components/shell/DashboardToolbar.vue`.
- Server: new tables are added by ent `Schema.Create` without `WithDropColumn` (`server/internal/db/client.go:151`). SSE resources use `sse.TypedBroadcaster[E]` (`server/internal/sse/typed_broadcaster.go`). Write routes in the protected group get `RequireSameOriginForMutations` (`server/internal/api/router.go:307-322`, `middleware.go:69`).
- `Agent.lastActivity` is RFC3339 with second precision (`server/internal/merger/merger.go:494`).

## Design

### 1. Attention item identity

Each `NextThing` gets two new fields, set in `rankNextThings`:

| Kind | `key` | `since` (watermark source) |
| --- | --- | --- |
| permission | `permission:<request.id>` | `request.requestedAt` |
| capability | `capability:<decision.id>` | `decision.requestedAt` |
| plan | `plan:<task.id>` | `task.updatedAt` |
| question | `question:<agent.sessionId>` | `agent.lastActivity` |
| dialog | `dialog:<agent.sessionId>` | `agent.lastActivity` |
| failed (new) | `failed:<agent.sessionId>` | `agent.lastActivity` |
| finished (new) | `finished:<agent.sessionId>` | `agent.lastActivity` |

`sessionId`, not `pid`: a pid changes on restart, the session does not. `NextThing.vue` uses `next.key` instead of its own `itemKey` derivation (one owner).

New kinds (`useNextThing.ts`):

- `failed`: `attentionFor(agent, null)?.kind === 'error'`.
- `finished`: `agent.status === 'finished'` or `attentionFor(agent, null)?.kind === 'yourTurn'`.
- One exported helper `agentOutcome(agent): 'failed' | 'finished' | null` holds this rule. `rankNextThings` and the view hook (below) both call it.
- `KIND_RANK`: `failed: 5`, `finished: 6` (below every blocking kind).
- `WHY.failed`: `'The run stopped with an error. Look at it or send the agent a new instruction.'`
- `WHY.finished`: `'The agent finished its turn and waits for your next instruction.'`

### 2. Marks (server, SQLite)

A mark says: "the user has dealt with item `key` up to time `seenThrough`". An item is hidden when a mark exists and `Date.parse(mark.seenThrough) >= Date.parse(item.since)`. When the agent posts something new, `since` moves past the mark and the item is back.

The server stores marks only. It does not derive attention items. The client already derives them from the agent, task and permission streams.

ent schema `AttentionMark` (`server/internal/db/ent/schema/attention_mark.go`):

- `IDTimestampsMixin`
- `item_key` string, unique, not empty, max 160
- `seen_through` time

Repo `AttentionMarkRepo` (`server/internal/db/repo/attention_mark_repo.go`):

- `List(ctx) ([]*ent.AttentionMark, error)`
- `Mark(ctx, key string, seenThrough time.Time) (*ent.AttentionMark, bool, error)`. Inside one transaction: if a row exists with `seen_through >= seenThrough`, return it with `changed=false`. Else upsert and return `changed=true`. A stale browser therefore cannot move a mark backwards.

API (package `server/internal/api/attention`, mounted in the protected group):

| Route | Behaviour |
| --- | --- |
| `GET /api/attention/marks` | `200 {"baseline", "marks": [{"key","seenThrough"}]}`, times are `RFC3339Nano` UTC; `baseline` per R12 |
| `PUT /api/attention/marks` | body `{"key","seenThrough"}`. `200` with the stored mark. Idempotent: the same body twice gives the same row and one broadcast. |
| `GET /api/attention/stream` | SSE. Event `{"type":"attention_marked","mark":{"key","seenThrough"}}`, sent only when `changed=true`. |

Boundary validation on `PUT` (all `400 {"error": …}`):

- Body over 4 KiB, invalid JSON, unknown fields.
- `key` not matching `^[a-z]+:[A-Za-z0-9._-]{1,128}$`.
- `seenThrough` not parseable by `time.Parse(time.RFC3339Nano, …)` or zero time.

The server does not validate the kind prefix against a kind list. A list would be a sixth hand-kept TS↔Go parity pair for no protection gain.

### 3. Client marks state

`src/features/mission/composables/useAttentionMarks.ts`, module-level state like `useCloudSessions.ts`:

- `marks: ShallowRef<ReadonlyMap<string, string>>`, filled by `GET` and kept current by `createSseResource` on `/api/attention/stream` (refetch on reconnect).
- `markSeen(key, since): Promise<void>`: optimistic local set, then `PUT`. On failure, revert and `toast.error`.
- Pure `isPutAway(thing, marks): boolean` and `visibleNextThings(things, marks): NextThing[]`.

`App.vue` ranks, then filters: `needsYou = computed(() => visibleNextThings(rankNextThings(…), marks.value))`. The title count, the hub core count and both queues read the filtered list.

### 4. Engagement rules

| Item | Clears when |
| --- | --- |
| finished | The agent detail opens (`selectedAgent` changes to that agent). `App.vue` calls `markSeen('finished:'+sessionId, lastActivity)` when `agentOutcome(agent) === 'finished'`. |
| failed | "Put away" on the card, or a successful send to that agent (`useAgentPrompt.ts` send path → `markSeen('failed:'+sessionId, lastActivity)` when `agentOutcome(agent) === 'failed'`). |
| question, dialog | The answer clears the source. "Put away" writes a mark. |
| permission, capability, plan | Approve / deny / plan approval clears the source. "Put away" writes a mark. |

### 5. Stacked list (`NeedsYouQueue.vue`)

- Header row: `Needs you · <n>` (`data-testid="needs-you-count"`). The `‹ ›` buttons and "1 of n" go away.
- One `<li>` per item in a `<ul>`, each with a `NextThing` card and a "Put away" button (`data-testid="needs-you-put-away"`, `aria-label="Put away until the agent posts something new"`).
- `strip` and `docked` keep today's container rules: strip hides when empty, docked always renders (calm line when empty), strip keeps `role="status"` + `aria-live="polite"`. `kinds` filter stays.
- `NextThing.vue`: the "NEXT" divider goes (the list header replaces it). `failed` and `finished` cards show the agent's project name, `formatErrorState(errorState)` (failed) or `lastOutput` (finished, one line, truncated by CSS), and an "Open agent" button that calls `useAgents({ autoStart: false }).selectAgent(agent)`.
- The hub docked box stays bounded by `QUEUE_MAX_HEIGHT_SHARE` and scrolls (`overflow-y-auto` is already on the docked wrapper).

### 6. Triage keys on every view

- Dashboard: unchanged. Keys act on the triage band (`attentionAgents`, `focusedSessionId`). `c` stays dashboard-only.
- Every other view: keys act on the visible needs-you list.
  - `n`: focus moves to the next item key, wrapping. The card gets `aria-current="true"`, a focus ring, and `scrollIntoView({ block: 'nearest' })`.
  - `a`: focused `permission` → `allow_once`; focused `capability` → `allow`. Other kinds: no-op.
  - `d`: focused `permission` → `deny_once`; focused `capability` → `deny`.
  - `⇧A`: `allow_once` for every `permission` item in the visible list. Put-away items are not in the list, so they are never approved blind.
- Guards stay in `App.vue` and apply to both branches: `isTypingTarget`, open overlay or dialog, Ctrl/Meta/Alt, and the `kbResolving` double-fire guard.
- Focus state: `src/features/mission/composables/useNeedsYouTriage.ts` owns `focusedKey` and the actions. `App.vue` creates one instance and provides `focusedKey` via a new `NEEDS_YOU_FOCUS` injection key in `src/composables/openTask.ts`. When the focused item leaves the list, focus resets to `null`; `a`/`d` then do nothing until `n` is pressed.

### 7. Fleet filter chips

- `src/utils/agentGroup.ts`: `FLEET_FILTERS = [{value:'all',label:'All'},{value:'needsYou',label:'Needs you'},{value:'working',label:'Working'},{value:'idle',label:'Idle or done'}] as const` and `fleetBucket(agent, secs): 'needsYou' | 'working' | 'idle'`:
  1. `needsYou` if `attentionFor(agent, secs)` is not null and its kind is not `yourTurn`.
  2. else `working` if `agentDisplayStatus(agent) === 'working'`.
  3. else `idle`.
  The buckets partition the roster, so the three counts add up to All.
- `src/features/cockpit/components/FleetFilterChips.vue`: a `role="group"` with `aria-label="Filter agents by state"`; one button per filter with `aria-pressed` and the count (`data-testid="fleet-chip-<value>"`).
- `DashboardView.vue`: counts come from the roster after project, spawner and search filters, before the chip filter. The chip filter is applied before sort and grouping.

## Rulings

| # | Ruling | Why | Cost if wrong |
| --- | --- | --- | --- |
| R1 | "Mark seen" and "Put away" are the same server operation and the same button, labelled "Put away". | Both mean "hide until the agent posts something new". Two buttons with one effect confuse. | A label map per kind; no server change. |
| R2 | `finished` and `failed` become queue kinds. The brief's clearing rules need them in the list. | Without them, rules 2a/2b have nothing to act on. | Remove two kinds and their `KIND_RANK`/`WHY` rows. |
| R3 | The title count and the hub core count follow the filtered list, so they include `finished`/`failed` and exclude put-away items. | One list, one count (SSOT). | First load after release shows every idle agent as `finished` until viewed or put away. A count split would need a second computed. |
| R4 | The dashboard triage band ignores marks. | The band shows live fleet state, not an inbox. The brief scopes marks to the queue. | One extra filter on `attentionAgents`. |
| R5 | "Viewed" for a `finished` item means the agent detail opened. Rendering a card on screen is not "viewed". | Rendering would clear every card the moment it appears. | Change the trigger in one watcher. |
| R6 | Server stores marks only. The client derives items. | The client already owns the derivation; a server copy would duplicate it in Go. | Cross-device views can disagree for one tick while streams catch up. |
| R7 | No down migration. The tables are added by ent `Schema.Create` (additive). Manual rollback: `DROP TABLE attention_marks; DROP TABLE attention_baselines;` — an older binary ignores both tables. Accepted by the user on 2026-11-19. | Repo convention since `client.go:151`; there is no migration framework with down steps. | Manual SQL on rollback. |
| R8 | Keys on non-dashboard views target the needs-you list, not `attentionAgents`. | The triage band is not rendered there; approving an invisible agent's permission is unsafe. | Users who expect dashboard semantics on other views see list semantics. |
| R9 | The fleet chip choice is not persisted. | It is always visible, so a stale hidden filter cannot empty the roster silently. | Add one key to `useViewState`. |
| R10 | The server keeps the later `seenThrough` per key. | A stale browser must not resurface an item another browser cleared. | None known. |
| R11 | Marks are never pruned. | One small row per put-away item; growth is user-paced. | Follow-up: prune rows older than 30 days. |
| R12 | A server-side baseline (one-row table `attention_baselines`, written once on first read, never changed) hides `failed` and `finished` items whose `since` is at or before it. `GET /api/attention/marks` returns `{"baseline", "marks"}`. Until the client has the baseline, it hides both kinds. Decided by the user on 2026-11-19. | Without it, every idle or failed agent from before the release floods the list on first load. | One more table; deleting its row resets the cutoff to the next read. |
| R13 | The docked hub queue leaves out `finished` items (`DOCKED_KINDS` in `needsYouPlacement.ts`). The title count and the strip on other views still include them; the hub core badge counts the dock. Decided by the user on 2026-11-19. | With many idle agents the dock fills its height share and covers map dots; the map already shows idle agents. | One kinds list; revert by dropping the filter. |

## Open questions

- Q1: Does `pnpm test:e2e` start with an empty database? The e2e spec uses unique session ids per run so leftover marks cannot collide. Check: `grep -n 'DASHBOARD_DB\|db' playwright.config.ts`.
- Q2: Does modernc/sqlite round-trip `time.Time` with nanoseconds? Task 1 pins it with a test; if it fails, store `seen_through` truncated to milliseconds (client precision).

## Tests (summary)

- Go: repo round-trip and keep-later; handler validation table, idempotent PUT with one broadcast, stream event; route goldens; bypass-auth smoke.
- Vitest: `rankNextThings` keys/since/new kinds; `visibleNextThings`; `useAttentionMarks` optimistic + revert; `NeedsYouQueue` list, put away, variants; `NextThing` failed/finished cards; `useNeedsYouTriage` cycle/decide/approve-all; `fleetBucket` partition; `FleetFilterChips` counts and filter.
- Playwright: put away survives reload; `n`/`a` on the Zentrale resolve a permission; chips filter the roster.

## Out of scope

Pruning marks, a "Put away all" action, marks on the triage band, persisting chip state.
