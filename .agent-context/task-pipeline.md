# Task Pipeline Architecture

The Task Pipeline subsystem (`server/internal/pipeline/`, `server/internal/api/tasks/`, `server/internal/db/repo/`, `server/internal/proc/`) follows a strict layered architecture with callback-based decoupling. See [ADR-0001](../docs/architecture/adr/0001-sqlite-for-task-pipeline.md) for why SQLite was added and [ADR-0002](../docs/architecture/adr/0002-runner-slot-priority-model.md) for the runner-slot priority model used by the driver loop.

## Key Pipeline Modules (`server/internal/pipeline/`)

- `orchestrator.go` — `PipelineOrchestrator` struct implementing the state machine, `Run()` driver loop, `ProgressTask()` per-task lock, completion finalization, and spawning of the next batch of tasks via the scheduler.
- `stage_handlers.go` — `agentStageHandler` struct and the `StageHandler` interface; produces real handlers spawning detached Claude processes via `Execute()`. Non-agent stages (backlog, approval) override the default behavior.
- `stage_prompts.go` — per-stage prompt builders. Each `PromptBundle` consists of `SystemPrompt` and `UserPrompt` strings. User prompts end with a `` ```json `` block defining the output schema contract.
- `spawner.go` — spawns the stage agent via `SpawnAgentOptions`, writes agent settings, injects dashboard-channel MCP. Supports both native Claude spawns and adapter-based spawns (Ollama, OpenAI, custom). See [ADR-0003](../docs/architecture/adr/0003-pluggable-spawners.md) for the spawner dispatch model.
- `completion_detector.go` — converts a dead PID + session JSONL into a next/retry/fail decision. Strict per-stage schema validators (`ValidateStageOutput`) with injectable dependencies for tests.
- `session_reader.go` — reads the last assistant turn from a session JSONL, extracts the `` ```json `` block, falls back to newest-by-mtime session discovery when the stage_run has no `session_id` yet. Scans all known `CLAUDE_CONFIG_DIR` directories.
- `session_manager.go` — lightweight wrapper for stage_run mutations; the core `IsPidAlive` check has been extracted to `proc.IsPidAlive` (see [ADR-0009](../docs/architecture/adr/0009-proc-leaf.md)).
- `types.go` — `StageTransition` sealed interface (concrete types: `NextTransition`, `DoneTransition`, `FailTransition`, `WaitUserTransition`, `IterateTransition`, `OnHoldTransition`, `AsyncRunningTransition`, `RequeueTransition`, `RateLimitedTransition`), `StageContext` with injected side-effect callbacks.

## Orchestrator Collaborators (Dependencies Injected into PipelineOrchestrator)

Extracted as separate components to satisfy [ADR-0001](../docs/architecture/adr/0001-sqlite-for-task-pipeline.md) (single-writer bound) and improve testability:

- `configCache` — caches pipeline_config rows with a short TTL, avoids repeated DB queries per tick. Used by `scheduler`, `modelResolver`, and config-read routes.
- `modelResolver` — applies precedence (task/spawner override → project-scoped config → global config → coded default) to resolve the LLM model for each stage. See `model_resolver.go`.
- `stageRunService` — abstraction over `repo.StageRunRepo` for all stage_run persistence within the pipeline. Ensures a single point of mutation.
- `scheduler` — `Pick()` selects the next tasks to spawn based on `maxParallelOrchestrators` config, priority, rank, and dependency constraints. See [ADR-0002](../docs/architecture/adr/0002-runner-slot-priority-model.md) and `scheduler.go`.

## Stage Output Routing

`server/internal/api/tasks/handler.go` — REST route layer that drives `ProgressTask()` for manual advances, task creation, and resume-from-user flows.

`server/internal/api/tasks/permission_request_routes.go` — handles permission request resolution, granting, and bulk-grant flows. Calls `ProgressTask()` with `ProgressOpts` to restart runs after grants.

## Process Liveness (ADR-0009)

`server/internal/proc/` — leaf package holding `IsPidAlive(pid)`, extracted from the orchestration core because `db/rawrepo` and `api/tasks/{enrich,analyze}_routes.go` needed it without importing the full pipeline. The leaf depends only on stdlib and `platform/` constants. See [ADR-0009](../docs/architecture/adr/0009-proc-leaf.md).

