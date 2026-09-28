import type { APIRequestContext, APIResponse, Page } from '@playwright/test'
import { expect, test } from '@playwright/test'

// The client itself retries a 429 (useWorkspace's fetchWithRateLimitRetry), so an
// in-between 429 is not a save's outcome — skip it and wait for the response that
// actually settles the request.
function patched(page: Page) {
  return page.waitForResponse(resp =>
    resp.url().includes('/api/settings/workspace.layout') && resp.request().method() === 'PATCH' && resp.status() !== 429)
}

// Origin must match the server's own host (see 'Task API needs Origin header'
// in .agent-context/memory). The browser under test shares the server's per-IP
// rate limiter, so a 429 is retried — an ignored one leaves a seeded layout in
// place for the next test.
async function storeLayout(request: APIRequestContext, baseURL: string | undefined, value: string) {
  let res: APIResponse | undefined
  for (let attempt = 0; attempt < 5 && (!res || res.status() === 429); attempt++) {
    if (res)
      await new Promise(resolve => setTimeout(resolve, 1000))
    res = await request.patch('/api/settings/workspace.layout', {
      headers: { Origin: baseURL ?? 'http://localhost:13199' },
      data: { value },
    })
  }
  expect(res?.ok(), `store layout request (HTTP ${res?.status()})`).toBe(true)
}

// The stored layout is shared server-side state, not per-test-context state —
// reset it after every test so a mutation here can never leak into the next
// spec file or the next run of this one.
test.afterEach(async ({ request, baseURL }) => {
  await storeLayout(request, baseURL, '')
})

test('the Zentrale is the default page with the nine widgets', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Zentrale')
  for (const id of ['live-work', 'agents', 'routines', 'hub', 'kontor', 'github', 'pipeline', 'memory', 'cost-today'])
    await expect(page.getByTestId(`workspace-tile-${id}`)).toBeVisible()
})

test('a stored page view shows that page, and a page that is gone falls back to the Zentrale', async ({ page, request, baseURL }) => {
  const layout = {
    version: 1,
    pages: [
      { id: 'zentrale', title: 'Zentrale', tiles: [] },
      { id: 'p-morning', title: 'Morning', tiles: [{ widget: 'github', col: 1, row: 1, colSpan: 3, rowSpan: 3 }] },
    ],
  }
  await storeLayout(request, baseURL, JSON.stringify(layout))

  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.evaluate(() => localStorage.setItem('agent-active-view', 'page:p-morning'))
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Morning')
  await expect(page.getByTestId('workspace-tile-github')).toBeVisible()
  await expect(page.getByTestId('workspace-edit-toggle')).toBeVisible()

  await page.evaluate(() => localStorage.setItem('agent-active-view', 'page:p-gone'))
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-page-zentrale')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Zentrale')
  expect(await page.evaluate(() => localStorage.getItem('agent-active-view'))).toBe('zentrale')
})

// Only the real app shows whether pages are known before any workspace page has
// mounted — mounting App.vue in jsdom would need every stream and poller mocked.
test('pages are offered in the command palette on a view that holds no workspace page', async ({ page, request, baseURL }) => {
  const layout = {
    version: 1,
    pages: [
      { id: 'zentrale', title: 'Zentrale', tiles: [] },
      { id: 'p-morning', title: 'Morning', tiles: [] },
    ],
  }
  await storeLayout(request, baseURL, JSON.stringify(layout))

  // One boot, straight into the Dashboard: a second boot's burst drains the shared per-IP rate limiter.
  await page.addInitScript(() => localStorage.setItem('agent-active-view', 'dashboard'))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Dashboard')
  await expect(page.locator('[data-testid^="workspace-page-"]')).toHaveCount(0)

  await page.keyboard.press('ControlOrMeta+k')
  await page.getByPlaceholder('Search tasks and agents…').fill('Morning')
  // The load retries a 429 up to 3 times, up to 5 s apart (useWorkspace), so the entry may land late.
  await expect(page.getByTestId('spotlight-command-view:page:p-morning')).toContainText('Go to Morning', { timeout: 20_000 })
})

