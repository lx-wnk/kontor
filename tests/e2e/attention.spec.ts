import type { Page } from '@playwright/test'
import { expect, test } from '@playwright/test'
import { parkPointerOffNav, stubAgents, stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

const NOW = new Date().toISOString()
const TASK = {
  id: 't1',
  slug: 'my-task',
  title: 'My Task',
  description: 'desc',
  cwd: '/tmp',
  worktreePath: '',
  sourceBranch: 'main',
  targetBranch: 'main',
  currentStage: 'implementation',
  parentTaskId: null,
  maxIterations: 3,
  tokenBudget: 0,
  costBudgetCents: 0,
  createdAt: NOW,
  updatedAt: NOW,
  metadata: {},
  silverBullet: false,
  planMode: false,
  priority: 'medium',
  userId: '',
  rank: 0,
  needsUser: false,
  hasPendingPermissions: true,
}
const request = (id: string) => ({ id, stageRunId: 'sr1', tool: 'Bash', pattern: id, reason: null, requestedAt: NOW, resolvedAt: null, outcome: null })

async function stubPermissions(page: Page, ids: string[]): Promise<{ resolved: () => Array<{ id: string, decision: unknown }> }> {
  await stubAuthDisabled(page)
  await stubAgents(page)
  await stubJson(page, '/api/projects', [])
  await stubEmptyStream(page, '/api/projects/stream')
  await stubJson(page, '/api/tasks', [TASK])
  await stubEmptyStream(page, '/api/tasks/stream')
  await stubJson(page, '/api/tasks/t1/permission-requests', ids.map(request))
  const resolved: Array<{ id: string, decision: unknown }> = []
  await page.route(/\/api\/tasks\/t1\/permission-requests\/([^/]+)\/resolve$/, async (route) => {
    resolved.push({ id: route.request().url().split('/').slice(-2)[0]!, decision: route.request().postDataJSON().decision })
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
  })
  return { resolved: () => resolved }
}

const card = (page: Page, id = 'p1') => page.locator(`[data-testid="needs-you"] li[data-key="permission:${id}"]`).first()

test.describe('triage keys on every view', () => {
  test('n focuses the first card and a approves it on the Zentrale', async ({ page }) => {
    const stub = await stubPermissions(page, ['p1'])
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page)).toBeVisible()
    await expect(card(page)).not.toHaveAttribute('aria-current', 'true')

    await page.keyboard.press('n')
    await expect(card(page)).toHaveAttribute('aria-current', 'true')

    await page.keyboard.press('a')
    await expect.poll(stub.resolved).toEqual([{ id: 'p1', decision: 'allow_once' }])
  })

  test('typing n in the spotlight input does not move focus', async ({ page }) => {
    await stubPermissions(page, ['p1'])
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page)).toBeVisible()

    await page.keyboard.press('ControlOrMeta+k')
    const input = page.getByRole('dialog').getByRole('combobox').or(page.getByRole('dialog').locator('input')).first()
    await input.fill('')
    await input.press('n')
    await expect(page.locator('[data-testid="needs-you"] li[aria-current="true"]')).toHaveCount(0)
  })
  test('Shift+A approves exactly the listed permissions', async ({ page }) => {
    const stub = await stubPermissions(page, ['p1', 'p2'])
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page, 'p2')).toBeVisible()

    await page.keyboard.press('Shift+A')
    await expect.poll(() => stub.resolved().map(r => r.id).sort()).toEqual(['p1', 'p2'])
    expect(stub.resolved().every(r => r.decision === 'allow_once')).toBe(true)
  })

  test('n then d denies the focused permission once', async ({ page }) => {
    const stub = await stubPermissions(page, ['p1'])
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page)).toBeVisible()

    await page.keyboard.press('n')
    await expect(card(page)).toHaveAttribute('aria-current', 'true')
    await page.keyboard.press('d')
    await expect.poll(stub.resolved).toEqual([{ id: 'p1', decision: 'deny_once' }])
  })

  test('Shift+A does nothing while the hub has not mounted its queue', async ({ page }) => {
    const stub = await stubPermissions(page, ['p1', 'p2'])
    let release: () => void = () => {}
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    await page.route('**/assets/WorkspacePage-*.js', async (route) => {
      await held
      await route.continue()
    })
    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await parkPointerOffNav(page)
    await expect(page).toHaveTitle(/^\(2\)/)
    await expect(page.locator('[data-testid="needs-you"]')).toHaveCount(0)

    await page.keyboard.press('Shift+A')
    await page.waitForTimeout(500)
    expect(stub.resolved()).toEqual([])

    release()
    await expect(card(page, 'p2')).toBeVisible()
    expect(stub.resolved()).toEqual([])
  })

  test('a repeated keydown does not resolve; a normal one does once', async ({ page }) => {
    const stub = await stubPermissions(page, ['p1'])
    await page.addInitScript(() => localStorage.setItem('agent-active-view', 'cost'))
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page)).toBeVisible()

    await page.keyboard.press('n')
    await expect(card(page)).toHaveAttribute('aria-current', 'true')

    const handled = await page.evaluate(() => !window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', repeat: true, bubbles: true, cancelable: true })))
    expect(handled).toBe(false)
    expect(stub.resolved()).toEqual([])

    await page.keyboard.press('a')
    await expect.poll(stub.resolved).toEqual([{ id: 'p1', decision: 'allow_once' }])
  })
})

