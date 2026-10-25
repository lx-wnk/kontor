# Per-Account Mail and Cloud Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restrict mail applications to chosen spawners, and let Kontor import, message, start and (as a preview) fire cloud sessions under the right Claude account.

**Architecture:** Slice 1 adds `allowed_spawners` to `mcp_application` and gates it in `mcpapps.Resolver.ResolveRun`, which gains the stage's resolved spawner. Slices 2–3 add a `cloud_session` entity, a `cloud` package that runs the unmodified `claude` CLI with a spawner's env, and a REST + SSE resource with a Vue view. Slice 4 adds the `features.preview` setting and a `cloud_routine` entity that calls the documented routine fire endpoint.

**Tech Stack:** Go 1.26 (chi, ent, modernc/sqlite), Vue 3 + TypeScript (Vite, Vitest).

**Spec:** `docs/superpowers/specs/2026-10-22-per-account-mail-and-cloud-sessions-design.md`

## Global Constraints

- Every artifact is English (code, comments, commits, PR text, docs).
- The server binds `127.0.0.1` only. All new write routes are mounted inside the protected group at `server/internal/api/router.go:307` so `RequireSameOriginForMutations` (`server/internal/api/middleware.go:69`) covers them.
- Kontor never reads, stores or forwards a claude.ai OAuth token and never calls an undocumented claude.ai endpoint. The CLI runner always execs a bare `claude` from `PATH`, never `spawner.command` / `spawner.args`.
- Only spawners with `adapter_type == "claude"` are cloud accounts.
- An account is `spawner.env["CLAUDE_CONFIG_DIR"]` (absent = server default dir). No account entity.
- Each slice is one PR to `main`, in order 1, 2, 3, 4. The RED test of each task is committed separately, before the code that turns it green.
- Schema changes are additive ent columns/tables. No down migration: `Schema.Create` runs without `WithDropColumn` (`server/internal/db/client.go:151`); every PR body states this.
- Ent codegen: `GOTOOLCHAIN=go$(cat .go-version) go test -tags entgen ./server/internal/db/entgen/ -count=1` from repo root. Commit only generated files belonging to the change.
- Gates per PR, raw output pasted: `pnpm lint && pnpm typecheck && pnpm test`; `task test`; `go vet ./...` in `server` and `sdk`; `task lint`; `task changelog:check`. Route changes also need `go test ./internal/api -run TestRouteGolden -update-golden` (from `server/`), and the new dep added to `buildBypassRouter` (`server/internal/api/bypass_auth_smoke_test.go:72`).
- Docs in the same PR: `CHANGELOG.md` under `[Unreleased]` (append to existing `### Added` / `### Fixed`, never a second heading), `README.md`, and where named per task `docs/guides/security.md`, `docs/guides/configuration.md`, `PRIVACY.md`.
- Spec correction: the export function is `claudeconfig.WriteServerEntry` (`server/internal/claudeconfig/claudeconfig.go:132`), not `:81-97`.

## Review Focus

1. **Every listed spawner was deleted.** `allowed_spawners = ["gone"]` must deny every spawner, not fall back to unrestricted. "Empty means unrestricted" applies to the stored list only. Test in Task 2.
2. **Spawner resolution failed for the stage** (`resolved == nil`). A restricted application must be denied (fail closed). Test in Task 2.
3. **Message text that looks like a flag or carries newlines** (`"--help\nrm -rf"`). It must reach `claude` on stdin byte for byte and never appear in argv. Test in Task 8.
4. **`claude` missing or hanging.** The send returns 502 within the timeout, and `last_error` holds the reason. It must not hang the request or return 500. Test in Task 8 (runner) and Task 10 (502).
5. **The same session imported twice.** The second import returns 409, not 500. Test in Task 10.

---

## Slice 1 — Mail restricted to spawners (PR 1)

### Task 1: `allowed_spawners` column and repo setter

**Files:**
- Modify: `server/internal/db/ent/schema/mcp_application.go` (fields at 22-50)
- Modify: `server/internal/db/repo/mcp_application_repo.go` (interface at 25-35)
- Generated: `server/internal/db/ent/...`
- Test: `server/internal/db/repo/mcp_application_repo_test.go`

**Interfaces:**
- Produces: ent field `AllowedSpawners []string`; `SetAllowedSpawners(ctx context.Context, resourceID string, ids []string) (*ent.MCPApplication, error)` on `repo.MCPApplicationRepo`.

