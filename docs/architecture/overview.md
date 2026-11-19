# Architecture Overview

A real-time monitoring and control dashboard for locally running Claude Code agents. The backend scans running processes and reads Claude Code's JSONL session logs; the frontend renders them live over Server-Sent Events.

## Stack

- **Backend:** Go 1.26 — chi router, [ent](https://entgo.io/) ORM, `modernc.org/sqlite` (pure-Go, no cgo), cobra CLI. Dependency injection is hand-written in `server/serverapp/di.go`.
- **Frontend:** Vue 3 + TypeScript SPA — Vite, Tailwind CSS, pnpm.
- **Workspace:** a Go workspace (`go.work`) with two modules — `./sdk` and `./server`. Build is driven by [Task](https://taskfile.dev) (`Taskfile.yml`); hot-reload by [air](https://github.com/air-verse/air).

## High-level flow

```
+------------------------+      +---------------------------+      +-------------------+
|  Browser (Vue 3 SPA)   |      |  Go Backend (:13120)      |      | Claude Code Agents|
+------------------------+      +---------------------------+      +-------------------+
| Cards / List           | <--- | GET  /api/agents/stream   | ---> | ps / lsof         |
| Agent Modal (chat)     |      |      (SSE + polling fb)   |      | ~/.claude/        |
| Prompt Input           | ---> | POST /agents/:id/message  |      | JSONL logs        |
| Spawn Dialog           | ---> | POST /agents/spawn        |      |                   |
+------------------------+      +---------------------------+      +-------------------+
                                            |
                                            v
                                +---------------------------+
                                |   dashboard-channel SDK   |
                                |  (Go MCP stdio binary)    |
                                +---------------------------+
```

## Backend packages (`server/internal/`)

| Package | Responsibility |
|---|---|
| `server/cmd/serve/` | cobra CLI entrypoint, hand-written DI wiring |
| `api/` | chi router, HTTP handlers grouped by domain |
| `pipeline/` | state machine, orchestrator, stage handlers, completion detector, agent spawner |
| `db/` | ent ORM schemas + repositories |
| `mcp/` | stateless StreamableHTTP MCP server (43 tools, 7 scopes) |
| `auth/` | JWT helpers + GitHub OAuth |
| `scanner/` | ps/lsof process scanner |
| `parser/` | JSONL session parser |
| `merger/` | agent data merger + cost estimation (`MODEL_PRICING`) |
| `sse/` | SSE broadcaster |
| `channel/` | channel discovery + proxy to per-agent MCP stdio server |
| `capability/` | Permission/grant resolution, decision engine for grantable capabilities (tool, reach, resource, spend classes) |
| `memory/` | System memory store (spaces, entries, retrieval) with capability-gated access and ranked search |
| `askgate/` | Permission-prompt hold-and-answer bridge; shared with PreToolUse hook asker |
| `serverask/` | Server-enforcer asker implementation for capability decisions, surfaces unanswered asks to dashboard |
| `api/capabilities/` | HTTP endpoints for capability-grant CRUD, query, decision responses |
| `refine/` | refinement chat repo + spawner |
| `history/` | cost-history importer service |
| `eval/` | passive drift detection over `stage_run` (leaf: `db/repo`, `db/ent`, `sdk`, `config`, `parser` only — never `pipeline`/`notifications`/`sse`/routes). See [ADR-0008](adr/0008-eval-drift-detection-leaf.md) |
| `webpush/` | Web Push VAPID service |
| `scheduler/` | cron scheduling leaf — fires recurring pipeline tasks on a configurable tick |

## SDK (`sdk/`)

A separate Go module that compiles the `dashboard-channel` MCP stdio binary. This binary is injected into every spawned pipeline stage agent to provide the two-way permission gate and the channel reply bridge. The SDK also defines the canonical data model (`Agent`, `TokenUsage`, `SessionMeta`, `SubAgent`, `TaskInfo`) shared by both the server and the channel binary.

## Data flow

1. **Process scanning** — `ps aux` + `lsof` (macOS) or `/proc/<pid>/cwd` (Linux) find running `claude` processes and their working directories.
2. **Session matching** — PIDs are matched to JSONL session files in `~/.claude/projects/{encoded_path}/`.
3. **Log parsing** — tail-reads the last 32 KB of each session file for tokens, tools, tasks, and model info; a full read happens when the agent modal opens.
4. **Cost estimation** — the `MODEL_PRICING` table in `server/internal/merger/` calculates API-equivalent costs.
5. **Status classification** — active (< 30 s), waiting (< 5 min), idle (> 5 min) since last activity.
6. **Real-time updates** — the browser subscribes to `/api/agents/stream` (SSE) with a polling fallback.

## Task pipeline

A multi-stage agentic workflow. Tasks progress through:

```
concept → backlog → implementation → self_review → finalization → done
```

Terminal states also include `on_hold` and `cancelled`. The `concept` and `backlog` stages are agent-less; `implementation`, `self_review`, and `finalization` each spawn a detached `claude` CLI process in an isolated git worktree.

Key characteristics:

- LLM output is validated against a per-stage JSON schema; one automatic retry with feedback injection before escalating to the user.
- Up to 3 tasks run in parallel (configurable via `maxParallelOrchestrators` in the pipeline DB config).
- Permission requests from stage agents are gated through the dashboard channel; a bulk-resolve UI lets the user grant or deny every pending request in one click.
- Notifications (email, webhook, browser, system) are dispatched on hold/failure events.

See the ADRs for rationale:

- [ADR-0001 — SQLite for the task pipeline](adr/0001-sqlite-for-task-pipeline.md)
- [ADR-0002 — Runner-slot priority model](adr/0002-runner-slot-priority-model.md)
- [ADR-0003 — Pluggable spawners](adr/0003-pluggable-spawners.md)

## Scheduler

`server/internal/scheduler/` is a leaf package that fires recurring pipeline tasks on a configurable tick (default 30 s). It runs as a 4th `errgroup` worker alongside the pipeline orchestrator in `server/cmd/serve/main.go`.

**Layering rules:**
- `scheduler/` must not import `pipeline/` or `api/`. It creates tasks via an injected `CreateTaskFromInput` closure supplied by the DI layer — this is the only path from the scheduler into task-creation logic.
- `pipeline/` must not import `scheduler/`. The dependency arrow points inward: `serve/` → `scheduler/` → `db/repo`.

**Key design properties:**
- NL→cron translation (`NLCron`) happens once, at schedule create/edit. The stored 5-field cron expression is what drives firing — deterministic and offline-safe.
- **Skip-on-overlap:** if the prior task from a schedule is still in a non-terminal stage, the fire is skipped and `next_run_at` advances.
- **Catchup policy:** `once` fires a single catch-up run after downtime; `none` skips missed windows. Both always advance `next_run_at`. Configurable globally via `scheduleCatchup` (pipeline_config) and overridable per schedule.

See [ADR-0007 — Cron scheduling engine](adr/0007-cron-scheduling-engine.md) for the rationale behind the stored-expression design and the choice of `robfig/cron/v3`.

## Key conventions

- **Path alias:** `@/*` maps to `./src/*`.
- **Dual persistence:** agent monitoring is filesystem-derived (no database); the task pipeline uses SQLite at `~/.claude/dashboard-tasks.db` (override via `DASHBOARD_DB_PATH`). See [ADR-0001](adr/0001-sqlite-for-task-pipeline.md).
- **Cost estimation** uses API-equivalent pricing — not actual billing for Pro/Max plan users.
- **Agent status thresholds:** active < 30 s, waiting < 5 min, idle > 5 min.
- **Subagent discovery:** `~/.claude/projects/{encoded_path}/{sessionId}/subagents/*.jsonl`.
