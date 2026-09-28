import type { Page, Route } from '@playwright/test'
import { expect } from '@playwright/test'

/**
 * README screenshot fixtures — entirely invented data. No path, name, or id
 * here may come from a real Claude session, a real repo, or this machine:
 * the PNGs these fixtures feed are committed to a public repo.
 */

// ---------------------------------------------------------------------------
// Fictional projects
// ---------------------------------------------------------------------------

export const DEMO_PROJECTS = [
  { id: 'proj-weather', name: 'weather-cli', path: '/home/dev/projects/weather-cli' },
  { id: 'proj-recipe', name: 'recipe-api', path: '/home/dev/projects/recipe-api' },
  { id: 'proj-pixel', name: 'pixel-editor', path: '/home/dev/projects/pixel-editor' },
  { id: 'proj-notes', name: 'notes-sync', path: '/home/dev/projects/notes-sync' },
  { id: 'proj-kontor', name: 'kontor-demo', path: '/home/dev/projects/kontor-demo' },
] as const

function projectsApiResponse() {
  return DEMO_PROJECTS.map(p => ({
    id: p.id,
    name: p.name,
    path: p.path,
    createdAt: '2026-08-01T09:00:00Z',
    updatedAt: '2026-09-01T09:00:00Z',
  }))
}

// ---------------------------------------------------------------------------
// Agents — a fixed clock so every "Xs/Xm ago" label is stable across runs.
// ---------------------------------------------------------------------------

export const DEMO_NOW = new Date('2026-09-27T15:00:00.000Z')

function minutesAgo(min: number): string {
  return new Date(DEMO_NOW.getTime() - min * 60_000).toISOString()
}

interface AgentOverrides {
  [key: string]: unknown
}

let nextPid = 41000

/** Builds a plain Agent-shaped object — deliberately untyped, mirroring the e2e spec fixtures. */
function makeAgent(base: AgentOverrides): Record<string, unknown> {
  nextPid += 1
  return {
    pid: nextPid,
    entrypoint: 'cli',
    working: false,
    permissionsBypassed: false,
    convergenceAlert: false,
    channelAvailable: true,
    liveInjectable: true,
    conversationTurns: 12,
    toolCounts: { Read: 4, Edit: 2, Bash: 1 },
    tokenUsage: { inputTokens: 84_000, outputTokens: 6_200, cacheReadTokens: 210_000, cacheCreationTokens: 12_000 },
    costEstimate: 1.42,
    cacheCreationCostEstimate: 0.08,
    cacheReadCostEstimate: 0.21,
    healthScore: 92,
    lastTools: [{ name: 'Read', detail: 'src/main.ts' }],
    tasks: [],
    subagents: [],
    ...base,
  }
}

