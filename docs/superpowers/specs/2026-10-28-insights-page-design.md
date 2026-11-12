# Insights page — design

Date: 2026-10-28 · Status: draft for review · Scope: PR 1 of 2

## Intent

Alexander opens Kontor regularly to get an overview of how his Claude work went: what it cost, how much
agent time it took, which sessions were expensive, and whether anything drifted. Today that overview is
split across three nav items under "Insights" (Cost, Eval, Workflows), and the most useful question —
"which session was expensive, and why?" — cannot be answered at all, because Kontor has no per-session
view of cost, time, turns and tokens.

The ponytail "band" board (work profile, ponytail 5.3.2) showed that a per-session table with a KPI row is
the useful core. Kontor already reads the same JSONL files, so it can offer the same view without a new data
source — and more accurately, because Claude Code writes its own `/cost` totals into the JSONL as
`cost-state` records, which neither Kontor nor ponytail reads today.

## Goals

1. One nav item **Insights** replaces Cost, Eval and Workflows.
2. A **KPI row** (cost, agent time, tokens, sessions) with the change against the previous period of equal
   length, plus one **filter bar** (period presets 7d / 30d / 90d / All, and project).
3. A **Sessions** tab: a sortable table with one row per session — title, project, start, cost, agent time,
   tokens, turns, lines added/removed. Clicking a row opens a **drawer** with the session's detail.
4. An **Overview** tab (the default) answering "what needs my attention": open drift alerts, the five most
   expensive sessions of the period, and the cost-per-day chart.
5. **Real cost** from `cost-state` where present, falling back to Kontor's token-based estimate, and the UI
   says which one a number is.

## Non-goals (this PR)

- Test status per session ("tests green after last edit") — PR 2.
- Reuse %, callers-left-open, or any code-structure heuristic from ponytail (heuristic, language-limited).
- A live Claude Code mod/hook integration for cost.
- Redesigning the Cost, Quality (Eval) or Workflows content. They move into tabs nearly unchanged.
- Project filtering for Quality and Workflows (their data is not keyed by project; see Filters).
- Streak heatmap, share card.

## Anti-goals

- The Insights page opening slower than the Cost view does today.
- Breaking the `cost-today` workspace widget (it reads `GET /api/cost/summary`; that endpoint stays as is).
- A user's saved view (`agent-active-view` = `cost`, `eval` or `workflows`) silently falling back to Zentrale.
- Two different cost numbers for the same session in two places without saying why.

## Decided

### Layout — hybrid of tabs and session-first (Alexander chose this)

```
Insights                                   [7d 30d 90d All]  [Project ▾]
┌ Cost $412 ▲12% ┐┌ Agent time 31 h ▼4% ┐┌ Tokens 1.2 B ┐┌ Sessions 58 ┐
Overview | Sessions | Cost | Quality | Workflows
```