- [ ] **Step 1: Write the failing test** `TestMCPApplicationRepo_SetAllowedSpawnersRoundTrip`. Upsert an app, assert `AllowedSpawners` is an empty slice (not nil). Then set `["sp-a","sp-b"]` and assert it reads back. Then set `[]` and assert it is empty again.
- [ ] **Step 2: Run** `cd server && go test ./internal/db/repo -run SetAllowedSpawners`. Expected: compile FAIL, `SetAllowedSpawners` undefined. Commit the test: `test(mcpapps): add RED test for allowed spawners column`.
- [ ] **Step 3: Implement.** Add `field.JSON("allowed_spawners", []string{}).Default([]string{}).Annotations(entsql.Default("[]"))`, using the same form as `required_env` (line 28-30). Add `SetAllowedSpawners` modelled on `SetRequiredEnv`. Regenerate ent.
- [ ] **Step 4: Run** the same test. Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcpapps): store the spawners an application is allowed for`.

### Task 2: Gate in `ResolveRun` on the stage's spawner

**Files:**
- Modify: `server/internal/mcpapps/resolve.go` (`ResolveRun` at 47, errors at 25-35)
- Modify: `server/internal/pipeline/types.go:197,420` (func type), `server/internal/pipeline/progress_guards.go:174` (forwarding), `server/serverapp/di_pipeline.go:188-193`
- Modify: `server/internal/pipeline/stage_handlers.go:202-209`. Rename the inner `resolved` to `runApps`, because it shadows the spawner `resolved` from line 48. Pass the spawner and `h.stage`.
- Test: `server/internal/mcpapps/resolve_test.go` (fixture `newResolveFixture`, line 30), `server/internal/pipeline/stage_handlers_test.go` (next to `TestAgentStageHandler_ApplicationResolutionFailureFailsBeforeSpawn`, line 608), `server/internal/pipeline/spawner_apps_test.go:65`

**Interfaces:**
- Consumes: `AllowedSpawners` (Task 1).
- Produces:
  - `func (r Resolver) ResolveRun(ctx context.Context, task *ent.Task, spawner *ent.Spawner, stage string) (RunApplications, error)`
  - `type SpawnerNotAllowedError struct{ Server, Spawner, Stage string }`. `Spawner` holds the spawner name, or `"(unresolved)"` when the spawner is nil. Error text: `application %q is not allowed for spawner %q (stage %s)`.
  - Pipeline func type `func(ctx context.Context, task *ent.Task, spawner *ent.Spawner, stage string) (mcpapps.RunApplications, error)`.

- [ ] **Step 1: Write the failing tests** in `resolve_test.go`, table `TestResolveRun_AllowedSpawners`:
  - Explicit attach, list `["sp-a"]`, spawner `sp-a` → server present.
  - Explicit attach, list `["sp-a"]`, spawner `sp-b` → `errors.As` gives `*SpawnerNotAllowedError{Server:"mail-private", Spawner:"B", Stage:"implementation"}`.
  - `attach_all`, list `["sp-a"]`, spawner `sp-b` → no error, server absent.
  - Empty list, spawner `sp-b` → server present (unrestricted).
  - List `["gone"]`, spawner `sp-a` → explicit attach errors (Review Focus 1).
  - List `["sp-a"]`, spawner `nil` → explicit attach errors with `Spawner:"(unresolved)"` (Review Focus 2).

  In `stage_handlers_test.go`, add `TestAgentStageHandler_SpawnerNotAllowedFailsBeforeSpawn`: the resolver stub returns the typed error, then assert `FailTransition` with a reason starting `MCP applications: application "mail-private" is not allowed` and no spawn call. Add `TestAgentStageHandler_PassesResolvedSpawnerAndStage`: two stages of one task resolve to different spawners, and the stub records `(spawner.ID, stage)` per call.
- [ ] **Step 2: Run** `cd server && go test ./internal/mcpapps ./internal/pipeline -run 'AllowedSpawners|SpawnerNotAllowed|PassesResolvedSpawner'`. Expected: compile FAIL. Commit the tests: `test(mcpapps): add RED tests for the spawner gate`.
- [ ] **Step 3: Implement.** In the per-app loop, put the check right after the `!explicit && !app.AttachAll` skip. `len(app.AllowedSpawners) > 0 && (spawner == nil || !slices.Contains(app.AllowedSpawners, spawner.ID))` → explicit: return the typed error. `attach_all`: `slog.Info("mcpapps: application skipped, spawner not allowed", "server", app.ServerName, "task", task.ID, "stage", stage, "spawner", <name>)` and `continue`. Update every caller and fake to the new signature.
- [ ] **Step 4: Run** the focused tests, then `task test`. Expected: PASS.
- [ ] **Step 5: Mutation check (spec §5: no mutation tool in the repo).** Temporarily replace the condition with `false`, run `go test ./internal/mcpapps -run AllowedSpawners`, and paste the FAIL output. Revert, re-run, and paste the PASS output.
- [ ] **Step 6: Commit** `feat(mcpapps): keep an application away from spawners it is not allowed for`.

### Task 3: Up-front check before an agent stage starts

**Files:**
- Modify: `server/internal/pipeline/progress_guards.go` (`runProgressTaskLocked`, after `ensureStageRun` at ~62, before the worktree block)
- Create: `server/internal/pipeline/remaining_stages.go`
- Test: `server/internal/pipeline/remaining_stages_test.go`, `server/internal/pipeline/progress_guards_test.go` (or the existing orchestrator test file that covers `ProgressTask`)

**Interfaces:**
- Consumes: the `ResolveApplications` func type (Task 2) and `o.opts.ResolveSpawner` (`SpawnerResolverFunc`, `types.go:94,128`).
- Produces: `func remainingAgentStages(task *ent.Task, requiresAgent func(stage string) bool) []string`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRemainingAgentStages`:
    - `ready` + `PlanMode=false` → `["implementation","self_review","finalization"]`
    - `ready` + `PlanMode=true` → `["plan_review","implementation","self_review","finalization"]`
    - `self_review` → `["self_review","finalization"]`
    - Kind `job` at stage `job` → `["job"]`
    - A kind registered via `RegisterTaskKindSequence("k", ["ready","implementation","done"])` → `["implementation"]`
  - `TestProgressTask_RejectsWhenALaterStageSpawnerIsNotAllowed`: the resolver gives stage `implementation` spawner A and `finalization` spawner B; the app lists only A and is explicitly attached. Progressing at `implementation` yields a failed stage_run whose `output.error` names `finalization`, and no spawn happens.
  - `TestProgressTask_UpFrontCheckIgnoresOtherResolveErrors`: a `MissingSecretError` on a later stage does not fail the start.