## Pluggable LLM Adapters (ADR-0005)

`server/internal/llmadapter/` — leaf package holding `LLMSpawner` and concrete adapters (Claude/Ollama/OpenAI/custom command). Extracted from `pipeline/` to break upward edges (`refine/ → pipeline`, `api/adapters → pipeline`). `stage_handlers.go::Execute` dispatches via `llmadapter.NewLLMSpawnerFromSpawner()` when `spawner.AdapterType` is non-empty. See [ADR-0005](../docs/architecture/adr/0005-llmadapter-leaf.md).

## Runner Slots

`maxParallelOrchestrators` (pipeline_config key, default 3) is the **global** cap on concurrently-driven tasks — it applies to every agent-driven stage, not just `implementation`. Agent-less stages (`backlog`, `ready`) bypass the cap. See ADR-0002 for the pickup priority order (silver-bullet → furthest stage → priority → rank → createdAt) and the sticky-run invariant. `rank` is the manual drag-and-drop order set via the Kanban board (`POST /api/tasks/{id}/rank`), a gap-based float seeded from createdAt; it breaks ties within the same priority tier.

## Stage Timeout

`stageTimeoutSeconds` (pipeline_config key, default 1800) is enforced by the orchestrator's tick loop in `progress_guards.go`. If a stage agent's PID is still alive after this many seconds, the orchestrator sends SIGTERM and applies a `fail` transition, freeing its runner slot. Set to `0` to disable. Written via `UPDATE pipeline_config SET value='3600' WHERE key='stageTimeoutSeconds'` (or INSERT if absent).

## Auto-Requeue (Self-Healing)

Infra-class stage-run failures are automatically requeued instead of parking on `needsUser`.

**Failure classification:**

| Class | Examples | Action |
|---|---|---|
| `Infra` | process killed, no session JSONL, unparseable output, quota/session-limit error | auto-requeue |
| Schema | stage output fails schema validation | iterate → wait_user (existing path, unchanged) |
| Hard (budget exhausted) | `retry_count >= maxAutoRetries`, or `rate_limit_retry_count >= maxRateLimitRetries` | hard `failed` → needsUser |

**Stage-run statuses `requeued` / `rate_limited`:** the run is updated in place (same row, no new iteration) with a `next_retry_at` cooldown timestamp. A rate-limit error (429/529/503) parks the run as `rate_limited`, increments `rate_limit_retry_count` and uses the `rateLimitBackoffSeconds` backoff and the `maxRateLimitRetries` budget; any other infra failure parks it as `requeued` and increments `retry_count` against `maxAutoRetries`. The two counters are independent, so rate-limit pauses never spend the infra budget. Both are non-blocking — `needsUser` excludes them — and both are listed once in `cooldownStageRunStatuses` (`stage_run_service.go`).

**Lifecycle:**

```
running
  │ infra fail, retry_count < maxAutoRetries
  ▼
requeued  ──(cooldown elapsed, sweep promotes)──►  pending  ──(picker)──►  running
  │
  │ retry_count >= maxAutoRetries
  ▼
failed  →  needsUser
```

**Tick order (required):** finalize completed runs → awaiting-user sweep → orphan sweep → requeue sweep (`sweepRequeueableRuns`) → picker. The requeue sweep promotes cooldown-elapsed `requeued` / `rate_limited` runs back to `pending`, clearing `started_at`/`pid`/`next_retry_at`/`output` (keeping `retry_count`). Clearing `output` matters: `DetectCompletion` treats a non-empty output as a tool-written stage result, so leftover requeue bookkeeping would otherwise complete the respawned run and advance the task. The picker skips both statuses during cooldown.

**Config keys:**

| Key | Default | Description |
|---|---|---|
| `maxAutoRetries` | `3` | Max infra-retries before hard fail |
| `retryBackoffSeconds` | `60` | Linear backoff; `next_retry_at = now + retryBackoffSeconds × attempt` |
| `rateLimitBackoffSeconds` | `600` | Backoff for rate-limit errors (API quota, usage limit) |
| `maxRateLimitRetries` | `36` | Max rate-limit retries before hard fail |

