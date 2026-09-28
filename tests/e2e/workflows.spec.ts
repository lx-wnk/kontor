import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubJson } from './helpers'

// VA-2 Workflows view: this happy-path test verifies the new view toggle
// is wired in and that all four chart tabs render without throwing.
//
// The Sankey / Spawn Tree / Co-occurrence endpoints scan whatever real
// session data lives on the machine running the suite (see
// server/internal/api/visualizations/handler.go → analytics.Build*), so a dev
// box with real Claude session history renders non-empty charts instead of
// the empty-state copy this test asserts. Stub the three endpoints to empty
// payloads so the empty-state renders deterministically regardless of what
// is on disk. /api/sessions and /api/analytics/patterns are stubbed too so
// the surrounding panels (session picker, pattern list) don't depend on the
// real backend either.
async function stubEmptyWorkflowsData(page: import('@playwright/test').Page) {
  await stubJson(page, '/api/visualizations/sankey', { nodes: [], links: [], meta: { sessionCount: 0, callCount: 0 } })
  await stubJson(page, '/api/visualizations/spawn-tree', { roots: [], nodes: [], links: [] })
  await stubJson(page, '/api/visualizations/co-occurrence', { tools: [], matrix: [], lift: [], meta: { sessionCount: 0, truncated: false } })
  await stubJson(page, '/api/sessions', [])
  await stubJson(page, '/api/analytics/patterns', { patterns: [] })
}

test('workflows view toggle opens the four chart tabs', async ({ page }) => {
  await stubAuthDisabled(page)
  await stubEmptyWorkflowsData(page)

  await page.goto('/')
  // The landing view is the Zentrale now; this test only needs a mounted
  // shell before switching to Workflows below.
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Zentrale')

  // The sidebar item, by test id: the hub's launcher for the same view carries the same
  // accessible name, so a role+name query matches both.
  await page.getByTestId('nav-item-workflows').click()

  // The three session-independent tab buttons must be present (Session DAG
  // only renders once a session id is selected — asserted absent below).
  for (const label of ['Sankey', 'Spawn Tree', 'Co-occurrence']) {
    await expect(page.getByRole('tab', { name: label })).toBeVisible()
  }

  // The Sankey tab is the default; the stubbed empty payload renders the
  // empty-state copy from SankeyChart.vue.
  await expect(page.locator('.sankey-chart')).toContainText('No tool calls found in this window.', { timeout: 5000 })

  // Spawn Tree is reachable without picking a session ID.
  await page.getByRole('tab', { name: 'Spawn Tree' }).click()
  await expect(page.locator('.spawn-tree-chart')).toContainText('No sessions found in this window.', { timeout: 5000 })

  // Co-occurrence likewise — aggregate endpoint, no session required.
  await page.getByRole('tab', { name: 'Co-occurrence' }).click()
  await expect(page.locator('.co-occurrence-matrix')).toContainText('No tool calls found in this window.', { timeout: 5000 })

  // The DAG tab is only rendered once a session id is selected — it is absent,
  // not merely disabled, when no session id is entered.
  await expect(page.getByRole('tab', { name: 'Session DAG' })).toHaveCount(0)
})