- [ ] **Step 2: Run** `go test ./internal/pipeline -run 'RemainingAgentStages|UpFront|LaterStageSpawner'`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement.**
  - `remainingAgentStages`: walk from `task.CurrentStage`. Use `stageAfterReady(task)` for `ready`, otherwise `NextStageForKind(task.Kind, s)`. Stop at `IsTerminalStage`, with a 32-step guard. Collect the stages where `requiresAgent` is true.
  - In `runProgressTaskLocked`, when `handler.RequiresAgent() && latest == nil` (first entry into this stage) and both funcs are set: for each remaining stage, resolve the spawner and call `ResolveApplications(ctx, task, sp, stage)`. On `errors.As(*mcpapps.SpawnerNotAllowedError)` → `return o.applyTransition(ctx, task, stageRun, FailTransition{Reason: "MCP applications: " + err.Error()})`. Ignore any other error; the per-stage gate still runs.
- [ ] **Step 4: Run** the focused tests, then `task test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(pipeline): refuse to start a task whose later stage may not use its applications`.

### Task 4: API — set `allowedSpawners`, export guard

**Files:**
- Modify: `server/internal/api/applications/handler.go`. Changes: the patch body (222-227), the patch logic (270-300), `applicationView` (126-137), and `NewHandler` (73), which gets a `spawners repo.SpawnerRepo` parameter.
- Modify: `server/serverapp/di.go` (the `applications.NewHandler` call)
- Test: `server/internal/api/applications/handler_test.go` (helpers `newMux`, `do` at 30/54)

**Interfaces:**
- Consumes: `SetAllowedSpawners` (Task 1).
- Produces: JSON `allowedSpawners: string[]` on the application view; PATCH field `allowedSpawners *[]string`.

- [ ] **Step 1: Write the failing tests.**
  - `TestPatch_SetsAllowedSpawners`: 200, and the view echoes the IDs.
  - `TestPatch_RejectsUnknownSpawner`: 400 `allowedSpawners: unknown spawner x`.
  - `TestPatch_RejectsRestrictWhileExported`: 400.
  - `TestPatch_RejectsExportWhileRestricted`: 400.
  - `TestPatch_ClearingRestrictionThenExportWorks`: 200.
- [ ] **Step 2: Run** `go test ./internal/api/applications -run 'AllowedSpawners|Restrict|Export'`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement.** Validate the IDs against `spawners.GetByID`. Compute the effective `exportToClaude` and the effective `allowedSpawners` from the request plus the stored state. Return 400 `exportToClaude: not allowed while allowedSpawners is set` when both are on, before any write. Dedupe the IDs.
- [ ] **Step 4: Run** the tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(applications): let an application name the spawners allowed to use it`.

### Task 5: Separate `HOME` per isolated application

**Files:**
- Modify: `server/internal/mcpapps/preset.go:43`. Add `IsolateHome bool \`json:"isolateHome,omitempty"\``.
- Modify: `server/internal/mcpapps/presets/imap-mcp-server.json`. Add `"isolateHome": true`.
- Create: `server/internal/mcpapps/home.go`
- Modify: `server/internal/mcpapps/resolve.go` (merge into `values` before `WithEnv`, line 81). `Resolver` gets a `HomeRoot string` field.
- Modify: `server/internal/api/applications/handler.go:648`. Merge the same env into the setup `env`. The handler gets `homeRoot string`.
- Modify: `server/serverapp/di.go` and `di_pipeline.go`. `HomeRoot = filepath.Join(home, ".claude", "kontor", "apps")`, the same base as modules at `di.go:506`.
- Test: `server/internal/mcpapps/home_test.go`, `resolve_test.go`, `handler_test.go`

**Interfaces:**
- Produces: `func IsolatedHomeEnv(homeRoot, resourceID string) (map[string]string, error)`. It returns `{"HOME": <dir>, "npm_config_cache": <real cache>}`.
  - `<dir>` is `homeRoot/<resourceID>`, created with mode `0700`.
  - A `resourceID` that does not match `^[A-Za-z0-9._:-]+$`, or that is `.` or `..`, returns an error.
  - The real cache is `$npm_config_cache` if set, else `$HOME/.npm` of the server process.

- [ ] **Step 1: Write the failing tests.**
  - `TestIsolatedHomeEnv`: dir mode `0700`, path under the root, `../x` rejected, `npm_config_cache` taken from the env when set.
  - `TestResolveRun_TwoMailAppsGetDifferentHome`: two imap apps resolve to different `HOME`.
  - `TestResolveRun_NonIsolatedAppKeepsEnv`: an app without the preset flag gets no `HOME`. This guards the anti-goal of breaking `obsidian` and `jetbrains`.
  - `TestSetup_UsesIsolatedHome`: the fake `SetupRunner` sees the same `HOME`.
