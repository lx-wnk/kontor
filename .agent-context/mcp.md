# MCP Endpoint

A stateless StreamableHTTP MCP server at `POST /api/mcp` — each request is self-contained (no server-side session map). `GET /api/mcp` opens an SSE stream (`mcp.Notifier`, over `sse.Broadcaster`) that carries `notifications/tools/list_changed`, fired by `watchObsidianSettings` on every `obsidian.*` save, plus a `: heartbeat` comment every `sse.HeartbeatInterval`.

## Authentication

Bearer token (`Authorization: Bearer mcp_<hex>`). Tokens are never stored — only their SHA-256 hash lives in the `api_keys` SQLite table. Clients must also send `Accept: application/json, text/event-stream`.

## Scope Model

`tasks:read` | `tasks:write` | `agent:coord` | `pipeline:control` | `keys:manage`. Higher scopes imply lower ones (`keys:manage` → all; `tasks:write` → `tasks:read`; `pipeline:control` → `agent:coord`). The `agent:coord` scope grants the coordination tools (`write_scratchpad`/`read_scratchpad`/`list_scratchpad`/`acquire_lock`/`release_lock`) for shared scratchpads and lease-based locks. Each MCP tool checks its required scope at call time and returns an MCP error if insufficient.

## Key Files

- `server/db/apiKeysRepo.ts` — CRUD for `api_keys` table (SHA-256 hashed tokens, `upsertStageRunApiKey` for iterate re-key)
- `server/mcp/mcpAuth.ts` — `mcpAuthMiddleware` (hash lookup + scope resolution), `TOOL_SCOPE_MAP`
- `server/mcp/mcpServer.ts` — `buildMcpServer(orchestrator, scopes, broadcast)` — all tool registrations
- `server/mcp/mcpRouter.ts` — `createMcpRouter` — thin Express router that creates a transport per request
- `server/routes/apiKeyRoutes.ts` — REST CRUD for API keys (`GET/POST/DELETE /api/settings/api-keys`)
- `src/components/ApiKeySettings.vue` — UI for creating/revoking keys

## Layering

`server/mcp/*` imports `db/*`, `services/*`, `src/types.ts`, and `pipeline/orchestrator.ts` (type-only) only. Never imports `notifications/` or `routes/`. (See Rule 5 in `task-pipeline.md` for the full routes/mcp ↔ pipeline policy.)

## Pipeline Env Vars Injected into Spawned Stage Agents

`DASHBOARD_MCP_TOKEN` (stage-scoped bearer token), `DASHBOARD_MCP_URL` (e.g. `http://127.0.0.1:13120/api/mcp`). These allow stage agents to call back into the dashboard MCP endpoint.

## Local Agent Integration

A `.mcp.json.example` is shipped at the repo root. Copy it to `.mcp.json` and export `DASHBOARD_MCP_TOKEN` — any Claude Code session opened in this repo then has automatic dashboard MCP access. `.mcp.json` is gitignored to prevent accidental token commits.
