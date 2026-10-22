# Per-Account Mail and Cloud Sessions — Design Spec

**Status:** approved 2026-10-22
**Scope:** everything Kontor connects to that does not run on this machine, separated by Claude account.
**Builds on:** `2026-09-16-mcp-applications-and-mail-design.md` (slice 1 merged), `server/internal/mcpapps/`.

## 1. Intent

Kontor monitors and drives local Claude Code agents. The user has two Claude subscriptions on this machine,
a private one (`~/.claude`, a symlink to `~/.claude-personal`) and a work one (`~/.claude-work`). Two things
outside the machine are missing:

1. **Mail.** Agents should read mail, but private mail must only ever be read by runs on the private
   subscription, work mail only by the work subscription.
2. **Cloud sessions** (Claude Code on the web, `claude.ai/code`). The user wants to start and steer them
   from Kontor, under the right subscription.

### Goals

- G1: A mail application can be restricted to a chosen set of spawners. A run whose spawner is not in that
  set never sees the mail server.
- G2: Kontor starts a cloud session under a spawner's account, sends follow-up messages into it, and keeps a
  registry of the sessions it knows, each with a link to claude.ai.
- G3: Kontor fires cloud routines (claude.ai scheduled tasks with an API trigger) and records the session
  each fire creates. This part ships as a **Kontor preview feature**, behind a new global preview switch.
- G4: Per account, Kontor shows whether the `claude` CLI is signed in in a way that cloud sessions accept.

### Non-goals

- A live, account-wide list of cloud sessions, their status, or their replies. No supported API exists
  (see §2.2). Kontor shows what it knows and links out.
- Stopping, archiving or deleting cloud sessions from Kontor. claude.ai only.
- Outlook / Microsoft 365 mail (needs OAuth; deferred in the 2026-09-16 spec §6.2, unchanged).
- A first-class "account" entity. An account stays what it is today: the `CLAUDE_CONFIG_DIR` in a spawner's
  `env` (decided with the user, §3.1).
- Firing routines from inside a pipeline stage. First version: from the UI only.

### Anti-goals (would make this a failure even if it works)

