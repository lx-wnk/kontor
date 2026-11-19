# Attention Inbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the needs-you carousel into a clearable stacked inbox backed by server-side marks, make triage keys work on every view, and add fleet filter chips to the dashboard roster.

**Architecture:** The server stores only attention marks (`item_key`, `seen_through`) in a new ent table, served by a small REST + SSE package. The client keeps deriving items in `rankNextThings`, gives each a stable `key` and a `since` watermark, and hides items whose mark is at or past `since`. Keys and chips are client-only.

**Tech Stack:** Go 1.26 (chi, ent, modernc/sqlite), Vue 3 + TypeScript (Vite, Vitest, Playwright).

**Spec:** `docs/superpowers/specs/2026-11-19-attention-inbox-design.md`

## Global Constraints

- Grade production. Every artifact is English. No comments unless they meet the narrow reasons (timing, external contract, non-obvious edge case, case→effect map).
- Server binds `127.0.0.1`. New write routes mount inside the protected group (`server/internal/api/router.go:307-322`) so `RequireSameOriginForMutations` covers them. E2E `request` calls that write send an `Origin` header equal to `baseURL`.
- Fail fast in core, validate at the boundary. `PUT /api/attention/marks` is idempotent.
- SSOT: `agentOutcome` (useNextThing.ts) owns the failed/finished rule; `NextThing.key` owns item identity; `fleetBucket` (agentGroup.ts) owns the chip partition; `useAttentionMarks` owns marks state.
- Schema change is additive via ent `Schema.Create`. No down migration; spec ruling R7 gives the manual rollback SQL. Every PR body states this.
- Ent codegen: `go test -tags entgen ./server/internal/db/entgen/ -count=1` from repo root. Commit only generated files for `AttentionMark`.
- Route changes: `cd server && go test ./internal/api -run 'TestRouteGolden' -update-golden`, and add the handler to `buildBypassRouter` (`server/internal/api/bypass_auth_smoke_test.go`).
- `pnpm build` wipes `server/frontend/dist/.gitkeep`; restore with `git checkout HEAD -- server/frontend/dist/.gitkeep` before committing.
- Each task commits its RED test first, then the implementation. Conventional Commits.
- Gates (raw output pasted at the end of each task that touches the layer, and all of them before the PR):
  - `pnpm lint && pnpm typecheck && pnpm test`
  - `task test`
  - `(cd server && go vet ./...) && (cd sdk && go vet ./...)`
  - `task lint`
  - `task changelog:check`
  - `pnpm test:e2e` (for tasks 6, 9, 11 and the final run)
- Docs in the same PR: `README.md`, `CHANGELOG.md` (append to existing `### Added` / `### Changed` under `[Unreleased]`, never a second heading), `.agent-context/layer2-project-core.md` SSOT rows. `CONTRIBUTING.md` only if a dev command changes (none planned).

## Review Focus

1. A stale browser PUTs an older `seenThrough`. The stored mark must not move back (Task 1).
2. `⇧A` on a non-dashboard view must approve only `permission` items in the visible list, never put-away ones (Task 8).
3. The focused item is resolved elsewhere. `a`/`d` must not act on the item that slid into its place (Task 8).
4. `seenThrough` precision: a mark set from `2026-11-19T10:00:00Z` must hide an item with that exact `since` (Tasks 1, 4).

---

## Backend (lands first)

### Task 1: `AttentionMark` schema and repo

**Files:**
- Create: `server/internal/db/ent/schema/attention_mark.go`
- Create: `server/internal/db/repo/attention_mark_repo.go`
- Test: `server/internal/db/repo/attention_mark_repo_test.go`
- Generated: `server/internal/db/ent/...` (attentionmark only)

**Interfaces:**
- Produces: `repo.AttentionMarkRepo` with `List(ctx) ([]*ent.AttentionMark, error)` and `Mark(ctx, key string, seenThrough time.Time) (*ent.AttentionMark, bool, error)`; `repo.NewAttentionMarkRepo(*ent.Client) AttentionMarkRepo`.

