# Module Contract v1 — Design

Status: proposed, unapproved
Date: 2026-09-19

## Why

Today a module can serve an HTTP route behind `/api/plugins/{id}/proxy/*`, fill one of five
named Vue slots, and act as an auth provider (`server/internal/plugin/types.go:54-60`).
Everything else that makes a feature interesting is registered in core with no seam:

| Concern | Owned by core at |
| --- | --- |
| Persistent entities | `server/internal/db/ent/schema/` (compiled), `server/internal/db/client.go:151` |
| Task kinds | `server/internal/pipeline/types.go:235-237` |
| Stage sequence | `server/internal/pipeline/types.go:209-218` (`StageOrder`) |
| Agent tools (MCP) | `server/serverapp/di_mcp.go:60-167` |
| Agent providers | `server/internal/provider/registry.go:52-76` (separate YAML mechanism) |
| Routines | `server/internal/db/repo/task_schedule_repo.go` (DB rows only) |

Consequence: every such feature is a core commit and a core release. A module also has no
way to call back into core — `DASHBOARD_MCP_TOKEN` is explicitly withheld from module
processes (`server/internal/plugin/registry.go:836`) — and no way to state which core it was
built against; the manifest has no compatibility field at all, and the server performs no
manifest validation beyond a slug check (`registry.go:151-159`).

This design widens the seam along four axes while keeping the core's own state space closed.

## Non-goals

- Replacing the existing process + reverse-proxy model. It stays.
- In-process modules (Go `plugin`/`.so`). Rejected previously, still rejected.
- A module registry service. Distribution is git.
- Letting a module define new *lifecycle states*. Only new *kinds* that plug into the
  existing lifecycle.

## Naming

This contract is a public interface: manifest field names, environment prefix, CLI verbs. It
is therefore born **after** the repository rename to `kontor`, so it is not renamed twice.
Names below use the post-rename spelling (`KONTOR_*`, `kontor module …`).

## Architecture

A module remains an OS process started and supervised by core. Three things are added:

1. **A declared contract version.** `contract: 1` becomes a required manifest field. Core
   refuses to load a module whose contract version it does not implement, and says so in the
   UI. An integer handshake, not a semver range — the Terraform provider-protocol model,
   chosen because it is one comparison and cannot be half-satisfied.
2. **A scoped callback credential.** On activation, core issues the module a bearer token
   bound to that module's identity and to the capability list its manifest declares under
   `uses`. The token is short-lived, rotated on restart, revoked on deactivate. It
   authenticates against the existing `/api/mcp` and HTTP API surfaces, so the module acts
   with its own identity rather than with a shared secret.
3. **Declarative registration.** Tools, task kinds, stage kinds, providers, and routines are
   declared in the manifest and validated at load. Core stores them in the registry; nothing
   is discovered by reflection or by a module mutating core state at runtime.

### Manifest v3 (additions)

```jsonc
{
  "contract": 1,                 // required; load fails if unsupported
  "id": "obsidian",              // existing, slug-validated
  "name": "Obsidian",            // now REQUIRED (today absent in 6 of 6 manifests)
  "version": "1.4.0",            // module's own version, git tag
  "capabilities": ["tools", "stages", "provider", "route_extension", "ui_extension"],
  "uses": ["task.read", "memory.write"],   // what the callback token may do
  "tools": [{ "name": "search", "description": "...", "input": { /* JSON Schema */ } }],
  "taskKinds": [{ "name": "research", "stages": ["intake", "gather", "report"] }],
  "stageKinds": [{ "name": "gather", "timeoutSeconds": 900 }],
  "providers": ["providers/*.yaml"],       // merged into the existing provider registry
  "routines": ["routines/*.yaml"],         // materialised as task_schedule rows on install
  "widgets": [{ "id": "recent", "title": "Recent notes", "minCols": 1 }]
}
```

Validation at load time is strict and refusing: unknown contract version, missing `name`,
malformed tool schema, a task kind whose stage list references an undeclared stage kind, or a
stage name colliding with a core stage — each is a load failure with a message, never a
silent skip. This is the one lesson every comparable ecosystem learned late: a permissive
default is what gets retrofitted under pressure.

### Axis (a) — Agent tools

Module serves its tools over its existing loopback address. Core aggregates them into its own
MCP surface under a namespace: a module tool appears to an agent as `<moduleId>__<tool>`.
Aggregation is dynamic — an unhealthy module's tools drop out of the tool list instead of
failing mid-call.

Each module tool is a permission subject in the existing grants machinery, **one grant per
tool**, default deny, learned on first use. A grant names `module:<id>` plus the tool, which
the grant table already supports through its `pattern` column
(`server/internal/db/ent/schema/grant.go:25-38`) — no schema change. So "this routine may use
Obsidian search but not Obsidian write" is expressible with what exists.

One gap this creates: `Gate.Authorize` checks a single capability per call
(`server/internal/memory/authorize.go:99`). Listing the tools an agent may see would issue one
check per tool and burn rate-limit budget, so Slice 2 adds a bulk form before it aggregates.

Direction of calls:

- agent → core `/api/mcp` → module tool endpoint (core proxies, records usage).
- module → core HTTP API with its scoped token, limited to its declared `uses`.

### Axis (b) — Own data

A module owns its own store. Core guarantees a stable directory per module,
`~/.claude/kontor/modules/<moduleId>/`, passed in as `KONTOR_MODULE_DATA_DIR`, preserved
across module updates, removed on uninstall (after an archive copy). Core's SQLite file stays
core's.

The directory deliberately sits beside the core database rather than inside the module's git
clone: modules are installed and updated with `git clone` and `git checkout`, so data kept in
the clone would share a tree with the update mechanism — every reinstall would be a data loss
and every module's working tree would be permanently dirty.