All keys are readable via `GET /api/pipeline/config` and writable via `PUT /api/pipeline/config`. Validation enforces sensible bounds.

**UI:** a distinct "Retrying N/max · next retry in Xs" chip is shown while a run is in `requeued` or `rate_limited` status.

## Spawner Resolution

`server/internal/services/spawner_resolver.go` resolves the effective spawner for a stage agent using the following precedence:

```
task.spawner_id ?? task.project.default_spawner_id ?? spawner WHERE is_default ?? slug claude-default
```

Resolution happens once, immediately before `exec`, inside `server/internal/pipeline/spawner.go::spawnStageAgent()`. The deployment-wide default is the row flagged `is_default` (exactly one at a time, enforced by `SpawnerRepo.SetDefault` in a clear-all-then-set-one transaction; the `claude-default` slug is the ultimate backstop if no row is flagged). The same `is_default`-then-slug fallback is mirrored in `server/internal/cmdscope/request_scope.go` for the config-enumeration scope.

`claude-default` is seeded as the initial default and stays an un-deletable backstop. **Built-in spawners are editable** (name/command/args/env/model/description) but their **slug is immutable** and they **cannot be deleted**; the current default also cannot be deleted (`ErrSpawnerIsDefault`) — pick another default first. Mark a different spawner default via `POST /api/spawners/{id}/default`.

**Env-merge order:** custom-spawner `env` is applied first; dashboard vars (`DASHBOARD_*`, `CLAUDE_*`) are overlaid afterward and always win. `DASHBOARD_JWT_SECRET` and `DASHBOARD_HOOKS_SECRET` are never forwarded to spawned agents.

**Command allow-list:** the `spawners.command` field is validated by `services.ValidateSpawnerCommand` in `server/internal/services/spawn_policy.go` at create/update time and again on the agent spawn path — one authority, no cross-package cycles. Bare names (`claude`, `claude-code`, `npx`) are allow-listed; absolute paths must `EvalSymlinks`-resolve and sit under a trusted bin dir (`/usr/bin`, `/bin`, `/usr/local/bin`, `/opt/homebrew/bin`, `~/.local/bin`, the resolved `claude` dir). Resolving symlinks before the trust check closes the symlink-into-`/tmp` bypass. Extend both sets via `DASHBOARD_SPAWNER_ALLOWED_COMMANDS` (comma-separated — bare names extend the name list, absolute paths add trusted dirs). CRUD requires the `keys:manage` MCP scope.

**Adapter dispatch:** the resolved Spawner row also carries an `adapter_type` field. `server/internal/pipeline/stage_handlers.go::agentStageHandler.Execute()` reads it and dispatches accordingly: `claude` (or empty) → native subprocess via `SpawnStageAgent`; any other value → adapter built via `llmadapter.NewLLMSpawnerFromSpawner(row)` followed by `LLMSpawner.Spawn(...)`. Custom rows require a non-empty `command` column.

See [ADR-0003](../docs/architecture/adr/0003-pluggable-spawners.md) for full rationale, security model, and alternatives considered.

## Awaiting-User Sweep

`sweepAwaitingUserRuns()` in `server/internal/pipeline/sweeps.go` reaps `awaiting_user` stage_runs whose agent process has died or whose wallclock time has exceeded the configured limit.

`awaitingUserTimeoutSeconds` (pipeline_config key, default 14400 = 4h) governs the sweep. Two zombie modes are reaped: (1) any `awaiting_user` stage_run whose PID is gone is immediately marked `failed` (stage timeout itself only inspects status='running' runs, so without this sweep dead-PID awaits would stay zombie forever); (2) any `awaiting_user` run whose PID is still alive but whose `started_at` is older than this limit is SIGTERMed and failed — defends against agents that busy-wait via shell polling loops (`until [ -e /tmp/x ]; do sleep N; done`) instead of yielding to the `request_permission` MCP gate. Set to `0` to disable the wallclock branch (the dead-PID branch always runs).

## Orphan Sweep