Schema:

```go
type AttentionMark struct{ ent.Schema }

func (AttentionMark) Mixin() []ent.Mixin { return []ent.Mixin{IDTimestampsMixin{}} }

func (AttentionMark) Fields() []ent.Field {
	return []ent.Field{
		field.String("item_key").Unique().NotEmpty().MaxLen(160),
		field.Time("seen_through"),
	}
}
```

`Mark` runs in `r.client.Tx(ctx)`: query by `item_key`; if found and `!seenThrough.After(row.SeenThrough)` → commit, return `(row, false, nil)`. Else create with `SetID(uuid.New().String())`, `OnConflictColumns(attentionmark.FieldItemKey).UpdateNewValues()`, reload, return `(row, true, nil)`. Errors wrap as `attentionmark.Mark: %w`. `List` orders by `item_key`.

- [ ] **Step 1: RED tests** (use the helper in `repo/helpers_test.go` that opens an in-memory client):
  - `TestAttentionMarkRepo_MarkCreates`: `Mark("finished:s1", 2026-11-19T10:00:00Z)` → `changed=true`, `List` has 1 row with that key and `SeenThrough.Equal(t0)`.
  - `TestAttentionMarkRepo_MarkKeepsLater`: mark at `10:00:05Z`, then at `10:00:00Z` → second call `changed=false`, stored stays `10:00:05Z`.
  - `TestAttentionMarkRepo_MarkSameIsNoChange`: mark `10:00:00Z` twice → second `changed=false`, `List` length 1.
  - `TestAttentionMarkRepo_MarkAdvances`: `10:00:00Z` then `10:00:07Z` → `changed=true`, stored `10:00:07Z`.
  - `TestAttentionMarkRepo_NanosRoundTrip`: `time.Date(2026,11,19,10,0,0,123456789,time.UTC)` reads back `Equal` (spec Q2; if it fails, truncate to ms in `Mark` and assert ms).
- [ ] **Step 2: Run** `cd server && go test ./internal/db/repo -run AttentionMark -count=1`. Expected: compile FAIL. Commit `test(attention): add RED tests for attention marks repo`.
- [ ] **Step 3: Implement** schema, regenerate ent, write repo.
- [ ] **Step 4: Run** the same command. Expected: PASS. Then `task test` and `cd server && go vet ./...`.
- [ ] **Step 5: Commit** `feat(attention): store attention marks in sqlite`.

### Task 2: Attention API handler and SSE event

**Files:**
- Modify: `server/internal/sse/typed_broadcaster.go` (add event type)
- Create: `server/internal/api/attention/handler.go`
- Test: `server/internal/api/attention/handler_test.go`

**Interfaces:**
- Consumes: `repo.AttentionMarkRepo` (Task 1).
- Produces: `sse.AttentionEvent{Type string "json:type"; Mark any "json:mark"}`, const `sse.AttentionMarked = "attention_marked"`; `attention.NewHandler(marks repo.AttentionMarkRepo, b *sse.TypedBroadcaster[sse.AttentionEvent]) *Handler` (panics on nil args); `(*Handler).Mount(r chi.Router)`; JSON view `markView{Key string "json:key"; SeenThrough string "json:seenThrough"}` with `SeenThrough` formatted `time.RFC3339Nano` in UTC.

Routes in `Mount`: `GET /api/attention/marks` (List), `PUT /api/attention/marks` (Put), `GET /api/attention/stream` (Stream, copy of `cloudsessions.Handler.Stream`). Use `apierr.ErrorMiddleware` and `apierr.WriteJSON` as in `server/internal/api/cloudsessions/handler.go:190-298`.

