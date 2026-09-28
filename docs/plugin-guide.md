# Plugin Developer Guide

Agent-dashboard supports a lightweight sidecar plugin system. Each plugin is an
independent HTTP server that the dashboard discovers at startup, health-checks, and
optionally spawns as a child process.

---

## Build your first plugin

The fastest path: read the [plugin SDK quickstart](../plugin-sdk/README.md), copy
`plugins/voice-whisper` as a template, add your `plugin.json`, and point
`DASHBOARD_PLUGIN_DIR` at the parent directory. The sections below cover every
field and capability in detail.

---

## What is a plugin?

A plugin is a standalone HTTP server that:

1. Listens on a loopback address (`127.0.0.1:<port>`).
2. Ships a `plugin.json` descriptor in its own directory.
3. Serves `GET /health` returning `{"ok":true}`.
4. Implements one or more **capability** contract(s).

The dashboard registry (`server/internal/plugin/`) reads `plugin.json`, starts the
process (if `command` is set), waits up to 5 seconds for `/health` to return 200, then
makes the plugin's capabilities available to the rest of the server.

---

## The `plugin.json` descriptor (v2)

```json
{
  "$schema": "../../plugin-sdk/plugin.schema.json",
  "id":           "my-plugin",
  "name":         "My Plugin",
  "version":      "1.0.0",
  "capabilities": ["route_extension"],
  "addr":         "127.0.0.1:19010",
  "command":      ["./my-plugin"],
  "env":          ["MY_API_KEY"],
  "settings": [
    { "key": "api_key", "type": "string", "label": "API Key", "secret": true },
    { "key": "mode",    "type": "enum",   "label": "Mode", "enum": ["fast", "slow"] }
  ],
  "lifecycle": {
    "activate":   "/hooks/activate",
    "deactivate": "/hooks/deactivate"
  }
}
```

Add `"$schema": "../../plugin-sdk/plugin.schema.json"` for editor autocomplete against
[`plugin-sdk/plugin.schema.json`](../plugin-sdk/plugin.schema.json). The relative path
assumes `plugins/<name>/plugin.json`.

### Core fields

| Field          | Required | Description |
|----------------|----------|-------------|
| `id`           | yes      | Unique plugin slug (`[a-z0-9][a-z0-9-]*`). |
| `version`      | no       | Semver string. |
| `capabilities` | no       | Capability strings the plugin implements (see below). |
| `addr`         | yes      | `127.0.0.1:<port>` the plugin HTTP server binds to. |
| `command`      | no       | Executable + args to launch. Omit if the process is already running. |
| `env`          | no       | Env var names to forward from the dashboard process. Only a fixed base set (PATH, HOME, TMPDIR, TEMP, USER, LANG, LC_ALL) plus names listed here are forwarded. |

### `settings` — per-plugin settings

Declared settings appear in the plugin's settings panel in the dashboard UI. Values are
stored per-plugin in the database.

| Field    | Description |
|----------|-------------|
| `key`    | Setting identifier (used in the API). |
| `type`   | `string` \| `url` \| `int` \| `bool` \| `enum`. |
| `label`  | Display label shown in the UI. |
| `secret` | `true` — value is encrypted at rest (AES-256-GCM) and masked (`***`) in the API. |
| `enum`   | Required when `type` is `"enum"` — array of allowed string values. |

Read/write settings: `GET /api/plugins/{id}/settings` and `PUT /api/plugins/{id}/settings`.

Stored values reach the plugin process as `PLUGIN_SETTING_<KEY>` environment variables on its next (re)start (no live reload) and are injected schema-free, so a key removed from `plugin.json` but still stored in the DB keeps being injected until it is cleared.

Setting keys must be unique **after sanitization** (uppercased, every non-alphanumeric character replaced with `_`). For example `api-key` and `api.key` both become `PLUGIN_SETTING_API_KEY`; when two keys collide one value silently wins (a warning is logged).

