# Insights Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One Insights page (KPI header, filter bar, tabs Overview / Sessions / Cost / Quality / Workflows, session drawer) backed by per-session metrics read from Claude Code's `cost-state` records, with the token estimate as a visible fallback.

**Architecture:** The history importer widens its existing one-row-per-session ingest into `agent_cost_trends` (new columns, subagent roll-up, one-shot rescan). A new `server/internal/api/insights` package serves summary / sessions / projects over that table, sharing the date and project filter with `/api/cost/summary`. The SPA replaces the three nav items Cost, Eval, Workflows with one `insights` view; the existing three views become tabs that take the page's filters as props.

**Tech Stack:** Go 1.26 (chi, ent, modernc/sqlite, testify), Vue 3 + TypeScript (Vite, Vitest, Playwright).

**Spec:** `docs/superpowers/specs/2026-10-28-insights-page-design.md` (approved 2026-10-29).

## Global Constraints

- Everything that ships is English: code, comments, commits, docs.
- Server stays on `127.0.0.1`; no new outbound network calls.
- `GET /api/cost/summary` without `project` behaves exactly as today (the `cost-today` widget depends on it).
- `cost_usd` keeps its meaning: the session's cost (real when `cost_source = cost_state`, estimate otherwise).
- Dates are `YYYY-MM-DD`, filtered on `recorded_at` with `substr(recorded_at, 1, 10)` everywhere.
- `title` is capped at 120 runes, whitespace-collapsed, and passed through the parser's secret scrubber before it is stored.
- Stale rule: an assistant record after the last `cost-state` means the estimate is used for cost and agent time, and the row says so.
- Agent-time estimate skips any gap over 30 minutes.
- Comment policy: default no comment; one short line only for timing, external contract, edge case, or case-to-effect mapping.
- Docs ship in the same change: README, CHANGELOG (append to existing `### Added` / `### Changed` under `[Unreleased]`), PRIVACY.md.
- After `pnpm build`, restore `server/frontend/dist/.gitkeep` with `git checkout HEAD -- server/frontend/dist/.gitkeep`.
- Ent regeneration: `go test -tags entgen ./server/internal/db/entgen/ -count=1` from repo root; commit the generated files with the schema change.
- Gates per task: Go `task test`, `GOTOOLCHAIN=go$(cat .go-version) go vet ./...` in `sdk` and `server`, `task lint`; frontend `pnpm lint && pnpm typecheck && pnpm test`; final `task changelog:check` and `pnpm test:e2e`.

## Rulings (made while planning; spec is silent or a detail changed)

1. **Title source:** `customTitle` (a `/rename`) beats `aiTitle`, which beats the first real prompt. Reason: Claude Code already writes both titles into the JSONL (`server/internal/parser/messages.go:41-42`, `parser.go:407`), they name the session better, and they store less of the raw prompt. Cost if wrong: one fallback order to flip.
2. **Tokens are always summed** from assistant `usage` across the parent and its subagent files. `cost-state` supplies only cost, agent time, lines and `model_costs`. Reason: both paths then count the same thing and the stale rule only has to cover cost and agent time. Cost if wrong: small token mismatch against `/cost`.
3. **Subagent agent time is not added on the estimate path.** The parent's gap from the `Agent` tool_use to its tool_result already covers the subagent's runtime, so adding it would count it twice. Subagent tokens and estimated cost are added.
4. **Estimated cost with subagents** = `parser.EstimateCost` per file, with each file's own last model, summed.
5. **Turn timeline for the agent-time estimate:** the turn-start user record, then every assistant record and every tool-result user record of that turn. Sum the deltas between consecutive timestamps, skipping deltas over 30 minutes.
6. **Prompt text for the title:** a string `content`, or the first `text` block of an array `content`. Text starting with `<` (system-injected tags such as `<command-name>`) is not used as a title but still starts a turn.
7. **Period "All":** the client omits `from`. Insights endpoints then cover all rows and return `previous: null`, so the UI shows no delta. The Cost tab keeps sending `2000-01-01` as today.
8. **Previous period:** for an inclusive window of N days `[from, to]`, previous is `[from − N, from − 1]`.
9. **Insights params are validated strictly** (400 on a bad date, `from > to`, unknown `sort`/`order`, `limit` outside 1..200, negative `offset`). `/api/cost/summary` keeps its lenient fallback for `from`/`to`.
10. **Project key** is `project_path`, matched as `COALESCE(NULLIF(project_path, ''), 'unknown') = ?`, the same bucket `queryByProject` already reports.
11. **Drawer** is a new `size: 'drawer'` on `AppModal.vue`, not a second component, so focus trap, Esc, scroll lock and focus restore stay in one place.
12. **The drawer's tool view** is the existing `SessionDagChart.vue`, fed from `/api/visualizations/dag?session=`. The "open session" link emits `navigate(sessionId)` the same way `WorkflowsView` does (`src/App.vue:426-427`).
13. **Period to eval hours:** 7d = 168, 30d = 720, 90d = 2160, All = `MAX_LOOKBACK_HOURS` (`src/utils/evalMetrics`).
14. **Rescan marker name:** `rescan-cost-trends-insights-v1`.

