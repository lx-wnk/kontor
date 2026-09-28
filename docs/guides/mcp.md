# MCP Endpoint

The dashboard exposes a stateless StreamableHTTP MCP server at `POST /api/mcp` for external agent control. Each request is self-contained — there is no server-side session map.

## Authentication

```
Authorization: Bearer mcp_<hex>
Accept: application/json, text/event-stream
```

Generate tokens in **Settings → API Keys**. Only the SHA-256 hash is stored; the raw token is shown once at creation and never again.

## Scopes

Scopes are hierarchical — a higher scope implies all lower ones.

| Scope | Access |
|---|---|
| `tasks:read` | List and read tasks, stage runs, audit log, permission requests, projects, spawners, schedules |
| `tasks:write` | Create, update, delete tasks; create projects (implies `tasks:read`) |
| `agent:coord` | Scratchpads, lease locks, and port waits shared between agents |
| `pipeline:control` | Progress, approve, cancel, retry tasks; manage permissions; refine and plan gates (implies `tasks:read` and `agent:coord`) |
| `memory:read` | Search and read entries from the system memory store |
| `memory:write` | Write entries to the system memory store |
| `obsidian:read` | Read and search notes in the configured Obsidian vault |
| `obsidian:write` | Create, overwrite, or delete a note in the configured Obsidian vault (implies `obsidian:read`) |
| `github:read` | List open pull requests and search issues/pull requests in the configured GitHub repositories |
| `github:write` | Post a comment on a GitHub issue or pull request (implies `github:read`; does **not** imply `github:merge`) |
| `github:merge` | Merge a GitHub pull request (implies `github:read`; does **not** imply `github:write`) |
| `keys:manage` | Full access including API key management |

## Tools (51)

**`tasks:read`** — `list_tasks`, `get_task`, `list_stage_runs`, `list_audit`, `list_permission_requests`, `list_projects`, `list_spawners`, `list_schedules`

**`tasks:write`** — `create_task`, `update_task`, `delete_task`, `manage_task`, `add_dependency`, `remove_dependency`, `create_project`, `manage_schedule`

**`agent:coord`** — `write_scratchpad`, `read_scratchpad`, `list_scratchpad`, `acquire_lock`, `release_lock`, `wait_for_port`

**`pipeline:control`** — `advance_task`, `hold_task`, `resume_task`, `progress_task`, `cancel_task`, `retry_task`, `grant_permission`, `resolve_permission_request`, `approve_all_pending`, `get_refine_status`, `approve_spec`, `refine_task`, `inject_concept`, `approve_plan`, `reject_plan`, `get_plan_status`

**`memory:read`** — `memory_search`

**`memory:write`** — `memory_write`

**`obsidian:read`** — `obsidian_read`, `obsidian_search`

**`obsidian:write`** — `obsidian_write`, `obsidian_delete`

**`github:read`** — `github_read`, `github_search`

**`github:write`** — `github_comment`

**`github:merge`** — `github_merge`

**`keys:manage`** — `list_api_keys`, `create_api_key`, `revoke_api_key`

The four `obsidian_*` tools reach the vault configured under **Settings → Obsidian**
(`server/internal/apps/obsidian`); when that vault is not fully configured (`obsidian.baseURL`,
`obsidian.vaultRoot`, and `obsidian.apiKey` are a required trio), none of the four are listed in
`tools/list` or callable via `tools/call`, rather than being offered and always failing. That check
is live, not a startup snapshot: a vault configured (or reconfigured, or turned off) while the
server is running changes what the very next `tools/list` or `tools/call` sees — no restart needed.
`obsidian_write` and `obsidian_delete` are irreversible: a write overwrites any existing note at
that path, and a delete cannot be undone.