> Slot bindings for `ui_extension` plugins are declared in `ui-manifest.json` served by the plugin — not in `plugin.json`. See the `ui_extension` capability section below.

### `lifecycle` — hook paths

Optional HTTP paths (relative to `addr`) that the dashboard calls as `POST <addr><path>`
on each state transition. Any 2xx response means success; an empty string means no hook.

| Key           | Called when |
|---------------|-------------|
| `install`     | Plugin first registered. |
| `postInstall` | After install completes. |
| `activate`    | Plugin activated (enabled). |
| `deactivate`  | Plugin deactivated (disabled). |
| `update`      | Descriptor version changed. |
| `uninstall`   | Plugin removed from the registry. |

---

## Capabilities

### `auth_provider`

Replaces the built-in bypass-auth with a real OAuth provider. Only the first discovered
`auth_provider` plugin is used.

**Liveness:** `auth_provider` is boot-wired — activating or deactivating it requires a
server restart. The dashboard UI surfaces a restart button when this capability changes
state.

The plugin implements a **standalone OAuth dance** and creates dashboard sessions by
calling core's `POST /api/auth/session`. Core has zero provider-specific knowledge — it
only issues JWT session cookies when a trusted plugin presents a verified user profile.

#### Standalone flow (primary)

```
Browser          Core                     Plugin              Provider
  │                │                        │                    │
  │ GET /api/auth/github │                   │                    │
  │───────────────►│                        │                    │
  │ 302 → <plugin>/login │                  │                    │
  │◄───────────────│                        │                    │
  │ GET /login     │                        │                    │
  │───────────────────────────────────────►│                    │
  │ 302 → provider │                        │                    │
  │◄───────────────────────────────────────│                    │
  │                                         Provider OAuth dance │
  │◄────────────────────────────────────────────────────────────│
  │ GET /callback?code=…                   │                    │
  │───────────────────────────────────────►│                    │
  │                │  POST /api/auth/session│                    │
  │                │◄───────────────────────│                    │
  │                │  Set-Cookie: auth_token│                    │
  │                │───────────────────────►│                    │
  │ 302 → /        │                        │                    │
  │◄───────────────────────────────────────│                    │
```

The plugin must implement:

**`GET /health`** — required by registry health-check.

Response: `{"ok":true}`

**`GET /login?nonce=<jwt>`** — entry point; core forwards here with a one-time nonce.

Redirects to the OAuth provider's authorization URL. Must embed the nonce in the CSRF
state value as `<csrfState>.<nonce>` — pass this combined value both as the state cookie
and as the OAuth `state` parameter. The nonce is recovered in the callback by splitting
the cookie value on the first `.` (the nonce is a JWT that may contain dots, so split
only on the first one).

**`GET /callback?code=<code>&state=<state>`** — OAuth callback.

1. Compare the `state` query parameter against the `github_oauth_state` cookie value —
   reject if they differ.
2. Extract nonce: split the cookie value on the first `.` only — everything after is the
   nonce (a JWT that may itself contain dots).
3. Exchange code for access token.
4. Fetch user profile from provider.
5. Call `POST /api/auth/session` (see below).
6. Forward the `auth_token` cookie to the browser.
7. Redirect to `DASHBOARD_URL/`.

**Calling core to create a session: `POST /api/auth/session`**

Request headers:

```
Authorization: Bearer <DASHBOARD_AUTH_PLUGIN_SECRET>
Content-Type: application/json
```

Request body:

```json
{
  "github_id":    "12345",
  "login":        "username",
  "display_name": "Full Name",
  "avatar_url":   "https://...",
  "nonce":        "<value received as ?nonce= on GET /login — pass verbatim>"
}
```

Response: `200 OK` with `Set-Cookie: auth_token=<jwt>`.

#### Required environment variables