HYPOTHESIS to settle in Task 2: `totalAPIDuration` and `totalToolDuration` are in milliseconds. Cheapest check: `grep -h '"type":"cost-state"' ~/.claude/projects/*/*.jsonl | tail -1 | jq '{totalAPIDuration,totalToolDuration,totalDuration}'`. Expect values of the same order as `totalDuration`, which is wall-clock ms.

## Review Focus

1. **A subagent finishes after the parent file last changed.** The parent's mtime is unchanged, so the importer must still re-parse it (newest mtime across the parent and its subagent files). Test: Task 3 `TestImporter_ReparsesWhenOnlySubagentChanged`.
2. **A user record whose `content` mixes `text` and `tool_result` blocks.** It is a tool result, not a new turn. Test: Task 2 `TestParseSession_MixedToolResultIsNotATurn`.
3. **A stored view `cost` / `eval` / `workflows` / legacy `agent-view-mode=cost-analytics` while localStorage throws on write.** The page must still open on Insights with the right tab. Test: Task 7 `migrates stored cost view when setItem throws`.
4. **A SQL-ish `sort` value or a project name with quotes.** Sort is allow-listed; the project goes in as a bound parameter. Test: Task 5 `TestSessions_RejectsUnknownSort`, `TestSessions_ProjectWithQuote`.
5. **Period "All".** There is no previous period: `previous` is null and no delta badge renders. Tests: Task 5 `TestSummary_AllTimeHasNoPrevious`, Task 9 `hides delta when previous is null`.

---

### Task 1: Schema columns, repo fields, one-shot rescan

**Files:**
- Modify: `server/internal/db/ent/schema/agent_cost_trend.go`
- Modify: `server/internal/db/repo/agent_cost_trend_repo.go` (`AgentCostRow`, `Upsert`)
- Modify: `server/internal/db/client.go` (call `migrateRescanCostTrends` after `migrateRoutineRunModes`)
- Generated: `server/internal/db/ent/**` (via entgen)
- Test: `server/internal/db/repo/agent_cost_trend_repo_test.go`, `server/internal/db/client_test.go`

**Interfaces:**
- Produces: ent fields on `AgentCostTrend`, all `Optional()` with defaults:
  - `started_at` Time, Optional, Nillable
  - `title` String, default `""`
  - `turns` Int, default 0
  - `agent_ms` Int64, default 0
  - `agent_time_source` String, default `"estimate"`
  - `cost_source` String, default `"estimate"`
  - `cost_unknown_model` Bool, default false
  - `cache_read_tokens` Int, default 0
  - `cache_creation_tokens` Int, default 0
  - `lines_added` Int, default 0
  - `lines_removed` Int, default 0
  - `model_costs` JSON `map[string]float64`, Optional
- Produces: `repo.AgentCostRow` gains `StartedAt time.Time` (zero means unset) plus `Title string`, `Turns int`, `AgentMs int64`, `AgentTimeSource string`, `CostSource string`, `CostUnknownModel bool`, `CacheReadTokens int`, `CacheCreationTokens int`, `LinesAdded int`, `LinesRemoved int`, `ModelCosts map[string]float64`.
- Produces: string constants in `repo`: `SourceCostState = "cost_state"`, `SourceEstimate = "estimate"`.
- Produces: `func migrateRescanCostTrends(db *sql.DB) error`, which runs `UPDATE agent_cost_trends SET source_mtime = 0` once behind marker `rescan-cost-trends-insights-v1` via `MarkerStore`. Its comment notes that the columns are additive and that the down path is to ignore them.