test.describe('fleet filter chips', () => {
  const agent = (pid: number, extra: Record<string, unknown>) => ({
    pid,
    sessionId: `sess-${pid}`,
    provider: 'claude',
    projectName: `proj-${pid}`,
    projectPath: `/repo/proj-${pid}`,
    cwd: `/repo/proj-${pid}`,
    status: 'active',
    working: true,
    lastActivity: new Date().toISOString(),
    uptime: 120,
    channelAvailable: false,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    costEstimate: 0,
    lastTools: [],
    tasks: [],
    subagents: [],
    ...extra,
  })
  const asking = agent(1, {
    pendingQuestion: {
      header: 'Colour',
      question: 'Which colour?',
      multiSelect: false,
      options: [{ index: 1, label: 'Red' }],
      typeSomethingIndex: 2,
      chatAboutIndex: 3,
    },
  })

  test('Needs you leaves only the asking agent on the roster', async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('agent-active-view', 'dashboard'))
    await stubAuthDisabled(page)
    await stubAgents(page, [asking, agent(2, {})])
    await page.goto('/')

    const chips = page.getByRole('group', { name: 'Filter agents by state' })
    await expect(chips.getByRole('button', { name: 'All 2' })).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByTestId('agent-card-project')).toHaveCount(2)

    await parkPointerOffNav(page)
    await chips.getByRole('button', { name: 'Needs you 1' }).click()

    await expect(chips.getByRole('button', { name: 'Needs you 1' })).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByTestId('agent-card-project')).toHaveCount(1)
    await expect(page.getByTestId('agent-card-project')).toHaveText('Proj 1')
  })
})

