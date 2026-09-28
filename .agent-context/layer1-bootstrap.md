# Layer 1 — Bootstrap

> Project identity, architecture rules, and agent coordination.

## Identity

**kontor** — Real-time monitoring dashboard for locally running Claude Code agents. Reads JSONL session logs and process metadata to display token usage, costs, tool activity, tasks, and subagents.

**Stack:** Go 1.26 backend (chi, ent ORM, modernc/sqlite, manual DI in `server/serverapp/di.go`, cobra CLI) + Vue 3 TypeScript SPA (Vite, pnpm). Go workspace: `./sdk` + `./server`. Build: Taskfile.yml (`task`). Hot-reload: `air`.

## Platform

macOS and Linux. `server/internal/platform/` provides `IS_LINUX` constant. CPU monitoring: `top` on macOS, `/proc/stat` on Linux. Process scanning: `lsof` on macOS, `/proc/<pid>/cwd` on Linux. Windows is unsupported.

## Data Flow

Browser subscribes to `/api/agents/stream` (SSE) → Go backend scans processes (`ps`/`lsof`) → matches PIDs to `~/.claude/projects/{encoded_path}/{sessionId}.jsonl` → tail-reads JSONL + reads session-meta JSON → merges, calculates cost/status → broadcasts `Agent[]` to SSE clients.

Non-Claude agents (Codex, Gemini) resolve their JSONL under each provider's own config dir instead of the Claude session path, dispatched per provider in the parser; cost is reported as unknown until a real foreign session schema lands.

## Excluded Directories

- `node_modules/`
- `dist/`
- `server/internal/db/ent/` (generated)