- [ ] **Step 1: Write the failing tests.**
  - `TestAgentCostTrendRepo_Upsert_InsightsFields`: upsert one row with every new field set, including `ModelCosts{"claude-opus-5-5": 1.25}` and a non-zero `StartedAt`. Read it back via `ListByTimeRange` and assert every field round-trips. Then upsert the same session again with `Turns: 9` and assert that the update wins.
  - `TestMigrateRescanCostTrends_RunsOnce`: insert a row with `source_mtime = 42` and run the migration; assert 0. Set it back to 42 and run again; assert it is still 42.
- [ ] **Step 2: Run them to see them fail.**
  - Run `cd server && go test ./internal/db/... -run 'InsightsFields|RescanCostTrends' -count=1`.
  - Expected: a compile failure on the unknown fields or function.
- [ ] **Step 3: Implement the change.**
  - Add the schema fields and regenerate ent (Global Constraints command).
  - Extend `AgentCostRow` and `Upsert`. Call `SetNillableStartedAt` only when the value is non-zero, and call `SetModelCosts` always; nil is allowed.
  - Add the migration and wire it in.
- [ ] **Step 4: Run the gates.** `task test`, `go vet ./...` (server), `task lint`. Expected: green.
- [ ] **Step 5: Commit.** `feat(history): store per-session insights columns and rescan once`

### Task 2: Session parser (pure)

**Files:**
- Create: `server/internal/history/sessionstats.go`
- Create: `server/internal/history/sessionstats_test.go`
- Modify: `server/internal/parser/scrub.go` (export the scrubber as `ScrubSecrets` and update its 5 callers)

**Interfaces:**
- Produces:
  ```go
  type sessionStats struct {
      Usage        sdk.TokenUsage
      Model        string    // last assistant model, default claudemodel.Latest(claudemodel.Sonnet)
      Cwd          string
      StartedAt    time.Time // first parsable timestamp
      LastActivity time.Time // latest assistant timestamp (today's rule)
      Title        string
      Turns        int
      EstimateMs   int64
      CostState    *costState // last cost-state record; nil when absent OR stale
  }
  type costState struct {
      TotalCostUSD      float64
      APIDurationMs     int64
      ToolDurationMs    int64
      LinesAdded        int
      LinesRemoved      int
      UnknownModelCost  bool
      ModelCosts        map[string]float64
  }
  func parseSession(r io.Reader) (sessionStats, error)
  ```
- JSON field names of the `cost-state` record: `totalCostUSD`, `totalAPIDuration`, `totalToolDuration`, `totalLinesAdded`, `totalLinesRemoved`, `hasUnknownModelCost`, `modelUsage.<model>.costUSD`.
- `parseSession` replaces `parseTokensFromReader` and `parseTokensFromRaw`. Port their existing tests in `importer_test.go` to `parseSession`. Same scanner buffer: 256 KiB initial, 4 MiB max.

- [ ] **Step 1: Settle the duration-unit HYPOTHESIS.** Run the check in Rulings and paste the output into the commit body.
- [ ] **Step 2: Write the failing tests** in `sessionstats_test.go`, each a table row of inline JSONL:
  - `TestParseSession_CostStateLastWins`: two cost-state records (1.0, then 2.5) and no assistant record after them. Expect `CostState.TotalCostUSD == 2.5`.
  - `TestParseSession_StaleCostStateIsDropped`: a cost-state followed by an assistant record. Expect `CostState == nil`.
  - `TestParseSession_NoCostState`: expect `CostState == nil`, with `Usage` summed as today.
  - `TestParseSession_TurnsAndEstimate`:
    - Turn 1 at 10:00:00 (prompt), assistant at 10:00:10, tool-result at 10:00:40, assistant at 10:01:00.
    - Turn 2 at 11:00:00 (prompt), assistant at 11:00:05.
    - Expect `Turns == 2` and `EstimateMs == 65000`.
  - `TestParseSession_GapOver30MinSkipped`: within one turn, assistant at 10:00:00 and a tool-result at 10:31:00 (31 min). Expect that delta to be excluded.
  - `TestParseSession_MixedToolResultIsNotATurn`: user content `[{"type":"text",...},{"type":"tool_result",...}]`. Expect `Turns == 1`.
  - `TestParseSession_MetaAndUnansweredPromptsNotTurns`: an `isMeta:true` user record, and a final prompt with no assistant after it. Neither counts.
  - `TestParseSession_TitleOrder`:
    - with `customTitle`, `aiTitle` and a prompt present, the custom title wins
    - with only `aiTitle` and a prompt, the AI title wins
    - with only a prompt, the prompt wins
    - a prompt starting with `<command-name>` is skipped for the title
  - `TestParseSession_TitleCappedAndScrubbed`: a prompt of 200 runes containing `\n\n` and an `sk-ant-` key. Expect at most 120 runes, no newline, and `[REDACTED]`.
  - `TestParseSession_StartedAt`: expect the first parsable timestamp of any record type.