- [ ] **Step 2: Run** `go test ./internal/mcpapps ./internal/api/applications -run 'Isolated|DifferentHome|NonIsolated'`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement** `IsolatedHomeEnv` and the two merges. Only apps whose matched preset has `IsolateHome` get the merge. `HOME` and `npm_config_cache` are merged after the secret values, so a secret cannot override them.
- [ ] **Step 4: Run** the tests, then `task test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcpapps): give each mail application its own account store`.

### Task 6 (Slice 1 UI + docs): spawner multi-select

**Files:**
- Modify: `src/features/settings/composables/useApplications.ts` (`ApplicationView` 28-39, `patch` 89-98). Add `allowedSpawners` and `setAllowedSpawners(resourceId, ids)`.
- Modify: `src/features/settings/components/ApplicationSettings.vue`. Add a checkbox fieldset in the style of `src/components/ScheduleForm.vue:270-287`. Spawners come from `useSpawners()`, filtered to `adapterType === 'claude'`. Each label is `` `${name} (${env.CLAUDE_CONFIG_DIR ?? '~/.claude'})` ``.
  - An app matched by the imap preset with an empty list shows the warning `Any spawner can read this mailbox.`
  - The export toggle is disabled while the list is non-empty, with the hint `Export is off while the application is limited to spawners.`
- Modify: `src/components/ScheduleForm.vue`. When the chosen spawner is not in an attached app's non-empty list, show the warning `<app> is not allowed for this spawner; runs will fail.`
- Test: `src/features/settings/components/ApplicationSettings.test.ts`, `src/components/__tests__/ScheduleForm.test.ts` (or the existing ScheduleForm test file)
- Docs: `README.md` (one imap app per trust boundary, e.g. `mail-private` and `mail-work`), `CHANGELOG.md` `### Added`, `PRIVACY.md` §2 (per-application home dirs under `~/.claude/kontor/apps`)

- [ ] **Step 1: Write the failing tests.** Toggling a checkbox PATCHes `allowedSpawners`. The warning renders for an imap app with an empty list. The export toggle is disabled while restricted. ScheduleForm shows the attach-time warning.
- [ ] **Step 2: Run** `pnpm test -- ApplicationSettings ScheduleForm`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement** the UI. Update the docs.
- [ ] **Step 4: Run** all gates (Global Constraints). Expected: green.
- [ ] **Step 5: Commit**, open PR 1, and run the live acceptance after the merge (spec §5 acceptance 1): a `mail-private` app allowed only for `Claude (default)`, attached to a routine on `Claude Work`, fails with the typed error, and on `Claude (default)` it lists mailboxes.

---

## Slice 2 — Cloud sessions: import, send, sign-in health (PR 2)

### Task 7: Base spawn env without a stage run

**Files:**
- Modify: `server/internal/pipeline/spawner.go:460`. Extract stages 1–2 and the `CLAUDE_CONFIG_DIR` rule into a new exported function. `BuildSpawnEnv` calls it, then adds stage 3. Today's `BuildSpawnEnv` dereferences `opts.StageRun.ID`, so a caller without a stage run panics.
- Test: `server/internal/pipeline/spawner_test.go` (existing `BuildSpawnEnv` tests must stay green unchanged)

**Interfaces:**
- Produces: `func BaseSpawnEnv(spawner *ent.Spawner) map[string]string`.

- [ ] **Step 1: Write the failing test** `TestBaseSpawnEnv_NoStageRun`. The spawner env is `{"CLAUDE_CONFIG_DIR":"~/.claude-work"}`. The result has the expanded dir and no `DASHBOARD_STAGE_RUN_ID`.
- [ ] **Step 2: Run** the test. Expected: compile FAIL. Commit the RED test.
- [ ] **Step 3: Implement** the extraction. Behaviour of `BuildSpawnEnv` stays the same.
- [ ] **Step 4: Run** `go test ./internal/pipeline -run 'SpawnEnv'`. Expected: PASS.
- [ ] **Step 5: Commit** `refactor(pipeline): build a spawner's base env without a stage run`.

### Task 8: `cloud` package — CLI runner and auth status

**Files:**
- Create: `server/internal/cloud/runner.go`, `server/internal/cloud/sessionref.go`
- Test: `server/internal/cloud/runner_test.go`, `server/internal/cloud/sessionref_test.go`. Use a fake `claude` written into `t.TempDir()` plus `t.Setenv("PATH", dir)`, in the pattern of `server/internal/apps/github/ghcli_test.go:127`. The fake records argv, env and stdin to files and prints canned JSON.

