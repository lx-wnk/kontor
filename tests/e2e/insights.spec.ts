import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

const ROW = {
  sessionId: 'sess-1',
  title: 'Refactor the forecast cache',
  projectPath: '/home/dev/weather',
  projectName: 'weather',
  model: 'claude-sonnet-5',
  startedAt: '2026-10-29T10:00:00Z',
  recordedAt: '2026-10-29T11:00:00Z',
  costUsd: 1.5,
  costSource: 'cost_state',
  costUnknownModel: false,
  agentMs: 600_000,
  agentTimeSource: 'cost_state',
  inputTokens: 1000,
  outputTokens: 500,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  turns: 12,
  linesAdded: 40,
  linesRemoved: 5,
  modelCosts: { 'claude-sonnet-5': 1.5 },
}

test.describe('Insights', () => {
  test.beforeEach(async ({ page }) => {
    await stubAuthDisabled(page)
    await stubJson(page, '/api/agents', [])
    await stubEmptyStream(page, '/api/agents/stream')
    await stubJson(page, '/api/insights/summary', {
      from: '2026-09-30',
      to: '2026-10-30',
      current: { costUsd: 1.5, agentMs: 600_000, tokens: 1500, sessions: 1, estimated: 0, legacy: 0 },
      previous: null,
    })
    await stubJson(page, '/api/insights/projects', [{ projectPath: ROW.projectPath, projectName: ROW.projectName, sessions: 1 }])
    await stubJson(page, '/api/insights/sessions', { rows: [ROW], total: 1, limit: 50, offset: 0 })
    await stubJson(page, '/api/visualizations/dag', { nodes: [], edges: [] })
    await stubJson(page, '/api/cost/summary', {
      byModel: [],
      byProject: [],
      byDay: [],
      byWeek: [],
      totalUsd: 0,
      totalInputTokens: 0,
      totalOutputTokens: 0,
      from: '',
      to: '',
      updatedAt: new Date().toISOString(),
    })
    await stubJson(page, '/api/eval/metrics', [])
    await stubJson(page, '/api/eval/drift', [])

    await page.goto('/')
    await page.getByTestId('nav-item-insights').click()
  })

  test('overview renders the KPI header', async ({ page }) => {
    await expect(page.getByTestId('insights-view').getByRole('heading', { level: 2, name: 'Insights', exact: true })).toBeVisible()
    await expect(page.getByTestId('insights-overview')).toBeVisible()
    await expect(page.getByTestId('kpi-sessions')).toHaveText(/^\D*1\D*$/)
  })

  test('quality tab shows the drift alerts', async ({ page }) => {
    await page.getByRole('tab', { name: 'Quality' }).click()
    await expect(page.getByRole('heading', { name: 'Open drift alerts' })).toBeVisible()
  })

  test('a session row opens the drawer and Escape closes it', async ({ page }) => {
    await page.getByRole('tab', { name: 'Sessions' }).click()
    await page.getByRole('row', { name: /Refactor the forecast cache/ }).click()
    const drawer = page.getByRole('dialog')
    await expect(drawer).toBeVisible()
    await expect(drawer).toContainText('Refactor the forecast cache')
    await page.keyboard.press('Escape')
    await expect(drawer).toBeHidden()
    await expect(page.getByRole('row', { name: /Refactor the forecast cache/ })).toBeFocused()
  })
})