`Put` validation, in order: `http.MaxBytesReader` 4096; `json.Decoder` with `DisallowUnknownFields`; `key` matches `var keyPattern = regexp.MustCompile(`^[a-z]+:[A-Za-z0-9._-]{1,128}$`)`; `seenThrough` parses with `time.RFC3339Nano` and is not zero. Each failure → `apierr.NewAppError(400, <message>)`. On success broadcast only when `changed`.

- [ ] **Step 1: RED tests** (use `httptest`, a real in-memory repo, a real `TypedBroadcaster` with a subscribed channel):
  - Table `TestPut_Rejects` → all 400: `{}`; `{"key":"","seenThrough":"2026-11-19T10:00:00Z"}`; `{"key":"Finished:s1",…}`; `{"key":"finished:a b",…}`; `{"key":"finished:"+129×"a",…}`; `{"key":"finished:s1","seenThrough":"yesterday"}`; `{"key":"finished:s1","seenThrough":"0001-01-01T00:00:00Z"}`; `{"key":"finished:s1","seenThrough":"2026-11-19T10:00:00Z","x":1}`; body of 5000 bytes.
  - `TestPut_StoresAndBroadcastsOnce`: PUT `{"key":"failed:s1","seenThrough":"2026-11-19T10:00:00Z"}` twice → both 200, both bodies `{"key":"failed:s1","seenThrough":"2026-11-19T10:00:00Z"}`, exactly one frame on the channel containing `"type":"attention_marked"`.
  - `TestPut_OlderDoesNotRegress`: PUT `10:00:05Z` then `10:00:00Z` → second body has `10:00:05Z`, one frame total.
  - `TestList`: after two PUTs (`a:1`, `b:2`) → GET 200 with 2 entries ordered by key.
- [ ] **Step 2: Run** `cd server && go test ./internal/api/attention -count=1`. Expected: compile FAIL. Commit RED.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** same command → PASS; `cd server && go vet ./...`.
- [ ] **Step 5: Commit** `feat(attention): add attention marks API and stream`.

### Task 3: Wire the handler into DI and the router

**Files:**
- Modify: `server/serverapp/di.go` (next to the cloud sessions block at ~1149; add `AttentionHandler` to the router deps at ~1213)
- Modify: `server/internal/api/router.go` (dep field next to `CloudSessionsHandler` at :175; `if deps.AttentionHandler != nil { deps.AttentionHandler.Mount(r) }` next to :415, inside the protected group)
- Modify: `server/internal/api/bypass_auth_smoke_test.go` (construct the handler in `buildBypassRouter` with `repo.NewAttentionMarkRepo(c)` and a fresh broadcaster)
- Generated: `server/internal/api/testdata/routes.golden` (`-update-golden`)

**Interfaces:**
- Consumes: Task 1 and 2 constructors.

- [ ] **Step 1: RED:** run `cd server && go test ./internal/api -run 'TestRouteGolden|Bypass' -count=1` after adding only the smoke-test wiring and router field. Expected: golden drift FAIL listing the three new routes. Commit RED.
- [ ] **Step 2: Implement** DI wiring (`if entClient != nil { attentionHandler = attention.NewHandler(repo.NewAttentionMarkRepo(entClient), sse.NewTypedBroadcaster[sse.AttentionEvent](sse.NewBroadcaster())) }`) and mount.
- [ ] **Step 3: Regenerate** goldens: `cd server && go test ./internal/api -run TestRouteGolden -update-golden`. Expected diff: `GET /api/attention/marks`, `GET /api/attention/stream`, `PUT /api/attention/marks`.
- [ ] **Step 4: Add** a same-origin test in `server/internal/api/middleware_security_test.go` style, inside the smoke test file: PUT `/api/attention/marks` without `Origin` → 403.
- [ ] **Step 5: Gates** `task test`, `(cd server && go vet ./...)`, `task lint`. Commit `feat(attention): mount attention routes`.

## Frontend

### Task 4: Item keys, watermarks and the failed/finished kinds