export const DEMO_AGENTS = [
  makeAgent({
    sessionId: 'demo-sess-weather-1',
    provider: 'claude',
    projectId: 'proj-weather',
    projectName: 'weather-cli',
    projectPath: '/home/dev/projects/weather-cli',
    cwd: '/home/dev/projects/weather-cli',
    status: 'active',
    working: true,
    model: 'claude-sonnet-5',
    currentAction: 'Refactoring the forecast cache layer',
    lastActivity: minutesAgo(0.2),
    uptime: 5_400,
    costEstimate: 2.14,
    healthScore: 96,
    lastTools: [{ name: 'Edit', detail: 'src/cache/forecastCache.ts' }, { name: 'Bash', detail: 'go test ./...' }],
    tasks: [
      { id: 'todo-1', subject: 'Add TTL eviction to forecast cache', status: 'in_progress' },
      { id: 'todo-2', subject: 'Write cache benchmark', status: 'pending' },
    ],
  }),
  makeAgent({
    sessionId: 'demo-sess-recipe-1',
    provider: 'claude',
    projectId: 'proj-recipe',
    projectName: 'recipe-api',
    projectPath: '/home/dev/projects/recipe-api',
    cwd: '/home/dev/projects/recipe-api',
    status: 'waiting',
    model: 'claude-sonnet-5',
    lastActivity: minutesAgo(1),
    uptime: 2_100,
    costEstimate: 0.63,
  }),
  makeAgent({
    sessionId: 'demo-sess-pixel-1',
    provider: 'claude',
    projectId: 'proj-pixel',
    projectName: 'pixel-editor',
    projectPath: '/home/dev/projects/pixel-editor',
    cwd: '/home/dev/projects/pixel-editor',
    status: 'waiting',
    model: 'claude-opus-5',
    lastActivity: minutesAgo(3),
    uptime: 9_000,
    costEstimate: 3.87,
    pipelineTaskId: 'task-pixel-release',
    pipelineTaskTitle: 'Cut pixel-editor v2.3 release',
    pendingPermissions: [
      { id: 'perm-pixel-1', tool: 'Bash', pattern: 'npm publish --access public', requestedAt: minutesAgo(3) },
    ],
  }),
  makeAgent({
    sessionId: 'demo-sess-notes-1',
    provider: 'claude',
    projectId: 'proj-notes',
    projectName: 'notes-sync',
    projectPath: '/home/dev/projects/notes-sync',
    cwd: '/home/dev/projects/notes-sync',
    status: 'idle',
    model: 'claude-sonnet-5',
    lastActivity: minutesAgo(8),
    uptime: 1_200,
    costEstimate: 0.04,
    errorState: 'rate_limited',
  }),
  makeAgent({
    sessionId: 'demo-sess-kontor-1',
    provider: 'claude',
    projectId: 'proj-kontor',
    projectName: 'kontor-demo',
    projectPath: '/home/dev/projects/kontor-demo',
    cwd: '/home/dev/projects/kontor-demo',
    status: 'idle',
    model: 'claude-haiku-4-5',
    lastActivity: minutesAgo(45),
    uptime: 600,
    costEstimate: 0.02,
  }),
  makeAgent({
    sessionId: 'demo-sess-weather-2',
    provider: 'claude',
    projectId: 'proj-weather',
    projectName: 'weather-cli',
    projectPath: '/home/dev/projects/weather-cli',
    cwd: '/home/dev/projects/weather-cli',
    status: 'waiting',
    model: 'claude-sonnet-5',
    lastActivity: minutesAgo(2),
    uptime: 3_000,
    costEstimate: 0.91,
  }),
  makeAgent({
    sessionId: 'demo-sess-recipe-2',
    provider: 'claude',
    projectId: 'proj-recipe',
    projectName: 'recipe-api',
    projectPath: '/home/dev/projects/recipe-api',
    cwd: '/home/dev/projects/recipe-api',
    status: 'active',
    working: true,
    model: 'claude-opus-5',
    currentAction: 'Writing integration tests for /recipes/search',
    lastActivity: minutesAgo(0.1),
    uptime: 780,
    costEstimate: 0.35,
  }),
  makeAgent({
    sessionId: 'demo-sess-pixel-2',
    provider: 'claude',
    projectId: 'proj-pixel',
    projectName: 'pixel-editor',
    projectPath: '/home/dev/projects/pixel-editor',
    cwd: '/home/dev/projects/pixel-editor',
    status: 'finished',
    model: 'claude-sonnet-5',
    lastActivity: minutesAgo(20),
    uptime: 4_500,
    costEstimate: 1.18,
  }),
]

// One agent with a rich transcript, opened as the agent-detail modal screenshot.
export const DETAIL_AGENT = makeAgent({
  sessionId: 'demo-sess-detail',
  provider: 'claude',
  projectId: 'proj-recipe',
  projectName: 'recipe-api',
  projectPath: '/home/dev/projects/recipe-api',
  cwd: '/home/dev/projects/recipe-api',
  status: 'active',
  working: true,
  model: 'claude-opus-5',
  currentAction: 'Implementing cursor-based pagination',
  lastActivity: minutesAgo(0.05),
  uptime: 3_600,
  costEstimate: 4.52,
  healthScore: 88,
  lastTools: [
    { name: 'Read', detail: 'src/routes/recipes.ts' },
    { name: 'Edit', detail: 'src/routes/recipes.ts' },
    { name: 'Bash', detail: 'go test ./internal/recipes/...' },
  ],
  tasks: [
    { id: 'todo-a', subject: 'Add cursor encode/decode helpers', status: 'completed' },
    { id: 'todo-b', subject: 'Wire cursor param into /recipes list', status: 'in_progress' },
    { id: 'todo-c', subject: 'Update OpenAPI spec', status: 'pending' },
  ],
})