**Interfaces:**
- Consumes: `pipeline.BaseSpawnEnv` (Task 7), `services.ValidateSpawnerCommand` (`server/internal/services/spawn_policy.go:63`).
- Produces:
  - `type Runner struct{ Timeout time.Duration }`. Default 60s when zero.
  - `type SendResult struct{ OK bool; SessionID, URL, Error string }`, decoded from the CLI's `{ok, session_id, url, error}`.
  - `func (r Runner) Send(ctx context.Context, sp *ent.Spawner, sessionID, text string) (SendResult, error)`. Argv is exactly `claude -p --cloud <sessionID> --output-format json`, and the text goes on stdin.
  - `type AuthStatus struct{ LoggedIn bool; AuthMethod, APIProvider, SubscriptionType string }`. Only these four JSON fields are decoded: `loggedIn`, `authMethod`, `apiProvider`, `subscriptionType`. Email and org data are never kept.
  - `func (a AuthStatus) CloudReady() bool`, which returns `LoggedIn && AuthMethod == "claude.ai"`. This shape was VERIFIED on CLI 2.1.308 on 2026-10-22 for both config dirs.
  - `func (r Runner) AuthStatus(ctx context.Context, sp *ent.Spawner) (AuthStatus, error)`. Argv is `claude auth status --json`.
  - `var ErrClaudeNotFound`, `var ErrTimeout`.
  - `func ParseSessionRef(s string) (string, error)`. It accepts a bare ID matching `^(session|cse)_[A-Za-z0-9_-]{8,128}$`, or `https://claude.ai/code/<id>` with an optional trailing `/` or query. Anything else is an error.
- Scrubbed from the child env: `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_FOUNDRY`. `CLAUDE_CODE_OAUTH_TOKEN` is on the list because it passes `BuildSpawnEnv`'s `CLAUDE_` prefix rule and would override the account chosen by `CLAUDE_CONFIG_DIR`.

- [ ] **Step 1: Write the failing tests.**
  - `TestSend_ArgvEnvStdin`: argv is exact. Stdin equals `"--help\nrm -rf"` byte for byte and argv does not contain it (Review Focus 3). The env has the spawner's `CLAUDE_CONFIG_DIR` and none of the scrubbed keys, even when they are set via `t.Setenv` and in the spawner env.
  - `TestSend_OkFalse`: the canned `{"ok":false,"session_id":"session_x","error":"archived"}` gives `SendResult{OK:false, Error:"archived"}` and a nil error.
  - `TestSend_NonJSONStderr`: exit 1 with stderr `not logged in` returns an error containing `not logged in`.
  - `TestSend_Timeout`: the fake sleeps 5 s with `Timeout: 100ms`, which gives `ErrTimeout` (Review Focus 4).
  - `TestSend_ClaudeMissing`: an empty PATH gives `ErrClaudeNotFound`.
  - `TestAuthStatus_DropsIdentityFields`: the canned JSON includes `email` and `orgName`, and the struct has no such fields.
  - `TestParseSessionRef`: a table of valid and invalid inputs, including `http://claude.ai/code/x`, `https://evil.com/code/session_abcdefgh` and `session_../..`.
- [ ] **Step 2: Run** `go test ./internal/cloud`. Expected: compile FAIL. Commit the RED tests.
- [ ] **Step 3: Implement.** Use `exec.CommandContext(ctx, "claude", …)` with `context.WithTimeout`. Before running, `services.ValidateSpawnerCommand("claude")`. Set `cmd.Env` from `BaseSpawnEnv` minus the scrub list. Map `exec.ErrNotFound`, `DeadlineExceeded` and `*exec.ExitError` stderr in the style of `server/internal/apps/github/ghcli.go:21-29`.
- [ ] **Step 4: Run** `go test -race ./internal/cloud`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cloud): run the claude CLI against cloud sessions under a spawner's account`.

### Task 9: `cloud_session` entity, repo, broadcaster

**Files:**
- Create: `server/internal/db/ent/schema/cloud_session.go`. Embed `IDTimestampsMixin` (`schema/mixins.go`). Fields: `session_id` (unique), `url`, `spawner_id`, `title`, `origin` (enum `started|imported|routine`), `last_message` (optional), `last_message_at` (optional, nillable), `last_error` (optional).
- Create: `server/internal/db/repo/cloud_session_repo.go`, modelled on `spawner_repo.go`
- Create: `server/internal/sse/typed_broadcaster.go`. This is a generic `TypedBroadcaster[E any]` with the same API as `SpawnerBroadcaster` (`server/internal/sse/spawner_broadcaster.go`). Moving the spawner and project broadcasters onto it is a FOLLOW-UP, not part of this plan.
- Test: `server/internal/db/repo/cloud_session_repo_test.go`, `server/internal/sse/typed_broadcaster_test.go`

**Interfaces:**
- Produces:
  - `repo.CloudSessionRepo` with these methods:
    - `Create(ctx, CreateCloudSessionInput) (*ent.CloudSession, error)`
    - `List(ctx) ([]*ent.CloudSession, error)`
    - `Get(ctx, id string) (*ent.CloudSession, error)`
    - `RecordSend(ctx, id, message string, at time.Time, errText string) (*ent.CloudSession, error)`
    - `Delete(ctx, id string) error`
    - `var ErrCloudSessionExists`, returned on the unique violation of `session_id`.
  - `sse.TypedBroadcaster[E]` with `NewTypedBroadcaster[E any](b *Broadcaster)`, `Broadcast(E)`, `Subscribe() chan []byte`, `Unsubscribe(chan []byte)`.
  - `type CloudSessionEvent struct{ Type string \`json:"type"\`; CloudSessionID string \`json:"cloudSessionId"\`; Payload any \`json:"payload,omitempty"\` }`. Type is one of `cloud_session_created`, `cloud_session_updated`, `cloud_session_deleted`.