`sweepOrphanRuns()` in `server/internal/pipeline/sweeps.go` runs every tick after the awaiting-user sweep and reaps four zombie modes that the status-specific sweepers leave behind: (1) any non-terminal stage_run (any of `pending`/`running`/`awaiting_user`/`on_hold`) whose parent task is parked at `done`/`cancelled`/`on_hold` — covers cancel-cascade and on_hold-cascade leaks where the dependency cascade flips `task.currentStage` without touching the in-flight stage_run; SIGTERMs any live PID and marks failed; (2) any `on_hold` stage_run with a dead PID; (3) any `pending` stage_run that has not been promoted to `running` within 5 minutes (orchestrator-crash escape hatch — healthy pendings transition within milliseconds); (4) any `running` stage_run whose `started_at` predates this orchestrator process — recovers a run left behind by a crashed orchestrator. The verdict comes from `DecideRecovery`, the same authority `recoverRunningStageRuns` uses at startup: a live PID is left alone, a run with a `session_id` is re-queued to `pending` so it can be respawned with `--resume`, and only a run with nothing to resume from is failed. The sweep covers what the startup pass structurally cannot — a run whose process was still alive during recovery and died afterwards. The process-lifetime guard is load-bearing: an HTTP-adapter stage legitimately runs with no local PID (`AsyncRunningTransition{PID: 0}`), so a nil PID alone must never be treated as evidence of a zombie. Combined with the awaiting-user sweep, every non-terminal stage_run status across every agent-driven stage now has a watchdog branch — the sweep is stage-agnostic by construction (filters on stage_run.status, not stage value).

## Multi-Spawn Re-entry Guard

`runProgressTaskLocked()` in `server/internal/pipeline/progress_guards.go` checks after `ensureStageRun` whether the returned stage_run is already `status='running'` with a live PID, and if so returns it without invoking `handler.Execute()` again. This blocks the user-grants-many-permissions cascade where the resolve route's conditional (shouldRestartRun=false because the resolved run is already failed from a prior grant's kill+restart) calls `progressTask()` and would otherwise spawn another claude process on top of the running one. The guard also leaves `started_at` untouched on re-entry so the awaiting-user / stage timeout windows continue to count from the original spawn.

## Lingering-Pending Gate

`runProgressTaskLocked()` checks **BEFORE** `ensureStageRun` whether the most recent stage_run on the current stage either (a) is terminal (`done`/`failed`) OR (b) is `awaiting_user` with a dead PID (zombie awaiting that `sweepAwaitingUserRuns` will reap on its next tick), AND still has unresolved `permission_request` rows attached. If so it returns null without spawning. Defense-in-depth against the same cascade as the re-entry guard, but from the opposite direction: re-entry guard catches a 2nd-spawn-on-top-of-running run; this gate prevents a 2nd spawn after the first run was killed-and-failed while sibling pendings were still waiting on the dead run, AND closes the synchronous-resolve-vs-tick-driven-sweep race for zombie awaiting_user runs. Surfaced to the UI as `task.blockedByPendingPermissions` (computed in `api/tasks/enrich.go`) so the kanban card and task modal show why the task is parked instead of looking generically "stuck".

## DB-Level Invariant: One Running Stage_run per Task (V7)

`migrateV7OneRunningRunPerTask` adds a partial unique index `idx_stage_runs_one_running ON stage_runs(task_id) WHERE status = 'running'`. Defense-in-depth catching multi-spawn cascades at the DB level if a future code path bypasses the runtime re-entry guard or the per-task lock. SQLite throws `SQLITE_CONSTRAINT_UNIQUE` instead of letting two parallel agents burn tokens on the same task. Pre-flight check in the migration aborts loudly (with the offending task IDs in the message) if a legacy DB already contains duplicate running rows. Iterate-flow is safe by construction: OLD → done is committed BEFORE NEW → pending is created, and NEW only flips to running on the next `ProgressTask()` call when OLD is no longer running.

## Schema Validation + Retry

Every agent-driven stage's output is validated against a strict per-stage schema in `completion_detector.go::ValidateStageOutput()`. A schema mismatch on the first iteration → `IterateTransition` with the validation error and the rejected payload fed back into the next prompt; a second failure → `WaitUserTransition`. Hard failures (no session, no parseable JSON) → `FailTransition` immediately without retry.