**Files:**
- Modify: `src/features/mission/composables/useNextThing.ts`
- Modify: `src/features/mission/index.ts` (export `agentOutcome`)
- Test: `src/features/mission/composables/__tests__/useNextThing.test.ts` (create if absent; else extend the existing ranking test)

**Interfaces:**
- Produces: `NextKind` adds `'failed' | 'finished'`; `NextThing` adds `key: string`, `since: string`, `sessionId?: string`; `export function agentOutcome(agent: Agent): 'failed' | 'finished' | null`; `KIND_RANK.failed = 5`, `KIND_RANK.finished = 6`; `WHY.failed`, `WHY.finished` verbatim from the spec §1.

`agentOutcome`: `if (agent.status === 'finished') return 'finished'`; `const k = attentionFor(agent, null)?.kind`; `k === 'error'` → `'failed'`; `k === 'yourTurn'` → `'finished'`; else `null`. Question/dialog items set `sessionId: agent.sessionId` and use it in the key. Failed/finished items: `taskId: agent.pipelineTaskId ?? ''`, `taskTitle`/`projectName: agent.projectName`, `pid`, `sessionId`, `title` = `agent.errorState?.message ?? 'Run failed'` (failed) or `agent.lastOutput ?? 'Finished'` (finished).

- [ ] **Step 1: RED tests:**
  - permission request `{id:'r1', requestedAt:'2026-11-19T10:00:00Z'}` → `key 'permission:r1'`, `since '2026-11-19T10:00:00Z'`.
  - agent `{sessionId:'s1', pendingQuestion:{…}, lastActivity:'2026-11-19T10:01:00Z'}` → `key 'question:s1'`, `since` = lastActivity.
  - agent `{status:'active', working:false, sessionId:'s2'}` → one item `kind 'finished'`, `key 'finished:s2'`.
  - agent `{status:'finished', sessionId:'s3'}` → `finished:s3`.
  - agent `{status:'active', working:true, errorState:{message:'boom'}, sessionId:'s4'}` → `failed:s4`, `title 'boom'`.
  - agent with `pendingQuestion` and `errorState` → only `question:<id>` (precedence from `attentionFor`).
  - plan task `{id:'t1', updatedAt:'2026-11-19T09:00:00Z'}` → `plan:t1`.
  - Order of one of each kind: `permission, question, dialog, capability, plan, failed, finished`.
- [ ] **Step 2: Run** `pnpm vitest run src/features/mission` → FAIL. Commit RED.
- [ ] **Step 3: Implement.** Fix any `Record<NextKind, …>` exhaustiveness errors that `pnpm typecheck` shows (e.g. hub label maps) with entries for the two new kinds.
- [ ] **Step 4: Run** `pnpm vitest run src/features/mission && pnpm typecheck` → PASS. Commit `feat(mission): key needs-you items and add failed and finished kinds`.

### Task 5: Marks composable and filtered list in App.vue

**Files:**
- Create: `src/features/mission/composables/useAttentionMarks.ts`
- Test: `src/features/mission/composables/__tests__/useAttentionMarks.test.ts`
- Modify: `src/App.vue:114` (filter), plus the `selectedAgent` view hook

**Interfaces:**
- Consumes: `NextThing.key/since`, `agentOutcome` (Task 4); API from Task 2.
- Produces: `useAttentionMarks(): { marks: ShallowRef<ReadonlyMap<string,string>>, start(): void }`; `markSeen(key: string, since: string): Promise<void>`; `isPutAway(t: NextThing, marks: ReadonlyMap<string,string>): boolean`; `visibleNextThings(list: NextThing[], marks): NextThing[]`.