- [ ] **Step 1: Write the failing tests.**
  - Repo round trip.
  - A duplicate `session_id` gives `ErrCloudSessionExists`.
  - `RecordSend` with an empty `errText` clears `last_error`.
  - The broadcaster delivers one frame to a subscriber.
- [ ] **Step 2: Run** the tests. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement** and regenerate ent.
- [ ] **Step 4: Run** the tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cloud): store the cloud sessions Kontor knows about`.

### Task 10: Cloud session API and account health

**Files:**
- Create: `server/internal/api/cloudsessions/handler.go`, modelled on `server/internal/api/spawners/handler.go` (`Mount` at 66, `Stream` at 40)
- Modify: `server/internal/api/router.go` (`RouterDeps` 158-176, mount inside the 307 group like `SchedulesHandler` at 405-423), `server/serverapp/di.go` (`routerDeps` 1143-1205), `server/internal/api/bypass_auth_smoke_test.go:72`, `server/internal/api/testdata/routes.golden`
- Test: `server/internal/api/cloudsessions/handler_test.go`. Use a fake `Runner` behind an interface the handler owns.

**Interfaces:**
- Consumes: Tasks 8 and 9, plus `repo.SpawnerRepo`.
- Produces routes:
  - `GET /api/cloud-sessions` returns `CloudSessionView[]` with `{id, sessionId, url, spawnerId, title, origin, lastMessage, lastMessageAt, lastError, createdAt, updatedAt}`.
  - `GET /api/cloud-sessions/stream` is SSE.
  - `POST /api/cloud-sessions/import` takes `{spawnerId, url, title?}`. It returns 201, 400 for a bad ref or a non-claude spawner, and 409 for a duplicate (Review Focus 5).
  - `POST /api/cloud-sessions/{id}/messages` takes `{text}`:
    - 400 when the text is empty or over 100000 bytes.
    - 409 `account is not signed in with claude.ai; run: CLAUDE_CONFIG_DIR=<dir> claude auth login` when `!CloudReady()`.
    - 502 when the CLI errors or times out, with `last_error` stored.
    - 200 with the view otherwise. `{ok:false}` is stored in `last_error` and still returns 200.
  - `DELETE /api/cloud-sessions/{id}` returns 204 and touches Kontor only.
  - `GET /api/cloud-accounts` returns `[{configDir, spawners:[{id,name}], auth:{loggedIn,authMethod,apiProvider,subscriptionType}|null, error?}]`. There is one row per distinct config dir among the claude spawners; the key is `pathutil.Canonical` of the expanded dir, so a symlink counts once. Results are cached 60 s per dir, and `?refresh=1` bypasses the cache.
- The handler-owned interface is `type CLI interface{ Send(ctx context.Context, sp *ent.Spawner, sessionID, text string) (cloud.SendResult, error); AuthStatus(ctx context.Context, sp *ent.Spawner) (cloud.AuthStatus, error) }`.

- [ ] **Step 1: Write the failing tests.** One test per route outcome listed above. The 502 test uses a fake returning `cloud.ErrTimeout`. The SSE test asserts that `cloud_session_created` is emitted on import and `cloud_session_updated` after a send. `TestCloudAccounts_SymlinkedDirCountsOnce` makes two spawners whose dirs are a dir and a symlink to it.
- [ ] **Step 2: Run** `go test ./internal/api/cloudsessions`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement**, wire DI and the router, and update the route golden file.
- [ ] **Step 4: Run** `task test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cloud): import cloud sessions and send them messages from Kontor`.

### Task 11: Cloud sessions view and docs

**Files:**
- Create: `src/features/cloud/composables/useCloudSessions.ts`. Use `createSseResource` (`src/composables/useSseResource.ts`) the way `src/composables/useSpawners.ts` does. Exports: `useCloudSessions()`, `importSession`, `sendMessage`, `removeSession`, `useCloudAccounts()`.
- Create: `src/features/cloud/CloudSessionsView.vue`. Sessions are grouped by account, i.e. the spawner's config dir.
  - Each row shows the title, an origin chip, the last message and time, an `AppBadge variant="error"` when `lastError` is set (the text is in a tooltip), an "Open in claude.ai" link (`target="_blank" rel="noopener"`), a send box, and "Remove from Kontor". The remove confirm text is `Removes the session from Kontor only. It stays on claude.ai.`
  - The account header shows the sign-in state and, when not ready, the `claude auth login` hint.
  - There is an import form (spawner select restricted to claude spawners, plus a URL field).
- Modify: `src/composables/useViewState.ts:6,14` (add the `'cloud'` view), `src/utils/navConfig.ts:15-23` (`{ view: 'cloud', label: 'Cloud sessions', group: 'Monitor', icon: '☁' }`), `src/App.vue:61,420-424`
- Test: `src/features/cloud/__tests__/useCloudSessions.test.ts`, `src/features/cloud/__tests__/CloudSessionsView.test.ts`
- Docs:
  - `README.md`: a Cloud sessions section that states what the CLI supports and what it does not (no list, stop or replies).
  - `CHANGELOG.md` `### Added`.
  - `docs/guides/security.md:37-43`: a row saying `POST /api/cloud-sessions/*` runs `claude` under a spawner's account.
  - `PRIVACY.md` §2: the `cloud_session` table.
  - `PRIVACY.md` §3: messages go to Anthropic via the user's `claude` CLI.