| Variable | Description |
|----------|-------------|
| `DASHBOARD_URL` | Base URL of the dashboard (e.g. `http://127.0.0.1:13120`) |
| `DASHBOARD_AUTH_PLUGIN_SECRET` | Shared secret ≥32 chars for `POST /api/auth/session` |
| `GITHUB_CLIENT_ID` | GitHub OAuth app client ID (github-oauth specific; name in `plugin.json` `env` array) |
| `GITHUB_CLIENT_SECRET` | GitHub OAuth app client secret (github-oauth specific; name in `plugin.json` `env` array) |

---

### `route_extension`

Plugins with `route_extension` have all requests to `/api/plugins/{id}/proxy/*`
reverse-proxied to the plugin's own `addr`. The plugin can serve any HTTP content —
JSON APIs, static assets, ES modules — under that prefix.

**Liveness:** live — no server restart needed. Routes are registered when the plugin is
activated and removed when it is deactivated.

---

### `ui_extension`

Lets a plugin contribute frontend UI into named slots of the dashboard SPA. The plugin
serves a **UI manifest** plus one JS module per slot through the proxy path; the
dashboard imports a module only when a host slot renders.

**Liveness:** live — modules are loaded on demand when a slot renders. On disable,
a browser refresh unloads the addon (the dashboard prompts the user to refresh).

**1. Declare the capability** in `plugin.json`:

```json
{ "capabilities": ["ui_extension"] }
```

**2. Serve a UI manifest** at `/ui-manifest.json` within your plugin's HTTP server. The
dashboard fetches it via the reverse proxy at `/api/plugins/{id}/proxy/ui-manifest.json`.

Each slot entry has one required field (`slot`, `module`) and two optional ones:

| Field | Type | Description |
|-------|------|-------------|
| `slot` | string | Slot name to target (see contract table below). |
| `module` | string | Relative path to the ES module served by your plugin (e.g. `task-footer.js`). Must not start with `/` and must not traverse `..`. |
| `priority` | number | Render order within a slot. Higher priority renders outer/first. Omit for default (0). |
| `mode` | `"override"` \| `"extend"` | Composition mode (see below). Omit to register as an independent sibling. |

```json
{
  "slots": [
    { "slot": "task-modal-footer", "module": "task-footer.js" },
    { "slot": "task-modal-footer", "module": "task-footer-wrapper.js", "priority": 10, "mode": "extend" },
    { "slot": "kanban-card-badge", "module": "badge.js", "priority": 100, "mode": "override" }
  ]
}
```

**3. Serve each module** (e.g. `task-footer.js`) as an ES module whose `default` export is
a `SlotAddon`. The dashboard imports it from `/api/plugins/{id}/proxy/{module}`.

```js
export default {
  slot: 'task-modal-footer',
  priority: 0,
  // mode: 'override' | 'extend' — omit for sibling (default)
  mount(el, ctx, parent) {
    // ctx shape depends on the slot (see the contract table below)
    el.textContent = ctx.task.slug
    return () => { /* teardown */ }
  },
}
```

**Composition modes** -- when multiple addons target the same slot, the dashboard resolves
them as follows:

- **No mode (sibling, default):** each addon is mounted into its own independent host
  element, in load order. All siblings coexist.
- **`override`:** the highest-priority `override` addon owns the slot exclusively. Every
  addon in the priority-sorted chain below it -- including other `extend` entries and all
  siblings -- is suppressed and never mounted.
- **`extend`:** addons with `mode: "extend"` are sorted by `priority` (descending) into a
  composition chain. The outermost (highest priority) is mounted first; its `mount`
  function receives a `parent` handle it may use to mount the next addon in the chain:

```js
export default {
  mount(el, ctx, parent) {
    // render this addon's own content
    const label = document.createElement('span')
    label.textContent = 'wrapper'
    el.appendChild(label)
    // compose the lower-priority chain below
    if (parent) {
      const child = document.createElement('div')
      el.appendChild(child)
      parent.mount(child)
    }
    return () => { /* teardown */ }
  },
}
```

If an `override` appears in the chain, `parent` is `null` for it -- it does not delegate
further. Siblings (no mode) are always mounted after the chain, regardless of priority,
unless an `override` is present (which suppresses them entirely).

