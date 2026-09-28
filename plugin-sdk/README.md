# kontor Plugin SDK

A plugin is an independent HTTP sidecar process plus a `plugin.json` descriptor, and an
optional vanilla-ESM UI addon module. The dashboard discovers plugins at startup,
health-checks them, then makes their capabilities available to the rest of the server.

---

## `plugin.json` quickstart

```json
{
  "$schema": "../../plugin-sdk/plugin.schema.json",
  "id": "my-plugin",
  "version": "1.0.0",
  "capabilities": ["route_extension"],
  "addr": "127.0.0.1:19010",
  "command": ["./my-plugin"],
  "env": ["MY_API_KEY"]
}
```

The `"$schema"` line gives editors (VS Code, WebStorm, …) field-level autocomplete and
inline validation against [`plugin.schema.json`](./plugin.schema.json).

Key fields — see the schema for the full reference:

| Field          | Description |
|----------------|-------------|
| `id`           | Unique slug (`[a-z0-9][a-z0-9-]*`). |
| `capabilities` | `auth_provider`, `route_extension`, or `ui_extension`. |
| `addr`         | `127.0.0.1:<port>` the plugin listens on. |
| `command`      | Executable to spawn. Omit if the process is already running. |
| `env`          | Env var names to forward from the dashboard process. |
| `settings`     | User-configurable fields — `[{ "key", "type", "label", "secret" }]`. |
| `lifecycle`    | Optional HTTP paths for lifecycle hooks (see below). |

---

## Backend contract

### Health check (required)

```
GET /health → 200  {"ok": true}
```

The dashboard polls this endpoint for up to 5 seconds at startup. A plugin that does not
respond in time is skipped.

### Lifecycle hooks (optional)

Declare hook paths in `plugin.json`:

```json
{
  "lifecycle": {
    "install": "/hooks/install",
    "postInstall": "/hooks/post-install",
    "activate": "/hooks/activate",
    "deactivate": "/hooks/deactivate",
    "update": "/hooks/update",
    "uninstall": "/hooks/uninstall"
  }
}
```

The dashboard calls each path as `POST <addr><path>`. Any 2xx response means success.
An empty path string means no hook for that transition.

Reference implementations: [`plugins/github-oauth`](../plugins/github-oauth/) (auth
provider) and [`plugins/voice-whisper`](../plugins/voice-whisper/) (route extension).

---

## UI addon

A `ui_extension` plugin serves ES modules that mount DOM into named host slots. Addons
are framework-agnostic — bundle any framework into the module, or write plain DOM.

```js
// addon.js — vanilla ESM, served from the plugin process
export default {
  slot: 'agent-modal-footer',
  priority: 0,
  // mode: 'override' | 'extend' — omit for sibling (default)
  mount(el, ctx, parent) {
    // el   — container element owned by the host slot
    // ctx  — slot context (shape depends on the slot; see addon.d.ts)
    // parent — SlotParent handle when mode === 'extend'; mount it first
    const div = document.createElement('div')
    div.textContent = 'hello from my plugin'
    el.appendChild(div)
    return () => div.remove() // teardown
  },
}
```

Reference the TypeScript types in [`addon.d.ts`](./addon.d.ts) for a typed `ctx` and the
full `SlotAddon<S>` interface. Available slots and their context shapes are documented in
that file.

The dashboard loads addon modules via the plugin proxy — the module URL is
`/api/plugins/{id}/proxy/<module-path>`. Slot bindings are declared exclusively in
`ui-manifest.json` served from your plugin's root:

```json
{
  "slots": [
    { "slot": "agent-modal-footer", "module": "addon.js" }
  ]
}
```

---

## Capabilities and liveness

| Capability       | Effect                                                              | Activation |
|------------------|---------------------------------------------------------------------|------------|
| `route_extension`| Plugin routes are reverse-proxied via `/api/plugins/{id}/proxy/*`. | Live — no server restart needed. |
| `ui_extension`   | Addon modules are loaded into dashboard slots.                      | Live — modules unload on browser refresh when disabled. |
| `auth_provider`  | Replaces the built-in bypass-auth with a real OAuth flow.           | Needs a server restart. The dashboard UI surfaces a restart button when an `auth_provider` plugin is activated or deactivated. |

## Referencing the schema from outside this repository

A module that lives in its own repository cannot use the relative
`../../plugin-sdk/plugin.schema.json` path the in-tree examples use — there is
no such path beside it. Reference the published schema instead:

```json
{
  "$schema": "https://raw.githubusercontent.com/lx-wnk/kontor/main/plugin-sdk/plugin.schema.json",
  "contract": 1,
  "id": "my-module",
  "name": "My Module"
}
```