Module-level state, `createSseResource({ streamUrl: '/api/attention/stream', fetchInitial, onMessage, onConnected: fetchInitial })` exactly as `src/features/cloud/composables/useCloudSessions.ts`. `isPutAway`: `const m = marks.get(t.key); return m !== undefined && Date.parse(m) >= Date.parse(t.since)`. `markSeen`: keep previous value, set new map with `key → since`, `PUT` JSON `{key, seenThrough: since}`; on non-OK or throw: restore previous value, `toast.error('Could not put that away')`.

App.vue: `const { marks, start: startMarks } = useAttentionMarks()`; call `startMarks()` next to `startAgents()` in the `loaded` watcher; `const needsYou = computed(() => visibleNextThings(rankNextThings(…), marks.value))`; `watch(selectedAgent, a => { if (a && agentOutcome(a) === 'finished') void markSeen(`finished:${a.sessionId}`, a.lastActivity) })`.

- [ ] **Step 1: RED tests:**
  - `isPutAway`: mark `'2026-11-19T10:00:00Z'`, since same → `true`; since `'2026-11-19T10:00:01Z'` → `false`; no mark → `false`; mark with nanos `'2026-11-19T10:00:00.000000000Z'` vs since `'2026-11-19T10:00:00Z'` → `true`.
  - `visibleNextThings` drops only the marked item of three.
  - `markSeen` with `fetch` mocked 200 → map has key immediately (before the promise resolves) and request is `PUT /api/attention/marks` with body `{"key":"failed:s1","seenThrough":"2026-11-19T10:00:00Z"}`.
  - `markSeen` with `fetch` mocked 500 → map back to previous, `toast.error` called once.
  - SSE message `{"type":"attention_marked","mark":{"key":"k:1","seenThrough":"…"}}` → map contains `k:1`.
- [ ] **Step 2: Run** `pnpm vitest run src/features/mission` → FAIL. Commit RED.
- [ ] **Step 3: Implement** composable and App.vue changes.
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test`. Commit `feat(mission): hide put-away needs-you items from server marks`.

### Task 6: Stacked needs-you list with "Put away"

**Files:**
- Modify: `src/features/mission/components/NeedsYouQueue.vue`
- Modify: `src/features/mission/components/NextThing.vue`
- Test: `src/features/mission/__tests__/NeedsYouQueue.test.ts` (replace carousel cases); `NextThing.test.ts` gets the failed/finished cases

**Interfaces:**
- Consumes: `markSeen` (Task 5), `NextThing.key` (Task 4).

Behaviour per spec §5. `NextThing.vue`: delete the "NEXT" divider and the local `itemKey` computed; use `next.key` for `:key` and the guard watcher. New branch `v-else-if="next.kind === 'failed' || next.kind === 'finished'"` with "Open agent" (`data-testid="mission-open-agent"`) → `const a = agents.value.find(x => x.sessionId === next.sessionId); if (a) selectAgent(a)` from `useAgents({ autoStart: false })`. `NeedsYouQueue.vue`: `<ul>` of `<li :key="t.key" :data-key="t.key">` each with `NextThing` and a "Put away" button calling `markSeen(t.key, t.since)`.

- [ ] **Step 1: RED tests** (NeedsYouQueue, with `NEEDS_YOU`, `OPEN_TASK`, `PENDING_PERMISSIONS` provided as today):
  - 3 items → 3 `li`, header `needs-you-count` text contains `3`, no `needs-you-next`/`needs-you-prev`/`needs-you-position`.
  - click "Put away" on the 2nd item → `markSeen` called with `(items[1].key, items[1].since)`.
  - `variant="strip"` with 0 items → no `needs-you` section; `docked` with 0 → section with the calm line.
  - strip keeps `role="status"` and `aria-live="polite"`; docked has no role.
  - `kinds: ['plan']` with 1 plan + 2 permissions → 1 `li`.
  - NextThing: failed item → shows `title` and `mission-open-agent`; clicking it calls `selectAgent` with the agent whose `sessionId` matches.
- [ ] **Step 2: Run** `pnpm vitest run src/features/mission` → FAIL. Commit RED.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test`, then `pnpm test:e2e tests/e2e/zentrale.spec.ts tests/e2e/hub.spec.ts tests/e2e/hub-launcher-reach.spec.ts tests/e2e/question-band.spec.ts` (docked queue must stay inside the 6×6 hub). Commit `feat(mission): show needs-you as a stacked list with put away`.