Addons are framework-agnostic — bundle any framework into the module. Reference
[`plugin-sdk/addon.d.ts`](../plugin-sdk/addon.d.ts) for TypeScript types.

**Available slots and their `ctx` contract** (SSOT: `src/utils/pluginSlot.ts`):

#### Slot composition

| `mode`     | Behaviour |
|------------|-----------|
| *(omit)*   | **Sibling** — rendered alongside other addons for the same slot, in priority order. |
| `"extend"` | **Extend** — wraps the parent chain. Receives a `SlotParent` handle as the third `mount` argument; call `parent.mount(el)` to include the parent content. |
| `"override"` | **Override** — replaces all other addons for the slot. Only the highest-priority override runs. |

#### Available slots and their `ctx` contract

SSOT: `src/utils/pluginSlot.ts` and [`plugin-sdk/addon.d.ts`](../plugin-sdk/addon.d.ts).

| Slot name                | Host              | `ctx` (props in)                                   |
|--------------------------|-------------------|----------------------------------------------------|
| `refinement-input-addon` | RefinementChat    | `{ insertText(text), setBusy(bool) }` (callbacks) |
| `task-modal-footer`      | TaskModal footer  | `{ task }`                                         |
| `agent-modal-footer`     | AgentModal footer | `{ agent }`                                        |
| `kanban-card-badge`      | TaskCard          | `{ task }`                                         |
| `settings-panel`         | PluginSettings    | `{}` (no entity context)                          |

The addon's only callback *out* is the `UnmountFn` it returns from `mount`, invoked when
the host unmounts.

#### Module loading

Imports are memoized per page load (plugin list + each manifest + each module fetched
once). Modules are imported only from `/api/plugins/{id}/proxy/*` — never an arbitrary
URL. A plugin must be discovered and healthy before any of its UI can load.

**Legacy fallback:** a `route_extension` plugin that ships no manifest but serves an
`addon.js` whose `default.slot` matches the requested slot still works — this keeps
existing plugins functioning unchanged.

---

## Plugin settings UI

Plugins with `settings` entries in their manifest get a dedicated settings panel in the
dashboard. The panel renders each field according to its `type` and `label`. Secret
fields (`"secret": true`) are stored encrypted at rest and displayed as `***` in the UI
— the raw value is never sent back to the frontend.

Read and write plugin settings via the REST API:

```
GET /api/plugins/{id}/settings
PUT /api/plugins/{id}/settings
```

---

## Enable, disable, and lifecycle management

The dashboard manages plugin state (`discovered` → `inactive` → `active`) via lifecycle
endpoints:

```
POST /api/plugins/{id}/install
POST /api/plugins/{id}/activate
POST /api/plugins/{id}/deactivate
POST /api/plugins/{id}/uninstall
```

Each call invokes the corresponding hook declared in `lifecycle` (if any) and updates the
plugin's DB state.

**Offline hatch:** if the server will not start because an `auth_provider` plugin is
broken, disable auth from the CLI without a running server:

```bash
kontor settings set auth.mode none
```

This writes directly to the database and takes effect on next boot.

---

## How to register a plugin

1. Build your plugin binary.
2. Create a directory under your plugin dir:

   ```
   /path/to/plugins/
   └── my-plugin/
       ├── plugin.json
       └── my-plugin   ← compiled binary
   ```

3. Set the env var (or config key) before starting the dashboard:

   ```bash
   export DASHBOARD_PLUGIN_DIR=/path/to/plugins
   ./kontor serve
   ```

   Or add `"plugin_dir": "/path/to/plugins"` to your JSON config file.

The registry scans every subdirectory of `plugin_dir` for `plugin.json` at startup.
Missing or malformed descriptors are skipped with a warning; a failed health check marks
the plugin unhealthy and skips it.

---

## Security