- [ ] **Step 3: Run them to see them fail.** Run `cd server && go test ./internal/history/ -run ParseSession -count=1`. Expected: compile failure.
- [ ] **Step 4: Implement `parseSession`** per Rulings 2, 5 and 6 and the spec's turn rule.
  - A user record is a tool result when any content block has `type == "tool_result"`.
  - Stale detection: remember the index of the last cost-state record, and clear it when any assistant record follows.
- [ ] **Step 5: Run the gates.** `task test`, vet, `task lint`. Expected: green.
- [ ] **Step 6: Commit.** `feat(history): parse cost-state, turns, agent time and title per session`

### Task 3: Importer, subagent roll-up and row fill

**Files:**
- Modify: `server/internal/history/importer.go`
- Test: `server/internal/history/importer_test.go`

**Interfaces:**
- Consumes: `parseSession` and `sessionStats` (Task 2); the new `repo.AgentCostRow` fields and `repo.SourceCostState` / `repo.SourceEstimate` (Task 1).
- Produces: `func subagentFiles(parentPath string) []string`, which returns the `*.jsonl` files in `<dir>/<sessionId>/subagents/`, skipping files over `maxFileSizeBytes`, and returns nil when the directory is absent.
- Produces: `func newestMtime(parentPath string, subs []string) int64` (unix nanoseconds), used both by the skip-unchanged check in `runImport` and as the row's `SourceMtime`.

