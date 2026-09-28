import type { Page } from '@playwright/test'
import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

// ---------------------------------------------------------------------------
// Cost Analytics view + the refinement chat overlay opened from a
// backlog-stage task on the Pipeline board.
// ---------------------------------------------------------------------------

test.describe('Cost analytics', () => {
  test.beforeEach(async ({ page }) => {
    await stubAuthDisabled(page)
    await stubJson(page, '/api/agents', [])
    await stubEmptyStream(page, '/api/agents/stream')
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

    await page.goto('/')
    await page
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('button', { name: 'Cost' })
      .click()
  })

  test('shows the empty state and the time-range presets', async ({ page }) => {
    await expect(page.getByText('No cost data yet.')).toBeVisible()

    for (const label of ['7d', '30d', '90d', 'All'])
      await expect(page.getByRole('button', { name: label, exact: true })).toBeVisible()
  })

  test('clicking a preset marks it active', async ({ page }) => {
    const preset30d = page.getByRole('button', { name: '30d', exact: true })
    const preset7d = page.getByRole('button', { name: '7d', exact: true })

    // '30d' is the default active preset — verify the baseline before switching.
    await expect(preset30d).toHaveClass(/bg-accent/)
    await expect(preset7d).not.toHaveClass(/bg-accent/)

    await preset7d.click()

    await expect(preset7d).toHaveClass(/bg-accent/)
    await expect(preset30d).not.toHaveClass(/bg-accent/)
  })
})

// ---------------------------------------------------------------------------
// Refinement chat
// ---------------------------------------------------------------------------

const TASK_ID = 'e2e-refine-task-1'

function backlogTask() {
  return {
    id: TASK_ID,
    slug: 'slug-e2e-refine',
    title: 'E2E refinement task',
    description: null,
    cwd: '/repo',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'backlog',
    parentTaskId: null,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
    rank: null,
    needsUser: false,
  }
}

async function stubPipelineWithBacklogTask(page: Page) {
  await stubAuthDisabled(page)
  await stubJson(page, '/api/agents', [])
  await stubEmptyStream(page, '/api/agents/stream')
  await stubJson(page, '/api/tasks', [backlogTask()])
  await stubEmptyStream(page, '/api/tasks/stream')
  await stubJson(page, '/api/projects', [])
  await stubEmptyStream(page, '/api/projects/stream')
  await stubJson(page, '/api/spawners', [])
  await stubEmptyStream(page, '/api/spawners/stream')
  await stubJson(page, `/api/refine/${TASK_ID}/turns`, [])
  await stubJson(page, `/api/refine/${TASK_ID}/status`, { status: 'none' })
}

test.describe('Refinement chat', () => {
  test.beforeEach(async ({ page }) => {
    await stubPipelineWithBacklogTask(page)

    await page.goto('/')
    await page
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('button', { name: 'Pipeline' })
      .click()

    await page.getByRole('button', { name: 'Continue Chat →' }).click()
    await expect(page.getByText('What would you like to build?')).toBeVisible()
  })

  test('sending a message streams the assistant reply into the transcript', async ({ page }) => {
    await page.route(`/api/refine/${TASK_ID}/turn`, route => route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: 'data: Hello from assistant\n\n',
    }))

    const textarea = page.getByPlaceholder('Message...')
    await textarea.fill('Build me a widget')
    await textarea.press('Enter')

    await expect(page.getByText('Build me a widget').first()).toBeVisible()
    await expect(page.getByText('Hello from assistant').first()).toBeVisible()
  })

  test('closes via the × button', async ({ page }) => {
    await page.getByRole('button', { name: '✕' }).click()
    await expect(page.getByText('What would you like to build?')).not.toBeVisible()
  })

  test('closes via a backdrop click', async ({ page }) => {
    // click.self on the outer overlay only fires for clicks on the backdrop
    // itself, so target a corner far from the centered dialog panel.
    await page.mouse.click(5, 5)
    await expect(page.getByText('What would you like to build?')).not.toBeVisible()
  })
})