- Plugins **must** bind to `127.0.0.1` only — never a public address.
- The dashboard waits for every plugin process it started to exit before shutting down (`Registry.Shutdown()`): SIGTERM to the process group, escalating to SIGKILL after a shared 5s deadline for any plugin that ignores it.
- Only a fixed base set of env vars (PATH, HOME, TMPDIR, TEMP, USER, LANG, LC_ALL) plus
  names listed in the `env` array in `plugin.json` are forwarded to the plugin process.
- `DASHBOARD_AUTH_PLUGIN_SECRET` must be at least 32 characters. Store it in a `.env`
  file, never commit it.
- Health check timeout is **5 seconds**. Plugins that do not respond in time are skipped.
- Addon modules are imported only from `/api/plugins/{id}/proxy/*`, served by the
  registry-discovered, health-checked, SSRF-guarded plugin proxy.

---

## Reference: github-oauth plugin

`plugins/github-oauth/` is the canonical reference implementation of the `auth_provider`
capability.

### Files

| File          | Purpose |
|---------------|---------|
| `plugin.json` | Descriptor — capability `auth_provider`, addr `127.0.0.1:19001`, command `./github-oauth` |
| `go.mod`      | Standalone Go module (`github.com/lx-wnk/kontor-plugin-github-oauth`) |
| `main.go`     | HTTP server implementing standalone OAuth flow + `/health` |

### Setup

```bash
# 1. Build the plugin binary
cd plugins/github-oauth
GOWORK=off go build -o github-oauth .

# 2. Export credentials
export GITHUB_CLIENT_ID=your_client_id
export GITHUB_CLIENT_SECRET=your_client_secret
export DASHBOARD_URL=http://127.0.0.1:13120
export DASHBOARD_AUTH_PLUGIN_SECRET=$(openssl rand -hex 32)

# 3. Point the dashboard at the plugin dir and start
export DASHBOARD_PLUGIN_DIR=/path/to/plugins   # directory containing github-oauth/
./kontor serve
```

The dashboard logs `plugin: loaded id=github-oauth capabilities=[auth_provider]` on
success.

### Endpoint summary

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/health` | Health check — returns `{"ok":true}` |
| `GET`  | `/login?nonce=<jwt>` | Start OAuth dance (primary entry point) |
| `GET`  | `/callback?code=<code>&state=<state>` | OAuth callback — creates session, redirects to dashboard |

---

## Reference: office365-oauth plugin

`plugins/office365-oauth/` implements the `auth_provider` capability for Microsoft
single-tenant Azure AD.

### Files

| File | Purpose |
|------|---------|
| `plugin.json` | Descriptor — capability `auth_provider`, addr `127.0.0.1:19002`, command `./office365-oauth` |
| `go.mod` | Standalone module (`github.com/lx-wnk/kontor-plugin-office365-oauth`) |
| `main.go` | HTTP server implementing standalone OAuth2 flow |

### Azure App Registration

1. Go to [Azure portal](https://portal.azure.com) → **Azure Active Directory** →
   **App registrations** → **New registration**.
2. Set redirect URI to `http://127.0.0.1:19002/callback` (type: Web).
3. Under **Certificates & secrets**, create a new client secret.
4. Under **API permissions**, add `User.Read` (delegated). If using group restriction,
   also add `GroupMember.Read.All` (delegated). Grant admin consent.

### Setup

```bash
# 1. Build the plugin binary
cd plugins/office365-oauth
GOWORK=off go build -o office365-oauth .

# 2. Export credentials
export AZURE_CLIENT_ID=your_application_client_id
export AZURE_CLIENT_SECRET=your_client_secret
export AZURE_TENANT_ID=your_tenant_directory_id
export DASHBOARD_URL=http://127.0.0.1:13120
export DASHBOARD_AUTH_PLUGIN_SECRET=$(openssl rand -hex 32)

# Optional: restrict to a specific Azure AD group
export OFFICE365_ALLOWED_GROUP_ID=your_group_object_id

# 3. Point the dashboard at the plugin dir and start
export PLUGIN_DIR=/path/to/plugins   # directory containing office365-oauth/
./kontor serve
```