- The header (KPI row + filter bar) stays visible above the tabs.
- Tabs: **Overview** (default), **Sessions**, **Cost** (today's CostAnalyticsView body), **Quality**
  (today's EvalView body), **Workflows** (today's WorkflowsView body).
- Session detail opens as a **drawer** over the page (right side; full width below the `md` breakpoint), not
  as a fixed side panel, so the table keeps its width in a narrow window.
- Only the active tab loads its data. The header loads the summary only.

### Navigation and compatibility

- New `CoreView` `insights`. `cost`, `eval` and `workflows` leave `NAV_ITEMS` and `ACTIVE_VIEWS`.
- Stored `agent-active-view` values `cost`, `eval`, `workflows` (and the legacy `cost-analytics`) migrate to
  `insights` with the matching tab selected (`src/composables/useViewState.ts:50-74`).
- The selected tab is a per-viewer convenience in `localStorage` (wrapped in try/catch).
- Hub launchers derive from `NAV_ITEMS` (`hub/hubLaunchers.ts:20`), so they follow automatically.

### Data — per-session metrics in the existing ingest

The importer already makes one row per session in `agent_cost_trends`
(`server/internal/db/ent/schema/agent_cost_trend.go`, written by `server/internal/history/importer.go`,
boot scan + ticker every `cost.scanIntervalMs`, default 300 s). Extend that row; do not add a second
per-session store.

New columns on `agent_cost_trends`:

| Column | Meaning |
|---|---|
| `started_at` | first parsable timestamp in the file |
| `title` | first real user prompt, whitespace-collapsed, at most 120 runes |
| `turns` | number of turns (rule below) |
| `agent_ms` | agent time in milliseconds |
| `agent_time_source` | `cost_state` or `estimate` |
| `cost_source` | `cost_state` or `estimate` |
| `cost_unknown_model` | `hasUnknownModelCost` from the last `cost-state` |
| `cache_read_tokens`, `cache_creation_tokens` | already summed by the parser, not stored today |
| `lines_added`, `lines_removed` | from the last `cost-state`; 0 when absent |
| `model_costs` | JSON map model → costUSD from the last `cost-state` `modelUsage`; empty when absent |

Rules:

- **`cost-state`:** a JSONL record `{"type":"cost-state", totalCostUSD, totalAPIDuration, totalToolDuration,
  totalLinesAdded, totalLinesRemoved, hasUnknownModelCost, modelUsage:{model:{costUSD,…}}, …}`. Values are
  cumulative and never decrease within a file, also across a resume (VERIFIED on the 60 newest local
  sessions: every multi-record file was monotonic, including one with two distinct `startTime`s). The
  **last** record in the file is authoritative. Present in 31 of the 40 newest local sessions.
- **Stale `cost-state` (my call):** Claude Code writes the record when a run ends. If any assistant record
  follows the last `cost-state` (a running or crashed session — 3 of 31 locally), the record does not cover
  the whole session: use the estimate for cost, tokens and agent time, and mark it. Re-evaluated on every
  import pass, so a session switches to `cost_state` once it ends.
- **Subagents are included (VERIFIED):** `cost-state.modelUsage` of a parent session lists Sonnet and Haiku
  usage that appears only in its `{sessionId}/subagents/*.jsonl` files, never in the parent file. So
  `cost-state` totals include subagent work. For the estimate path to measure the same thing, the importer
  rolls `{sessionId}/subagents/*.jsonl` into the parent row (tokens, estimated cost, agent time). Today
  `collectJSONLFiles` reads only `projects/<dir>/*.jsonl` and skips directories. Subagent files are never
  rows of their own. The row's `source_mtime` is the newest mtime of the parent and its subagent files, so a
  finished subagent triggers a re-parse. Turns count only the parent's turns (a subagent's turns are work
  inside one parent turn).