- [ ] **Step 1: Write the failing tests.**
  - The composable applies the created, updated and deleted events.
  - The view groups by account.
  - Send POSTs the text and clears the box on 200 but keeps it on error.
  - The not-signed-in hint renders.
- [ ] **Step 2: Run** `pnpm test -- cloud`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement** the view, the composable, the nav entry and the docs.
- [ ] **Step 4: Run** all gates. Expected: green.
- [ ] **Step 5: Commit**, open PR 2, and after the merge run the live acceptance (spec §5 acceptance 2): import an existing cloud session URL, send a message, and see it arrive on claude.ai.

---

## Slice 3 — Start a cloud session from Kontor (PR 3)

**Outcome (2026-10-24):** the probe showed that `claude -p --cloud` is refused (`--cloud cannot be combined with --print`) and that `--cloud` without a terminal prints `Error: --cloud requires an interactive terminal.` Task 13 was built anyway, running `claude --cloud "<description>"` under a pseudo-terminal in a stored project folder (`folderId`).

### Task 12: Probe (needs the user's go-ahead; creates a real cloud session)

- [ ] **Step 1: Ask the user.** Name the throwaway GitHub repo and say that one cloud session will be created on the private account. Wait for `lgtm`.
- [ ] **Step 2: Run the probe** from a checkout of that repo: `claude -p --cloud "echo hi" --output-format json < /dev/null`. Record stdout, stderr and the exit code in the PR body.
- [ ] **Step 3: Decide.**
  - If stdout is `{ok:true, session_id, url}`, Task 13 uses this exact argv.
  - Otherwise, Task 13 does not run. The start form becomes a link to `https://claude.ai/code` plus the import form, and the README states why.

### Task 13: `POST /api/cloud-sessions` (only if the probe returned JSON)

