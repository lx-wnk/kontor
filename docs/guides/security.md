# Security

The dashboard reads sensitive Claude session data from your machine. It is designed local-first and defensive by default.

- **Loopback only** — the server binds to `127.0.0.1` and is never exposed to the network. (Multi-machine mode is opt-in and expects a VPN/SSH tunnel — see [Configuration](configuration.md).)
- **Local-trust auth bypass** — `auth.mode` defaults to `none`, and in that mode all API requests are allowed without login. This is the intended posture for a single-user developer machine, and it is a real trust decision rather than a gap: see [Authentication and the local-trust default](#authentication-and-the-local-trust-default) for exactly what it permits. For shared or multi-user machines, configure GitHub OAuth (`KONTOR_GITHUB_CLIENT_ID` + `KONTOR_GITHUB_CLIENT_SECRET`).
- **Ephemeral JWT secret** — `KONTOR_JWT_SECRET` is auto-generated if unset (sessions reset on restart). Set a stable value for production.
- **Hashed tokens** — bearer tokens are SHA-256 hashed before storage; raw tokens are shown once and never persisted in plaintext.
- **Authenticated channel replies** — per-agent bearer tokens authenticate channel replies.
- **Sanitized output** — markdown is sanitized via DOMPurify before any `v-html` rendering.
- **Rate-limited spawns** — user-initiated spawns are rate-limited (default 5/min, configurable).
- **Dangerous-command block-list** — a block-list in the spawner rejects `curl`/`wget`/`eval`/shell-substitution in agent tool grants.
- **`git push` hard-blocked** by default even when granted; opt out with `KONTOR_ALLOW_GIT_PUSH=true` or per-task `metadata.allowGitPush=true`.

See also the [Privacy policy](../../PRIVACY.md).

## Authentication and the local-trust default

`auth.mode` is a database setting (`server/internal/settings/registry.go`) and its
default is `none`. `resolveBypassAuth` (`server/serverapp/di_router.go`) turns that
into `BypassAuth`, which makes the router skip `RequireAuth` altogether. Two guards
still apply in that mode, and they are the ones doing the work:

- `RequireLoopbackHost` — the request must arrive on the loopback interface.
- `RequireSameOriginForMutations` — every mutating request needs a matching `Origin`
  header, which is what keeps a random web page in your browser from posting here.

There is no third guard. The dashboard has **no admin role in effect**: nothing in the
codebase writes `is_admin`, and `server/internal/api/router.go` installs no admin
middleware. An admin gate did exist on spawner CRUD and was removed precisely because
it was ungrantable — it rejected every authenticated user and passed everything through
in bypass mode.

### What that permits

On a default install, any local process that reaches the loopback port with a matching
`Origin` can:

| Route group | What it grants |
| --- | --- |
| `POST/PATCH /api/spawners` | Define the command a spawner executes — the code calls this "RCE-equivalent" (`server/internal/pipeline/spawner.go`, `server/internal/llmadapter/llm_custom.go`) |
| `PATCH /api/settings/{key}` | Change `auth.mode`, `git.allowPush`, `worktree.force` |
| `POST /api/grants` | Write the `grants` rows the capability gate reads |

The third entry is worth stating plainly, because it bounds what the capability gate
in [Capabilities and the permission gate](#capabilities-and-the-permission-gate) is
claiming. The gate is enforced correctly on every call site listed there — but under
`auth.mode = none`, whoever can write a grant never has to pass the gate at all. The
gate constrains *agents*; it does not constrain whoever can write to the API.

Two comments in the code state contracts that no caller currently meets — the mount
sites do not wrap these routes in the admin middleware they ask for:

- `server/internal/api/spawners/handler.go` — "Caller must wrap r with RequireAuth +
  RequireAdminOrBypass"
- `server/internal/api/settings/handler.go` — "Callers must gate this behind admin
  authorization"

They are kept as the specification of where the gate belongs once the role is
grantable, tracked in [#427](https://github.com/lx-wnk/kontor/issues/427).

### When this posture is wrong for you

It assumes every local process is as trusted as you are. That assumption breaks on a
shared or multi-user machine, and it is weaker than it looks on a single-user machine
too: the agents this dashboard spawns run as the same user, with `Bash`, and are
therefore inside the trust boundary rather than outside it.

If that is not the posture you want, set `auth.mode` to `plugin` and configure an auth
provider. `auth.mode` is registered `ApplyRestart`: the running server holds the value
it read at startup, so changing it — through the settings UI or `kontor
settings set` — has no effect until the server restarts. Verify by reloading the
dashboard and confirming you are asked to log in; do not assume the write took.

Note that the capability gate's human-in-the-loop asker is only constructed
when authentication is on — under `auth.mode = none` an `ask` decision fails closed
instead, deliberately, because "a human decided" would otherwise reduce to "any local
process decided".

## Capabilities and the permission gate

A **capability** is a named permission coarser than a raw tool name — `Bash`,
`WebFetch`, or a future action like `mail.send` that has no Claude Code tool
behind it at all. Claude Code's own permission system can only grant or deny
a *tool*; it has no unit for "this agent may reach the network" or "this
agent may spend up to $5", so those questions had nowhere to live. Every
capability also carries a **class** (`tool`, `reach`, `resource`, `spend`)
that decides its default when nothing has granted or denied it explicitly:
`tool`/`reach`/`resource` default to asking, `spend` and any unrecognised
class default to deny.

One pure **Decider** (`server/internal/capability`) resolves a capability
request to allow / deny / ask by ranking the grants that apply to it. The
model defines six context levels, from most to least specific — agent
session, task, routine, application, project, global — and the Decider
ranks grants across all six, with a deny beating an allow beating an ask
within whichever level wins. Grants are rows with an optional expiry and
rate limit, bound to one of those contexts.

**State this plainly: that ranking is now partially exercised, not entirely
theoretical.** `SpawnEnforcer` (`server/internal/pipeline/spawner.go`),
which resolves every spawned task-pipeline agent's permissions, still never
reads the `grants` table — it builds a `GrantView` on the fly from each
granted `TaskPermission` row, pinned to one fixed, synthetic task-level
context. A migration does backfill real rows into `grants` (`task` context
from `task_permissions`, `project` context from `permission_presets`, both
idempotent — see [`PRIVACY.md`](../../PRIVACY.md)). The memory store's
`internal/memory.Authorize` (gating `memory.read`/`memory.write` for both
`/api/memory/*` and the `memory_search`/`memory_write` MCP tools — see
[`CHANGELOG.md`](../../CHANGELOG.md)) is now a real, production caller of
`GrantRepo.ListForCapability`, and it resolves against up to two context
levels — a memory scope's own context (project or application) plus the
global fallback, or just the global level alone when the request itself is
global-scoped — rather than one fixed level. So the six-level hierarchy has
a second level to rank against for memory today; and a third, `routine`, for
the subset of those calls that a routine started: a task materialized by a
schedule carries that schedule's id in `task.routine_id`, and the pipeline's
memory push passes it to `memory.Gate.Authorize` as an extra context, so a
grant made with `--scope routine:<schedule id>` decides that push. **It is no longer only the push.** Every pipeline stage run is now spawned
with a second, ephemeral `dashboard-tasks` MCP server entry alongside
`dashboard-channel` — a per-stage-run bearer credential
(`server/internal/mcp/stagekey.go`, `StageKeyIssuer.Issue`), distinct from
`KONTOR_MCP_TOKEN`. That env var is still a single value taken from
config and handed to every spawn — it has not changed and still identifies
no task — but it was never the credential that reaches `/api/mcp` in the
first place: it belongs to the channel bridge alone
(`server/internal/channel/bridge.go:55`), authenticating the three callback
tools (`dashboard_reply`, `request_permission`, `set_stage_output`), a
different endpoint from the one the Decider's grants govern. The stage-run
key's row carries `stage_run_id`, and `mcp.CallerResolver.Contexts`
(`server/internal/mcp/caller.go`) resolves it through
`stage_run.task_id → task.routine_id` into the same `[{task, <id>},
{routine, <id>}]` pair the memory push builds, passed to `Gate.Authorize` as
extra context on every call. It is wired into the memory MCP tools
(`memory_search`, `memory_write`) and the four `obsidian_*` tools
(`server/internal/mcp/tools/memory.go`, `.../tools/obsidian.go`) — the tools
that already run through the capability gate — so a `task:` or `routine:`
grant now decides those calls too, not just the automatic push.

A stage-run key carries two independent expiries, because either one alone
leaves a hole. The orchestrator revokes it (`active = false`, via
`RevokeForStageRun`) the moment its stage run reaches a terminal state; and
independently, the row's `expires_at` (the stage's timeout plus a
five-minute buffer, `mcp.StageKeyTTLBuffer`) is enforced by `GetByHash`
(`server/internal/db/repo/api_key_repo.go`) itself, refusing an expired row
with the same "Invalid or revoked API key" message an unknown token gets —
an expired key never reaches a tool handler, and a distinct message would
tell an attacker the token was once real. The buffer covers a server that
dies between spawn and the orchestrator recording the transition; without it
that stuck run's key stays valid forever (the same class of gap as
`lesson_worktree_failure_silent_stall`). A background sweep
(`server/internal/mcp/sweep.go`, `SweepExpiredKeys`, started alongside the
cost-history importer and the drift-detection scanner) hard-deletes expired
rows hourly — they are deleted, not tombstoned, because they carry no audit
value beyond the stage run's own record.

Every issued key gets the same fixed scope set — `tasks:read`, `agent:coord`,
`memory:read`, `memory:write`, `obsidian:read`, `obsidian:write`. That set is
narrower than the first draft, which also included `pipeline:control` and
`tasks:write`. **The scope set is the actual boundary for everything except
memory and Obsidian.** Only `memory_search`/`memory_write`
(`server/internal/mcp/tools/memory.go`) and the four `obsidian_*` tools
(`server/internal/mcp/tools/obsidian.go`) call `Gate.Authorize` and resolve
against the capability gate's grants; every other MCP tool checks nothing
beyond `ToolScopeMap` (`server/internal/mcp/auth.go`) — a scope in the set
above hands out that scope's tools outright, with no gate to narrow it
later. `pipeline:control` reaches `grant_permission`,
`resolve_permission_request` and `approve_all_pending` on scope alone, which
would let a `spec_gated` agent approve its own spec and resolve its own
permission requests; `tasks:write` reaches `manage_task`'s
`grant_permissions` action, which lets a caller widen its own permissions.
Both are excluded for that reason. `keys:manage` is excluded the same way an
agent able to mint its own keys could mint one with no stage run and escape
its own attribution.

That scope set only binds because the spawn is launched with
`--strict-mcp-config` next to its `--mcp-config`
(`server/internal/pipeline/spawner.go`). Without the strict flag the written
file is *added to* the user- and project-scope registrations rather than
replacing them, and `claude mcp add --scope user` (what the onboarding
one-click registration runs) registers `dashboard-tasks` under the same name
with a broad, long-lived key — `tasks:read`, `tasks:write`,
`pipeline:control` — which a stage agent could have used instead of its own.
Because strictness would otherwise cost an agent every MCP server its
operator configured, the user-scope servers from `~/.claude.json`
(`server/internal/claudeconfig`) are merged into the spawn's config file,
with the dashboard's own two names reserved: a user-scope `dashboard-tasks`
or `dashboard-channel` entry is dropped rather than merged, so the spawn's
per-stage-run credential always wins under that name. A `~/.claude.json`
that is missing, unreadable or malformed is logged and skipped — a file the
dashboard does not own may not break a spawn. A project-scope `.mcp.json` in
the worktree is not carried over.

Agent session still has no `grants`-table reader — an interactive session
started by hand keeps using the machine-wide key and resolves with no task
context, so the `agent_session` context level stays without a producer.
`SpawnEnforcer` still bypasses the `grants` table entirely via the mechanism
above; that allow-list reading real grants instead of synthesizing them from
`task_permissions` remains a separate, unstarted change.

A capability also declares which enforcement points it can be applied at,
and a resolved decision carries that same list forward — but the two wired
enforcement points treat it differently. `SpawnEnforcer` renders a shared
batch of decisions into one allow-list, so it must filter out capabilities
that aren't its concern: a point with no standing over a capability (e.g.
one only enforceable at the server) does not act on it there just because
the Decider returned "allow". `ServerEnforcer` deliberately does the
opposite and ignores `Enforceable` entirely: it is the complete backstop
judging one decision at its point of use — the other two points are each
incomplete in their own way (the hook fails open on timeout, the spawn
point is static and cannot ask) — so it enforces every decision handed to
it regardless of where else that capability is declared enforceable
(`server/internal/capability/enforcer_server.go`, pinned by
`TestServerEnforcerIgnoresEnforceable`).