## Layer Direction (No Upward Imports)

```
cmd/serve/main.go + di.go   ← composition root only
        │
        ├── api/tasks/                may import: db/repo, pipeline (ProgressOpts + allowlist), services
        ├── mcp/tools/                may import: db/repo, pipeline (ProgressOpts + allowlist), services
        ├── pipeline/                 may import: db/repo, db/ent, services, llmadapter
        ├── services/                 may import: db/repo, db/ent, auth, paths, platform
        ├── llmadapter/               may import: db/ent only (leaf)
        ├── proc/                     may import: stdlib + platform only (leaf)
        ├── db/repo/                  may import: db/ent only
        └── plugin/                   may import: auth only
```

`proc/` holds `IsPidAlive()` (process-liveness probe), extracted from `pipeline/` (see [ADR-0009](../docs/architecture/adr/0009-proc-leaf.md)). It depends only on stdlib and `platform/`.

`llmadapter/` is a leaf package holding pluggable-spawner transports (`LLMSpawner`, `StreamingLLMSpawner`, `LLMSpawnArgs`, `NewLLMSpawnerFromSpawner`, `AvailableAdapters`). It was extracted from `pipeline/` (see [ADR-0005](../docs/architecture/adr/0005-llmadapter-leaf.md)) to delete upward edges (`refine/ → pipeline`, `api/adapters → pipeline`). `pipeline/stage_handlers.go` uses the factory as a normal high-to-low import.

`services/` hosts stateless helpers (worktree manager, spawner resolver, resource recommender, approval utils) that may be composed by routes and the pipeline, but must never reach back into those layers at runtime. Type-only imports from `pipeline/` are permitted so a service can express its inputs/outputs in pipeline terms.

### Runtime import whitelist for routes (api/*) and mcp/*

The following `pipeline/` symbols may be imported at runtime from `api/*` and `mcp/*`. All other `pipeline/` imports must be type-only:

| Symbol | File in pipeline/ | Consumers |
|---|---|---|
| `ProgressOpts` | `types.go` | `api/tasks/handler.go`, `mcp/tools/control.go` |
| `ResolvedProjectDir` | `session_reader.go` | `api/tasks/analyze_routes.go` |
| `FindNewestSessionID` | `session_reader.go` | `api/tasks/cost_stage_routes.go` |
| `ReadLastStageJsonOutput` | `session_reader.go` | `api/tasks/cost_stage_routes.go` |
| `SessionFileExists` | `session_reader.go` | `api/tasks/handler.go` |
| `ValidateStageOutput` | `completion_detector.go` | `api/agents/channel_stage_output.go` |
| `StageOutputSubmittedKey` | `completion_detector.go` | `api/agents/channel_stage_output.go` (writes the marker), `api/plan/service.go` (strips it from the live plan) |

These are session-reader and process-monitor helpers — they do not touch the state machine (orchestrator, stage handlers, completion detector). New `pipeline/` imports from `api/*` or `mcp/*` require an explicit justification in this table before being added.

`ValidateStageOutput` is a pure schema validator with no state-machine touch — used by the channel-stage-output ingress handler (`set_stage_output` MCP tool) to validate agent-submitted output synchronously, so a live agent gets a 422 and can correct without a kill-restart.

Never import from `pipeline/` in `db/`, `plugin/`, or `proc/` packages.

## Stage-Run Credential Lifecycle

Every stage run gets its own short-lived MCP key (`mcp.StageKeyIssuer.Issue`, wired at
`serverapp/di_pipeline.go`) so an MCP call can be attributed to the task and routine that made it.
The key must be handed back the moment its agent is gone.

`stageRunService.releaseStageRun(ctx, stageRunID)` is the single seam that does it, and it does two
things: revoke the run's keys, and run the cleanup closure `SpawnStageAgent` returned (which deletes
the temp `--mcp-config` file holding the token, and the settings allow-list entries).

It is reached two ways:

- **Automatically** on any `stageRunService.Update` whose status is in `terminalStageRunStatuses`
  (`done`, `failed`, `cancelled`). `awaiting_user` is deliberately absent — such a run is resumable
  and its agent may still be alive; `expires_at` caps it instead.
