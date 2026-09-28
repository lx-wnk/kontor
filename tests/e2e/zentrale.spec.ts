import { expect, test } from '@playwright/test'
import { waitForLayoutPatch as patched, storeLayout } from './helpers'

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

  // Set the view before the first boot: the Dashboard has to be the view the app opens on, not one it navigates to.
  await page.addInitScript(() => localStorage.setItem('agent-active-view', 'dashboard'))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Dashboard')
  await expect(page.locator('[data-testid^="workspace-page-"]')).toHaveCount(0)

  await page.keyboard.press('ControlOrMeta+k')
  await page.getByPlaceholder('Search tasks and agents…').fill('Morning')
  await expect(page.getByTestId('spotlight-command-view:page:p-morning')).toContainText('Go to Morning')
})

test('a moved tile stays moved after a reload', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('workspace-edit-toggle').click()
  // One Done button while editing: the topbar toggle hides in favour of the edit bar's own.
  await expect(page.getByTestId('workspace-edit-toggle')).toHaveCount(0)
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
  await page.getByTestId('workspace-done').click()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-tile-cost-today')).toHaveAttribute('style', /--row: 11/)
})

test('opening and closing edit mode by keyboard keeps focus on a reachable control', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('workspace-edit-toggle').focus()
  await page.keyboard.press('Enter')
  await expect(page.getByTestId('workspace-done')).toBeFocused()

  await page.keyboard.press('Enter')
  await expect(page.getByTestId('workspace-edit-toggle')).toBeFocused()
})

test('edit mode ends on navigation and does not survive coming back', async ({ page, request, baseURL }) => {
  const layout = {
    version: 1,
    pages: [
      { id: 'zentrale', title: 'Zentrale', tiles: [] },
      { id: 'p-morning', title: 'Morning', tiles: [] },
    ],
  }
  await storeLayout(request, baseURL, JSON.stringify(layout))
  await page.addInitScript(() => localStorage.setItem('agent-active-view', 'page:p-morning'))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('workspace-edit-toggle').click()
  await expect(page.getByTestId('workspace-edit-bar')).toBeVisible()

  await page.getByTestId('nav-item-dashboard').click()
  await expect(page.getByTestId('workspace-edit-bar')).toHaveCount(0)

  await page.locator('[data-testid^="nav-page-"]', { hasText: 'Morning' }).click()
  await expect(page.getByTestId('workspace-edit-bar')).toHaveCount(0)
  await expect(page.getByTestId('workspace-edit-toggle')).toBeVisible()
})

// SC 2.4.3: choosing a view lands focus in its content, not stuck on the nav button just activated.
test('choosing a view from the sidebar still lands focus on #main-content', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  // The hub's launchers mirror the sidebar's views; resolve the nav only once they are on screen.
  await expect(page.getByTestId('hub-launcher-dashboard')).toBeVisible()

  await page.getByTestId('nav-item-dashboard').focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main-content')).toBeFocused()

  await page.getByTestId('nav-item-pipeline').click()
  await expect(page.locator('#main-content')).toBeFocused()
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

  await page.getByTestId('workspace-done').click()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('workspace-tile-cost-today')).toHaveAttribute('style', /--row-span: 4\b/)
})

// A reload reads the layout from the server exactly as a restart does — it lives only in the settings table.
test('a page of my own survives a reload', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('nav-new-page').click()
  await page.getByTestId('nav-new-page-input').fill('Morning')
  const created = patched(page)
  await page.getByTestId('nav-new-page-input').press('Enter')
  expect((await created).ok(), 'save (new page) request').toBe(true)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Morning')
  // Edit mode survived the navigation watcher, and focus landed on the new page's
  // own nav item rather than being pulled back to #main-content by App.vue's watcher.
  await expect(page.getByTestId('workspace-edit-bar')).toBeVisible()
  await expect(page.locator('[data-testid^="nav-page-"]', { hasText: 'Morning' })).toBeFocused()

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

  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Dawn')
  await expect(page.getByTestId('nav-page-p-morning')).toContainText('Dawn')
  await page.getByTestId('workspace-edit-toggle').click()

  await page.getByTestId('workspace-delete-page').click()
  await expect(page.getByTestId('workspace-delete-confirm')).toHaveText('Delete Dawn and its tiles?')
  const removed = patched(page)
  await page.getByTestId('workspace-delete-confirm').click()
  expect((await removed).ok(), 'save (delete page) request').toBe(true)
  await expect(page.getByTestId('workspace-page-zentrale')).toBeVisible()
  await expect(page.getByTestId('nav-page-p-morning')).toHaveCount(0)
  // Focus landed on the Zentrale nav item, not pulled back to #main-content by the navigation watcher.
  await expect(page.getByTestId('nav-item-zentrale')).toBeFocused()

  await page.reload({ waitUntil: 'domcontentloaded' })
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
  await expect(page.getByTestId('nav-new-page')).toBeDisabled()

  release()
  await expect(page.getByTestId('workspace-edit-toggle')).toBeVisible()
  await expect(page.getByTestId('nav-new-page')).toBeEnabled()
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

test('the hub widens on F, opens the sidebar New page input from its launcher and fires a launcher by digit', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  const stage = page.getByTestId('hub-stage')
  const widen = stage.getByRole('button', { name: 'Widen' })

  await stage.press('f')
  await expect(widen).toHaveAttribute('aria-pressed', 'true')
  await stage.press('f')
  await expect(widen).toHaveAttribute('aria-pressed', 'false')

  await page.getByTestId('hub-launcher-new-page').click()
  await expect(page.getByTestId('nav-new-page-input')).toBeFocused()

  await stage.press('1')
  await expect.poll(() => page.evaluate(() => localStorage.getItem('agent-active-view'))).toBe('dashboard')
})

test('Escape on the hub with the Kontor overlay open collapses only the overlay', async ({ page }) => {
  // Reduced motion makes the hub's fit synchronous, so a wrong fit shows up without waiting out a flight.
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  const stage = page.getByTestId('hub-stage')
  const camera = () => stage.locator('svg g').first().getAttribute('transform')

  const before = await camera()
  await stage.press('+')
  const zoomed = await camera()
  expect(zoomed).not.toBe(before)
  await page.getByTestId('hub-core').click()
  await expect(page.getByTestId('kontor-expanded')).toBeVisible()
  // The overlay focuses its own input; the operator going back to the map leaves it open.
  await stage.focus()

  await page.keyboard.press('Escape')
  await expect(page.getByTestId('kontor-expanded')).toHaveCount(0)
  expect(await camera()).toBe(zoomed)
})

test('below md the stacked tiles keep their content height and the hub stays visible', async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 900 })
  await page.goto('/')
  await expect(page.getByTestId('hub-stage')).toBeVisible()
  expect((await page.getByTestId('hub').boundingBox())!.height).toBeGreaterThanOrEqual(544)
  const tiles = await page.locator('[data-testid^="workspace-tile-"]').evaluateAll(els => els
    .map(el => ({ widget: el.getAttribute('data-testid'), top: el.getBoundingClientRect().top, contentBottom: el.firstElementChild!.getBoundingClientRect().bottom }))
    .sort((a, b) => a.top - b.top))
  for (let i = 1; i < tiles.length; i++)
    expect(tiles[i].top, `${tiles[i].widget} starts below ${tiles[i - 1].widget}'s content`).toBeGreaterThanOrEqual(tiles[i - 1].contentBottom - 0.5)
})