That one Decider is shared by all three enforcement points below, but they
are not otherwise identical — each has a different guarantee, and one of
them (the hook) cannot be bypassed by a network outage without also being
able to give up on purpose:

| Enforcement point | What it covers |
|---|---|
| **Server** (`ServerEnforcer`) | The only point with complete coverage once a call site invokes it — nothing routes around it, and it cannot time out into an implicit allow. It is implemented and tested (`server/internal/capability/enforcer_server.go`) and has real production callers: every `/api/memory/*` request, both memory MCP tools (`memory_search`, `memory_write`), the task pipeline's automatic memory push into a stage's spawn prompt, the `POST /api/obsidian/index` trigger, the four `obsidian_*` MCP tools, the four `/api/github/*` HTTP routes (`summary`, `search`, `comment`, `merge`), and the four `github_*` MCP tools, all through `memory.Gate.Authorize` (`server/internal/memory/authorize.go`). **An `Asker` is now wired to it — but only when authentication is on.** With `KONTOR_AUTH` in any real mode, `server/serverapp/di.go` builds a `serverask.Asker` (`server/internal/serverask/asker.go`): an `ask` decision holds the caller's request open, the pending ask rides the agent SSE frame into the dashboard's triage band, and **Allow**/**Deny** there posts to `POST /api/capabilities/decisions/respond`, which releases the held call. Under `KONTOR_AUTH=none` no asker is constructed and an `ask` still fails closed to `ErrAskRequired` — deliberately: the respond route is not mounted in that mode either, so "a human decided" would reduce to "any local process decided". Six of the call sites that reach the gate may block for a human — the HTTP memory handler (`server/internal/api/memory/handler.go`), `GET /api/resources` (`server/internal/api/resources/handler.go`, which authorizes `memory.read` for `kind=memory_space`), the memory MCP tools (`server/internal/mcp/tools/memory.go`), the four `obsidian_*` MCP tools (`server/internal/mcp/tools/obsidian.go`, sharing the same `Asker` the memory tools use — an agent is genuinely waiting on the tool response either way), the four `/api/github/*` HTTP routes (`server/internal/api/github/handler.go`, built with that same `Asker` — unlike the Obsidian HTTP route just below, these four are the direct request a browser is blocked on, the same shape the memory handler already gates with an `Asker`, not an unattended background run), and the four `github_*` MCP tools (`server/internal/mcp/tools/github.go`, sharing the memory tools' `Asker` too). `github_merge` never actually reaches the asker regardless of any of this: its `spend` class resolves to deny in `capability.Decide`, and `ServerEnforcer` returns `ErrDenied` before the ask branch runs — see [GitHub's token and repository boundary](#githubs-token-and-repository-boundary). The pipeline's memory push (`server/serverapp/di_pipeline.go`) and the Obsidian index trigger's three checks — `memory.write` on the target space, `obsidian.search`, `obsidian.read` (`server/internal/apps/obsidian/index.go`, called from `POST /api/obsidian/index`) — construct their own `Gate` with no `Asker` on purpose: nothing is waiting on either, so an unanswerable ask must deny rather than stall a spawn or a background index run. The two gates do not check the same set: `IndexNotes` checks all three — `memory.write`, `obsidian.search`, `obsidian.read` — together on every run (`server/internal/apps/obsidian/index.go:66,82,92`), while each MCP tool checks exactly one capability per call — `obsidian_read` only `obsidian.read`, `obsidian_search` only `obsidian.search`, `obsidian_write` only `obsidian.write`, `obsidian_delete` only `obsidian.delete` — and none of the four ever checks `memory.write`. What the two gates share is only the asker asymmetry: a fresh install denies the index trigger outright on a missing grant, while the identical kind of `ask` decision through an MCP tool call can surface as a card instead. An ask nobody answers within 25 seconds (`askHoldTimeout`) denies, and so does an ask still pending when the server restarts: a pending ask lives only in memory, is never persisted, and the caller's request fails closed when it disappears. A `deny` decision never reaches the asker at all — `ServerEnforcer.Enforce` returns `ErrDenied` for `EffectDeny` before the ask branch — so nobody can click an explicit denial into an allow; the only decision a human sees is one the Decider itself resolved to `ask`. One such case is a rate limit: an `allow` whose winning grant is exhausted is downgraded to `ask` by `Enforce`, with a reason naming the limit, so with an asker wired a human can now be asked to permit one use past a cap they set themselves. The other half of the old gap closed separately: the `agent-dashboard grants` CLI now creates standing grants, so a `memory.read`/`memory.write` request can be allowed once and for all rather than one call at a time. That is no longer the only surface for it — `GET`/`POST /api/grants` and `DELETE /api/grants/{id}` (`server/internal/api/grants/handler.go`) and **Settings → Grants** create and revoke `grants` rows too — but a fresh install still denies every memory call until a grant exists through one of these or an ask is answered by hand. |
| **Spawn** (`SpawnEnforcer`) | Complete for every agent the dashboard's task pipeline spawns itself: each granted `TaskPermission` is resolved through the Decider and rendered into that process's `--allowedTools` list (`server/internal/pipeline/spawner.go`). It cannot ask — the file is written before the process starts — so an `ask` decision is simply omitted, and the agent falls back to its own permission prompt for that call. |
| **Hook** (`HookEnforcer`) | The only point that can reach a session you started by hand, because it rides Claude Code's own `PreToolUse` hook instead of a start-time handshake. **It fails open on a timeout, by design** — see below. |

### Creating and revoking grants

The `kontor grants` CLI, `GET`/`POST /api/grants` plus
`DELETE /api/grants/{id}` (`server/internal/api/grants/handler.go`), and
**Settings → Grants** all create and revoke `grants` rows (the boot
backfill migration writes rows too, but nobody invokes it by hand). Only
the CLI opens the SQLite database directly, the same way
`kontor settings` and `kontor plugins` do, so it is the
one that still works while the server is down.

```bash
kontor grants add memory.read --pattern '*' --scope global --mode allow
kontor grants list --capability memory.read
```

`add`, `list`, `revoke`, and `capabilities` are the four subcommands —
`capabilities` lists the grantable names, `list` lists existing grants
(newest first, optionally filtered by capability, with an `ENFORCEMENT`
column saying whether anything reads that capability's grants and a
`GRANTED-BY` column saying who created each one), and `revoke <id>`
tombstones a grant (`revoked_at`/`revoked_by` set) rather than deleting it,
so the audit trail survives. `revoke` refuses a grant that is already
revoked, so a second call can never overwrite who revoked it first.

`--pattern` is required on `add`: pass `--pattern '*'` to cover every value,
or a specific pattern (a prefix pattern ends in `*`, e.g. `'git status*'`).
`*` is stored literally and behaves exactly like the empty wildcard the
schema already uses, because the matcher routes a trailing `*` through a
prefix test with an empty prefix — the requirement exists so the widest grant
the system can express has to be asked for out loud, not arrived at by
accepting three defaults.

Two further inputs are rejected rather than stored inert: `--expires-in` must
be a positive duration (a zero or negative one used to create a grant that
was already expired), and a non-`global` `--scope` must carry a ref —
`--scope project:/home/me/app`, not bare `--scope project`. A scope with an
empty ref can never match anything, because the caller's context chain
collapses an empty ref to `global` and the Decider's scope test is an exact
match on kind *and* ref.

`add` also warns on stderr when nothing reads the grant it just wrote. The
`grants` table has exactly one production reader — `internal/memory.Authorize`
via the server enforcer — so a grant for a capability that is not enforceable
at the **server** point (which is every Claude Code tool name, `Bash`
included) is recorded and read by nobody. The grant is still created and the
exit code is unchanged; the warning only says it will take effect once a
reader exists.

#### Specificity is resolved before mode

**A narrower grant wins outright, whatever its mode.** `capability.Decide`
ranks matching grants by context specificity first (agent session → task →
routine → application → project → global) and the most specific level with
*any* matching grant decides on its own; `deny` beats `allow` beats `ask`
only among grants at that same level. A broader grant never gets a vote once
a narrower one matches.

So these two grants do **not** compose the way they read:

```bash
kontor grants add memory.read --pattern '*' --scope global --mode deny
kontor grants add memory.read --pattern '*' --scope project:/home/me/app --mode allow
```

Inside `/home/me/app` the request is **allowed**. The global `deny` is not a
safety net over the project `allow` — it only applies where no project-,
application-, task-, routine-, or session-level grant matches. To close a
capability off in one project, revoke the project-level grant; a global deny
cannot do it. (Pinned by "a global deny does NOT overrule a task allow" in
`server/internal/capability/decide_test.go`.)

`--mode` accepts `ask` and stores it without complaint, but **the server
enforcer has no `Asker` wired to it** (see the table above), so an `ask`
grant resolves to a denial there just like an unset one. `allow` is the only
mode that actually opens the gate at the server point today.

### The hook point's three outcomes

A hook call that gets no explicit decision looks the same from the terminal
in every case, but `HookEnforcer.Point()` (`server/internal/api/hooks/permission.go`)
distinguishes three situations that must not be flattened into one:

- **Actively vetoed** — the call matches one of your own `permissions.deny`
  rules. It is held and offered in the dashboard without an Allow button,
  and the server refuses to turn a "deny" into an "allow" even if the client
  is asked to. This is the one guarantee this enforcement point makes.
- **Never observed** — the session was never armed, or the hook payload was
  malformed. Nothing was evaluated at all, so this is neither open nor
  closed: it is exactly as if the hook were not installed, and Claude
  Code's own terminal prompt runs unmodified.
- **Deliberately lapsed — fails open.** A call was genuinely held (an armed
  session, a valid payload, no deny rule matched) and nobody answered within
  25 seconds. The hold gives up before Claude Code's own hook timeout does,
  on purpose, so a dashboard that is down, slow, or simply not being watched
  degrades the session back to its normal terminal prompt instead of hanging
  it forever. **State this plainly: an armed session nobody is watching is
  not protected any more strongly than an unarmed one.** The deny-rule check
  above is the only thing this enforcement point guarantees regardless of
  whether a human ever answers.

The hook point also does not consult the Decider's own pattern matcher for
its one active protection. The deny-rule check runs its own matcher
(`server/internal/claudesettings/deny.go:52-53`), which treats a
`domain:host` rule as matching whenever `strings.Contains(arg, host)` —
broader than the Decider's matcher (`server/internal/capability/pattern.go:84-97`),
which compares hostnames label by label so `example.com` cannot match
`evilexample.com`. This is deliberate, not an oversight: on the deny side,
matching *more* is the safe direction, because this matcher never grants
anything — it only decides whether to offer the Allow button, and the user
can still answer for real in their terminal. Swapping the Decider's strict
matcher onto the deny side would make deny rules match *less*, and start
offering Allow on calls the user's own settings already forbid. The two
matchers stay separate on purpose.

### Application secrets and MCP applications

Application secrets (see [MCP applications](mcp.md#mcp-applications)) are encrypted at rest with AES-256-GCM and never returned by the API. That protects the database, not a running agent.

- At spawn, the secrets of a run's attached applications are written in clear into the `env` of each server's own entry in the run's temporary MCP config, `$TMPDIR/dashboard-<uid>/dashboard-channel-mcp-*.json` (`/tmp/dashboard-<uid>/…` when `TMPDIR` is unset), mode `0600`. The file is removed when the run is cleaned up. A file a crash left behind is removed at the next dashboard start once it is a day old; a younger one may belong to another dashboard instance's live run.
- **An agent with Bash runs as the same OS user as that file and as the MCP server process, so it can read the secrets of every application attached to its own run.** Separate OS users per run are not implemented.
- Use credentials you can revoke on their own — app-specific passwords where the provider offers them, never an account's main password — and attach an application only to routines whose agents you would trust with that credential.
- Under `auth.mode=none` (see [Authentication and the local-trust default](#authentication-and-the-local-trust-default)), no authentication guards `/api`, so any local process, an agent's Bash included, can call the schedules and applications API — for example to attach an application to a routine or replace a secret. With authentication enabled, these routes require a session.
- **Refresh tool list** and every run start the server from the definition stored in the dashboard, as the same OS user, without a shell. Anyone who can add or edit an application through the API can therefore choose a command that runs as that user — under `auth.mode=none` that includes any local process. The definition is no longer read from `~/.claude.json` at run time.
- Mirroring an application into `~/.claude.json` (**Also available in Claude Code sessions**) writes only `mcpServers.<name>`, keeps every other key and the file's mode, writes through a temporary file in the same directory, and refuses a symlinked config rather than following it. The exported entry is the stored definition, so **no secret value is ever written into that file** — secrets are merged only into the run's own temporary MCP config.
- Removing an application revokes the grants on its tools, so a server that is gone cannot leave an allow behind for a tool of the same name later. It also removes that server from `~/.claude.json` when the application was mirrored there — only a mirror the dashboard itself wrote, never a server it never exported.
- A known server's dangerous tools are denied the moment the application appears — on add, on import, and again after every catalogue refresh — and the denies cover tools the catalogue has not listed yet, so the gap between adding a server and reading its tools is closed. Until this release the shipped preset carried `"confirmed": false` and those denies were never applied at all.
- **Set up** runs the preset's setup command as the same OS user, with the application's secrets in its environment, and the page it serves listens on all interfaces — anyone on your local network can reach it while it runs. The panel says so, and the session ends on Done, after fifteen minutes idle, after thirty at most, or at shutdown. The command comes from an embedded preset, not from request input; the only substitution is the `{port}` the preset itself declares.
- `POST /api/applications/{resourceId}/accounts` calls the server's own `imap_list_accounts` and **deliberately consults no grant**: grants gate what an agent may call, and this is a human clicking a button in the UI. It returns derived variable names only — never account details — and stores nothing. It also removes that server from `~/.claude.json` when the application was mirrored there — only a mirror the dashboard itself wrote, never a server it never exported.
- Application tools default to refusal: a tool without an allow grant is not in a run's allow list, and allow-all autonomy does not change that. An agent's permission request for an application tool is never approved automatically either, whatever the task's autonomy — a human decides (`server/internal/api/tasks/permission_request_routes.go`).
- A permission request is answered with a decision, not a bare yes: `allow_once`, `allow_routine`, `deny_routine` or `deny_once` (`server/internal/api/tasks/permission_decision.go`). `allow_routine` and `deny_routine` write a grant in that routine's context, so the answer applies to every later run of the routine; they are accepted only for a task a routine created and only for application tools, because a routine-context grant for a built-in tool never reaches a run's allow list. A refused decision is rejected before anything is written: the request stays pending and the run is not resumed. `allow_once` for an application tool writes a grant scoped to that one task — the allow-list build skips stored task permissions on allow-all autonomy, so without it the approval would not reach the agent (`server/internal/pipeline/spawner.go`, `server/internal/mcpapps/resolve.go`).
- Decisions saved for a routine are listed under **Saved decisions** on that routine in Schedules, and each can be revoked there; they also appear in Settings → Grants with context `routine`.

### Obsidian's TLS trust model

The Obsidian Application (`server/internal/apps/obsidian`) talks to Obsidian's
Local REST API, which serves HTTPS with a self-signed certificate — the
common workaround is `curl -k`, verification off entirely. `obsidian.Client`
makes that a decision instead of a silent default, through `Config.TLSMode`:

| Mode | What it does | When `NewClient` refuses it |
|---|---|---|
| `verify` | Normal certificate verification against the system trust store. | Never — but only works if you have separately installed and trusted the vault's certificate. |
| `pinned` | Trust-on-first-use: the first certificate seen is trusted and its SHA-256 fingerprint kept in memory; any later connection presenting a different fingerprint is refused. | Never at construction. The fingerprint is in-memory only and lost on restart — a fresh boot re-trusts whatever certificate answers first. |
| `insecure-loopback` | Skips certificate verification outright. | Unless the configured host actually resolves to loopback. That is the one case a network attacker cannot be the party presenting the certificate, which is what makes skipping verification tolerable here and nowhere else. |

Independent of `TLSMode`, the client resolves its configured host to a
single IP exactly once, at construction, and its dialer refuses to connect
to any other host or port afterwards — a DNS answer that changes after that
resolution (rebinding) is never consulted again. This runs *alongside*, not
instead of, the server-wide SSRF guard (`validation.IsBlockedIP`): that
guard blocks loopback on purpose, and Obsidian's API lives on loopback, so
the client carries its own narrow, named dial policy rather than widening
the shared guard to accommodate one application.

**The client is now constructed at boot, from `Settings → Obsidian`.**
`buildObsidianClient` (`server/serverapp/di_obsidian.go`) reads
`obsidian.baseURL`, `obsidian.vaultRoot`, `obsidian.apiKey`, and
`obsidian.tlsMode` from the settings registry after `obsidian.Register` runs.
Nothing set at all leaves the integration off; any one of `baseURL`,
`vaultRoot`, or `apiKey` set without the other two fails the boot outright,
naming the missing keys — a half-configured client would otherwise look
healthy while every request shipped an empty `Authorization` header and
failed 401. `obsidian.apiKey` is registered `Secret: true`
(`server/internal/settings/registry.go`): it is encrypted at rest with the
same AES-256-GCM `internal/secretbox` path the plugin secret mechanism
already used, and reads back as `********` on every surface except
`settings.Service.Secret`, the one accessor `buildObsidianClient` itself
calls to decrypt it. `Client.Read`/`Write`/`Search`/`Delete` still take no
capability repos and enforce nothing themselves — every production caller
(the `POST /api/obsidian/index` trigger, `GET /api/obsidian/graph`,
`POST /api/obsidian/open`, and the four `obsidian_*` MCP tools below)
authorizes through a `memory.Gate` before reaching the client,
so a future caller that reaches the client directly instead would bypass
that gate entirely.

`VaultRoot` is a second, independent boundary: `Read`/`Write`/`Delete`
resolve their note path inside it (`resolveVaultPath`) and refuse anything
that escapes, and the vault's own search endpoint — which is vault-wide by
design — is only ever reached through `Client.SearchUnderRoot`, which drops
results outside the root and rewrites the survivors to the root-relative
form `obsidian_read` accepts. Both callers use it: the index pass and the
`obsidian_search` MCP tool. Returning raw search results would disclose the
names and existence of notes outside the configured root to any holder of a
single `obsidian.search` grant, even though reading them would still be
refused. See [`PRIVACY.md`](../../PRIVACY.md) for what an indexing pass
persists.

**The hub's vault graph and its *Open in Obsidian* action are gated by
`memory.read`.** `GET /api/obsidian/graph` and `POST /api/obsidian/open`
(`server/internal/api/obsidian/handler.go`) both call
`memory.Gate.Authorize(memory.read, "", global)` before the vault is
contacted, through the same no-`Asker` gate the index trigger uses, so a
missing grant is a `403`, never a held request. Only a refusal
(`ErrDenied`/`ErrAskRequired`) is a `403`; a failed grant lookup answers
`500` with a generic body instead, never `403` — the gate fails closed
elsewhere (an unknown capability denies, `memory/authorize.go` ~101-103),
but a grant lookup it cannot complete is a server error, not a refusal. The
graph is built from two JsonLogic searches (`Client.Graph`,
`server/internal/apps/obsidian/graph.go`): each note's modification time and
its resolved outgoing links — **no note body is requested or returned** (the
REST plugin may read note content to evaluate the JsonLogic query, but
neither search asks for it back). Both searches are vault-wide upstream, so every note and every
link target is confined to `obsidian.vaultRoot` (`pathUnderRoot`) before it is
returned; a note outside the root, and a link to or from one, never appears.
The agent stream (`/api/agents/stream`) carries each agent's `recentNotes` —
vault-relative paths of notes it read or wrote in the last ten minutes —
without a `memory.read` check, because a capability grant is scoped
(global/project), not per viewer, and the stream already carries the agents'
full shell commands. Note contents and the graph stay behind `memory.read`.
The graph is cached for 60 seconds and rebuilt once for all concurrent
callers. An upstream failure answers `502` with a fixed message, never the
upstream error text, which can carry the vault URL. **The open route refuses
any path the graph does not list** (`404`): Obsidian's `POST /open/{path}`
creates a note that does not exist, so passing a client-chosen path through
would let the dashboard create files in the vault. The check runs against a
**freshly built** graph, not the cached one — a note deleted or renamed in
Obsidian within the last 60 seconds would otherwise still be listed, and
opening it would recreate it empty under the old name. The fresh graph joins
a rebuild already in flight and replaces the cached one. A sub-second window
remains between that check and the open call, because Obsidian's REST API has
no open-only-if-it-exists call. A path containing `#` is refused with `400`
before anything else: Obsidian reads `#` as a heading inside a link, so
`a.md#x` would open, or create, a different note than the one checked. A
listed path is resolved inside `obsidian.vaultRoot` again
(`resolveVaultPath`) before the request is built.

### GitHub's token and repository boundary

The GitHub Application (`server/internal/apps/github`) authenticates with one
fine-grained personal access token, `github.token` — registered `Secret: true`
(`server/internal/settings/registry.go`), so it is encrypted at rest the same
way `obsidian.apiKey` is and reads back as `********` on every surface except
`settings.Service.Secret`, the one accessor `buildGitHubClient`
(`server/serverapp/di_github.go`) calls to decrypt it. **The token's blast
radius is whatever repositories it was actually issued for on GitHub's own
side** — a fine-grained PAT is scoped to a chosen set of repositories at
creation, unlike a classic PAT's account-wide reach. `github.repos`
(`owner/name`, comma-separated) is a second, independent boundary this
project enforces on top of that: `Client.AllowsRepo` refuses any repository
not on the list before a request is built, so a token issued more broadly
than intended is still narrowed by this application's own allow-list —
though that narrowing is enforced here, not on GitHub's side, and does
nothing about a token whose GitHub-side scope was already too wide.

**The allow-list runs before the capability gate, not after.**
`Handler.allow` (`server/internal/api/github/handler.go`) and
`GitHubDeps.authorize` (`server/internal/mcp/tools/github.go`) both check
`Client.AllowsRepo` first, on the same `owner/name` string the client will
act on, and only then call `memory.Gate.Authorize`. A grant that names a
capability but not a repository (`--pattern '*'`) therefore still cannot
reach a repository outside `github.repos` — the allow-list is not a
capability question the Decider could ever be asked to widen. `github_search`
and `GET /api/github/search` apply the same allow-list to the query itself,
appending a `repo:` qualifier per configured repository before it reaches
GitHub, rather than filtering the response afterward — the same reasoning
`obsidian_search`'s `SearchUnderRoot` uses.

**`github.merge` is class `spend`, deliberately, so it needs no `Asker` to
stay closed.** `github.read`, `github.search`, and `github.comment` are
class `reach` — data leaves this machine toward GitHub, or arrives from it,
the same reasoning that puts the `obsidian.*` capabilities and `WebFetch` in
`reach` — which `capability.defaultEffect` sends to `EffectAsk` with no
grant. `github.merge` is class `spend` instead, which `defaultEffect` sends
straight to `EffectDeny`: with no grant, a merge is refused outright, never
held open for a human to click through. This does not depend on whether an
`Asker` is wired at all (see the table above) — under `KONTOR_AUTH=none`
an `ask` decision fails closed anyway, but a merge with a `reach` class
would still surface as a held request the moment `KONTOR_AUTH` and an
asker were both configured, and a merge is not the decision this project
wants one tired approval away from happening by accident. The GitHub tile's **Merge**
button calls that same gated route and is refused the same way without a
grant; it adds a second barrier of its own rather than removing one, replacing
itself with a confirmation that names the repository, number and title, so a
stray click cannot merge. `Client.do`
(`server/internal/apps/github/client.go`) builds every error from the
response's own status and message, never from the request, so the token
cannot ride along in something a user is likely to paste somewhere public;
**`github.tokenSource = gh-cli` removes the stored secret, and nothing else.**
With it the token is read from `gh auth token` at each start and never written
to the settings table, so the dashboard's database holds no GitHub credential
and a database copied off this machine carries none. That is the whole of the
gain. It does **not** reduce what an agent on this machine can reach: an agent
with Bash runs as the same OS user that `gh` stores its credential for, so it
can invoke `gh auth token` itself whether or not the dashboard does. Nor does
it narrow the token's own scope — a `gh` session is typically authorised for
every repository its account can see, which is wider than a fine-grained PAT
limited to one repository, so the two choices trade a stored secret against a
smaller blast radius and neither dominates. The command is hardcoded in
`server/internal/apps/github/ghcli.go`: no setting, request or environment
value reaches its argument list, which is why this path is not a way to run a
configured command.

`buildGitHubClient` is the one place in the server that holds the token
decrypted, and its own doc comment states the same rule for itself: nothing
in that function may put the token in an error or a log line.

## Reporting a vulnerability

Please report security issues privately via [GitHub Security Advisories](https://github.com/lx-wnk/kontor/security/advisories/new) rather than opening a public issue.
