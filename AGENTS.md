# AGENTS.md — Project Bootstrap

> All agents MUST read and follow this file.

## Identity

<!-- TODO: Project Name | Tech Stack | Docker Container -->

## Context Architecture

| Layer | File                                      | Content                         |
| ----- | ----------------------------------------- | ------------------------------- |
| 0     | `.agent-context/layer0-agent-workflow.md` | Agent Workflow (shared)         |
| 1     | `.agent-context/layer1-bootstrap.md`      | Project identity, tech stack    |
| 2     | `.agent-context/layer2-project-core.md`   | Dev principles + critical rules |
| 3     | `.agent-context/layer3-guidebook.md`      | Task routing, skills, memory    |

@.agent-context/agent-startup.md
@.agent-context/layer0-agent-workflow.md
@.agent-context/layer1-bootstrap.md
@.agent-context/layer2-project-core.md
@.agent-context/layer3-guidebook.md

## Quick Rules (Always Apply)

### Gate Commands (paste raw output — a summary is not evidence)

| Scope | Command |
| --- | --- |
| Frontend | `pnpm lint && pnpm typecheck && pnpm test` |
| Go | `task test` (sdk + server, `-race`) and `go vet ./...` per module |
| Go lint | `task lint` (golangci-lint, sdk + server) |
| Changelog | `task changelog:check` (no duplicate `###` heading per release section; also runs in CI) |
| E2E | `pnpm test:e2e` (Playwright; starts its own server on port 13199, never reuses one) |

`go vet ./...` runs module-wide on purpose: a narrow `go test ./pkg/...` misses `_test.go` files in parent or
sibling packages that reference a changed exported type, and `go build` skips test files entirely.

### Repository Traps

- `task test` and `go test ./...` regenerate `server/internal/db/ent/`. Run `git checkout -- server/internal/db/ent/` before committing unless the regeneration is the change.
- `pnpm build` (vite `outDir: server/frontend/dist`) wipes `server/frontend/dist/.gitkeep`, which `//go:embed all:dist` in `server/frontend/embed.go` needs to compile without a frontend build. Restore it with `git checkout HEAD -- server/frontend/dist/.gitkeep` before committing.
- Feature branches and Dependabot target `main` — `main` is the trunk.
- Never `gh pr merge --delete-branch`: worktrees under `dashboard-worktrees/` keep branches checked out, and the flag has already closed a PR unmerged.

## Compaction Preservation

When compacting context, always preserve:

- List of modified/created files in this session
- Active test/lint commands and their last results
- Unfinished tasks and next steps