Rejected alternative: modules shipping migrations against the shared ent database. ent has no
mechanism to merge a foreign migration graph, and this project has already paid for a
hand-edited generated schema once (phantom-column rebuild crash). The cost of the chosen
option is accepted explicitly: a module cannot hold a foreign key into core rows and must
reference them by id, and backup is now N stores rather than one.

Where core UI needs module data inline, it fetches it through the module's HTTP endpoint via
the existing proxy — no cross-reading of another module's store.

### Axis (c) — Task kinds and stage kinds

Core keeps `StageOrder` as *its* default sequence and keeps the stage-run lifecycle
(`pending`, `running`, `awaiting_user`, …) closed. A module may:

- declare a **stage kind**: a named handler. When the pipeline reaches a stage of that kind,
  it calls `POST /stages/{kind}/run` on the module instead of spawning an agent. The stage run
  row, its states, its timeouts, and its retry path are unchanged core machinery.
- declare a **task kind** with its own stage sequence, as data. The sequence must be finite,
  must consist of core stages or declared stage kinds, and terminates in core's `done`.

This is the Airflow split — providers add operator types, never scheduler states — and the
reason the state space stays bounded and inspectable.

### Axis (d) — Providers and routines

Three mechanisms exist today for adjacent purposes: plugin processes, provider YAML merged
from `DASHBOARD_PROVIDER_DIR` (`provider/registry.go:52-76`), and spawner rows in the database
that drive the exec adapter (`plugins/anthropic-spawner/plugin.json` is decorative — it has no
`addr`, so the plugin registry would reject it; it runs through
`llmadapter/anthropic_path.go`).

A module may ship provider descriptors and routine definitions as files; core loads them
through the existing registries on activation and withdraws them on deactivation. The
separate `DASHBOARD_PROVIDER_DIR` stays supported for hand-written descriptors.

### Distribution

`kontor module add <git-url>[@ref]` clones into the module root; `kontor module update <id>`
fetches and checks out the ref; the resolved commit is recorded. A module's version is its git
tag. No registry to operate, independent versioning by construction, and the natural upgrade
path later is a release tarball plus a signed checksum manifest.

## Data flow

```
agent ──MCP──▶ core /api/mcp ──namespaced dispatch──▶ module :port/tools/<name>
                     │                                        │
                     └──grant check (module:<id>, tool)        └── module store (own file)

pipeline stage of module kind ──▶ module :port/stages/<kind>/run ──▶ stage run row (core)

module ──scoped bearer──▶ core HTTP API (limited to manifest `uses`)
```

## Error handling

| Failure | Behaviour |
| --- | --- |
| Unsupported or missing `contract` | Load refused, module listed as incompatible with the required version named |
| Manifest invalid | Load refused with the offending field; module never reaches the registry |
| Module unhealthy | Existing health/backoff/restart applies; its tools leave the MCP list and its stage kinds fail the stage run with captured stderr |
| Stage handler timeout | Existing stage-run timeout and requeue path, no new state |
| Token misuse (capability not in `uses`) | 403, logged with module identity and correlation id |
| Deactivate / uninstall | Token revoked, tools withdrawn, provider and routine registrations withdrawn, data dir archived then removed |

## Testing

- A **reference module** fixture in-repo exercising all four axes, used by integration tests:
  wrong contract version is refused; tool names are namespaced; the data dir is isolated and
  survives an update; a module stage kind drives a stage run to `done`; deactivate revokes the
  token (a call with the old token must return 403).
- Manifest schema ↔ Go struct parity, extending the existing drift guard in
  `src/plugin-sdk.test.ts`.
- Migration test: the six existing modules load under contract 1 after declaring `name`.

## Slices

| # | Slice | Rationale for the order |
| --- | --- | --- |
| 0 | Contract field, strict manifest validation, `name` required, migrate the six in-repo manifests | Everything else is unsafe without a load-time gate |
| 1 | Scoped module token, identity on core API calls, revoke on deactivate | The callback channel every later axis needs |
| 2 | Tool aggregation into `/api/mcp` with namespacing and grants | Highest-value axis, now safe to expose |
| 3 | Module data dir, lifecycle, archive on uninstall | Independent of 2, smallest blast radius |
| 4 | Stage kinds and module task kinds | Touches the pipeline; wants 0 and 1 in place |
| 5 | Provider, spawner, and routine declarations; `kontor module add/update` | Consolidates the three mechanisms |
| 6 | Toolchain decoupling: CI module list read from disk, license generation, schema `$schema` URL, published SDK | Removes the last in-repo assumptions |

## Decisions taken 2026-09-19

1. **Grants are per tool**, default deny, learned on first use — through the grant table's
   existing `pattern` column. Slice 2 adds the bulk authorize form it implies.
2. **`<dataRoot>` is `~/.claude/kontor/modules/`**, beside the core database, never inside
   the module's git clone.
3. **The five UI slots stay as they are in v1, and the surface they belong to is being
   replaced.** The operator wants the cockpit to be composed by hand — widgets added, moved
   and removed — plus pages of their own. That is a subsystem of its own (widget registry,
   layout model, edit mode, server-side persistence so the desktop window and the browser
   agree), and it gets its own design document. This contract reserves exactly one thing for
   it: the manifest's `widgets` key, so a module can contribute a widget once the grid
   exists. Today's cockpit hardcodes five panels
   (`src/features/cockpit/components/CockpitView.vue:9-17`) and `agent-dashboard-layout` in
   localStorage only switches the agent list between `cards` and `list`
   (`src/composables/useViewState.ts:21,129`) — there is no registry and no layout model yet.