### Task 7: Clear `failed` on send

**Files:**
- Modify: `src/features/agents/composables/useAgentPrompt.ts` (after a successful `POST /api/agents/{pid}/message`, ~line 103)
- Test: `src/features/agents/composables/__tests__/useAgentPrompt.test.ts` (create or extend)

**Interfaces:**
- Consumes: `markSeen` (Task 5), `agentOutcome` (Task 4).

After a 2xx send: `if (agentOutcome(agent) === 'failed') void markSeen(`failed:${agent.sessionId}`, agent.lastActivity)`. No call on a failed send or for a spawn.

- [ ] **Step 1: RED tests:** failed agent + 200 → `markSeen('failed:s1', '2026-11-19T10:00:00Z')` once; failed agent + 500 → not called; healthy working agent + 200 → not called.
- [ ] **Step 2: Run** `pnpm vitest run src/features/agents` → FAIL. Commit RED.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test`. Commit `feat(agents): clear a failed item when you send the agent a message`.

### Task 8: Needs-you triage composable

**Files:**
- Create: `src/features/mission/composables/useNeedsYouTriage.ts`
- Test: `src/features/mission/composables/__tests__/useNeedsYouTriage.test.ts`
- Modify: `src/composables/openTask.ts` (add `NEEDS_YOU_FOCUS: InjectionKey<Ref<string | null>>`)

**Interfaces:**
- Produces: `useNeedsYouTriage(list: ComputedRef<NextThing[]>, refresh: () => void): { focusedKey: Ref<string|null>, focusNext(): void, decide(choice: 'allow' | 'deny'): Promise<string | null>, approveAll(): Promise<string | null> }` (returns an error message or `null`).

Rules: `focusNext` moves to the item after `focusedKey` in `list`, wrapping; from `null` or a missing key → first item; empty list → `null`. A `watch(list)` resets `focusedKey` to `null` when its key is gone. `decide` acts only on the focused item: `permission` → `resolvePermissionRequest(taskId, request.id, choice === 'allow' ? 'allow_once' : 'deny_once')`; `capability` → `useCapabilityDecisions().resolve(decision.id, choice)`; other kinds → `null`, no call. `approveAll` → `allow_once` for every `permission` item in `list`, then `refresh()`.

- [ ] **Step 1: RED tests** (mock `@/features/pipeline` `resolvePermissionRequest` and `@/features/agents` `useCapabilityDecisions`):
  - list `[p1, q1, c1]`: `focusNext` ×4 → `permission:p1`, `question:s1`, `capability:c1`, `permission:p1`.
  - focus `p1`, `decide('allow')` → `resolvePermissionRequest('t1','p1','allow_once')`.
  - focus `c1`, `decide('deny')` → `resolve('c1','deny')`.
  - focus `q1`, `decide('allow')` → no call, returns `null`.
  - focus `p1`, then list becomes `[p2]` → `focusedKey` is `null`; `decide('allow')` makes no call (Review Focus 3).
  - list `[p1, p2, c1]` → `approveAll` calls `resolvePermissionRequest` twice (p1, p2), never `resolve`, then `refresh` once.
- [ ] **Step 2: Run** `pnpm vitest run src/features/mission` → FAIL. Commit RED.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test`. Commit `feat(mission): add keyboard triage for the needs-you list`.

### Task 9: Triage keys on every view

**Files:**
- Modify: `src/App.vue:198-271` (key handler) and provide `NEEDS_YOU_FOCUS`
- Modify: `src/features/mission/components/NeedsYouQueue.vue` (inject focus; ring, `aria-current`, `scrollIntoView`)
- Test: `tests/e2e/attention.spec.ts` (create; keys part) and extend `NeedsYouQueue.test.ts` for the focus ring