- **Cost:** `totalCostUSD` of the last `cost-state` → `cost_source = cost_state`; else today's
  `parser.EstimateCost` → `cost_source = estimate`. `cost_usd` keeps its meaning (the session's cost), so
  `/api/cost/summary`, the heatmap and the forecast pick up the better number without changes.
- **Agent time:** `totalAPIDuration + totalToolDuration` of the last `cost-state` → `cost_state`. Else
  estimate: a turn starts at a user record that is neither a tool result nor `isMeta`; within a turn, sum the
  gaps between consecutive assistant / tool-result timestamps, skipping any gap over 30 minutes (an
  unanswered permission prompt, not work). Same rule as ponytail's importer
  (`ponytail-band-import.js:130-147`).
- **Turns:** count turn starts (rule above) that are followed by at least one assistant record.
- **Rescan:** existing rows must be re-parsed once. A one-shot marker via `db.MarkerStore`
  (`server/internal/db/markers.go`) resets `source_mtime` to 0 so the next import pass re-reads every file.
  New columns are additive and default to zero/empty (ent auto-migrate); the down path is "ignore the
  columns" — note that in the migration's comment.

### API

New package `server/internal/api/insights`:

- `GET /api/insights/summary?from=&to=&project=` → KPI values for the period and for the previous period of
  equal length: cost, agent_ms, tokens (input + output + cache), sessions; plus how many sessions in the
  period used estimates (`cost_source = estimate`).
- `GET /api/insights/sessions?from=&to=&project=&sort=&order=&limit=&offset=` → rows with every column
  above plus `session_id`, `project_name`, `model`. Default sort: cost desc. `limit` capped.
- `GET /api/insights/projects?from=&to=` → distinct projects with sessions in the period, for the filter.
- Dates are `YYYY-MM-DD`, filtered the way `api/cost/handler.go:167-173` does it (`substr(recorded_at,1,10)`),
  on `recorded_at` (last activity) — the same column `/api/cost/summary`, the heatmap and the forecast use
  (`api/cost/handler.go:181`, `recordedAtRangeClause`). My call: one date column everywhere, so the header
  KPI and the Cost tab total for the same period are always equal. A session counts in the period of its last
  activity; `started_at` is shown, not filtered on.
- Validate every query param at the boundary; reject unknown `sort` values.

### Session drawer

Shows: title, project, start / last activity, model(s) with per-model cost when `model_costs` is present,
tokens split into input / output / cache read / cache write, turns, agent time with its source, lines
added/removed, and the session's tool waterfall from the existing `GET /api/visualizations/…?session=`.
A link opens the session in Kontor's existing session/transcript view if one exists for it.

Correction to the earlier chat sketch: Eval metrics are **not** per session. They come from pipeline
stage runs (`server/internal/eval/collector.go:40`), so the drawer shows no eval values.

### Filters

- Period and project apply to the header, Overview and Sessions.
- Cost tab: period applies (it already takes `from`/`to`); project applies by passing it to
  `/api/cost/summary` — **new optional `project` param**, absent = today's behaviour.
- Quality and Workflows: period applies where their APIs take one (eval `hours`, visualizations
  `from`/`to`); the project control is shown disabled there with a one-line hint.

### Estimates must be visible

Any cost or agent-time value from an estimate gets a marker (e.g. `≈`) and a tooltip naming the source;
the header says "n of m sessions estimated" when n > 0. `cost_unknown_model` is marked the same way.

### Privacy

`title` stores the first prompt (capped at 120 runes) in SQLite, which Kontor does not store today (it reads
it from disk per request). Add the row to `PRIVACY.md`. Everything stays local; the server stays on
`127.0.0.1`.

### Docs and tests

- README (Insights section replaces Cost/Eval/Workflows), CHANGELOG (`### Added` / `### Changed` under
  `[Unreleased]`), PRIVACY.md.
- E2E: `tests/e2e/workflows.spec.ts:36` (`nav-item-workflows`), `tests/e2e/refinement-cost.spec.ts:10`,
  `tests/screenshots/readme.spec.ts:76` (`pinView(page,'cost')`, heading "Cost Analytics") move to the
  Insights page and its tabs. No eval E2E exists today; add one smoke test for the Quality tab.
- Unit tests for the parser rules (cost-state last-wins, fallback, turn and agent-time estimate, 30-minute
  gap), the marker rescan, the new endpoints (validation, previous-period maths), and the view migration.

## Left to the builder

- Component split, composable names, and whether the header summary is polled (today's Cost view polls
  every 60 s) or refreshed on tab focus.
- Pagination vs. a capped list for Sessions, as long as sorting is server-side.
- The drawer component (none exists; `AppModal.vue` is the closest pattern) and its keyboard/focus handling,
  which must meet the usual a11y basics (focus trap, Esc closes, focus returns to the row).
- Exact chart reuse on Overview (`CostTrend.vue` is the natural candidate).
- How to widen `parseTokensFromReader` (a result struct beats more tuple values).