test('a moved tile stays moved after a reload', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('workspace-edit-toggle').click()
  await page.getByTestId('workspace-tile-cost-today').focus()
  // Each keypress saves through a fire-and-forget fetch (useWorkspace's
  // `write`, chained one save after the other), so reloading right after can
  // race an in-flight PATCH and revert to the still-unsaved value (observed
  // flaky) — wait for each save in turn before the next input or the reload.
  const firstSaved = patched(page)
  await page.keyboard.press('Shift+ArrowUp') // 3×3 → 3×2 frees row 12
  expect((await firstSaved).ok(), 'first save (resize) request').toBe(true)
  const secondSaved = patched(page)
  await page.keyboard.press('ArrowDown')
  expect((await secondSaved).ok(), 'second save (move) request').toBe(true)
  await page.getByTestId('workspace-edit-toggle').click()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-tile-cost-today')).toHaveAttribute('style', /--row: 11/)
})

test('"/" opens the Kontor tile and Escape closes it', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  // Wait for the collapsed tile — the Kontor widget is behind an async chunk,
  // and pressing '/' before it mounts its own key listener is a race.
  await expect(page.getByTestId('kontor-collapsed')).toBeVisible()
  await page.locator('body').press('/')
  await expect(page.getByTestId('kontor-expanded')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('kontor-expanded')).toBeHidden()
})

// R11: jsdom (the component-test environment) never lays out real pixels, so
// the CSS that keeps the resize handle clickable — its position, its stacking
// order above the tile's own pointer-events:none overlay — can only be
// exercised by a real browser drag.
test('a pointer drag on the resize handle resizes the cost-today tile and the new size persists', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('workspace-edit-toggle').click()

  // cost-today sits at the bottom of a 12-row grid that is commonly taller
  // than the viewport in edit mode — scroll it into view first, so the raw
  // page.mouse coordinates below land inside the actual visible viewport.
  const handleLocator = page.getByTestId('workspace-resize-cost-today')
  await handleLocator.scrollIntoViewIfNeeded()

  const grid = await page.getByTestId('workspace-grid').boundingBox()
  const handle = await handleLocator.boundingBox()
  if (!grid || !handle)
    throw new Error('workspace grid or resize handle did not render')

  // Default layout: cost-today spans rows 10-12 of 12 — the page's last row.
  // cellAt() (gridGeometry.ts) maps a pointer position to a row via
  // floor((y - top) / (rowHeight + gap)) + 1, so moving the pointer down by
  // roughly one row's pitch from row 12's band reaches row 13 — one row past
  // the grid, which growing the grid allows. Rather than trust one blind
  // pixel offset (observed flaky against sub-pixel layout variance), the drag
  // watches the live ghost preview (workspace-ghost carries the same
  // --row-span the app renders) and stops the moment it shows the target
  // size — the same feedback a real user watches while dragging.
  const rowPitch = (grid.height + 12) / 12
  const x = handle.x + handle.width / 2
  const startY = handle.y + handle.height / 2
  const ghost = page.getByTestId('workspace-ghost')

  // The resize saves through a fire-and-forget fetch (useWorkspace's `write`),
  // so the drop and its PATCH are two different moments — reloading before the
  // request lands reverts to the still-unsaved server value (observed flaky:
  // the ghost proves the drag itself always lands correctly, but the reload
  // assertion below does not). Arm the wait before the drop that triggers it.
  const saved = patched(page)

  await page.mouse.move(x, startY)
  await page.mouse.down()
  let reachedTarget = false
  for (let i = 1; i <= 20 && !reachedTarget; i++) {
    await page.mouse.move(x, startY + i * (rowPitch / 4))
    const style = await ghost.getAttribute('style')
    reachedTarget = !!style && /--row-span: 4\b/.test(style)
  }
  await page.mouse.up()
  if (!reachedTarget)
    throw new Error('drag never reached a 4-row ghost preview')

  await expect(page.getByTestId('workspace-tile-cost-today')).toHaveAttribute('style', /--row-span: 4\b/)
  expect((await saved).ok(), 'save (resize) request').toBe(true)

  await page.getByTestId('workspace-edit-toggle').click()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-tile-cost-today')).toHaveAttribute('style', /--row-span: 4\b/)
})

// A reload reads the layout from the server exactly as a restart does: the
// layout lives only in the settings table.
test('a page of my own survives a reload', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('nav-new-page').click()
  await page.getByTestId('nav-new-page-input').fill('Morning')
  const created = patched(page)
  await page.getByTestId('nav-new-page-input').press('Enter')
  expect((await created).ok(), 'save (new page) request').toBe(true)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Morning')

  await page.getByTestId('workspace-add').selectOption('github')
  const filled = patched(page)
  await page.getByTestId('workspace-add-submit').click()
  expect((await filled).ok(), 'save (add tile) request').toBe(true)
  await page.getByTestId('workspace-done').click()

  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.locator('[data-testid^="nav-page-"]', { hasText: 'Morning' }).click()
  await expect(page.getByTestId('workspace-tile-github')).toBeVisible()
})

