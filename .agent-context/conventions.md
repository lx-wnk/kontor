# Key Conventions

- **Path alias:** `@/*` maps to `./src/*` (configured in `tsconfig.json` and `vite.config.ts`).
- **Network binding:** Server binds to `127.0.0.1` only — never expose to network (reads sensitive session data). **Multi-machine mode** (`DASHBOARD_REMOTES` env var) requires remote instances to be network-accessible; use a VPN or SSH tunnel — never bind to `0.0.0.0` on an untrusted network.
- **Dual persistence model:** Agent monitoring is filesystem-derived (no database); the task pipeline uses SQLite at `~/.claude/dashboard-tasks.db` (override via `DASHBOARD_DB_PATH`; see ADR-0001). One deliberate crossing: `merger.GetAgents` accepts an injected `merger.Enricher` callback (the `Enricher` field on `GetAgentsOpts`) that annotates each scanned agent with its linked pipeline task ID/title. The DB-backed implementation, `agentbroadcast.NewPipelineTaskEnricher` (`server/internal/agentbroadcast/enricher.go`), resolves `agent.SessionID → StageRunRepo.GetBySessionID → stageRun.TaskID → TaskRepo.GetByID → task.Title`. The crossing is one-way (pipeline → agent annotation only), read-only, and fail-soft (lookup errors leave the agent's fields empty without failing the scan). `merger` itself never imports `db/repo` — the enricher is built once in the composition root (`cmd/serve/di.go`) and threaded into every `GetAgents` call site: the SSE broadcast loop (`agentbroadcast.Run`), the router's request-scoped accessor (`api/router.go`, shared by `/api/agents`, hooks rescan, commands, config), and the admin spotlight search handler (`api/search`, injected via `search.NewHandler`). Adding a new `GetAgents` call site requires passing this same enricher so results stay symmetric.
- **Subagents discovery:** Read from `~/.claude/projects/{encoded_path}/{sessionId}/subagents/*.jsonl`.
- **Cost estimation:** Uses the `MODEL_PRICING` lookup table in `server/pricing.ts`.
- **Spawner env-merge precedence:** Custom-spawner `env` (from the `spawners` table) is injected into the process environment first. Dashboard-controlled vars (`DASHBOARD_*`, `CLAUDE_*`) are then overlaid and always win. `DASHBOARD_JWT_SECRET` and `DASHBOARD_HOOKS_SECRET` are never forwarded to spawned agents regardless of what a custom spawner's `env` map declares. See [ADR-0003](../docs/architecture/adr/0003-pluggable-spawners.md).
- **Platform support:** macOS and Linux. `server/systemMonitor.ts` uses `top` on macOS and `/proc/stat` on Linux for CPU; `server/processScanner.ts` uses `lsof` on macOS and `/proc/<pid>/cwd` on Linux. Windows is unsupported.
- **Live prompt injection into a running interactive session** needs real keyboard input on the session's pty — MCP has no inbound primitive (go-sdk `ss.Log` is gated behind a client `setLevel` Claude never sends; TIOCSTI is disabled by default on Linux ≥6.2). The session must therefore be started through `kontor live` (a session launched with bare `claude` is **not** injectable — the dashboard then resumes it as a new session, signalled with an amber ⤳). `live` sets `Agent.liveInjectable` and reuses the same dashboard delivery (`POST /api/agents/{pid}/message`). It is not required for anything else — monitoring, the pipeline, and agent→dashboard replies/permissions all work without it.

  `agent-dashboard live` auto-selects the transport and always loads the dashboard-channel MCP:

  | Transport | When selected | pty owner | Inject | Detach / reattach |
  |---|---|---|---|---|
  | Inside-tmux | `$TMUX` is set (already in tmux) | tmux server | `tmux send-keys` | ✅ survives disconnect, `tmux attach` to reattach |
  | New-tmux | tmux on PATH, not inside tmux | tmux server | `tmux send-keys` | ✅ survives disconnect |
  | PTY broker | tmux not available | `kontor ptyhost` (built in) | loopback HTTP → writes pty master | ❌ tied to the terminal (close / SSH drop ⇒ session ends) |

  **Typical usage:** `kontor live` with no flags. Pass `--yolo` to add `--dangerously-skip-permissions`; all other flags are forwarded to claude unchanged. Install tmux for long-lived use (detach/reattach resilience); the pty broker is the zero-install fallback. Use exactly one transport per session (two pty owners conflict). GNU screen (`screen -X stuff`) is an equivalent not currently implemented.

## Pipeline Env Vars

| Var                              | Purpose                                                                                                       |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `DASHBOARD_DB_PATH`              | SQLite path                                                                                                   |
| `DASHBOARD_WORKTREE_ROOT`        | Per-task git worktree root, default `~/.claude/dashboard-worktrees`                                           |
| `DASHBOARD_STAGE_RUN_ID`         | Injected into spawned stage agents for the channel bridge                                                     |
| `DASHBOARD_TASK_ID`              | Injected into spawned stage agents for the channel bridge                                                     |
| `DASHBOARD_MCP_TOKEN`            | Injected for MCP callback access                                                                              |
| `DASHBOARD_MCP_URL`              | Injected for MCP callback access                                                                              |
| `DASHBOARD_HOST`                 | Bind address, default `127.0.0.1`; non-loopback address causes boot failure unless `DASHBOARD_REMOTES_ENABLED=true` |
| `DASHBOARD_REMOTES_ENABLED`      | `true` or `false`, default `false`; opt-in to binding on a non-loopback address (use a VPN or SSH tunnel — never expose to an untrusted network) |
| `DASHBOARD_SSE_INTERVAL_MS`      | Agent SSE broadcast interval ms, default `3000`                                                               |
| `DASHBOARD_COST_SCAN_INTERVAL_MS` | Interval ms for the server-side cost-history scan that fills `agent_cost_trends` (read by the Cost Analytics view). Default `300000` (5 min). A scan also always runs once at boot. Set `<= 0` for boot-scan-only (no periodic loop) — note this does NOT fully disable scanning. The scan reads session JSONL across all `DASHBOARD_CLAUDE_CONFIG_DIRS` and providers; it never has agents write cost data. |
| `DASHBOARD_SPAWN_RATE_LIMIT`     | Max user-initiated spawns per window, default `5`; must be positive integer                                   |
| `DASHBOARD_SPAWN_RATE_WINDOW_MS` | Spawn rate-limit window ms, default `60000`; must be positive integer                                         |
| `DASHBOARD_INJECT_RATE_LIMIT`    | Max live message injections (`POST /api/agents/{pid}/message`) per user per window, default `30`; exceeding returns `429`. Non-positive falls back to default |
| `DASHBOARD_INJECT_RATE_WINDOW_MS` | Inject rate-limit window ms, default `60000`; non-positive falls back to default |
| `DASHBOARD_INJECT_TOKEN_ROTATE_MS` | Rotation interval ms for the pty-broker / channel-bridge discovery bearer token, default `300000` (5 min). Set `<= 0` to disable rotation. The previous token stays valid for one interval (grace window) so in-flight deliveries are not dropped |
| `DASHBOARD_ALLOW_GIT_PUSH`       | `true` or `false`, default `false`; when `true`, removes the global `git push` filter from spawned-agent allow-lists. Per-task override: `metadata.allowGitPush=true`. |
| `DASHBOARD_HOOKS_SECRET`         | Shared bearer token for all `/api/hooks/*` endpoints; always required — if unset, `config.Load` auto-generates and persists a secret to `~/.claude/dashboard-hooks-secret`. Set explicitly via env var or config file to keep the secret stable across restarts. |
| `DASHBOARD_HOOKS_DEBOUNCE_MS`    | Debounce window before SSE rescan after a hook event, default `100`ms |
| `DASHBOARD_ALLOW_GIT_PULL`       | `true` or `false`, default `false`; enables `POST /api/tasks/:id/git-action` with `action:'pull'` (git pull --ff-only on task worktree) |
| `DASHBOARD_CLAUDE_CONFIG_DIRS`   | Comma-separated list of Claude config directories to search for session JSONL files, e.g. `~/.claude-personal,~/.claude-work`. Searched before auto-detection. Useful when the dashboard process is not started with `CLAUDE_CONFIG_DIR` set, or on shared machines with multiple profiles. |
| `DASHBOARD_SPAWNER_ALLOWED_COMMANDS` | Comma-separated extension of the `spawners.command` allow-list enforced by `services.ValidateSpawnerCommand`. Bare entries (e.g. `my-claude-wrapper`) extend the permitted bare-name set; absolute entries (e.g. `/opt/company/bin`) add **trusted bin directories** under which an absolute command may resolve. Absolute commands must `EvalSymlinks`-resolve and sit under a trusted dir — the former "any path outside /tmp" rule (and its symlink-into-/tmp bypass) is gone. Optional. |

## Auto-Requeue Config Keys

`maxAutoRetries` (default 3) and `retryBackoffSeconds` (default 60) are pipeline_config keys that govern the self-healing auto-requeue for infra-class stage-run failures. Both are read/write via `GET|PUT /api/pipeline/config`. The `requeued` stage-run status represents a run in cooldown between retries; it is non-blocking (`needsUser` excludes it). Full lifecycle and classification rules: [task-pipeline.md — Auto-Requeue](task-pipeline.md#auto-requeue-self-healing).

## Legacy Adapter Migration (post-merge)

- `DASHBOARD_SPAWN_COMMAND` is deprecated. On first boot it is migrated to a Spawner row with `slug='imported-custom'` (`adapter_type='custom'`); after migration the env var has no runtime effect. Prefer creating Custom-adapter spawners via the UI (`/settings/spawners`) or `POST /api/spawners`.
- `adapter-config.json` migration is idempotent and only seeds rows that are missing by reserved slug: `imported-ollama`, `imported-openai`, `imported-custom`. Editing the JSON file post-migration has no effect — edit the spawner row instead (UI or `PATCH /api/spawners/:id`).
- The legacy `Adapters.Default` key has no equivalent in the new model. If present in `adapter-config.json` it surfaces as `slog.Warn` on boot; pick a per-project `default_spawner_id` (or per-task `spawner_id`) explicitly.

See [ADR-0003 section A](../docs/architecture/adr/0003-pluggable-spawners.md) for full rationale.

## Compaction Preservation

When compacting context, always preserve:

- List of modified/created files in this session
- Active test/lint commands and their last results
- Unfinished tasks and next steps
