# Layer 2 — Project Core

> Development principles and critical project rules.

## Development Principles

@.agent-context/base-principles.md

## Critical Rules

- Server MUST bind to `127.0.0.1` — never `0.0.0.0` (reads sensitive Claude session data)
- Subagents discovered from `~/.claude/projects/{encoded_path}/{sessionId}/subagents/*.jsonl`
- Agent status thresholds: `activeThreshold` (30s) and `waitingThreshold` (5min) in `server/internal/merger/merger.go` (`CalculateStatus`); idle is the default case beyond `waitingThreshold` (no const)
- **Keep project docs current with code.** Whenever a change adds, removes, or alters a user-facing feature, dependency, command, config, or workflow, update the affected docs in the SAME change — at minimum `README.md`, `CHANGELOG.md` (Keep a Changelog headings), and `CONTRIBUTING.md`, plus any other touched file (`docs/`, `PRIVACY.md`, `SECURITY.md`, `THIRD_PARTY_LICENSES.md`). Stale docs are a defect, not a follow-up. Verify every doc claim against actual code before writing it.

## Single Source of Truth (SSOT)

**Rule:** Every constant, regex, validation rule, or type that is used in more than one place MUST live in exactly one canonical location. Duplication between implementation branches is forbidden.

**Canonical locations by category:**

| Category | Location | Example |
|---|---|---|
| Shared validation (client + server) | `src/utils/validation.ts` | `SLUG_RE`, `SLUG_PATTERN_MESSAGE`, `slugify()` |
| Shared type constants | `src/types.ts` | `AGENT_STATUSES`, `AgentStatus` |
| Shared UI utilities | `src/utils/format.ts`, `src/utils/agentSort.ts`, `src/utils/sse.ts` | `formatCost`, `STATUS_ORDER`, `SSE_RETRY_DELAY_MS` |
| Shared form helper text (client) | `src/utils/slugHint.ts` | `SLUG_FORMAT_HINT`, `derivedSlugHint` |
| Shared model/config lists | `src/utils/models.ts` | `AVAILABLE_MODELS`, `latestModel` |
| Claude model catalog + default models (Go) | `server/internal/claudemodel/catalog.go` | `IDs`, `IsKnown`, `Latest` — never pin a default model ID |
| Server defaults (Go) | `server/internal/db/defaults.go` | `DefaultStage`, `DefaultStageTimeoutSeconds` |
| Server validation (Go) | `server/internal/validation/slug.go` | `SlugPattern`, `SlugPatternMessage` |
| Server status thresholds (Go) | `server/internal/merger/merger.go` | `activeThreshold`, `waitingThreshold` |
| Pipeline stage labels (client) | `src/utils/stageLabels.ts` | `STAGE_LABELS`, `STAGE_DESCRIPTIONS` |
| Shared task option lists (client) | `src/utils/taskOptions.ts` | `TASK_PRIORITY_OPTIONS`, `TASK_AUTONOMY_OPTIONS`, `TaskPriority`, `TaskAutonomy` |
| Shared UI component types (client) | `src/components/ui/selectOption.ts` | `SelectOption<T>` |
| Workspace layout rules (client) | `src/features/workspace/layout.ts` | `GRID_COLUMNS`, `ZENTRALE_PAGE_ID`, `PAGE_ID_PATTERN`, `WIDGET_ID_PATTERN`, `DEFAULT_LAYOUT` |
| Workspace layout rules (server, hand-kept parity with the client) | `server/internal/settings/workspace_layout.go` | `workspaceColumns`, `workspacePageID`, `workspaceWidgetID`, caps |
| Widget catalogue (titles, spans, minimums) | `src/features/workspace/widgetSpecs.ts` | `WIDGET_SPECS`, `HUB_WIDGET` |
| Widget components | `src/features/workspace/widgetRegistry.ts` | `WIDGETS`, `widgetIds()` |
| Needs-you placement rule | `src/composables/needsYouPlacement.ts` | `needsYouPlacement` |
| Hub geometry constants | `src/features/hub/hubGeometry.ts` | `R0`, `R_MAX`, `RINGS`, `agentRingPx`, `planSectors`, `DAY_MS`, `SECTOR_PALETTE_SIZE`, `sectorColour` |
| Hub world extent, incl. the minimap's own frame | `src/features/hub/hubGeometry.ts` | `WORLD_RADIUS`, `MINIMAP_RIM`, `MINIMAP_HALF` — the minimap derives its viewBox, never a second radius |
| Hub camera thresholds | `src/features/hub/hubCamera.ts` | `LEVEL_TOPICS`, `LEVEL_NOTES`, `MIN_REL`, `MAX_REL`, `levelOf`, `launchersDocked` |
| Hub graph notices | `src/features/hub/hubGraphNotices.ts` | `GRAPH_NOTICES`, `LIST_GRAPH_NOTICES` |
| Hub launcher cap | `src/features/hub/hubLaunchers.ts` | `MAX_LAUNCHERS` |
| Obsidian graph response type (client, hand-kept parity with `server/internal/api/obsidian/handler.go`) | `src/features/hub/graphApi.ts` | `GraphResponse` |
| Shared 429 retry | `src/utils/fetchWithRateLimitRetry.ts` | `fetchWithRateLimitRetry` |
| "Is the user typing" guard | `src/utils/isTypingTarget.ts` | `isTypingTarget` |
| Nav item test id | `src/utils/navConfig.ts` | `navItemTestId`, `navItemSelector` |
| View to page id mapping | `src/composables/useViewState.ts` | `pageIdOf`, `pageView` |

**Client and server are different languages — no cross-import.** The Vue client (TypeScript) and the Go server each keep their own copy of a shared rule; Go cannot import TS. Where a rule must agree on both sides (e.g. the task-slug pattern), keep `server/internal/validation/slug.go` and `src/utils/validation.ts` in parity by hand — there is no shared module. The workspace layout rules are the second hand-kept TS↔Go pair, between `src/features/workspace/layout.ts` and `server/internal/settings/workspace_layout.go`.

**How to apply:** Before adding any constant or utility function, grep the codebase for existing implementations. If one already exists, import it — never copy it. If adding a new shared value, put it in the canonical location first, then import everywhere.

## Testing Strategy

- **Unit:** Vitest — `pnpm test` / `pnpm test:watch`
- **E2E:** Playwright — `pnpm test:e2e` (auto-starts its own server on port 13199; never reuses a running dashboard)

## Commit Convention

- Conventional Commits: `feat:`, `fix:`, `refactor:`, `init:`
- Feature branches: `feat/*`
- PRs merged to `main`
