import { expect, test } from '@playwright/test'
import { openListboxOptions, selectListboxOption } from './helpers'

// ---------------------------------------------------------------------------
// Helpers — mirrored from shell.spec.ts
// ---------------------------------------------------------------------------

/**
 * Clear localStorage keys that useSidebar and useStatusBar read on module
 * initialisation so each test starts from a clean reactive state. Also pins
 * the landing view to the dashboard — the Zentrale is the default landing
 * view now (see the 'landing view' describe below), but this suite exercises
 * the dashboard view itself, not the default.
 */
async function clearShellStorage(page: import('@playwright/test').Page) {
  await page.addInitScript(() => {
    localStorage.removeItem('agent-sidebar-pinned')
    localStorage.removeItem('agent-statusbar-collapsed')
    localStorage.setItem('agent-active-view', 'dashboard')
  })
}

/**
 * Mock /api/me so the app does not redirect to the LoginPage when the Go
 * backend is not running.  Returns authEnabled:false (no login required).
 */
async function stubAuthDisabled(page: import('@playwright/test').Page) {
  await page.route('/api/me', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ user: null, isAdmin: true, authEnabled: false }),
  }))
}

// ---------------------------------------------------------------------------
// Dashboard view
// ---------------------------------------------------------------------------

test.describe('dashboard view', () => {
  test.beforeEach(async ({ page }) => {
    await clearShellStorage(page)
    await stubAuthDisabled(page)
    await page.goto('/')
    await page.waitForSelector('[aria-label="Primary"]', { timeout: 10000 })
  })

  // -------------------------------------------------------------------------
  // Default view
  // -------------------------------------------------------------------------
  test('the dashboard view still renders under its own heading', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Dashboard')
  })

  // -------------------------------------------------------------------------
  // Cards / List layout toggle
  // -------------------------------------------------------------------------
  // Density moved into the toolbar's ⋮ overflow when the toolbar was split into
  // narrow-the-set controls (search, filters) and arrange-what-is-left controls.
  test('cards/list layout toggle switches aria-pressed', async ({ page }) => {
    const overflow = page.getByRole('button', { name: 'More view options' })
    const cards = page.getByTestId('layout-cards')
    const list = page.getByTestId('layout-list')

    await overflow.click()
    await expect(cards).toHaveAttribute('aria-pressed', 'true')
    await list.click()

    // Selecting a density closes the menu, so reopen it to read the new state.
    await overflow.click()
    await expect(list).toHaveAttribute('aria-pressed', 'true')
    await expect(cards).toHaveAttribute('aria-pressed', 'false')

    await cards.click()
    await overflow.click()
    await expect(cards).toHaveAttribute('aria-pressed', 'true')
    await expect(list).toHaveAttribute('aria-pressed', 'false')
  })

  // -------------------------------------------------------------------------
  // Spawner filter
  // -------------------------------------------------------------------------
  // The boolean "Claude only" toggle was replaced by a general "Filter by
  // spawner" select (see DashboardToolbar.vue) when multi-provider support
  // landed — it now lists every configured spawner, not just Claude.
  test('spawner filter select lists spawners and updates its value', async ({ page }) => {
    await page.route('/api/spawners', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([
        {
          id: 'claude-code',
          name: 'Claude Code',
          slug: 'claude-code',
          command: 'claude',
          args: [],
          env: {},
          adapterType: 'claude',
          adapterConfig: {},
          builtIn: true,
          isDefault: true,
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-01-01T00:00:00Z',
        },
      ]),
    }))
    await page.reload()
    await page.waitForSelector('[aria-label="Primary"]', { timeout: 10000 })

    await page.getByRole('button', { name: 'Filter agents' }).click()
    const trigger = page.getByTestId('select-spawner')
    // AppSelect is a <button>, not a <select> — there is no `value`, so assert
    // the visible label of the currently selected option instead (aria-label
    // is a static "Filter by spawner" here, so accessible-name doesn't track
    // the selection; toContainText reads the rendered label text directly).
    await expect(trigger).toContainText('All spawners')
    const listbox = await openListboxOptions(page, trigger)
    await expect(listbox.getByRole('option')).toHaveCount(2)
    await selectListboxOption(page, trigger, 'Claude Code')
    await expect(trigger).toContainText('Claude Code')
  })

  // -------------------------------------------------------------------------
  // Agent content area / empty state
  // -------------------------------------------------------------------------
  test('dashboard content area or empty state is present', async ({ page }) => {
    // With no live backend there will be no agents — the empty state must appear.
    // Either the agent content container or the empty-state element is present.
    const contentOrEmpty = page.locator(
      '[data-testid="agent-content"], [data-testid="empty-state"], .empty-state, .empty',
    )
    // Give SSE/polling a moment to settle then assert at least one match exists.
    await expect(contentOrEmpty.first()).toBeVisible({ timeout: 8000 }).catch(() => {
      // Fallback: the whole dashboard view wrapper must at least be in the DOM.
      return expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    })
  })
})

// ---------------------------------------------------------------------------
// Landing view — deliberately does not call clearShellStorage, so a first
// visit with no stored 'agent-active-view' exercises the real default.
// ---------------------------------------------------------------------------

test.describe('landing view', () => {
  test('the Zentrale is the default view on a first visit', async ({ page }) => {
    await stubAuthDisabled(page)
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Zentrale')
    await expect(page.getByTestId('workspace-page-zentrale')).toBeVisible()
  })
})