- Kontor reading, storing or forwarding the user's claude.ai OAuth token, or calling undocumented
  claude.ai endpoints. The Consumer Terms (§3.7, automated access) and
  `code.claude.com/docs/en/legal-and-compliance` ("may not … intermediate Claude.ai credentials or session
  tokens") forbid it, and enforcement would hit both subscriptions. Kontor only runs the unmodified `claude`
  binary and the documented routine fire endpoint.
- A private mailbox reachable from a work run because of a default.
- Breaking existing applications (`obsidian` with `attach_all`, `jetbrains`) by the new restriction.

## 2. Current State (verified 2026-10-21/22)

### 2.1 Accounts and spawners

- No account entity. `server/internal/db/ent/schema/spawner.go:18-44` has no account field; the account is
  `env["CLAUDE_CONFIG_DIR"]` (line 23), absent meaning the server default (`~/.claude`).
- Live spawners: `Claude (default)` and `Custom (imported)` (no `CLAUDE_CONFIG_DIR`, so private) and
  `Claude Work` (`~/.claude-work`).
- Spawn env is built by `BuildSpawnEnv`, `server/internal/pipeline/spawner.go:459-473` (expands `~`).

### 2.2 Cloud sessions: what the CLI supports (CLI 2.1.307)

Source: `https://code.claude.com/docs/en/claude-code-on-the-web`, read 2026-10-22.

| Action | Supported | How |
|---|---|---|
| Start | yes | `claude --cloud "<task>"` from a git checkout; clones the GitHub remote at the current branch |
| Send follow-up | yes | `claude -p "<msg>" --cloud <session_id\|cse_…\|claude.ai/code URL> --output-format json` → `{ok, session_id, url}` or `{ok:false, session_id, error}`; posts and exits, no reply |
| List, stop, archive, delete | no | claude.ai UI only |
| Read replies | no | transcript on claude.ai |
| Auth | per config dir | needs `claude auth login` (claude.ai sign-in); API keys, Bedrock, Vertex are refused |

Routines: `POST https://api.anthropic.com/v1/claude_code/routines/<trig_id>/fire` with
`Authorization: Bearer <routine token>`, `anthropic-beta: experimental-cc-routine-2026-04-01`,
`anthropic-version: 2023-06-01`, optional body `{"text": "…"}`; returns
`{claude_code_session_id, claude_code_session_url}`. Token created in the claude.ai UI. Research preview.
Limits 30 fires/h per routine, 100/h per account. Source: `https://code.claude.com/docs/en/routines`.

### 2.3 Mail

- Mail runs through MCP applications (`server/internal/mcpapps/`), preset
  `server/internal/mcpapps/presets/imap-mcp-server.json` for `imap-mcp-server@2.0.0`; secrets per mail
  account as `IMAP_MCP_ACCOUNT_{ACCOUNT}_IMAP_PASSWORD` / `_SMTP_PASSWORD`, encrypted in
  `application_secret` (secretbox).
- An application reaches a run when the task lists it (`task.applications`, copied from the routine,
  `server/internal/api/tasks/handler.go:396-399`) or when it has `attach_all`.
- The single decision point is `mcpapps.Resolver.ResolveRun` (`server/internal/mcpapps/resolve.go:47`).
  It ignores the task's spawner today.
- No mail application is configured on the live instance (`/api/applications`: `jetbrains`, `obsidian`).

### 2.4 Known defect that touches "per account" (separate fix, prerequisite)

`~/.claude` and `~/.claude-personal` are counted as two accounts: `/api/usage` returns both with identical
totals (7 d: 845,965,944 tokens). Config dirs are deduplicated by path string only
(`server/internal/parser/parser.go:145-150`), and `planusage` keys (`server/internal/planusage/store.go:44`)
do not resolve symlinks either. Fix: `filepath.EvalSymlinks` before comparing. Ships as its own PR before
this work, because every per-account view in this spec would show the duplicate.

## 3. Design

### 3.1 Mail restricted to spawners

**Decided with the user:** the restriction is per spawner, not per account. If a spawner's
`CLAUDE_CONFIG_DIR` is later changed, the restriction follows the spawner, not the account. That is the
user's maintenance responsibility; Kontor does not snapshot or re-check the config dir.

- New field on `mcp_application`: `allowed_spawners` (JSON list of spawner IDs), default empty.
- **Empty means unrestricted.** This keeps `obsidian` and `jetbrains` working unchanged.
- **The gate is per stage, on the spawner that stage actually runs on.** The spawner is resolved per task
  *and stage* in five tiers (`server/internal/services/spawner_resolver.go:41-55`: task, project stage,
  project default, global stage, default), so two stages of one task can run on different accounts.
  `ResolveRun` takes the resolved spawner as a parameter; the stage handler already has it when it calls
  `ResolveApplications` (`server/internal/pipeline/stage_handlers.go:203`). For each application with a
  non-empty `allowed_spawners` that does not contain that spawner:
  - attached via `attach_all`: skipped, with a log line naming application, task, stage and spawner;
  - attached explicitly (task lists it): the stage fails before spawning with a typed error
    (`SpawnerNotAllowedError{Server, Spawner, Stage}`), shown like `MissingSecretError` today.
- **Up-front check (builder call):** when a task with explicitly attached restricted applications is
  started, Kontor resolves the spawner of every agent stage once and rejects the start if any stage would
  fail, so a task never spends stages 1–2 and dies at stage 3. The per-stage gate stays as the real guard
  (config can change between start and stage).
- A spawner ID in the list that no longer exists is ignored (it cannot match).
- **Export (builder call):** `export_to_claude` writes the server into one config dir's `.claude.json`
  (`server/internal/claudeconfig/claudeconfig.go:81-97`), where every plain session of that account sees
  it regardless of `allowed_spawners`. Export is therefore refused (400) while `allowed_spawners` is
  non-empty, and setting `allowed_spawners` on an exported application is refused until export is off.
- UI (`ApplicationSettings.vue`): a multi-select of spawners, each shown with its account
  (`Claude Work (~/.claude-work)`, `Claude (default) (~/.claude)`). An imap application with an empty list
  shows a warning: "Any spawner can read this mailbox."
- When attaching an application to a routine or task whose spawner is not allowed, the UI warns at
  attach time; the server-side check in `ResolveRun` is the actual gate.
- **Mailbox setup:** one imap application per trust boundary, e.g. `mail-private` (iCloud, private Gmail)
  and `mail-work`, each with its own `allowed_spawners`. The restriction is per application, not per mail
  account inside one application. README documents this.
- **Separate account stores per application (builder call).** `imap-mcp-server@2.0.0` keeps its account
  list and stored passwords in `os.homedir()/.imap-mcp/accounts.json` with the key in `~/.imap-mcp/.key`
  (`dist/index.js:1586`, `:1856`); env passwords are only an override. Two applications sharing `HOME`
  would share every account. Kontor therefore sets a per-application `HOME` in the rendered server env
  (a directory under Kontor's data dir, `0700`, named by the application's resource ID), and points
  `npm_config_cache` back at the real npm cache so `npx` does not re-download per application. Account
  setup for a mail application runs with that same `HOME`. The preset's attachment download dir
  (`IMAP_DOWNLOAD_DIR`, default under `HOME`) follows along.
- **Threat model.** The restriction prevents a mailbox reaching the wrong account *by configuration or
  default*. It does not stop an agent with shell access on this OS user from reading another
  application's `HOME` directory; that needs separate OS users and is out of scope, consistent with the
  local-trust stance of the 2026-09-16 spec.
- Migration: additive column. No down path: `Schema.Create` runs without `WithDropColumn`
  (`server/internal/db/client.go:151`), older binaries ignore the column, so a rollback is harmless (same
  reasoning as `project.vault_folder`).

### 3.2 Cloud sessions (stable)

New entity `cloud_session`:

| Field | Notes |
|---|---|
| `id` | Kontor ID |
| `session_id` | `session_…` or `cse_…`, unique |
| `url` | `https://claude.ai/code/<session_id>` |
| `spawner_id` | which spawner's account it runs under (the account handle, same as §3.1) |
| `title` | user-given or the start description |
| `origin` | `started` \| `imported` \| `routine` |
| `last_message`, `last_message_at` | last message Kontor sent (trimmed for display) |
| `last_error` | last failed send or start, verbatim from the CLI JSON |
| `created_at`, `updated_at` | |

API (all write routes behind the existing Origin/CSRF check):

- `POST /api/cloud-sessions` `{spawnerId, cwd, description}`: start. Runs `claude --cloud "<description>"`
  in `cwd` (a project folder) with the spawner's env, non-interactively, and records the returned session ID.
- `POST /api/cloud-sessions/import` `{spawnerId, url}`: accept a claude.ai/code URL or bare ID; validate the
  ID shape; no network call.
- `POST /api/cloud-sessions/{id}/messages` `{text}`: runs `claude -p --cloud <session_id>
  --output-format json` with the text on stdin; stores the result in `last_message*` / `last_error`.
  (Stdin with `-p --cloud` is documented in the "Send follow-ups from the CLI" section.)
- `DELETE /api/cloud-sessions/{id}`: removes it from Kontor only. The UI says so.
- `GET /api/cloud-sessions`, plus SSE events like the other resources.

Process rules for every `claude` call:

- **Binary (builder call):** always a bare `claude` resolved from `PATH`, never `spawner.command` or
  `spawner.args`. Spawner commands can be `npx …` or carry local flags (permission mode etc.) that do not
  belong on `--cloud`. Only spawners with `adapter_type = claude` are offered as cloud accounts; Codex and
  Gemini spawners do not appear in the cloud or sign-in views.
- Env built through `BuildSpawnEnv` from the spawner's env, so `CLAUDE_CONFIG_DIR` selects the account.
- Scrub from the child env: `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_USE_BEDROCK`,
  `CLAUDE_CODE_USE_VERTEX` (and any other provider switch the CLI docs list). Cloud sessions refuse them.
- Follow-up text goes on stdin (documented: `echo "msg" | claude -p --cloud <session-id>`), so message
  content stays out of `ps`. The start description goes in argv (the documented form); a description
  starting with `-` is rejected so it cannot be read as a flag.
- Timeout per call; exit code and stderr are surfaced in `last_error` (config errors print to stderr
  without JSON, per the docs).
- The command must pass the existing `spawn.allowedCommands` policy.

UI: a "Cloud sessions" view: list grouped by account, each row with title, origin, last message, error
badge, "Open in claude.ai" link, a send box, and "Remove from Kontor". "Start" form: spawner, project
folder, description. "Import" form: URL.

### 3.3 Account sign-in health (stable)

Per distinct config dir among the spawners: `claude auth status --json` (with the same scrubbed env), shown
next to the account. A cloud action on an account whose `authMethod` is not the claude.ai sign-in is
refused before running, with the hint `claude auth login` (run with that `CLAUDE_CONFIG_DIR`). Cached for a
short interval; refreshed on demand.

### 3.4 Routines (Kontor preview)

New entity `cloud_routine`: `name`, `fire_url`, `spawner_id` (account label only; the fire endpoint does
not use the CLI), token in secretbox (write-only, like application secrets).

- `fire_url` must match exactly `https://api.anthropic.com/v1/claude_code/routines/trig_<id>/fire`. No other
  host is accepted (this is a server-side outbound call; the strict match is the SSRF guard).
- `POST /api/cloud-routines/{id}/fire` `{text?}`: sends the documented request; on success creates a
  `cloud_session` row with `origin=routine` from `claude_code_session_id` / `claude_code_session_url`.
  HTTP 429 and other errors are shown verbatim; Kontor does not count fires itself.
- The beta header value lives in one constant.

### 3.5 Preview switch

There is no preview mechanism in Kontor today (only a cosmetic label at
`src/features/settings/components/SystemPromptSettings.vue:175`).

- New setting `features.preview` (bool, default `false`) in `server/internal/settings/registry.go`.
- When off, the routine routes return 404 and the UI hides routines. Stored routines stay in the DB.
- Everything behind the switch carries a "Preview" badge. Later preview features reuse the same switch.

## 4. Error handling summary

| Case | Behaviour |
|---|---|
| Spawner not allowed, explicit attach | run fails before spawn, typed error on the task |
| Spawner not allowed, `attach_all` | application skipped, logged |
| CLI not signed in with claude.ai | action refused, hint shown |
| CLI returns `{ok:false}` | stored in `last_error`, shown on the row |
| Session archived / not found | same as above (CLI error text) |
| Routine fire non-2xx | error shown, no session row |
| Preview off | routine routes 404 |

## 5. Verification

- Unit: `ResolveRun` with allowed / not allowed / empty list / stale ID, for both attach paths, and two
  stages of one task on different spawners. Up-front start check rejects a task whose later stage would
  fail. Export refused while restricted. Two mail applications get different `HOME` values.
  **Mutation tests required** (permission path): removing the spawner check must fail a test.
- Unit: env scrub and stdin-only message passing for the cloud CLI runner (fake `claude` binary on PATH
  that records argv, env and stdin and prints canned JSON).
- Unit: `fire_url` validator (accepts the exact shape, rejects other hosts, paths, schemes, userinfo).
- Integration: cloud-session routes against the fake binary; routine fire against an `httptest` server
  injected through the client (the URL validator stays strict in production).
- Acceptance on the live instance:
  1. a `mail-private` application allowed only for `Claude (default)`: a task on `Claude Work` with it
     attached fails with the typed error; on `Claude (default)` it lists mailboxes;
  2. import an existing cloud session URL, send a message, see it arrive on claude.ai;
  3. start a cloud session from a project folder;
  4. with preview on, fire a routine and see the new session row.
- Gates: `pnpm lint && pnpm typecheck && pnpm test`, `task test`, `go vet ./...` per module, `task lint`,
  `task changelog:check`. Docs: README, CHANGELOG, CONTRIBUTING, PRIVACY (Kontor now calls
  api.anthropic.com for routines and stores routine tokens).

## 6. Delivery slices

0. Fix: symlinked config dirs counted twice (§2.4). Own PR.
1. Mail restricted to spawners (§3.1).
2. Cloud sessions: entity, CLI runner, import + send, auth health, UI (§3.2, §3.3).
3. Start from Kontor (§3.2 start route), after the probe in §7.
4. Preview switch + routines (§3.4, §3.5).

## 7. Open for the first plan step

- **Non-interactive start (HYPOTHESIS).** The docs show `claude --cloud "<task>"` as interactive and print
  the session ID only for follow-ups. Probe once in a throwaway GitHub repo:
  `claude -p --cloud "echo hi" --output-format json`. If it returns the session ID, use it. If not, slice 3
  falls back to "start on claude.ai, then import", and the plan says so.
- Whether `claude auth status --json` exposes `authMethod` in a stable shape on 2.1.307 (cheapest check:
  run it once per config dir and record the fields).