export const DETAIL_TRANSCRIPT = [
  { role: 'human', content: 'Switch /recipes to cursor-based pagination instead of offset.', timestamp: minutesAgo(30) },
  { role: 'assistant', content: 'Sounds good. I will add an opaque cursor encoding the last (createdAt, id) pair and update the handler.', timestamp: minutesAgo(29) },
  { role: 'tool_call', toolName: 'Read', filePath: 'src/routes/recipes.ts', content: 'Read', timestamp: minutesAgo(28) },
  { role: 'tool_result', content: 'export function listRecipes(req, res) { ... }', timestamp: minutesAgo(28) },
  { role: 'task', taskId: 'todo-a', taskStatus: 'pending', content: 'Add cursor encode/decode helpers', timestamp: minutesAgo(27) },
  { role: 'task', taskId: 'todo-a', taskStatus: 'completed', content: 'Add cursor encode/decode helpers', timestamp: minutesAgo(20) },
  { role: 'tool_call', toolName: 'Edit', filePath: 'src/routes/recipes.ts', content: 'Edit', timestamp: minutesAgo(19) },
  { role: 'tool_result', content: '12 lines changed', timestamp: minutesAgo(19) },
  { role: 'assistant', content: 'Cursor helpers are in. Now wiring the `cursor` query param into the list handler and adding a regression test.', timestamp: minutesAgo(18) },
  { role: 'task', taskId: 'todo-b', taskStatus: 'in_progress', content: 'Wire cursor param into /recipes list', timestamp: minutesAgo(17) },
  { role: 'tool_call', toolName: 'Bash', content: 'go test ./internal/recipes/...', timestamp: minutesAgo(5) },
  { role: 'tool_result', content: 'ok  \tgithub.com/dev/recipe-api/internal/recipes\t0.412s', timestamp: minutesAgo(5) },
  { role: 'assistant', content: 'Tests pass. Updating the OpenAPI spec next.', timestamp: minutesAgo(3) },
]

// ---------------------------------------------------------------------------
// Pipeline tasks — one per board column, tied to the fictional projects.
// ---------------------------------------------------------------------------

function makeTask(overrides: Record<string, unknown>): Record<string, unknown> {
  return {
    description: null,
    worktreePath: null,
    sourceBranch: 'main',
    targetBranch: 'main',
    parentTaskId: null,
    maxIterations: 3,
    tokenBudget: null,
    costBudgetCents: null,
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: 'demo-user',
    rank: 0,
    needsUser: false,
    kind: 'pipeline',
    createdAt: '2026-09-20T09:00:00.000Z',
    updatedAt: minutesAgo(10),
    ...overrides,
  }
}

export const DEMO_TASKS = [
  makeTask({
    id: 'task-notes-backlog',
    slug: 'notes-conflict-banner',
    title: 'Surface sync-conflict banner in notes-sync',
    cwd: '/home/dev/projects/notes-sync',
    currentStage: 'backlog',
    priority: 'low',
  }),
  makeTask({
    id: 'task-kontor-ready',
    slug: 'kontor-demo-seed',
    title: 'Seed kontor-demo with sample widgets',
    cwd: '/home/dev/projects/kontor-demo',
    currentStage: 'ready',
  }),
  makeTask({
    id: 'task-weather-plan',
    slug: 'weather-radar-overlay',
    title: 'Add radar overlay to weather-cli map view',
    cwd: '/home/dev/projects/weather-cli',
    currentStage: 'plan_review',
    priority: 'high',
  }),
  makeTask({
    id: 'task-recipe-impl',
    slug: 'recipe-cursor-pagination',
    title: 'Switch /recipes to cursor-based pagination',
    cwd: '/home/dev/projects/recipe-api',
    currentStage: 'implementation',
    priority: 'high',
    activeSessionId: 'demo-sess-detail',
    activePid: (DETAIL_AGENT as { pid: number }).pid,
  }),
  makeTask({
    id: 'task-pixel-impl',
    slug: 'pixel-brush-engine',
    title: 'Rewrite brush stroke smoothing in pixel-editor',
    cwd: '/home/dev/projects/pixel-editor',
    currentStage: 'implementation',
  }),
  makeTask({
    id: 'task-pixel-release',
    slug: 'pixel-editor-release',
    title: 'Cut pixel-editor v2.3 release',
    cwd: '/home/dev/projects/pixel-editor',
    currentStage: 'self_review',
    hasPendingPermissions: true,
  }),
  makeTask({
    id: 'task-weather-final',
    slug: 'weather-cli-changelog',
    title: 'Finalize weather-cli v1.8 changelog',
    cwd: '/home/dev/projects/weather-cli',
    currentStage: 'finalization',
  }),
  makeTask({
    id: 'task-notes-done',
    slug: 'notes-sync-retry-backoff',
    title: 'Add exponential backoff to notes-sync retries',
    cwd: '/home/dev/projects/notes-sync',
    currentStage: 'done',
  }),
  makeTask({
    id: 'task-kontor-hold',
    slug: 'kontor-demo-onboarding',
    title: 'Draft kontor-demo onboarding checklist',
    cwd: '/home/dev/projects/kontor-demo',
    currentStage: 'on_hold',
  }),
  makeTask({
    id: 'task-recipe-cancelled',
    slug: 'recipe-graphql-spike',
    title: 'Spike: GraphQL layer over recipe-api',
    cwd: '/home/dev/projects/recipe-api',
    currentStage: 'cancelled',
  }),
]

