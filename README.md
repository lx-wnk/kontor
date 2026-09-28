<div align="center">

# Kontor

**A real-time monitoring and control plane for locally running Claude Code agents.**

See every running agent at a glance — tokens, cost, status, tools, tasks, and subagents — then send instructions, spawn new agents, and drive a multi-stage task pipeline, all from a local browser tab.

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Vue 3](https://img.shields.io/badge/vue-3-4FC08D?logo=vue.js&logoColor=white)](https://vuejs.org/)
[![pnpm](https://img.shields.io/badge/pnpm-10-F69220?logo=pnpm&logoColor=white)](https://pnpm.io/)
[![Status](https://img.shields.io/badge/status-active-brightgreen)](https://github.com/lx-wnk/kontor)

</div>

<p align="center">
  <img src="docs/assets/hero.png" alt="Kontor — live agent roster with permission triage band, per-agent cost, and system footer" width="900">
</p>

## Why Kontor

Most agent monitors require you to wire hooks or wrappers into every project. This one doesn't — it reads what Claude Code already writes to disk and watches the processes you already run.

- 🔌 **Zero-config monitoring** — discovers agents by scanning processes (`ps`/`lsof`) and reading `CLAUDE_CONFIG_DIR` from them to tell profiles apart; no per-project hooks or wrappers
- 🔁 **Autonomous task pipeline** — a real state machine that spawns a `claude` CLI process per stage in an isolated git worktree
- 🎛️ **MCP control plane** — an authenticated MCP endpoint with scoped tokens lets agents (and you) drive the dashboard back
- 🔐 **Auth & permissions** — GitHub OAuth + JWT, scoped API keys, and a capability gate: one shared decision model (allow/deny/ask, ranked across six context levels) that resolves every spawned task-pipeline agent's permissions (still only the task level for spawns) and, separately, every memory read/write (up to two levels: a project/application scope plus the global fallback); a pipeline stage run is now also spawned with its own per-stage-run MCP credential, so its memory and Obsidian tool calls carry task (and, when routine-produced, routine) context into that same model instead of resolving as an unattributed machine-wide key — see [Security model](docs/guides/security.md) for which of its three enforcement points are actually wired in and which fails open
- 🙋 **Answer prompts where you are** — opt-in hooks let you approve or deny a session's permission prompt from the dashboard instead of its terminal, for any session including ones you started by hand; when nobody answers, it falls back to the terminal prompt untouched
- 🏠 **Local-first** — binds to `127.0.0.1`, hashes tokens, makes no outbound calls unless you opt in. No telemetry, no SaaS

> 📄 [Privacy policy](./PRIVACY.md) · 🔒 [Security model](docs/guides/security.md)

## Features

**The Zentrale** — the default view is a hub: what needs you sits in the centre, live work and the agents to the left, GitHub, the pipeline, memory and today's cost to the right, and the **Kontor session** in a tile below. Every tile can be moved, resized, swapped, added or removed with **Edit layout** — by pointer or keyboard — and the arrangement is stored on the server (the `workspace.layout` setting) so the desktop window and a browser agree. The Kontor tile collapses to one row showing the session's state and last output; a click or `/` grows it for input, Escape collapses it. The session itself is a Claude session in auto permission mode that can read and steer Kontor through its task API, shown as a chat with the same header, transcript and prompt as the agent modal. Before a session runs, the input shows the reading it will act on before you press Enter — navigate, START KONTOR, or SEND TO KONTOR. Text that is not a view name starts the session as its first prompt; after that, the chat's own prompt talks to it; the session creates a backlog item only when asked and picks the project from context. **New** ends the session and starts a fresh one, **End** ends it. There is one session per installation; it survives a view switch, a reload and a server restart.

**The hub** — the Zentrale's centre tile is a zoomable live map rather than a panel: an orbit of every running agent grouped into sectors by project, drawn over the Obsidian vault's notes and links as the hub's *brain*. Both share one coordinate system — angle is the sector, distance from the core is freshness (a note's age since its last change; an agent never sits closer than a fixed on-screen radius, closer still while it waits on you). Wheel or pinch zooms around the pointer, dragging pans, and three semantic levels switch at fixed thresholds as you zoom in — **Overview** (agents and sector names, notes as small dots, freshness rings), **Topics** (notes grow, each sector's most-linked notes get a label, halos mark notes touched today), **Notes** (every note's title, greedily kept or dropped by priority so labels never overlap) — with the core, agents, launchers and labels holding a constant on-screen size at every level so they read as landmarks. Agent labels thin out the same way when a sector is crowded: the agents that need you are placed first, then the working ones, and a label is dropped rather than drawn over another label, over another agent's dot, or over a sector name. A dropped label returns on hover or keyboard focus, and the dot, the agent's accessible name and the `L` list always carry every agent. Keyboard, while the hub has focus: arrows pan, `+`/`-` zoom, `0` fits everything, `F` widens the hub tile to the page's full width for this window (navigating to another view ends it too), `L` opens the same content as an accessible list (agents, recently touched notes, the launchers — which ring the map when it is wide enough for them and dock into a rail when it is not), `1`-`8` fire a launcher, `Esc` closes the topmost layer and otherwise fits everything. A minimap in the corner shows the whole space and the viewport; clicking it flies there, as does clicking a sector, an agent, or a note. Clicking a note opens a card with its title, vault path, last-changed time, link/backlink chips that fly to their target, **Ask Kontor about this** (opens the Kontor tile with the note's `[[wikilink]]` prefilled) and **Open in Obsidian**. Dashed and dotted lines connect an agent to the notes it read or wrote in the last ten minutes, read from its Obsidian MCP calls and `curl …/vault/…` commands, and fade as the touch ages; the `L` list names the same notes under each agent. The brain needs an Obsidian vault configured (**Settings → Obsidian**, see [Obsidian vault](#obsidian-vault) below) and a `memory.read` grant; the orbit still runs on its own when either is missing, with a hint naming which: **Connect Obsidian to see your notes here** when the vault isn't configured, or that memory reads aren't granted when it is configured but `memory.read` isn't. The memory tile lists the vault's most recently touched notes, and the command palette (`Cmd+K`) finds a note by title and flies to it in the hub.

**Monitor** — real-time agent roster over SSE (list, card, kanban) with tokens, cost, status, and uptime; live active-subtask metrics (token usage, duration, latest output) on every agent and task card; chat-style transcript with collapsible tool groups and subagent badges; `Cmd+K` spotlight search and n-gram pattern discovery. The same `Cmd+K` field also runs navigation commands, and — when your text matches nothing at all — hands it to the Kontor session and switches to the Zentrale.

A **Working** indicator shows when an agent is actively generating, rather than just recently active. It's inferred from whether the agent owes the next reply (conversation turn-state) together with live session output from tmux or the pty broker, so it's distinct from the staleness-based active/waiting/idle states.

When a controllable (channel/MCP-connected) agent exits, its card stays on the dashboard as a **Finished** card in the card grid view instead of vanishing — click it to view the final result, or click ✕ to dismiss it (which removes the agent's channel discovery file). Finished cards are tracked per server run, so after a dashboard restart they're gone; reach those sessions through the **Sessions** overview instead. Agents started in a plain terminal without the dashboard channel still disappear when they exit.

**Build & control** — multi-stage task pipeline (`concept → implementation → review → done`) running in isolated git worktrees behind per-stage permission gates; per-stage engine and model selection (global and per-project), plus a per-stage reasoning-effort setting for the `claude` adapter (`low` through `max`, reaching the spawned CLI process as `--effort`) — always shown, disabled with a reason for adapters that don't support it; spawn agents and send follow-ups or `/btw` interrupts via MCP channels; refinement chat to shape a concept first; an opt-in plan-review stage that auto-generates and self-reviews the implementation plan and gates on your approval before any files are edited; seed a task straight from a GitHub or Jira issue — paste a reference into the New-Task form's **Import from issue** field to pre-fill the title and description (tracker tokens are stored encrypted and managed under **Settings → Tracker**); shared scratchpads and lease-based locks for agent coordination (`agent:coord` MCP scope); per-turn worktree checkpoints with one-click revert (a debounced filesystem watcher snapshots each agent turn into a hidden git ref; revert restores the worktree and parks the task for manual resume); drag-and-drop task reordering.

Agents spawned from the dashboard run as interactive **live** sessions rather than one-shot runs, so you can converse with them as they work. Live injection uses tmux when it's available (the agent runs in a detached session) and falls back to a built-in pty broker otherwise. The spawned agent's chat modal opens automatically once it appears on the roster.

A live-injectable session's agent modal has a **Terminal** tab — a real `xterm.js` terminal streamed over a WebSocket (`GET /api/agents/{pid}/terminal`), so you see the session's actual pty output and can type into it directly. When the session asks an interactive multiple-choice question (`AskUserQuestion`), an overlay detects it from the live terminal screen and renders the options inline — pick them and **Send answer** drives the session's selector with real keystrokes over the same WebSocket. A multi-question flow's closing **review/submit** screen ("Ready to submit your answers?") is detected the same way, so the whole round can be completed without touching the terminal. Because it drives the pty directly, this works over any transport (tmux or the built-in pty broker). The same cards appear in the main needs-you band, backed by server-side detection of the session's rendered screen.

**Extend** — authenticated MCP control plane with scoped tokens; in-dashboard `~/.claude` config explorer and git-worktree panel; frontend plugin slots and pluggable LLM adapters (OpenAI-compatible, Ollama, `anthropic`); Web Push, webhook, and email notifications. Plugins are enabled and disabled **live** from **Settings → Plugins** via `POST /api/plugins/{id}/activate` and `/deactivate` — no server restart needed for most plugins. Exception: plugins with the `auth_provider` capability are boot-wired (they affect server startup) and require a restart to take effect — after toggling one, the panel shows a "Restart required to apply" badge and a **Restart server** button; clicking it triggers the restart and a reconnect overlay polls until the server is back, then reloads the page automatically. Per-plugin settings with schema-defined fields (string, URL, integer, boolean, enum) are editable in the same panel; secret fields are masked and unchanged secrets are preserved on save. Route and UI extension plugins are reverse-proxied through a single catch-all at `/api/plugins/{id}/proxy/*`; a stopped or crashed plugin returns HTTP 503 rather than silently disappearing. A system-owned **memory** store lets agents and you persist facts, preferences, and lessons that outlive one task (`memory_search`/`memory_write` MCP tools, `/api/memory/*` HTTP routes, both gated by the capability model above) — a budgeted, ranked extract is automatically appended to every stage's spawn prompt. **Settings → Registry** lists the registry's own rows — applications, routines, skills and memory spaces — by kind and scope, over a read-only `GET /api/resources`; the memory-spaces kind needs a `memory.read` grant in the scope being viewed, the same one `/api/memory/spaces` requires. Routines are the scheduled tasks, projected from `task_schedule` rather than mirrored into the registry table, and the id each row reports is the one a `--scope routine:<id>` grant names — today that grant decides the automatic memory push into the tasks that routine fires, plus any memory/obsidian MCP tool call a pipelined stage run for that task makes (see [Security](docs/guides/security.md#capabilities-and-the-permission-gate)). **Settings → Memory** browses the spaces in a scope, opens any one of them to list its entries, searches entries by text, and creates, supersedes and expires them — a space has to be openable because an empty search query deliberately returns nothing, so search alone could never show what a space holds; the task modal's **Stages** tab shows what each stage run's spawn actually received — entries pushed, characters spent against the budget, and how many candidates the ranker chose from — read from `GET /api/memory/injections`, which gates on `memory.read` at global scope. See [Privacy policy](./PRIVACY.md) for what it stores and how it's removed. An Obsidian vault is registered as a resource-registry **Application** (`server/internal/apps/obsidian`) with four gated capabilities (read/search/write/delete): configure it from **Settings → Obsidian**, then reach it through `POST /api/obsidian/index` (turns vault notes into memory pointers) or four capability-gated `obsidian_*` MCP tools that read, search, write, and delete notes directly — see [Obsidian vault](#obsidian-vault) below for what it takes to actually turn it on. GitHub is registered as a resource-registry **Application** (`server/internal/apps/github`) the same way, with four gated capabilities — `github.read`, `github.search`, `github.comment`, `github.merge` — of which `github.merge` is class `spend`, so it is denied outright without an explicit grant rather than surfaced as a prompt; configure it from **Settings → GitHub** (`github.token`, `github.repos`, `github.baseURL`), then reach it at `GET /api/github/summary`, `GET /api/github/search`, `POST /api/github/comment`, `POST /api/github/merge`, or as the four capability-gated `github_*` MCP tools. The GitHub tile shows each open pull request's check-run state and offers a **Merge** button behind a confirmation that names the pull request; it calls the same gated route, so it needs a `github.merge` grant like any other caller — see [GitHub](#github) below for the allow-list, the grant commands, and the GHE-on-LAN limitation.

**Supported agents / providers** — Claude Code is monitored by default. Codex CLI, Gemini CLI, and Junie CLI can be monitored too, but are opt-in. Enable or disable them from **Settings → Providers** in the dashboard UI — the change is persisted and takes effect within a few seconds without a restart. You can also set `KONTOR_PROVIDERS_ENABLED` to a comma-separated list of ids (`codex`, `gemini`, `junie`) as a fallback when no database row exists, or drop your own descriptor YAML files in a directory pointed to by `KONTOR_PROVIDER_DIR`. Agents using a local Ollama-served model show a cost of **$0** ("local") instead of "unknown".

See [`docs/`](docs/README.md) for the full feature reference.

## Quickstart

**Prerequisite:** [Claude Code](https://claude.ai/code) installed and run at least once — the dashboard reads the session data it writes to `~/.claude`. macOS and Linux only.

### Install (no build tools needed)

**One-liner (macOS / Linux):**
```sh
curl -fsSL https://raw.githubusercontent.com/lx-wnk/kontor/main/install.sh | sh
```

Or use Homebrew (macOS):
```sh
brew install lx-wnk/tap/kontor
```

Then:
```sh
kontor serve
```

Open **http://localhost:13120** — any running Claude Code agents appear automatically. On loopback with no OAuth configured, the dashboard runs in local-trust mode (no login). See [Security](docs/guides/security.md) before exposing it anywhere.

See [docs/guides/install.md](docs/guides/install.md) for Docker, manual binary download, and all options.

### First-run setup

On first launch, a guided setup flow opens automatically and walks you through the fastest path from install to a controllable session: (1) detects the Claude Code CLI and shows its version, or the install command if it's missing; (2) connects the dashboard to Claude with one click by registering it as an MCP server in your Claude config (`claude mcp add --scope user …`), with a copy-the-command fallback if that fails; (3) discovers your existing Claude sessions and lets you make one controllable with one click, so it becomes answerable from the dashboard. Skip it and it won't show again on its own. You can re-open the same guided flow at any time from Settings → API Keys via the "Re-run first-run setup" button.

### Develop / build from source

Requires Go 1.26+, [Task](https://taskfile.dev), [air](https://github.com/air-verse/air), Node.js 22+, and [pnpm](https://pnpm.io/installation).

```bash
git clone https://github.com/lx-wnk/kontor.git
cd Kontor
pnpm install        # frontend dependencies (Go deps are fetched on first build)
task dev            # Go backend (air hot-reload) + Vite — serves on :13120
```

When iterating on the UI, run `pnpm dev` in a second terminal for HMR on `:5173` (it proxies `/api` → `:13120`). See [CONTRIBUTING.md](./CONTRIBUTING.md) for the full dev setup, the production-build steps, and the command reference.

## Desktop app (macOS)

A native macOS shell (`desktop/`, [wails](https://wails.io) v2) wraps the same dashboard as one binary — no separate server process, no sidecar. It starts the dashboard HTTP server in-process on `127.0.0.1:13120` and opens a native WKWebView window pointed at it, so it's the identical Vue SPA you get in a browser tab, just packaged as an app. Other platforms keep running `kontor serve` in a browser; the desktop shell is macOS-only.

Build and run it for a smoke test (requires macOS + Xcode command-line tools; no `wails` CLI needed for this):

```bash
task desktop:run
```

That builds the SPA, embeds it, and links the shell with the wails production tag and the `UniformTypeIdentifiers` framework (a plain `go build` omits both — see [CONTRIBUTING.md](./CONTRIBUTING.md#desktop-shell-macos)), then launches the window.

An unsigned `.app`/`.dmg` build (`task desktop:dist` / `task desktop:dmg`) plus the full signing and
notarization steps are documented in [docs/desktop-distribution.md](docs/desktop-distribution.md).

**Manual smoke checklist** (real Mac):

1. The webview loads the dashboard (not a blank page) — it fetches the SPA and `/api/*` from `http://127.0.0.1:13120`, so the redirect landed on the loopback origin.
2. A mutating action (spawn an agent, create a task, answer a question) succeeds with no `403`.
3. No App Transport Security block appears in Console.app.

## Restart

`POST /api/admin/restart` triggers a validated, graceful restart. The endpoint refuses with **409** if an active `auth_provider` plugin is currently unhealthy — restarting in that state would cause an auth lockout on the next boot.

**Activating an `auth_provider` plugin requires a restart to apply** (auth is boot-wired, not live-reloadable).

**Default (`KONTOR_RESTART_MODE=reexec`):** the process re-execs itself in place — same PID, no supervisor needed. Works with plain `./bin/agent-dashboard serve`.

**Rebuilding first.** A plain restart re-execs the *same binary on disk*, so it never picks up a merged change. Send `{"rebuild": true}` — or use **Rebuild and restart** in **Settings → Server** — and the server builds first and only relaunches if that succeeded. A failed build answers **500** with the last 40 lines of output and leaves the running binary untouched. The build command is fixed in Go; nothing from the request reaches it.

The status bar warns when the running build is older than the source it was built from, comparing the binary's stamped version against `git describe` in the server's working directory. If either is unknown, no warning is shown.

**Supervised (`KONTOR_RESTART_MODE=exit`):** the process exits cleanly so the supervisor relaunches it.

| Supervisor | Required config |
|---|---|
| systemd | `Restart=always` in the service unit |
| launchd | `KeepAlive` in the plist |
| Wrapper loop | `while true; do ./bin/kontor serve; done` |

## Locked out?

A bad `auth.mode` or a broken `auth_provider` plugin can lock you out of the UI. The CLI edits the SQLite database directly, so it works even while the server is down:

```bash
kontor settings set auth.mode none   # reset auth, then restart the server
kontor grants add memory.read --pattern '*' --scope global --mode allow
```

See [Configuration](docs/guides/configuration.md) for the full settings/grants/plugins CLI reference.

## Obsidian vault

Point the dashboard at a local [Obsidian](https://obsidian.md) vault, via the
[Local REST API](https://github.com/coddingtonbear/obsidian-local-rest-api) community plugin, from
**Settings → Obsidian**: base URL, vault root, API key, and TLS mode. All four settings apply as
soon as they are saved, and `baseURL`/`vaultRoot`/`apiKey` are a required trio: set all three, or
none. With only one or two set the vault stays off, and the next start **fails** and names the missing keys, rather than
booting with the vault silently disabled — a vault you configured and that quietly does not run is
worse than a refused start. Clearing all three in the panel (the API key field included) turns the
integration back off.

Once configured, **Index now** in the same panel (or `POST /api/obsidian/index`) turns vault notes
into searchable memory pointers — but only once the `obsidian.read`, `obsidian.search`, and
`memory.write` capability grants exist. A fresh install denies the run otherwise:

```bash
kontor grants add obsidian.read --pattern '*' --scope global --mode allow
kontor grants add obsidian.search --pattern '*' --scope global --mode allow
kontor grants add memory.write --pattern '*' --scope global --mode allow
```

(or the same three from **Settings → Grants**). Agents can also reach the vault directly — read,
search, write, and delete a note — through four MCP tools gated the same way; see
[MCP endpoint](docs/guides/mcp.md#scopes) for the scopes and
[Security](docs/guides/security.md#obsidians-tls-trust-model) for the TLS trust model.

## GitHub

Point the dashboard at GitHub from **Settings → GitHub**: a fine-grained personal access token, a
comma-separated `owner/name` repository allow-list, and a base URL (defaults to
`https://api.github.com`; override it for GitHub Enterprise). All three apply only after a server
restart, and the token and the repository list are a required pair — set both, or clear both. A
half-set pair fails the next start and names the missing key, rather than booting with the
integration silently disabled; the base URL is not part of that pair, since it always carries a
default and so is never a missing half of anything.

**Or no token at all.** Set **Token source** to *From the GitHub CLI* (`github.tokenSource =
gh-cli`) and the dashboard reads the token from `gh auth token` at each start instead of holding
one. The token field disappears, the pair rule reduces to the repository list alone, and nothing is
stored: the credential stays in the GitHub CLI's own store. A `gh` that is missing or logged out
fails the next start and says which, rather than booting into GitHub calls that all answer 401. This
narrows what is *stored*, not what an agent on this machine can *reach* — an agent with Bash runs as
the same OS user and can invoke `gh` itself either way.

Once configured, the **GitHub** tile reads open pull requests from
`GET /api/github/summary`: each allow-listed repository's own open pull requests, merged with
whatever the `involves:@me` search finds you across every repository your token can see, deduped,
sorted by most recently updated, and capped at 20. `GET /api/github/search`, `POST /api/github/comment`, and
`POST /api/github/merge` reach the same allow-listed repositories — every route bounded to
`github.repos` before it is bounded by a capability at all. Each route, and its matching `github_*`
MCP tool, is gated by one of four capabilities. A fresh install denies all four by default:

```bash
kontor grants add github.read --pattern '*' --scope global --mode allow
kontor grants add github.search --pattern '*' --scope global --mode allow
```

`github.comment` and `github.merge` are deliberately not on that list. Posting a comment is public
and irreversible the moment it lands, so it is not something an initial setup script should hand out
sight-unseen — grant it explicitly, the same way, once you actually want an agent to comment.
`github.merge` is class `spend`, so with no grant it is denied outright rather than surfaced as a
prompt, regardless of whether you ever add one to that list — see
[Security](docs/guides/security.md#githubs-token-and-repository-boundary) for why. See
[MCP endpoint](docs/guides/mcp.md#scopes) for the `github:read`/`github:write`/`github:merge` scopes.

**GitHub Enterprise on a LAN address is not reachable today.** The client dials through the same
SSRF guard the rest of the server uses (`validation.SafeDialContext`), which refuses loopback,
private, link-local, and CGNAT addresses at connection time — so a GHE host on `10.x`/`192.168.x`
fails to dial, with no clearer error than a failed connection. Widening the shared guard for one
application was rejected the same way it was for Obsidian's own TLS trust model; the fix, if this is
ever needed, is a narrow per-client dial policy, not a change to `validation.IsBlockedIP`.

## Documentation

| Topic | |
|---|---|
| [Install](docs/guides/install.md) | Download, Homebrew, Docker, build from source |
| [Configuration](docs/guides/configuration.md) | Every environment variable |
| [MCP endpoint](docs/guides/mcp.md) | Scopes, tools, local integration |
| [Controlling & spawning agents](docs/guides/agent-control.md) | Channels, spawn dialog, permissions |
| [Security](docs/guides/security.md) | Threat model & hardening |
| [Architecture](docs/architecture/overview.md) | Stack, packages, data flow, pipeline |
| [Shell statusline](docs/guides/statusline.md) | PS1 integration |
| [Agent skills](docs/guides/agent-skills.md) | Registry-owned skills and how they reach disk |

Full index: [`docs/`](docs/README.md).

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](./CONTRIBUTING.md) for setup, the full command reference, the PR process, and code guidelines. In short:

```bash
task test           # all Go tests, race detector
task lint           # golangci-lint
pnpm typecheck      # vue-tsc
```

Found a bug or have an idea? [Open an issue](https://github.com/lx-wnk/kontor/issues/new/choose).

## License

[MIT](./LICENSE) © Alexander Wink