The four `github_*` tools reach the repositories configured under **Settings → GitHub**
(`server/internal/apps/github`); when `github.token`/`github.repos` are not both set, none of the
four are registered, for the same reason. `github:write` implies `github:read` but deliberately
**not** `github:merge`, and `github:merge` implies `github:read` but deliberately not `github:write`
— a key that may comment must not be able to merge by accident, and a key that may merge has no
business editing discussions. That scope check is the coarser net above the capability gate:
`github_merge` still needs its own `github.merge` grant, and that capability's class (`spend`)
denies it outright with no grant at all, regardless of scope — see
[Security](security.md#githubs-token-and-repository-boundary).

### Attaching a task to a project

`create_task` takes either a `projectId` or a `projectSlug` — never both. The slug is resolved to
its project and the call fails if no project carries it, so a typo cannot silently produce an
unattached task. When no project matches, create one with `create_project` (slug and name required)
and use the returned id or slug. A project created this way has no folders yet, and the UI's New-Task
form takes its working directory *only* from a project's folders — so that form cannot be submitted
for the new project until a folder is added under **Settings → Projects**. `create_project` says so
in its tool description and returns it as `nextStep` on the created project, so the agent can pass
the handover on; tasks created over MCP are unaffected because `create_task` carries its own `cwd`.

`create_project` accepts `description`, `color`, and `defaultSpawnerId`, but **not** `setupCommand`.
That command is executed as `sh -c` in every worktree the project creates, so `POST /api/projects`
restricts it to admins **when `auth.mode` is not `none`**; the MCP tool omits the field
unconditionally. It is the one tool whose schema declares `"additionalProperties": false`, and the
handler enforces it: any key outside `slug`, `name`, `description`, `color`, and `defaultSpawnerId`
fails the call by name instead of being dropped in silence. `name` is capped at 200 characters and
`description` at 10 000 — the same limits `POST /api/projects` applies, so the rule does not depend
on which door the caller used. Set it in the UI under **Settings → Projects** instead. For the same reason
`list_projects` and `create_project` report `hasSetupCommand` (a boolean) rather than the command
itself: the text is deliberately never put on the wire, because those strings routinely carry
registry tokens and `tasks:read` is enough to call `list_projects`.

A successful `create_project` publishes a `project_created` event on `/api/projects/stream`, the
same channel `POST /api/projects` uses, so the dashboard picks the new project up without a reload.

It is also attributed: an audit event (`project_created`, target `project:<id>`, metadata
`{slug, source: "mcp_create_project"}`) is written, and a `mcp: project created` line is logged with
the slug, the project id, and the id of the API key that made the call. Neither records the name or
description — those are agent-supplied free text.

Each tool checks its required scope at call time and returns an MCP error if the token's scope is insufficient.

## Pipeline agents already have a key

A stage run the dashboard's own task pipeline spawns needs none of the setup below: the spawner
mints a per-stage-run credential and writes it into the agent's `--mcp-config` as a second server,
`dashboard-tasks` (`http`, alongside the stdio `dashboard-channel` server every spawn already
gets) — no hand-made key, no `claude mcp add`. That spawn runs with `--strict-mcp-config`, so
the written file is the agent's whole MCP surface: your user-scope servers from
`~/.claude.json` are copied into it, but a user-scope `dashboard-tasks` registration is
deliberately not — the stage run uses its own narrower credential, never yours. That credential is scoped to the stage run that
holds it, expires with it, and is revoked the moment the run ends; see
[Security](security.md#capabilities-and-the-permission-gate) for what it can do and how long it
lives.

The rest of this page is for **your own** Claude Code client — a session you start yourself in a
terminal — which still needs a key from **Settings → API Keys**, because there is no stage run to
attribute it to.

## MCP applications

An MCP server the dashboard knows is an *application*. The dashboard holds the server definition and its secrets, a pipeline run gets an application only when its routine attaches it, and grants decide which of its tools an agent may call.

### Add a server

**Settings → Applications → Add server**: a name, the command, its arguments and any non-secret environment variables. The server is available to the next run — no restart.

- The name must be a valid slug — lowercase letters, digits and hyphens (`^[a-z0-9][a-z0-9-]{0,63}$`). Anything else is refused with the pattern in the message.
- `dashboard-channel` and `dashboard-tasks` are the dashboard's own servers; those names are refused.
- A name that already exists is refused with `409`.
- Credentials do not belong here. Declare their variable names under [Secrets](#secrets) and store the values there.

The same over HTTP: `POST /api/applications` with `{"name": "mail", "command": "npx", "args": ["-y", "imap-mcp-server"], "env": {"IMAP_HOST": "imap.example.com"}}`. Editing a definition is `PATCH /api/applications/{resourceId}` with `{"entry": {...}}` — it replaces the fields the app knows (`type`, `command`, `args`, `url`, `env`) and keeps any other key the entry carries, so a definition imported from Claude's config does not lose what the CLI wrote there.

**Removing** a server (`DELETE /api/applications/{resourceId}`) revokes the grants on its tools, deletes its secrets and drops the definition. If it was mirrored into `~/.claude.json`, that entry goes with it; a server the app never mirrored stays in the file and is then offered as found. It is refused with `409` while a routine still attaches it, and the message names those routines. The registry row stays behind as `orphaned` so grants anchored to it still resolve.

### Servers registered with Claude Code

Servers that existed in `~/.claude.json` when this version first started were imported once, definition and all, and are marked as mirrored back into that file. Afterwards the app's copy is the one runs use, and the two are compared rather than merged:

- A server in `~/.claude.json` that the app does not know is offered in Settings → Applications as **Found `<name>` — import?**. Importing reads the definition out of that file; the browser never supplies one.
- A server the app mirrors whose entry in that file was edited elsewhere is shown as **Changed outside the app**, with two ways out: *Take the change* stops mirroring, so the edit stands, and *Write the app's version back* overwrites it with the stored definition.
- The dashboard watches the file and refreshes the panel when it changes, so neither banner needs a restart to appear. `GET /api/applications/drift` returns the same two lists. A config that cannot be read reports neither — no information is not evidence of an edit.

### Also available in Claude Code sessions

The switch on an application writes its definition into `~/.claude.json` as `mcpServers.<name>`, so a plain `claude` session sees the same server. The dashboard changes only that one key, keeps every other key and the file's mode, writes through a temporary file in the same directory, and refuses to write through a symlinked config. Secrets are never written: the exported entry is the stored definition, not the one a run gets with its secrets merged in.

### Routine run modes over MCP

`manage_schedule` takes `runMode` on `create` and `update`: `job` (the default) fires one agent run in the routine's working directory, `pipeline` fires a pipeline task that starts in `ready`. A `pipeline` routine needs a git working directory — the tool refuses anything else with `working directory is not a git repository`, and an unknown value with `runMode must be job or pipeline`, exactly as `POST`/`PATCH /api/schedules` do.

### Which runs get a server

A run receives the dashboard's own servers, every application marked **Attach to every run**, and the applications of the routine that created its task — nothing else from `~/.claude.json`.

- Servers that already existed when applications were introduced are marked **Attach to every run**, so existing setups keep working. Servers added later start unattached. Change it per application in Settings → Applications.
- To attach an application to a routine, tick it under **Applications** in the routine form (**Build → Schedules**), or send `applications` — a list of application resource ids — with `POST` or `PATCH /api/schedules`. An unknown id is rejected with `400`.
- The scheduler copies the routine's list onto every task it creates. The task API does not accept `applications`; a task gets applications only from its routine.

### Secrets

In Settings → Applications, add the environment variable names the server reads its credentials from with **Add variable** (names match `^[A-Z_][A-Z0-9_]*$`), then enter a value for each. A server that takes per-account passwords from the environment might, for example, need `IMAP_MCP_ACCOUNT_OVH_IMAP_PASSWORD` and `IMAP_MCP_ACCOUNT_OVH_SMTP_PASSWORD`.

- Values are encrypted with AES-256-GCM using the dashboard's secret key and never returned by any API; the panel only shows whether a value is set and when it last changed. Without a configured key, storing a value fails with `503`.
- At spawn, the values are written into the `env` of that application's entry in the run's temporary MCP config. No other server in the run sees them.
- If an attached application is missing a required value, or has no server definition yet, the stage run **fails before the agent starts**, with a reason beginning `MCP applications:`. Add the value or the definition and retry the task.
- An application reached only through **Attach to every run** and still without a definition is skipped silently instead, so one half-finished application cannot block every run in the system.

The same over HTTP: `PATCH /api/applications/{resourceId}` with `{"requiredEnv": [...]}` or `{"attachAll": true}`; `PUT /api/applications/{resourceId}/secrets/{NAME}` with `{"value": "..."}`; `DELETE` on the same path.

### Tools and grants

**Refresh tool list** (`POST /api/applications/{resourceId}/refresh`) starts the server from the definition stored in the app, with its secrets, and reads its tool list. Only stdio servers — entries with a `command` — are supported; `http` and `sse` servers are refused with an error. A failed refresh keeps the previous list and shows the error.

Each tool becomes a capability named the way Claude Code names it, `mcp__<server>__<tool>`, with class `tool`. A later refresh never changes the class of a capability that already exists. The server's `readOnlyHint` and `destructiveHint` are shown for orientation only; the MCP specification says clients must not trust them.

Refresh before granting — the grants API rejects capability names it does not know. Then:

```bash
kontor grants add mcp__mail__search_emails --scope routine:<schedule id> --mode allow
kontor grants add mcp__mail__send_email --scope global --mode deny
```

Grants resolve from the most specific context: task, routine, project (the task's working directory), global. An allow lands in the run's `--allowedTools`, a deny in `--disallowedTools`.

**Dangerous tools are denied without being asked about.** A shipped preset lists the tools of a known server that must never run unattended — for `imap-mcp-server` that is sending, replying, forwarding, deleting, attachment download and upload, account and spam-list changes. The preset is found by what the server actually runs: its `match` string is looked for in the application's command line, so a definition is recognised without anyone naming a preset. Its denies are written as global `deny` grants when the application is added, when it is imported, and again on demand with `POST /api/applications/{resourceId}/denies`, which answers with the capability names it `created` and those already `existing`. It is idempotent, and it denies a tool the last refresh did not list — the window between adding a server and reading its tool list is exactly when an unguarded tool would be reachable.

Allows are not part of a preset. Everything the denies do not cover asks on first use, and the answer — once, always for this routine, always deny for this routine, deny once — is what writes the grant. A request for a tool that is already denied carries no decision at all: the needs-you card says so and points at Settings → Grants, because answering it would change nothing — the run's allow list is built from the same resolution.

Settings → Applications lists every tool with what it resolves to today: **denied by default**, **asks on first use**, or **allowed**, plus the contexts a live allow grant names (`routine:<id>`, for instance). The server's own `readOnlyHint` and `destructiveHint` stay visible beside it and stay untrusted.

### Setting a server up

A preset may also declare a `setup` block — a command, its arguments with a `{port}` placeholder, and a readiness path. **Set up** in Settings → Applications then starts that command on a free port, passing the application's own environment and its stored secrets, and shows the server's own configuration page in a sandboxed frame once the readiness path answers.

- The port is also handed to the child as `SETUP_PORT`.
- One setup runs per application; starting a second replaces the first.
- It stops on **Done**, on fifteen minutes idle, after thirty minutes at most, and when the dashboard shuts down.
- **The setup page is reachable on your local network while it runs** — the panel says so for as long as the process lives. Click Done when you are finished.
- `POST /api/applications/{resourceId}/setup` answers the session, `GET` returns it, `DELETE` stops it. A server without a setup block answers `409`; a command that fails to start or never becomes ready answers `502` with the tail of its own stderr.

After a setup, `POST /api/applications/{resourceId}/accounts` asks the server which accounts it now knows and answers with the **variable names** those accounts need — `{"names": ["IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD", …]}`. The accounts themselves are not part of the answer and nothing is stored: the panel renders one write-only password field per name, which you fill as any other secret. The names are built the way the server builds them, upper-casing the account name and replacing every character outside `A-Z0-9` with an underscore. This call reaches the server's own tool directly and consults no grant — an operator clicked it, no agent is involved.

**No grant is not an allow.** Pipeline runs are headless, so an ungranted tool is refused; the agent can ask through a permission request, and a human's approval is honoured for that task. Allow-all autonomy (`spec_gated`, `full`) does not allow application tools.

## Connect the dashboard to Claude

The fastest way to wire a Claude Code session to the dashboard's task tools is the one-command
method available in the key dialog.

### One-command setup (recommended)

1. Open **Settings → API Keys** in the dashboard.
2. Click **+ Add Key**, choose the **Developer** or **Admin** role, and click **Create Key**.
3. In the token-reveal dialog, find the **CLI command** block — it shows a ready-to-run command
   like:
   ```sh
   claude mcp add --scope user --transport http kontor \
     http://127.0.0.1:13120/api/mcp \
     --header "Authorization: Bearer mcp_<your-token>"
   ```
4. Click the copy button, then run the command in your terminal. It writes the MCP server config to
   `~/.claude.json` at user scope — every Claude Code session you open will auto-connect to the
   dashboard.

The `--scope user` flag makes the connection global (all sessions). To scope it to one project
only, replace `--scope user` with `--scope project` — this writes to the project's `.mcp.json`
instead.

> **Verify the exact `claude mcp add` flags for your Claude Code version** by running
> `claude mcp add --help`. The flags above match the HTTP-transport syntax (`--transport http`,
> `--scope <local|user|project>`, `--header`) as of Claude Code 2025. If a flag name differs, adapt
> accordingly and update this doc.

### Make the session controllable (`kontor live`)

The MCP connection above lets a session **report to** the dashboard (task tools, replies,
permission requests). To also **control** a session from the dashboard — answer its
AskUserQuestion prompts, inject prompts, drive it from the Terminal tab — start it with:

```sh
kontor live -- <your usual claude args>
```

`live` runs your normal, interactive Claude session (it proxies your real terminal, so you use it
exactly as before) but wraps it so the dashboard owns an input path to it: it auto-loads the
channel MCP and picks a transport automatically — inside/with tmux it uses the tmux pane, otherwise
a built-in pty broker (no tmux required). Either way the session becomes **live-injectable**: its
AskUserQuestion prompts surface as answerable cards in the needs-you band and Terminal tab, and you
can push prompts to it. Add `--yolo` to skip permission prompts.

Sessions the dashboard **spawns** for you already run this way. A plain `claude` you started
yourself (not via `live`, not in tmux) is monitor-only — the dashboard can see it but has no input
path. This cannot be retrofitted onto an already-running session (a session's terminal is owned at
launch); relaunch it via `kontor live` to make it controllable.

### Manual / JSON config alternative

If you prefer to manage the config file directly, add an entry to your `.mcp.json` (project-scoped)
or `~/.claude.json` (user-scoped):

```json
{
  "mcpServers": {
    "kontor": {
      "type": "http",
      "url": "http://127.0.0.1:13120/api/mcp",
      "headers": {
        "Authorization": "Bearer mcp_<your-token>"
      }
    }
  }
}
```

`.mcp.json` is gitignored to prevent accidental token commits. The JSON block is also available in
the key dialog's **JSON config** block for one-click copy.