// ---------------------------------------------------------------------------
// Obsidian graph (hub) — fictional vault, no real note titles.
// ---------------------------------------------------------------------------

const DEMO_NOTES: Array<[string, number]> = [
  ['Ideas/Caching strategy.md', DEMO_NOW.getTime() - 2 * 60 * 60_000],
  ['Ideas/Release checklist.md', DEMO_NOW.getTime() - 26 * 60 * 60_000],
  ['Ideas/Pagination options.md', DEMO_NOW.getTime() - 5 * 60 * 60_000],
  ['Journal/2026-09-26.md', DEMO_NOW.getTime() - 20 * 60 * 60_000],
  ['Journal/2026-09-25.md', DEMO_NOW.getTime() - 44 * 60 * 60_000],
  ['Reference/API design notes.md', DEMO_NOW.getTime() - 10 * 24 * 60 * 60_000],
  ['Reference/Brush engine research.md', DEMO_NOW.getTime() - 30 * 24 * 60 * 60_000],
  ['Reference/Onboarding draft.md', DEMO_NOW.getTime() - 60 * 24 * 60 * 60_000],
]

export function demoGraph() {
  return {
    configured: true,
    notes: DEMO_NOTES,
    links: [[0, 2], [1, 6], [2, 3], [3, 4], [5, 7]],
  }
}

// ---------------------------------------------------------------------------
// Cost analytics
// ---------------------------------------------------------------------------

function dayStr(daysAgo: number): string {
  return new Date(DEMO_NOW.getTime() - daysAgo * 86_400_000).toISOString().slice(0, 10)
}

export function demoCostSummary() {
  const byDay = Array.from({ length: 14 }, (_, i) => ({
    day: dayStr(13 - i),
    model: i % 2 === 0 ? 'claude-sonnet-5' : 'claude-opus-5',
    costUsd: Number((1.2 + Math.sin(i) * 0.6 + i * 0.08).toFixed(2)),
  }))
  const byWeek = [
    { week: '2026-W36', costUsd: 18.4 },
    { week: '2026-W37', costUsd: 22.1 },
  ]
  return {
    byModel: [
      { model: 'claude-opus-5', costUsd: 24.3, inputTokens: 1_400_000, outputTokens: 210_000, sessions: 18 },
      { model: 'claude-sonnet-5', costUsd: 15.8, inputTokens: 2_100_000, outputTokens: 180_000, sessions: 32 },
      { model: 'claude-haiku-4-5', costUsd: 1.9, inputTokens: 640_000, outputTokens: 40_000, sessions: 11 },
    ],
    byProject: DEMO_PROJECTS.map((p, i) => ({
      projectPath: p.path,
      projectName: p.name,
      costUsd: Number((14.2 - i * 2.4).toFixed(2)),
      inputTokens: 800_000 - i * 90_000,
      outputTokens: 60_000 - i * 6_000,
      sessions: 14 - i * 2,
    })),
    byDay,
    byWeek,
    totalUsd: 42.0,
    totalInputTokens: 4_140_000,
    totalOutputTokens: 430_000,
    from: dayStr(13),
    to: dayStr(0),
    updatedAt: DEMO_NOW.getTime(),
  }
}

export function demoHeatmap() {
  return { grid: Array.from({ length: 7 }, (_, d) => Array.from({ length: 24 }, (_, h) => Math.max(0, Math.round(Math.sin((d + h) / 3) * 4 + 3)))) }
}