- **Explicitly**, as a post-commit hook in `applyTransitionWrites` for arms that end a run inside the
  transaction, and for `requeued`, which is not terminal but always follows a completion result.

Two rules follow, and both have been violated before:

1. **Writing a stage-run status through `repo.StageRunRepo` directly bypasses revocation.** Only
   `stageRunService` is wired to `RevokeTaskAPIKeys`. `sweeps.go` and `MarkPending` write via the
   repo on purpose (they set non-terminal statuses); anything ending a run must not.
2. **Ending an agent without ending its run leaks a live key until `expires_at`.** Task cancellation
   used to write the terminal stage only on the task row; `NotifyTaskTerminated` now calls
   `cancelOpenStageRuns` so both cancel routes (HTTP handler and the `task_cancel` MCP tool) end the
   run too.

`OnHoldTransition` has no producer anywhere in the tree — its `applyTransitionWrites` arm and the
`on_hold` stage-run status are unreachable. Do not add credential handling there expecting it to run.

## When Modifying the Pipeline

- **New stage transition?** Add it to `StageTransition` in `server/internal/pipeline/types.go`, then handle it in `PipelineOrchestrator.applyTransition()`. Wrap DB writes in a transaction via `o.opts.Client.Tx()`.
- **New side effect (metrics, tracing, a new callback)?** Add an optional callback to `OrchestratorOptions`, wire it in `cmd/serve/di.go`. Do **not** import the side-effect module from inside `pipeline/`.
- **New agent-driven stage handler?** Create a stage-specific prompt builder in `stage_prompts.go`, then call `newAgentStageHandler(stage, buildPromptFunc)` in `NewStageHandlers()`. The factory handles spawn, feedback-prefix injection (from `priorIterationOutput`), and `AsyncRunningTransition` return. Then add a per-stage schema validator in `completion_detector.go::ValidateStageOutput()` so the orchestrator's retry-then-escalate loop recognizes malformed output.
- **Custom completion routing** (e.g. a stage that loops back to an earlier stage on a specific output flag)? Edit `PipelineOrchestrator.decideCompletedTransition()` and return a `NextTransition`. If you need to mutate `task.metadata` as part of the transition, pass it via `MetadataPatch` on the `NextTransition` variant so the write lands inside `applyTransitionWrites()`'s transaction — never perform bare `UpdateTask()` calls in the completion path.
- **New path that ends an agent?** Route the stage-run status write through `stageRunService`, not
  `repo.StageRunRepo` — that is what releases the run's MCP credentials and its spawn artifacts. See
  *Stage-Run Credential Lifecycle* above.
- **New runner-picker priority tier?** Update the sorting logic in `scheduler.go` and the priority documentation. The memory captures user intent; code changes must stay consistent with it.
- **SSE event coverage for new transitions / mutation paths:** the kanban relies on `onTaskChanged` (fires after every successful `applyTransition()`) plus the route-level `broadcastEnrichedUpdate(taskId)` helper in `api/tasks/handler.go`. Both must produce **enriched** payloads (via `enrich.go::enrichTask()`), otherwise the cards lose `latestStageRunStatus` / `needsUser` and the run-status chip vanishes. When you add a new mutation endpoint, call `broadcastEnrichedUpdate(taskId)` — never raw `BroadcastTaskEvent()`. When you add a new transition kind to `applyTransition()`, the existing `onTaskChanged` call covers it automatically.

## Worktree Lifecycle

`server/internal/pipeline/worktree.go` manages git worktrees for tasks:

- `ensureTaskWorktree()` creates a worktree at `<worktreeRoot>/<slug>`, idempotent. When `task.SourceBranch` is nil, falls back to `feat/<slug>` as the branch name. Returns the worktree path and the branch name used.
- `removeTaskWorktree()` runs `git worktree remove --force` and prunes stale metadata. Non-fatal by design — called after terminal transitions.

Worktree paths are stored on `task.WorktreePath`. When set, the path overrides `task.Cwd` as the working directory for agent spawns, ensuring stages run within the isolated worktree context.