**Interfaces:**
- Consumes: `useNeedsYouTriage` (Task 8), `NEEDS_YOU_FOCUS`.

App.vue: create `const triage = useNeedsYouTriage(needsYou, pendingPermissions.refresh)`, `provide(NEEDS_YOU_FOCUS, triage.focusedKey)`. In `handleKeydown`, drop `activeView.value !== 'dashboard'` from the shared guard. Then: if dashboard → the existing `n`/`a`/`d`/`⇧A`/`c` code unchanged; else `n` → `triage.focusNext()`, `⇧A` → `triage.approveAll()`, `a` → `triage.decide('allow')`, `d` → `triage.decide('deny')`, each under the existing `kbResolving` guard and `toast.error` on a returned message; `c` stays dashboard-only. NeedsYouQueue: `inject(NEEDS_YOU_FOCUS)` (throw if missing, same as the other injects); the matching `li` gets `aria-current="true"` and `ring-2 ring-accent`; `watch(focusedKey)` → `nextTick` → `querySelector('[data-key="…"]')?.scrollIntoView({ block: 'nearest' })`.

- [ ] **Step 1: RED:** unit — focused key `permission:p1` → that `li` has `aria-current="true"`, others none. E2E (`stubAgents` + a stubbed pipeline permission as the existing `question-band.spec.ts` does): on the Zentrale, press `n` → first card has `aria-current="true"`; press `a` → the resolve request is sent with `allow_once`; type `n` inside the spotlight input → no focus change; on the dashboard `n` still focuses the triage band item (existing `question-band.spec.ts` stays green).
- [ ] **Step 2: Run** `pnpm vitest run src/features/mission` and `pnpm test:e2e tests/e2e/attention.spec.ts` → FAIL. Commit RED.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test` and `pnpm test:e2e tests/e2e/attention.spec.ts tests/e2e/question-band.spec.ts`. Commit `feat: triage keys act on the needs-you list on every view`.

### Task 10: Fleet bucket rule

**Files:**
- Modify: `src/utils/agentGroup.ts`
- Test: `src/utils/__tests__/agentGroup.test.ts` (extend the existing file, or create beside it)

**Interfaces:**
- Produces: `FLEET_FILTERS` (spec §7, `as const`); `type FleetFilter = typeof FLEET_FILTERS[number]['value']`; `type FleetBucket = Exclude<FleetFilter, 'all'>`; `fleetBucket(agent: Agent, secs: number | null): FleetBucket`; `fleetCounts(list: Agent[], secsOf: (a: Agent) => number | null): Record<FleetFilter, number>`.

- [ ] **Step 1: RED tests:** `pendingQuestion` → `needsYou`; `errorState` → `needsYou`; `working:true` no attention → `working`; `status:'active', working:false` (yourTurn) → `idle`; `status:'finished'` → `idle`; `fleetCounts` of those five → `{ all: 5, needsYou: 2, working: 1, idle: 2 }`.
- [ ] **Step 2: Run** `pnpm vitest run src/utils` → FAIL. Commit RED.
- [ ] **Step 3: Implement** using `attentionFor` from `./attention` and `agentDisplayStatus` from `./statusColors`.
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test`. Commit `feat(dashboard): classify agents into fleet buckets`.

### Task 11: Fleet filter chips on the dashboard roster

**Files:**
- Create: `src/features/cockpit/components/FleetFilterChips.vue`
- Modify: `src/features/cockpit/components/DashboardView.vue:34-44, 85-102`
- Test: `src/features/cockpit/components/FleetFilterChips.test.ts`; extend `DashboardView.test.ts`

