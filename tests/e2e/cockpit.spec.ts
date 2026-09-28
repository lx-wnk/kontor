import type { Page } from '@playwright/test'
import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

const PANEL_STATES = ['loading', 'notAsked', 'denied', 'empty', 'failed']

/**
 * The five-state rule, asserted at the surface a user actually sees: exactly
 * one state marker per panel, never two. `toHaveCount(0)` on every sibling is
 * what makes it an assertion rather than a hope — the same shape
 * settings-gated-panels.spec.ts uses.
 */
async function expectOnlyState(page: Page, panel: string, state: string) {
  await expect(page.getByTestId(`cockpit-${panel}-${state}`)).toBeVisible()
  for (const other of PANEL_STATES.filter(s => s !== state))
    await expect(page.getByTestId(`cockpit-${panel}-${other}`)).toHaveCount(0)
}

async function openCockpit(page: Page) {
  await page.goto('/')
  await expect(page.getByTestId('workspace-page-zentrale')).toBeVisible()
}

test.describe('cockpit panels', () => {
  test.beforeEach(async ({ page }) => {
    await stubAuthDisabled(page)
    await stubJson(page, '/api/agents', [])
    await stubEmptyStream(page, '/api/agents/stream')
    await stubEmptyStream(page, '/api/tasks/stream')
    await stubJson(page, '/api/config', { mcpServerName: 'agent-dashboard', mcpEndpoint: '/api/mcp' })
    await stubJson(page, '/api/spawners', [])
  })

  test('GitHub: unconfigured is not an empty repository list', async ({ page }) => {
    await stubJson(page, '/api/github/summary', { error: 'github is not configured' }, 503)
    await openCockpit(page)
    await expectOnlyState(page, 'github', 'notAsked')
  })

  test('GitHub: denied renders the server reason, not an empty list', async ({ page }) => {
    await stubJson(page, '/api/github/summary', { error: 'capability denied: github.read' }, 403)
    await openCockpit(page)
    await expectOnlyState(page, 'github', 'denied')
    await expect(page.getByTestId('cockpit-github-denied')).toContainText('github.read')
  })

  test('GitHub: a confirmed-empty answer is distinct from a refusal and from a failure', async ({ page }) => {
    await stubJson(page, '/api/github/summary', { repos: [{ repo: 'lx-wnk/agent-dashboard', pullRequests: [] }] })
    await openCockpit(page)
    await expectOnlyState(page, 'github', 'empty')
  })

  // PANEL_STATES above carries only the five marker states, so expectOnlyState
  // can never assert the populated panel — and no other case in this file stubs
  // a non-empty pullRequests. Without this, a panel stuck on 'empty' passes the
  // whole suite, which is the same shape as a loading state nothing pins.
  test('GitHub: a populated answer renders the pull requests, not a state marker', async ({ page }) => {
    await stubJson(page, '/api/github/summary', {
      repos: [{
        repo: 'lx-wnk/agent-dashboard',
        pullRequests: [{ number: 42, title: 'Add the cockpit', author: 'lx-wnk', url: 'https://example.test/42', draft: false, updatedAt: '2026-09-01T10:00:00Z' }],
      }],
    })
    await openCockpit(page)

    await expect(page.getByTestId('cockpit-github-pr-42')).toContainText('Add the cockpit')
    for (const state of PANEL_STATES)
      await expect(page.getByTestId(`cockpit-github-${state}`)).toHaveCount(0)
  })

  test('GitHub: a 500 is failed, never denied', async ({ page }) => {
    await stubJson(page, '/api/github/summary', { error: 'upstream exploded' }, 500)
    await openCockpit(page)
    await expectOnlyState(page, 'github', 'failed')
  })

  test('Memory: a memory.read refusal is reported, not drawn as an empty store', async ({ page }) => {
    await page.route(/\/api\/resources(\?.*)?$/, async (route) => {
      const kind = new URL(route.request().url()).searchParams.get('kind')
      if (kind === 'memory_space') {
        await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'memory.read is not granted in this scope' }) })
        return
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    })
    await stubJson(page, '/api/github/summary', { error: 'github is not configured' }, 503)
    await openCockpit(page)

    await expectOnlyState(page, 'memory', 'denied')
    await expect(page.getByTestId('cockpit-memory-denied')).toContainText('memory.read is not granted in this scope')
    // The sibling panel reading the same route with a different kind must be
    // unaffected — one refusal must not blank the cockpit.
    await expectOnlyState(page, 'routines', 'empty')
  })

  test('the sidebar lists Zentrale before Dashboard', async ({ page }) => {
    await stubJson(page, '/api/github/summary', { error: 'github is not configured' }, 503)
    await openCockpit(page)
    const labels = await page.locator('[data-testid="nav-items"] button').allTextContents()
    const zentraleIndex = labels.findIndex(t => t.includes('Zentrale'))
    const dashboardIndex = labels.findIndex(t => t.includes('Dashboard'))
    expect(zentraleIndex).toBeGreaterThanOrEqual(0)
    expect(dashboardIndex).toBeGreaterThan(zentraleIndex)
  })
})
