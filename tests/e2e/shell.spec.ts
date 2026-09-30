import { expect, test } from '@playwright/test'
import { stubAgents } from './helpers'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Clear localStorage keys that useSidebar and useStatusBar read on module
 * initialisation. Without this, a previous test's pinned/collapsed state
 * leaks into the next test because the composables are module-level singletons
 * whose initial `ref` values are read from localStorage at import time.
 * Clearing before navigation ensures a clean reactive state when the page loads.
 */
async function clearShellStorage(page: import('@playwright/test').Page) {
  // addInitScript runs before the page executes any script, so the composables
  // read a clean localStorage on every test run.
  await page.addInitScript(() => {
    localStorage.removeItem('agent-sidebar-pinned')
    localStorage.removeItem('agent-statusbar-collapsed')
  })
}

/**
 * Mock /api/me so the app does not redirect to the LoginPage when the Go
 * backend is not running.  Returns authEnabled:false, which is also the
 * default standalone value (no login required).
 */
async function stubAuthDisabled(page: import('@playwright/test').Page) {
  await page.route('/api/me', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ user: null, isAdmin: true, authEnabled: false }),
  }))
}

// ---------------------------------------------------------------------------
// App shell
// ---------------------------------------------------------------------------

test.describe('app shell', () => {
  test.beforeEach(async ({ page }) => {
    await clearShellStorage(page)
    await stubAuthDisabled(page)
    await stubAgents(page)
    await page.goto('/')
    // Wait for the shell to mount (the Primary nav must be in the DOM).
    await page.waitForSelector('[aria-label="Primary"]', { timeout: 10000 })
  })

  // -------------------------------------------------------------------------
  // Sidebar nav switches view
  // -------------------------------------------------------------------------
  test('sidebar nav switches the active view', async ({ page }) => {
    await page
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('button', { name: 'Pipeline' })
      .click()
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Pipeline')
  })

  // -------------------------------------------------------------------------
  // Ctrl+B / Cmd+B toggles sidebar pin
  // -------------------------------------------------------------------------
  test('Ctrl+B toggles the sidebar pin', async ({ page }) => {
    const pin = page.getByTestId('sidebar-pin')
    // localStorage was cleared → sidebar starts unpinned
    await expect(pin).toHaveAttribute('aria-expanded', 'false')
    await page.keyboard.press('Control+b')
    await expect(pin).toHaveAttribute('aria-expanded', 'true')
    // Toggle back
    await page.keyboard.press('Control+b')
    await expect(pin).toHaveAttribute('aria-expanded', 'false')
  })

  // -------------------------------------------------------------------------
  // Status bar segment expands a detail panel
  // -------------------------------------------------------------------------
  test('status bar segment expands a detail panel', async ({ page }) => {
    // seg-cost is always rendered regardless of live system metrics; clicking it
    // opens panel-cost unconditionally (no v-if on metrics data inside that panel).
    await page.getByTestId('seg-cost').click()
    await expect(page.getByTestId('panel-cost')).toBeVisible()
  })

  // -------------------------------------------------------------------------
  // Status bar collapses to a corner tab
  // -------------------------------------------------------------------------
  test('status bar collapses to a corner tab', async ({ page }) => {
    // localStorage cleared → statusbar starts expanded; collapse button is visible
    await page.getByTestId('statusbar-collapse').click()
    await expect(page.getByTestId('statusbar-tab')).toBeVisible()
    // The main status bar row must be gone
    await expect(page.getByTestId('statusbar-collapse')).not.toBeVisible()
  })
})
