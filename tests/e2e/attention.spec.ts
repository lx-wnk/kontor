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
const REQUEST = { id: 'p1', stageRunId: 'sr1', tool: 'Bash', pattern: 'ls', reason: null, requestedAt: NOW, resolvedAt: null, outcome: null }

async function stubPermission(page: Page): Promise<{ resolved: () => unknown }> {
  await stubAuthDisabled(page)
  await stubAgents(page)
  await stubJson(page, '/api/projects', [])
  await stubEmptyStream(page, '/api/projects/stream')
  await stubJson(page, '/api/tasks', [TASK])
  await stubEmptyStream(page, '/api/tasks/stream')
  await stubJson(page, '/api/tasks/t1/permission-requests', [REQUEST])
  let body: unknown = null
  await page.route('/api/tasks/t1/permission-requests/p1/resolve', async (route) => {
    body = route.request().postDataJSON()
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
  })
  return { resolved: () => body }
}

const card = (page: Page) => page.locator('[data-testid="needs-you"] li[data-key="permission:p1"]').first()

test.describe('triage keys on every view', () => {
  test('n focuses the first card and a approves it on the Zentrale', async ({ page }) => {
    const stub = await stubPermission(page)
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page)).toBeVisible()
    await expect(card(page)).not.toHaveAttribute('aria-current', 'true')

    await page.keyboard.press('n')
    await expect(card(page)).toHaveAttribute('aria-current', 'true')

    await page.keyboard.press('a')
    await expect.poll(stub.resolved).toEqual({ decision: 'allow_once' })
  })

  test('typing n in the spotlight input does not move focus', async ({ page }) => {
    await stubPermission(page)
    await page.goto('/')
    await parkPointerOffNav(page)
    await expect(card(page)).toBeVisible()

    await page.keyboard.press('ControlOrMeta+k')
    const input = page.getByRole('dialog').getByRole('combobox').or(page.getByRole('dialog').locator('input')).first()
    await input.fill('')
    await input.press('n')
    await expect(page.locator('[data-testid="needs-you"] li[aria-current="true"]')).toHaveCount(0)
  })
})