test.describe('put away persists', () => {
  const finished = (sessionId: string, lastActivity: string) => ({
    pid: 4242,
    sessionId,
    provider: 'claude',
    projectName: 'put-away-proj',
    projectPath: '/repo/put-away-proj',
    cwd: '/repo/put-away-proj',
    status: 'finished',
    working: false,
    lastActivity,
    uptime: 120,
    channelAvailable: false,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    costEstimate: 0,
    lastTools: [],
    tasks: [],
    subagents: [],
  })

  async function stubLive(page: Page, current: () => unknown[]): Promise<void> {
    await stubAuthDisabled(page)
    await page.route('/api/agents', route => route.fulfill({ json: current() }))
    await page.route('/api/agents/stream', route => route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: `data: ${JSON.stringify({ agents: current() })}\n\n`,
    }))
    await stubJson(page, '/api/projects', [])
    await stubEmptyStream(page, '/api/projects/stream')
    await stubJson(page, '/api/tasks', [])
    await stubEmptyStream(page, '/api/tasks/stream')
  }

  test('a put-away finished agent stays gone across reload and contexts until it posts again', async ({ page, browser, baseURL }) => {
    const sid = `e2e-${Date.now()}-finished`
    const oldSid = `${sid}-old`
    const controlSid = `e2e-${Date.now()}-control`
    let agents: unknown[] = []
    await stubLive(page, () => agents)
    await page.addInitScript(() => localStorage.setItem('agent-active-view', 'cost'))

    // On a fresh DB the first read writes the server baseline (R12); activity must be newer than it.
    const [baselineRes] = await Promise.all([
      page.waitForResponse(r => r.url().includes('/api/attention/marks') && r.request().method() === 'GET'),
      page.goto('/'),
    ])
    expect(baselineRes.status()).toBe(200)

    const lastActivity = new Date().toISOString()
    agents = [finished(sid, lastActivity), finished(controlSid, lastActivity), finished(oldSid, '2020-01-01T00:00:00Z')]
    await page.reload()
    const shown = (p: Page, id: string) => p.locator(`[data-testid="needs-you"] li[data-key="finished:${id}"]`)
    await expect(shown(page, sid)).toBeVisible()
    await expect(shown(page, controlSid)).toBeVisible()
    await expect(shown(page, oldSid)).toHaveCount(0)

    await parkPointerOffNav(page)
    await shown(page, sid).getByTestId('needs-you-put-away').click()
    await expect(shown(page, controlSid)).toBeVisible()
    await expect(shown(page, sid)).toHaveCount(0)

    await page.reload()
    await expect(shown(page, controlSid)).toBeVisible()
    await expect(shown(page, sid)).toHaveCount(0)

    const other = await browser.newContext({ baseURL })
    try {
      const page2 = await other.newPage()
      await stubLive(page2, () => agents)
      await page2.addInitScript(() => localStorage.setItem('agent-active-view', 'cost'))
      await page2.goto('/')
      await expect(shown(page2, controlSid)).toBeVisible()
      await expect(shown(page2, sid)).toHaveCount(0)
    }
    finally {
      await other.close()
    }

    agents = [finished(sid, new Date(Date.parse(lastActivity) + 1000).toISOString()), finished(controlSid, lastActivity)]
    await page.reload()
    await expect(shown(page, sid)).toBeVisible()
  })

  test('opening a finished agent clears its card across a reload', async ({ page }) => {
    const sid = `e2e-${Date.now()}-opened`
    const controlSid = `e2e-${Date.now()}-opened-control`
    let agents: unknown[] = []
    await stubLive(page, () => agents)
    await stubJson(page, `/api/agents/${sid}/output`, { messages: [] })
    await stubJson(page, `/api/agents/${sid}/replies`, { replies: [] })
    await page.addInitScript(() => localStorage.setItem('agent-active-view', 'cost'))
    await page.goto('/')

    const lastActivity = new Date().toISOString()
    agents = [finished(sid, lastActivity), finished(controlSid, lastActivity)]
    await page.reload()
    const shown = (id: string) => page.locator(`[data-testid="needs-you"] li[data-key="finished:${id}"]`)
    await expect(shown(sid)).toBeVisible()
    await expect(shown(controlSid)).toBeVisible()

    const marked = page.waitForResponse(r => r.url().includes('/api/attention/marks') && r.request().method() === 'PUT')
    await parkPointerOffNav(page)
    await shown(sid).getByTestId('mission-open-agent').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    expect((await marked).status()).toBe(200)
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()

    await page.reload()
    await expect(shown(controlSid)).toBeVisible()
    await expect(shown(sid)).toHaveCount(0)
  })

  test('the marks endpoint rejects a malformed key', async ({ request, baseURL }) => {
    const res = await request.put('/api/attention/marks', {
      headers: { Origin: baseURL! },
      data: { key: 'bad key', seenThrough: 'x' },
    })
    expect(res.status()).toBe(400)
  })
})

test('the docked hub queue leaves out finished agents but keeps permissions', async ({ page }) => {
  await stubPermissions(page, ['p1'])
  await stubAgents(page, [{
    pid: 4343,
    sessionId: `e2e-${Date.now()}-dock`,
    provider: 'claude',
    projectName: 'dock-proj',
    projectPath: '/repo/dock-proj',
    cwd: '/repo/dock-proj',
    status: 'finished',
    working: false,
    lastActivity: new Date(Date.now() + 60_000).toISOString(),
    uptime: 120,
    channelAvailable: false,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    costEstimate: 0,
    lastTools: [],
    tasks: [],
    subagents: [],
  }])
  await page.goto('/')
  await expect(card(page)).toBeVisible()
  await expect(page).toHaveTitle(/^\(2\)/)
  await expect(page.locator('[data-testid="needs-you"] li[data-key^="finished:"]')).toHaveCount(0)
})
