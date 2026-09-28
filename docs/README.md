# Documentation

Reference and guides for Kontor. Start with the [project README](../README.md) for an
overview and quickstart.

The tree has three tiers, and they are kept apart on purpose:

- **Guides and architecture** — current, maintained, listed below.
- **[`harness/`](harness/)** — the operating procedure agents follow when delivering a feature.
- **[`archive/`](archive/)** — dated specs and plans. History, not documentation, and not maintained.

## Guides

| Guide | What it covers |
|---|---|
| [Install](guides/install.md) | Binary, Homebrew, Docker, and source install paths |
| [Configuration](guides/configuration.md) | Every environment variable, grouped by concern |
| [MCP Endpoint](guides/mcp.md) | Authenticated MCP server, scopes, the tools, and connecting Claude |
| [Controlling & Spawning Agents](guides/agent-control.md) | Channel control, the spawn dialog, slash commands, permissions |
| [Security](guides/security.md) | Threat model, auth, hardening defaults, the capability gate, and the Obsidian vault client's TLS trust model |
| [Shell Statusline](guides/statusline.md) | `scripts/statusline.py` PS1 integration |
| [Agent Skills](guides/agent-skills.md) | Registry-owned skills and how they reach disk |
| [Plugins](plugin-guide.md) | Sidecar plugin architecture |
| [Hooks setup](hooks-setup.md) | Optional hook-based rescan triggers |
| [Desktop distribution](desktop-distribution.md) | Packaging the macOS app, signing, notarization |

## Architecture

| Document | What it covers |
|---|---|
| [Overview](architecture/overview.md) | Stack, package layout, data flow, task pipeline |
| [Server review](architecture/server-review.md) | Structural review of the Go server |
| [D3 workflow visualizations](architecture/d3-workflow-visualizations.md) | Sankey, spawn tree, and co-occurrence charts |

### Decision records

| ADR | Decision |
|---|---|
| [0001](architecture/adr/0001-sqlite-for-task-pipeline.md) | SQLite for task-pipeline persistence |
| [0002](architecture/adr/0002-runner-slot-priority-model.md) | Runner-slot priority model for task pickup |
| [0003](architecture/adr/0003-pluggable-spawners.md) | Pluggable spawners for stage agents |
| [0004](architecture/adr/0004-domain-error-sentinels.md) | Stdlib-only domain error sentinels |
| [0005](architecture/adr/0005-llmadapter-leaf.md) | Extract the `llmadapter` leaf package |
| [0006](architecture/adr/0006-worktree-leaf.md) | Extract the `worktree` leaf package |
| [0007](architecture/adr/0007-cron-scheduling-engine.md) | Cron scheduling engine |
| [0008](architecture/adr/0008-eval-drift-detection-leaf.md) | Passive drift detection over `stage_run` |
| [0009](architecture/adr/0009-proc-leaf.md) | Extract the `internal/proc` process-liveness leaf |
| [0010](architecture/adr/0010-single-process-boundary.md) | Local-first single-process boundary |
| [0011](architecture/adr/0011-cross-language-ssot-parity.md) | Cross-language SSOT parity enforcement |
| [0012](architecture/adr/0012-plugin-domain-boundaries.md) | Plugin domain package boundaries |
| [0013](architecture/adr/0013-remote-spawner-nodes.md) | Remote spawner nodes (proposed) |

## Delivery harness

[`harness/`](harness/) holds the contracts an orchestrated feature delivery runs on: the
[runbook](harness/ofd-runbook.md) for the coordinating thread, the
[orchestrator prompt](harness/ofd-orchestrator-prompt.md), and the
[role contracts](harness/ofd-roles.md) for implementer, reviewer, and verifier.

## Releasing

See [RELEASING.md](RELEASING.md) for the version scheme, one-time setup, and per-release steps.
For the first public release specifically, [launch-checklist.md](launch-checklist.md) tracks the
discoverability and polish items around it.

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for setup, commands, PR process, and code guidelines.