**Files:**
- Modify: `server/internal/cloud/runner.go`. Add `Start`.
- Modify: `server/internal/api/cloudsessions/handler.go`. Add the route and extend the `CLI` interface.
- Modify: `src/features/cloud/CloudSessionsView.vue`. Add a start form: spawner, project folder (from the projects' folders), description.
- Test: `runner_test.go`, `handler_test.go`, `CloudSessionsView.test.ts`
- Docs: `README.md`, `CHANGELOG.md` `### Added`

**Interfaces:**
- Produces:
  - `func (r Runner) Start(ctx context.Context, sp *ent.Spawner, cwd, description string) (SendResult, error)`. Argv is the probe's exact form, with `cmd.Dir = cwd`.
  - `POST /api/cloud-sessions` takes `{spawnerId, cwd, description}`:
    - 400 when the description is empty or starts with `-`.
    - 400 when `cwd` is not a folder of a Kontor project (match it against the project folder repo, not just any path).
    - 409 when the account is not cloud-ready.
    - 502 on a CLI error.
    - 201 with `origin=started` and `title=description` (trimmed to 120 runes for display).

- [ ] **Step 1: Write the failing tests** for each outcome above, plus a fake-binary test that argv carries the description as one element and `cmd.Dir` equals `cwd`.
- [ ] **Step 2: Run** the tests. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** all gates. Expected: green.
- [ ] **Step 5: Commit**, open PR 3, and run the live acceptance (spec §5 acceptance 3) after the merge.

---

## Slice 4 — Preview switch and routines (PR 4)

### Task 14: `features.preview` setting

**Files:**
- Modify: `server/internal/settings/registry.go:126-176`. Add `{Key: "features.preview", Type: TypeBool, Default: "false", Apply: ApplyLive, Category: "features"}`.
- Modify: the settings UI that lists the bool settings (find the component that renders `useSettings().items`). Label `Preview features`, help `Turns on features that may change or go away.`
- Test: `server/internal/settings/registry_test.go` (default false, live), plus the settings component test
- Docs: `docs/guides/configuration.md:79-99` row `| \`features.preview\` | bool | \`false\` | live |`

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run** them and watch them fail. Commit the RED tests.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(settings): add a switch for preview features`.

### Task 15: Routine fire client and URL validator

**Files:**
- Create: `server/internal/cloud/routine.go`
- Test: `server/internal/cloud/routine_test.go`

**Interfaces:**
- Produces:
  - `const RoutineBetaHeader = "experimental-cc-routine-2026-04-01"` and `const AnthropicVersion = "2023-06-01"`.
  - `func ValidateFireURL(s string) error`. The string must fully match `^https://api\.anthropic\.com/v1/claude_code/routines/trig_[A-Za-z0-9]+/fire$`.
  - `type RoutineClient struct{ HTTP *http.Client }`.
  - `type FireResult struct{ SessionID, URL string }`.
  - `func (c RoutineClient) Fire(ctx context.Context, fireURL, token, text string) (FireResult, error)`:
    - It calls `ValidateFireURL` first.
    - It sends `Authorization: Bearer <token>`, `anthropic-beta`, `anthropic-version` and `Content-Type: application/json`.
    - The body is `{"text": text}`, or empty when the text is empty.
    - It decodes `claude_code_session_id` and `claude_code_session_url`.
    - A non-2xx response returns `*FireError{Status int; Body string}`, with the body capped at 4 KiB.

- [ ] **Step 1: Write the failing tests.**
  - `TestValidateFireURL`: accepts the exact shape. Rejects `http://`, another host, `api.anthropic.com.evil.com`, a userinfo `https://x@api.anthropic.com/...`, an extra path segment, a query string, and `trig_` with no ID.
  - `TestFire_SendsDocumentedRequest`: an `httptest.Server`, reached via a `RoundTripper` that rewrites the scheme and host to the test server, so the validator stays strict. Assert the headers, the body and the decoded result.
  - `TestFire_429IsFireError`.
  - `TestFire_DoesNotFollowRedirects`: a 302 is returned as a `FireError`, and the token is not sent to the redirect target.
- [ ] **Step 2: Run** `go test ./internal/cloud -run 'Fire'`. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement.** The production client is `&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(...) error { return http.ErrUseLastResponse }}`.
- [ ] **Step 4: Run** the tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cloud): fire claude.ai routines through the documented endpoint`.

### Task 16: `cloud_routine` entity, API behind the preview switch

**Files:**
- Create: `server/internal/db/ent/schema/cloud_routine.go`. Embed `IDTimestampsMixin`. Fields: `name`, `fire_url`, `spawner_id`, `token_ciphertext` (Sensitive), `token_nonce` (Sensitive).
- Create: `server/internal/db/repo/cloud_routine_repo.go`. It encrypts and decrypts with `*secretbox.Box` (`server/internal/secretbox/secretbox.go:53,63`); a nil box returns `ErrSecretsUnavailable`, in the pattern of `application_secret_repo.go:16,40`.
- Create: `server/internal/api/cloudroutines/handler.go`
- Modify: `server/internal/api/router.go`, `server/serverapp/di.go`, `bypass_auth_smoke_test.go`, `routes.golden`
- Test: repo test, handler test

**Interfaces:**
- Consumes: Task 14 (`settingsSvc.Bool("features.preview")`, injected as `func() bool`), Task 15, and `repo.CloudSessionRepo.Create` (Task 9).
- Produces routes. Every route returns 404 while preview is off. Stored rows stay in the DB.
  - `GET /api/cloud-routines` returns `[{id,name,fireUrl,spawnerId,hasToken,createdAt,updatedAt}]`. The token is never returned.
  - `POST /api/cloud-routines` takes `{name, fireUrl, spawnerId, token}` and returns 400 on a validator failure, a non-claude spawner, or an empty token.
  - `PATCH /api/cloud-routines/{id}` takes `{name?, fireUrl?, token?}`. An empty token keeps the stored one.
  - `DELETE /api/cloud-routines/{id}`.
  - `POST /api/cloud-routines/{id}/fire` takes `{text?}`. It returns 201 with a `CloudSessionView` (`origin=routine`, `title=<routine name>`), and emits `cloud_session_created`. A `FireError` returns 502 with `{status, body}` and creates no session row.

- [ ] **Step 1: Write the failing tests.**
  - Every route returns 404 with preview off and works with it on.
  - The token does not appear in any GET body.
  - A fire creates the session row.
  - A fire with a 429 creates no row and returns 502 carrying `status: 429`.
  - An invalid `fireUrl` is rejected on both create and patch.
- [ ] **Step 2: Run** the tests. Expected: FAIL. Commit the RED tests.
- [ ] **Step 3: Implement**, wire DI and the router, and update the golden file.
- [ ] **Step 4: Run** `task test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cloud): keep routines and fire them from Kontor (preview)`.

### Task 17: Routines UI and docs

**Files:**
- Modify: `src/features/cloud/CloudSessionsView.vue`. Add a Routines section rendered only when `features.preview` is true (read via `useSettings()`). It carries an `AppBadge variant="info" label="Preview"`.
  - It has a list, a create form (name, fire URL, spawner, token as `type="password"`), and a Fire button with an optional text field.
  - Error display shows the status and body.
- Create: `src/features/cloud/composables/useCloudRoutines.ts`
- Test: `src/features/cloud/__tests__/CloudRoutines.test.ts`
- Docs:
  - `README.md` (routines, preview).
  - `CHANGELOG.md` `### Added`.
  - `PRIVACY.md` §3, a new `###` subsection: what is sent to `api.anthropic.com` (routine text), and that tokens are stored encrypted (AES-GCM secretbox). Add a Transfer basis line.
  - `PRIVACY.md` §2: the `cloud_routine` table.
  - `docs/guides/security.md`: a row saying routine routes hold a token and make an outbound call.

- [ ] **Step 1: Write the failing tests.** The section is hidden with preview off. The badge renders. Fire shows the new session row, and a 502 shows the status.
- [ ] **Step 2: Run** the tests and watch them fail. Commit the RED tests.
- [ ] **Step 3: Implement**, and update the docs.
- [ ] **Step 4: Run** all gates. Expected: green.
- [ ] **Step 5: Commit**, open PR 4, and run the live acceptance after the merge (spec §5 acceptance 4): with preview on, fire a routine and see the new session row.

---

## FOLLOW-UPS (not in this plan)

- Move `SpawnerBroadcaster` and `ProjectBroadcaster` onto `sse.TypedBroadcaster`.
- Surface `SpawnerNotAllowedError` / `MissingSecretError` as typed errors in the task UI. Today it shows the stage-run error string.