test('a page of my own is renamed and deleted in its edit mode', async ({ page, request, baseURL }) => {
  const layout = {
    version: 1,
    pages: [
      { id: 'zentrale', title: 'Zentrale', tiles: [] },
      { id: 'p-morning', title: 'Morning', tiles: [{ widget: 'github', col: 1, row: 1, colSpan: 3, rowSpan: 3 }] },
    ],
  }
  await storeLayout(request, baseURL, JSON.stringify(layout))
  await page.addInitScript(() => localStorage.setItem('agent-active-view', 'page:p-morning'))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Morning')
  await page.getByTestId('workspace-edit-toggle').click()

  await page.getByTestId('workspace-rename').fill('Dawn')
  const renamed = patched(page)
  await page.getByTestId('workspace-rename').press('Enter')
  expect((await renamed).ok(), 'save (rename) request').toBe(true)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Dawn')
  await expect(page.getByTestId('nav-page-p-morning')).toContainText('Dawn')

  await page.getByTestId('workspace-delete-page').click()
  await expect(page.getByTestId('workspace-delete-confirm')).toHaveText('Delete Dawn and its tiles?')
  const removed = patched(page)
  await page.getByTestId('workspace-delete-confirm').click()
  expect((await removed).ok(), 'save (delete page) request').toBe(true)
  await expect(page.getByTestId('workspace-page-zentrale')).toBeVisible()
  await expect(page.getByTestId('nav-page-p-morning')).toHaveCount(0)
})

// An edit on the built-in layout before the stored one arrives would save over it.
test('the layout cannot be edited before it has loaded', async ({ page }) => {
  let release!: () => void
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  await page.route('**/api/settings', async (route) => {
    await held
    await route.continue()
  })
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-page-zentrale')).toBeVisible()
  await expect(page.getByTestId('workspace-edit-toggle')).toHaveCount(0)
  await expect(page.getByTestId('nav-new-page')).toHaveCount(0)

  release()
  await expect(page.getByTestId('workspace-edit-toggle')).toBeVisible()
  await expect(page.getByTestId('nav-new-page')).toBeVisible()
})

// A stale tab after a server upgrade asks for a page chunk the server no longer has.
test('a page whose code fails to load says so and keeps what needs you on screen', async ({ page }) => {
  await page.route('**/assets/WorkspacePage-*.js', route => route.abort())
  const agent = {
    pid: 4242,
    sessionId: 'sess-colour',
    provider: 'claude',
    projectName: 'agent-dashboard',
    projectPath: '/repo/agent-dashboard',
    cwd: '/repo/agent-dashboard',
    status: 'waiting',
    working: false,
    lastActivity: new Date().toISOString(),
    uptime: 120,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    costEstimate: 0,
    lastTools: [],
    tasks: [],
    subagents: [],
    pendingQuestion: { header: 'Colour', question: 'Which colour do you prefer?', multiSelect: false, options: [{ index: 1, label: 'Red' }], typeSomethingIndex: 2, chatAboutIndex: 3 },
  }
  await page.route('/api/agents', route => route.fulfill({ json: [agent] }))
  await page.route('/api/agents/stream', route => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    body: `data: ${JSON.stringify({ agents: [agent] })}\n\n`,
  }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('page-load-error')).toBeVisible()
  const strip = page.getByTestId('needs-you')
  await expect(strip).toHaveAttribute('data-variant', 'strip')
  await expect(strip).toContainText('Which colour do you prefer?')
  await expect(page.getByTestId('nav-new-page')).toBeVisible()
  await expect(page.getByTestId('workspace-edit-toggle')).toHaveCount(0)
})

test('the layout cannot be edited while the error line replaces the page', async ({ page }) => {
  await page.route('/api/agents', route => route.fulfill({ status: 500 }))
  await page.route('/api/agents/stream', route => route.abort())
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByText('Error: HTTP 500')).toBeVisible()
  await expect(page.getByTestId('nav-new-page')).toBeVisible()
  await expect(page.getByTestId('workspace-edit-toggle')).toHaveCount(0)
})