- [ ] **Step 1: Write the failing tests** (use `t.TempDir()` and `WithCollectFn`):
  - `TestImporter_RollsUpSubagents`:
    - Parent: 100 in / 10 out, Opus.
    - `subagents/agent-a.jsonl`: 50 in / 5 out, Haiku.
    - Expect `InputTokens == 150` and `OutputTokens == 15`.
    - Expect `CostUSD == EstimateCost(parent) + EstimateCost(sub)` and `CostSource == "estimate"`.
    - Expect `Turns` to count the parent's turns only.
  - `TestImporter_CostStateWins`: a parent with a fresh cost-state (`totalCostUSD` 3.2, API duration 1000, tool duration 500, lines 7/2, `modelUsage` with two models). Expect:
    - `CostUSD == 3.2` and `CostSource == "cost_state"`
    - `AgentMs == 1500` and `AgentTimeSource == "cost_state"`
    - lines 7/2, and `ModelCosts` with both keys
  - `TestImporter_StaleCostStateFallsBack`: expect `CostSource == "estimate"` and `AgentMs == EstimateMs`.
  - `TestImporter_ReparsesWhenOnlySubagentChanged`:
    - First run: the stored mtime equals the parent's mtime.
    - Touch the subagent file to parent + 1s and run again. Expect one upserted row the second time.
  - `TestImporter_SubagentFilesAreNotRows`: `collectJSONLFiles` over a tree with a subagent directory returns the parent only (pins today's behaviour).
- [ ] **Step 2: Run them to see them fail.** Run `cd server && go test ./internal/history/ -run Importer -count=1`. Expected: assertion failures.
- [ ] **Step 3: Implement the change.**
  - `extractCostRow` parses the parent, plus each subagent file for usage, model and per-file estimate.
  - It fills every new field per Rulings 2–4. `RecordedAt` keeps today's rule; `StartedAt` comes from the parent.
  - The skip check in `runImport` compares against `newestMtime`.
- [ ] **Step 4: Run the gates.** `task test`, vet, `task lint`. Run `go test -bench . ./internal/history/ -run '^$' -benchtime 1x` and paste it; expected: it still runs.
- [ ] **Step 5: Commit.** `feat(history): roll subagent usage into the parent session row`

### Task 4: Shared filter and the optional `project` on `/api/cost/summary`

**Files:**
- Modify: `server/internal/api/cost/handler.go`
- Test: `server/internal/api/cost/handler_test.go`

**Interfaces:**
- Produces:
  - `func RangeWhere(from, to, project string) (clause string, args []any)`. `from` empty means no lower bound. `project` empty means no project term. The project term is Ruling 10's expression. The clause has no leading `WHERE`.
  - `const ProjectParamMaxLen = 4096`.
- All four `query*` functions take `project` and use `RangeWhere`.

- [ ] **Step 1: Write the failing tests.**
  - `TestSummary_ProjectFilter`: two rows in different `project_path`s. With `?project=/a`, expect only `/a` in the totals, `byProject` and `byDay`.
  - `TestSummary_NoProjectUnchanged`: the same fixture without the param equals today's response.
  - `TestSummary_UnknownProjectBucket`: a row with empty `project_path` matches `project=unknown`.
- [ ] **Step 2: Run them to see them fail.** Run `cd server && go test ./internal/api/cost/ -count=1`.
- [ ] **Step 3: Implement the change.** Reject a `project` longer than `ProjectParamMaxLen` with 400.
- [ ] **Step 4: Run the gates.** `task test`, vet, `task lint`.
- [ ] **Step 5: Commit.** `feat(cost): filter the cost summary by project`

### Task 5: Insights API

**Files:**
- Create: `server/internal/api/insights/handler.go`
- Create: `server/internal/api/insights/handler_test.go`
- Modify: `server/internal/api/router.go` (`RouterDeps.InsightsHandler *insights.Handler`, mounted next to `CostHandler`), `server/serverapp/di.go` (construct it with `bundle.DB` next to `costHandler`)

**Interfaces:**
- Consumes: `apicost.RangeWhere` and `apicost.ProjectParamMaxLen` (Task 4); the columns from Task 1.
- Produces: `func NewHandler(db *sql.DB) *Handler` and `func (h *Handler) Mount(r chi.Router)`. A nil `db` returns empty payloads with status 200, like cost.
- Produces the JSON contract (the client mirrors it in Task 7):
  ```
  GET /api/insights/summary?from&to&project
    { from, to, current: Totals, previous: Totals|null }
    Totals = { costUsd, agentMs, tokens, sessions, estimated }
    tokens = input + output + cache_read + cache_creation
    estimated = count(cost_source = 'estimate')
  GET /api/insights/sessions?from&to&project&sort&order&limit&offset
    { rows: SessionRow[], total, limit, offset }
    sort ∈ cost|agent_ms|tokens|turns|started_at|recorded_at|lines (default cost)
    order ∈ asc|desc (default desc); limit 1..200 (default 50); offset ≥ 0 (default 0)
    SessionRow = { sessionId, title, projectPath, projectName, model, startedAt|null,
      recordedAt, costUsd, costSource, costUnknownModel, agentMs, agentTimeSource,
      inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, turns,
      linesAdded, linesRemoved, modelCosts }
    `lines` sorts by lines_added + lines_removed; ties break on session_id
  GET /api/insights/projects?from&to
    [{ projectPath, projectName, sessions }] ordered by sessions desc
  ```
- `previous` follows Rulings 7 and 8; validation follows Ruling 9 (`apierr.JSONError` with 400).

- [ ] **Step 1: Write the failing tests** (seed `agent_cost_trends` through `repo.Upsert` against an in-memory DB, following the pattern in `cost/handler_test.go`):
  - `TestSummary_CurrentAndPrevious`:
    - Rows on 2026-10-20 ($1, estimate) and 2026-10-27 ($2, cost_state).
    - Query `from=2026-10-24&to=2026-10-30`.
    - Expect current $2 with `estimated` 0, and previous (2026-10-17..2026-10-23) $1 with `estimated` 1.
  - `TestSummary_AllTimeHasNoPrevious`: no `from`. Expect `previous` null.
  - `TestSummary_TokensIncludeCache`.
  - `TestSessions_DefaultSortCostDesc`, `TestSessions_SortAgentMsAsc`, `TestSessions_Pagination` (total stays the full count).
  - `TestSessions_RejectsUnknownSort` (`sort=cost;drop`), `TestSessions_RejectsBadDate`, `TestSessions_RejectsFromAfterTo`, `TestSessions_RejectsLimit201`.
  - `TestSessions_ProjectWithQuote` (`project=/a'b`): expect 200 and only the matching row.
  - `TestProjects_DistinctInPeriod`.
- [ ] **Step 2: Run them to see them fail.** Run `cd server && go test ./internal/api/insights/ -count=1`.
- [ ] **Step 3: Implement the handler and the wiring.** Check the auth smoke tests that build `RouterDeps` (`internal/api/bypass_auth_smoke_test.go:179`, `permission_ingress_auth_test.go:109`). Add the handler there so `/api/insights/*` stays inside the protected group.
- [ ] **Step 4: Run the gates.** `task test`, vet (server), `task lint`.
- [ ] **Step 5: Commit.** `feat(insights): serve summary, sessions and projects for the Insights page`

### Task 6: Drawer size on AppModal

**Files:**
- Modify: `src/components/ui/AppModal.vue`
- Create: `src/components/ui/AppModal.test.ts` (if one exists elsewhere, extend that one instead)

**Interfaces:**
- Produces: `size?: 'standard' | 'auto' | 'drawer'`. `drawer` is a right-aligned panel, full height, `width` as the max width, and full width below the `md` breakpoint. It keeps the focus trap, Esc to close, the scroll lock and focus restore.

- [ ] **Step 1: Write the failing tests.**
  - `renders drawer panel right-aligned`: the panel has the drawer class.
  - `drawer closes on Escape and restores focus`.
- [ ] **Step 2: Run them to see them fail.** Run `pnpm vitest run src/components/ui/AppModal.test.ts`.
- [ ] **Step 3: Implement the drawer size.**
- [ ] **Step 4: Run the gates.** `pnpm lint && pnpm typecheck && pnpm test`.
- [ ] **Step 5: Commit.** `feat(ui): add a drawer size to AppModal`

### Task 7: Client contract, tab state and view migration

**Files:**
- Create: `src/features/insights/insightsApi.ts` (types plus fetchers; hand-kept parity with `server/internal/api/insights/handler.go`)
- Create: `src/features/insights/insightsTab.ts`
- Modify: `src/composables/useViewState.ts`, `src/utils/navConfig.ts`
- Tests:
  - `src/features/insights/insightsTab.test.ts`, `src/composables/useViewState.test.ts`, `src/utils/navConfig.test.ts`
  - fixtures in `src/features/hub/__tests__/HubWidget.test.ts:886`, `src/composables/__tests__/needsYouPlacement.test.ts:25`, `src/components/shell/AppTopbar.test.ts:7`

**Interfaces:**
- Produces, in `insightsApi.ts`:
  - the types `InsightsTotals`, `InsightsSummary`, `InsightsSessionRow`, `InsightsSessionsPage`, `InsightsProject`, `InsightsSort`, `CostSource = 'cost_state' | 'estimate'`
  - `fetchSummary(f: InsightsFilters, signal?)`, `fetchSessions(f: InsightsFilters, q: { sort, order, limit, offset }, signal?)`, `fetchProjects(f: Pick<InsightsFilters,'from'|'to'>, signal?)`
  - `InsightsFilters = { preset: Preset, from?: string, to: string, project?: string }`, where `Preset = '7d'|'30d'|'90d'|'all'`
  - `presetRange(p: Preset, today: Date): { from?: string, to: string }` (Ruling 7)
  - `presetHours(p: Preset): number` (Ruling 13)
- Produces, in `insightsTab.ts`:
  - `INSIGHTS_TABS = ['overview','sessions','cost','quality','workflows'] as const` and `type InsightsTab`
  - `INSIGHTS_TAB_KEY = 'insights-tab'`
  - `readInsightsTab(): InsightsTab` (default `overview`) and `writeInsightsTab(t)`; every storage access is wrapped in try/catch
- Produces: `CoreView` drops `cost`, `eval`, `workflows` and gains `insights`. The stored values `cost`, `eval`, `workflows` and the legacy `agent-view-mode` value `cost-analytics` become `insights`, and the tab is written as `cost`, `quality`, `workflows` and `cost` respectively.
- Produces: `NAV_ITEMS` replaces the three Insights entries with `{ view: 'insights', label: 'Insights', icon: '◷', group: 'Insights' }`.

- [ ] **Step 1: Write the failing tests.**
  - `presetRange('all')` has no `from`; `presetRange('7d', new Date('2026-10-29'))` gives `{from:'2026-10-22', to:'2026-10-29'}`; `presetHours` table.
  - `readInsightsTab` defaults to `overview`, rejects an unknown stored value, and survives a throwing `getItem`.
  - `useViewState`, one case for each mapping: stored `eval` gives view `insights` and tab `quality`.
  - `migrates stored cost view when setItem throws`.
  - `navConfig`: the Insights group equals `['insights']`, and `viewTitle('insights') === 'Insights'`.
  - Update the hub, needsYou and topbar fixtures to the new view list.
- [ ] **Step 2: Run them to see them fail.** Run `pnpm vitest run src/features/insights src/composables src/utils src/features/hub src/components/shell`.
- [ ] **Step 3: Implement the change.** `App.vue` still references the old views at this point. Add a temporary `v-else-if="activeView === 'insights'"` rendering `CostAnalyticsView` so that typecheck passes; Task 10 replaces it.
- [ ] **Step 4: Run the gates.** `pnpm lint && pnpm typecheck && pnpm test`.
- [ ] **Step 5: Commit.** `feat(insights): one Insights view with tab state and migrated stored views`

### Task 8: Existing views take the page filters

**Files:**
- Modify: `src/features/analytics/composables/useCostAnalytics.ts` (gains `project` param), `src/features/analytics/components/CostAnalyticsView.vue`, `src/features/analytics/components/EvalView.vue`
- Modify: `src/features/workflows/components/WorkflowsView.vue`
- Tests: the existing `CostAnalyticsView.test.ts` and `EvalView.test.ts`, plus a new `WorkflowsView.test.ts` if none exists

**Interfaces:**
- Consumes: `InsightsFilters`, `presetRange` and `presetHours` (Task 7).
- Produces:
  - `CostAnalyticsView` takes the optional props `from?: string`, `to?: string` and `project?: string`. When `to` is set, its own preset and custom-range bar is hidden and the props drive `setRange(from ?? '2000-01-01', to, project)`.
  - `EvalView` takes the optional prop `hours?: number`. When it is set, the hours select is hidden and `setHours` follows the prop.
  - `WorkflowsView` takes the optional props `from?` and `to?`. When they are set, they write `filters.from` and `filters.to` and the two date inputs are hidden.
  - With no props, every view behaves exactly as today.

- [ ] **Step 1: Write the failing tests.**
  - `CostAnalyticsView`: with props, the fetch URL contains `project=%2Fa` and the preset buttons are absent; without props, the buttons are present.
  - `EvalView`: the prop `hours=720` fetches `hours=720`.
  - `WorkflowsView`: props set the URL's `from`/`to`.
- [ ] **Step 2: Run them to see them fail.** Run `pnpm vitest run src/features/analytics src/features/workflows`.
- [ ] **Step 3: Implement the change.**
- [ ] **Step 4: Run the gates.** `pnpm lint && pnpm typecheck && pnpm test`.
- [ ] **Step 5: Commit.** `feat(insights): let the cost, eval and workflows views follow page filters`

### Task 9: Header, sessions table and session drawer

**Files:**
- Create: `src/features/insights/components/InsightsKpiRow.vue`
- Create: `src/features/insights/components/InsightsSessionsTable.vue`
- Create: `src/features/insights/components/InsightsSessionDrawer.vue`
- Tests: one `*.test.ts` beside each

**Interfaces:**
- Consumes: the types from Task 7, `AppModal size="drawer"` (Task 6), `SessionDagChart.vue`, and `formatCost` / `formatTokens` from `src/utils/format.ts`.
- Produces:
  - `InsightsKpiRow`, props `{ summary: InsightsSummary | null }`.
    - Four tiles: Cost, Agent time, Tokens, Sessions.
    - Delta tile text such as `▲12%` against `previous`, hidden when `previous` is null or the previous value is 0.
    - A line "n of m sessions estimated" when `estimated > 0`.
  - `InsightsSessionsTable`.
    - Props: `{ page: InsightsSessionsPage, sort: InsightsSort, order: 'asc'|'desc' }`.
    - Emits: `sort(key)`, `page(offset)`, `open(row, el: HTMLElement)`.
    - Columns: title, project, start, cost, agent time, tokens, turns, lines (+/−).
    - Sortable column headers are buttons with `aria-sort`.
    - Rows open on click and on Enter.
  - `InsightsSessionDrawer`.
    - Props: `{ row: InsightsSessionRow | null }`. Emits: `close` and `navigate(sessionId)`.
    - Shows the fields from the spec's Session drawer section.
    - Fetches `/api/visualizations/dag?session=<id>` on open and renders it in `SessionDagChart`.
  - Estimate marker: cost and agent time get the prefix `≈` and a `title` naming the source when the source is `estimate`, or when `costUnknownModel` is true. The marker is one function, `estimateMark(source: CostSource, unknownModel?: boolean): { prefix: string, title: string }`, exported from `InsightsKpiRow.vue`'s sibling `src/features/insights/estimateMark.ts`. It adds a fourth file, but it is shared by all three components.

- [ ] **Step 1: Write the failing tests.**
  - `hides delta when previous is null`, `shows ▲ and percent`, `shows estimated count`.
  - The table emits `sort('agent_ms')` on a header click, sets `aria-sort` on the active column, and marks estimate rows with `≈`.
  - The drawer renders the title and the token split, fetches the dag URL with the session id, and emits `navigate`.
- [ ] **Step 2: Run them to see them fail.** Run `pnpm vitest run src/features/insights/components`.
- [ ] **Step 3: Implement the components.**
- [ ] **Step 4: Run the gates.** `pnpm lint && pnpm typecheck && pnpm test`.
- [ ] **Step 5: Commit.** `feat(insights): KPI row, sessions table and session drawer`

### Task 10: InsightsView, Overview, App wiring

**Files:**
- Create: `src/features/insights/components/InsightsView.vue`
- Create: `src/features/insights/components/InsightsOverview.vue`
- Modify: `src/App.vue:421-428` (one `InsightsView` branch, forwarding `@navigate` as `WorkflowsView` does today)
- Test: `src/features/insights/components/InsightsView.test.ts`

**Interfaces:**
- Consumes: Tasks 7–9; `CostTrend.vue` for the cost-per-day chart; `useEvalMetrics().openAlerts` for the drift alerts.
- Produces:
  - `InsightsView`.
    - `h1` "Insights".
    - Filter bar: preset buttons 7d / 30d / 90d / All (default `30d`) and a project select fed by `fetchProjects`.
    - KPI row and tablist; the tabs follow the WAI-ARIA tabs pattern: `role=tablist`, arrow keys, `aria-selected`.
    - Only the active tab mounts. The summary refreshes on filter change and every 60 s while mounted.
    - On Quality and Workflows, the project select is disabled with the hint "Project filter does not apply to this tab."
  - `InsightsOverview`: open drift alerts, the top 5 sessions by cost (opening the drawer), and the cost-per-day chart for the period.

- [ ] **Step 1: Write the failing tests.**
  - The default tab is `overview`.
  - A tab click persists the tab via `writeInsightsTab`.
  - The Cost tab receives `project`.
  - On the Quality tab the project select is disabled and shows the hint.
  - Changing the preset refetches the summary with the new range.
  - Arrow-right moves focus to the next tab.
- [ ] **Step 2: Run them to see them fail.** Run `pnpm vitest run src/features/insights`.
- [ ] **Step 3: Implement the change.** Remove the Task 7 placeholder.
- [ ] **Step 4: Run the gates.** `pnpm lint && pnpm typecheck && pnpm test`.
- [ ] **Step 5: Commit.** `feat(insights): Insights page with overview and tabs`

### Task 11: E2E, screenshots, docs

**Files:**
- Modify: `tests/e2e/workflows.spec.ts:36`, `tests/e2e/refinement-cost.spec.ts`, `tests/screenshots/readme.spec.ts:76`
- Create: `tests/e2e/insights.spec.ts`
- Modify: `README.md`, `CHANGELOG.md`, `PRIVACY.md`, `.agent-context/layer2-project-core.md` (SSOT table row for `insightsApi.ts` ↔ `api/insights/handler.go`)

**Interfaces:**
- Consumes: the test ids `nav-item-insights` and the tab roles from Task 10.

- [ ] **Step 1: Update the specs.**
  - Workflows: `nav-item-insights`, then the `Workflows` tab.
  - Cost: Insights, then the Cost tab.
  - Screenshot: `pinView(page, 'insights')` with tab `cost` seeded in `insights-tab`, heading "Insights".
  - New `insights.spec.ts`:
    - Overview renders with stubbed `/api/insights/*`.
    - The Quality tab shows "Open drift alerts".
    - A sessions row opens the drawer, and Esc closes it.
- [ ] **Step 2: Write the docs.**
  - README: the Insights section replaces Cost, Eval and Workflows.
  - CHANGELOG: `### Added` the Insights page and per-session metrics; `### Changed` that Cost, Eval and Workflows moved into Insights tabs, that subagent usage now counts toward session cost, and that real `/cost` totals are used.
  - PRIVACY: a row for `agent_cost_trends.title`.
- [ ] **Step 3: Run every gate** and paste the raw output.
  - `task test`
  - `go vet` (sdk and server)
  - `task lint`
  - `task changelog:check`
  - `pnpm lint && pnpm typecheck && pnpm test`
  - `pnpm test:e2e`
- [ ] **Step 4: Commit.** `test(insights): e2e for the Insights page; docs`
