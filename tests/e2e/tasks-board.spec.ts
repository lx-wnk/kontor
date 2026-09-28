import type { PipelineTask } from '../../src/types'
import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

const TASK: PipelineTask = {
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
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
  metadata: {},
  silverBullet: false,
  planMode: false,
  priority: 'medium',
  userId: '',
  rank: 0,
  needsUser: false,
}

async function stubPipelineBoard(page: import('@playwright/test').Page) {
  await stubAuthDisabled(page)

  await stubJson(page, '/api/agents', [])
  await stubEmptyStream(page, '/api/agents/stream')
  await stubJson(page, '/api/projects', [])
  await stubEmptyStream(page, '/api/projects/stream')

  await stubJson(page, '/api/tasks', [TASK])
  await stubEmptyStream(page, '/api/tasks/stream')

  // The task-detail composables (useTaskDetails, useCheckpoints) fire eagerly
  // once the modal opens — stub each so no request is left pending.
  await stubJson(page, '/api/tasks/t1/stage-runs', [])
  await stubJson(page, '/api/tasks/t1/permissions', [])
  await stubJson(page, '/api/tasks/t1/permission-requests', [])
  await stubJson(page, '/api/tasks/t1/feedback', [])
  await stubJson(page, '/api/tasks/t1/checkpoints', [])
}

async function openPipeline(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page
    .getByRole('navigation', { name: 'Primary' })
    .getByRole('button', { name: 'Pipeline' })
    .click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Pipeline')
}

test.describe('Pipeline board', () => {
  test('renders a task in the Implementation column', async ({ page }) => {
    await stubPipelineBoard(page)
    await openPipeline(page)

    await expect(page.getByText('Implementation', { exact: true }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Open task My Task' })).toBeVisible()
  })

  test('opens the task modal, switches tabs, and closes it', async ({ page }) => {
    await stubPipelineBoard(page)
    await openPipeline(page)

    await page.getByRole('button', { name: 'Open task My Task' }).click()

    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(page.getByRole('heading', { name: 'My Task' })).toBeVisible()

    const tablist = page.getByRole('tablist', { name: 'Task details' })
    const overviewTab = tablist.getByRole('tab', { name: 'Overview' })
    await expect(overviewTab).toHaveAttribute('aria-selected', 'true')

    const stagesTab = tablist.getByRole('tab', { name: /^Stages/ })
    await stagesTab.click()
    await expect(stagesTab).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByText('No stage runs yet.')).toBeVisible()

    await dialog.getByRole('button', { name: 'Close' }).click()
    await expect(dialog).not.toBeVisible()
  })
})