export function demoForecast() {
  const trend = Array.from({ length: 14 }, (_, i) => ({ t: DEMO_NOW.getTime() - (13 - i) * 86_400_000, y: 1.2 + i * 0.15 }))
  const forecast = Array.from({ length: 7 }, (_, i) => ({ t: DEMO_NOW.getTime() + i * 86_400_000, projectedCost: 3.2 + i * 0.2 }))
  return {
    trend,
    forecast,
    alerts: [{ level: 'warn' as const, message: 'Projected to exceed last week\'s spend by 18% if this pace holds.' }],
  }
}

// ---------------------------------------------------------------------------
// GitHub / routines / memory widgets (Zentrale tiles)
// ---------------------------------------------------------------------------

function demoGitHubSummary() {
  return {
    repos: [{
      repo: 'devuser/recipe-api',
      pullRequests: [
        { number: 42, title: 'Switch /recipes to cursor-based pagination', author: 'devuser', url: 'https://github.com/devuser/recipe-api/pull/42', draft: false, updatedAt: minutesAgo(15), checks: { state: 'pending', passed: 6, failed: 0, total: 8, url: 'https://github.com/devuser/recipe-api/pull/42/checks' } },
        { number: 39, title: 'Add rate-limit headers', author: 'devuser', url: 'https://github.com/devuser/recipe-api/pull/39', draft: true, updatedAt: minutesAgo(180) },
      ],
      mergeable: true,
    }],
  }
}

function demoResources(kind: string) {
  if (kind === 'routine') {
    return [
      { id: 'routine-1', name: 'Nightly cost rescan', kind: 'routine', updatedAt: minutesAgo(600) },
      { id: 'routine-2', name: 'Weekly changelog draft', kind: 'routine', updatedAt: minutesAgo(1200) },
    ]
  }
  return [
    { id: 'mem-1', name: 'Caching strategy', kind: 'memory_space', updatedAt: minutesAgo(120) },
    { id: 'mem-2', name: 'Release checklist', kind: 'memory_space', updatedAt: minutesAgo(1560) },
  ]
}

// ---------------------------------------------------------------------------
// Route installation
// ---------------------------------------------------------------------------

const MOCK_HEADER = { 'x-screenshot-mock': '1' }

function json(body: unknown, status = 200) {
  return { status, contentType: 'application/json', headers: MOCK_HEADER, body: JSON.stringify(body) }
}

function emptyStream() {
  return { status: 200, contentType: 'text/event-stream', headers: MOCK_HEADER, body: '' }
}

/** Fails a request that reaches this handler with no more specific route above it. */
function fallbackFulfill(route: Route) {
  const url = route.request().url()
  if (url.includes('/stream') || url.includes('/import/status'))
    return route.fulfill(emptyStream())
  return route.fulfill(json({}))
}

export interface NetworkGuard {
  violations: string[]
}

/**
 * Fails closed: every /api/** request gets a safe empty response unless a more
 * specific route below overrides it. Also arms the network guard — any request
 * that is not same-origin with the page (i.e. would have left the vite dev
 * server) is recorded as a violation.
 */
export function installNetworkGuard(page: Page, viteOrigin: string): NetworkGuard {
  const guard: NetworkGuard = { violations: [] }

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.protocol === 'data:' || url.protocol === 'blob:')
      return
    if (url.origin !== viteOrigin)
      guard.violations.push(`request left the vite origin: ${request.url()}`)
  })

  page.on('requestfailed', (request) => {
    if (request.url().includes('/api/'))
      guard.violations.push(`/api request reached the network and failed: ${request.url()} (${request.failure()?.errorText})`)
  })

  page.on('response', (response) => {
    const url = response.url()
    if (!url.includes('/api/'))
      return
    if (response.headers()['x-screenshot-mock'] !== '1')
      guard.violations.push(`/api response was not served by a fixture route: ${url}`)
  })

  return guard
}

export async function assertNoNetworkLeaks(guard: NetworkGuard): Promise<void> {
  expect(guard.violations, `network guard recorded ${guard.violations.length} leak(s)`).toEqual([])
}

/**
 * Installs the fail-closed catch-all FIRST, then the specific fixture routes
 * on top — Playwright resolves the most-recently-registered matching route
 * first, so a specific route always wins and anything left over falls back to
 * the catch-all instead of the real network.
 */