**Interfaces:**
- Consumes: `FLEET_FILTERS`, `fleetBucket`, `fleetCounts` (Task 10).
- Produces: `FleetFilterChips` props `{ modelValue: FleetFilter, counts: Record<FleetFilter, number> }`, emit `update:modelValue`.

DashboardView: `const fleet = ref<FleetFilter>('all')`; `preChip` = filtered by project and spawner (today's `rosterAgents` before sort); `counts = fleetCounts(preChip, secsOf)` with `secsOf = a => secondsSince(a.lastActivity, nowMs.value)`; `rosterAgents` = `preChip` filtered by `fleet.value === 'all' || fleetBucket(a, secsOf(a)) === fleet.value`, then sorted. Render chips directly above `DashboardToolbar`. `shownCount` stays `rosterAgents.length`.

- [ ] **Step 1: RED tests:** chips render 4 buttons labelled `All 5`, `Needs you 2`, `Working 1`, `Idle or done 2`; `aria-pressed="true"` only on the active one; click `Working` emits `'working'`. DashboardView with the five agents from Task 10: click `fleet-chip-needsYou` → `rosterAgents` length 2; counts unchanged after the click. E2E in `tests/e2e/attention.spec.ts`: dashboard, stubbed agents, click "Needs you" → only the asking agent's card is visible.
- [ ] **Step 2: Run** `pnpm vitest run src/features/cockpit` → FAIL. Commit RED.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `pnpm lint && pnpm typecheck && pnpm test` and `pnpm test:e2e tests/e2e/attention.spec.ts`. Commit `feat(dashboard): filter the roster with fleet chips`.

### Task 12: Put-away persistence end to end

**Files:**
- Modify: `tests/e2e/attention.spec.ts`

- [ ] **Step 1: Test:** stub one finished agent with session id `e2e-${Date.now()}` (avoids leftover marks, spec Q1). On the Zentrale the card shows; click "Put away" → card gone; `page.reload()` → still gone; open a second `browser.newContext()` page → gone there too. Then stub the same agent with a later `lastActivity` → the card is back. Separately: `request.put('/api/attention/marks', { headers: { Origin: baseURL }, data: { key: 'bad key', seenThrough: 'x' } })` → 400.
- [ ] **Step 2: Run** `pnpm test:e2e tests/e2e/attention.spec.ts` → PASS. Commit `test(e2e): put away survives reload and syncs across contexts`.

### Task 13: Docs

**Files:**
- Modify: `CHANGELOG.md` (append under `[Unreleased]` existing `### Added` / `### Changed`)
- Modify: `README.md` (feature list + keyboard shortcuts section)
- Modify: `.agent-context/layer2-project-core.md` (SSOT table rows)

Content:
- CHANGELOG Added: stacked needs-you list with "Put away"; attention marks stored on the server and synced across browsers; finished/failed items in the queue; fleet filter chips with counts. Changed: `n`/`a`/`d`/`⇧A` work on every view and act on the needs-you list outside the dashboard.
- README: describe the inbox, the clearing rules table from spec §4, and the key behaviour per view.
- SSOT rows: `Needs-you item identity and failed/finished rule` → `src/features/mission/composables/useNextThing.ts` (`NextThing.key`, `agentOutcome`); `Attention marks (client)` → `src/features/mission/composables/useAttentionMarks.ts` (`markSeen`, `isPutAway`, `visibleNextThings`); `Attention mark key rule (Go)` → `server/internal/api/attention/handler.go` (`keyPattern`); `Fleet filter partition` → `src/utils/agentGroup.ts` (`FLEET_FILTERS`, `fleetBucket`, `fleetCounts`). Add the marks view (`{key, seenThrough}`) as the sixth hand-kept TS↔Go pair in the parity paragraph.

- [ ] **Step 1:** Verify every doc claim against the merged code (`grep` each named symbol).
- [ ] **Step 2: Run** all gates from Global Constraints, paste raw output.
- [ ] **Step 3: Commit** `docs: document the attention inbox`.