const TASKS_RE = /\/api\/tasks(\?.*)?$/
const RESOURCES_RE = /\/api\/resources\?kind=/
const COST_SUMMARY_RE = /\/api\/cost\/summary/
const AGENT_OUTPUT_RE = /\/api\/agents\/[^/]+\/output/
const AGENT_REPLIES_RE = /\/api\/agents\/[^/]+\/replies/

export async function installDemoRoutes(page: Page): Promise<void> {
  await page.route('**/api/**', route => fallbackFulfill(route))

  await page.route('**/api/me', route => route.fulfill(json({ user: null, isAdmin: true, authEnabled: false })))
  await page.route('**/api/onboarding/status', route => route.fulfill(json({ completed: true, cliInstalled: true, cliVersion: '2.4.0', mcpRegistered: true })))
  await page.route('**/api/config', route => route.fulfill(json({ mcpServerName: 'kontor', mcpEndpoint: 'http://127.0.0.1:13120/mcp', homedir: '/home/dev', scriptPath: '/home/dev/.local/bin/kontor' })))
  await page.route('**/api/settings', route => route.fulfill(json([])))
  await page.route('**/api/usage', route => route.fulfill(json({
    windows: [
      { key: '5h', tokens: 1_840_000, costCents: 612, budgetTokens: 4_000_000, pct: 0.46 },
      { key: '7d', tokens: 21_300_000, costCents: 7_140, budgetTokens: 60_000_000, pct: 0.35 },
    ],
    accounts: [],
  })))
  await page.route('**/api/system', route => route.fulfill(json({
    cpu: { usage: 23, cores: 10, model: 'Demo CPU' },
    memory: { total: 32e9, used: 17.6e9, available: 14.4e9, usagePercent: 55 },
    disk: { total: 1e12, used: 4.1e11, available: 5.9e11, usagePercent: 41, mount: '/' },
    loadAvg: [1.42, 1.18, 0.97],
    uptime: 86_400,
  })))
  await page.route('**/api/spawners', route => route.fulfill(json([
    { id: 'claude-code', name: 'Claude Code', slug: 'claude-code', command: 'claude', args: [], env: {}, adapterType: 'claude', adapterConfig: {}, builtIn: true, isDefault: true, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' },
  ])))

  await page.route('**/api/agents', route => route.fulfill(json(DEMO_AGENTS)))
  await page.route('**/api/agents/stream', route => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    headers: MOCK_HEADER,
    body: `data: ${JSON.stringify({ agents: DEMO_AGENTS })}\n\n`,
  }))

  await page.route(TASKS_RE, route => route.fulfill(json(DEMO_TASKS)))
  await page.route('**/api/tasks/stream', route => route.fulfill(emptyStream()))
  await page.route('**/api/tasks/*/stage-runs', route => route.fulfill(json([])))
  await page.route('**/api/tasks/*/permissions', route => route.fulfill(json([])))
  await page.route('**/api/tasks/*/permission-requests', route => route.fulfill(json([])))
  await page.route('**/api/tasks/*/feedback', route => route.fulfill(json([])))
  await page.route('**/api/tasks/*/checkpoints', route => route.fulfill(json([])))

  await page.route('**/api/projects', route => route.fulfill(json(projectsApiResponse())))
  await page.route('**/api/projects/stream', route => route.fulfill(emptyStream()))

  await page.route('**/api/obsidian/graph', route => route.fulfill(json(demoGraph())))
  await page.route('**/api/github/summary', route => route.fulfill(json(demoGitHubSummary())))
  await page.route(RESOURCES_RE, (route) => {
    const kind = new URL(route.request().url()).searchParams.get('kind') ?? ''
    return route.fulfill(json(demoResources(kind)))
  })

  await page.route(COST_SUMMARY_RE, route => route.fulfill(json(demoCostSummary())))
  await page.route('**/api/analytics/heatmap', route => route.fulfill(json(demoHeatmap())))
  await page.route('**/api/analytics/cost-forecast', route => route.fulfill(json(demoForecast())))

  await page.route(AGENT_OUTPUT_RE, route => route.fulfill(json({ messages: [] })))
  await page.route(AGENT_REPLIES_RE, route => route.fulfill(json({ replies: [] })))
  await page.route('**/api/agents/demo-sess-detail/output', route => route.fulfill(json({ messages: DETAIL_TRANSCRIPT })))
  await page.route('**/api/agents/demo-sess-detail/replies', route => route.fulfill(json({ replies: [] })))
}
