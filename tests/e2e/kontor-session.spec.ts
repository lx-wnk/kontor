import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

const PID = 4242

test('Mission input starts a Kontor session instead of creating a task', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('agent-active-view', 'mission'))
  await stubAuthDisabled(page)
  await stubJson(page, '/api/agents', [])
  await stubEmptyStream(page, '/api/agents/stream')
  await stubEmptyStream(page, '/api/tasks/stream')
  await stubJson(page, '/api/tasks', [])
  await stubJson(page, '/api/config', { mcpServerName: 'agent-dashboard', mcpEndpoint: '/api/mcp' })
  await stubJson(page, '/api/spawners', [])
  await stubJson(page, '/api/github/summary', { error: 'github is not configured' }, 503)

  let running = false
  const prompts: unknown[] = []
  await page.route(/\/api\/kontor-session$/, async (route) => {
    if (route.request().method() === 'POST') {
      prompts.push(route.request().postDataJSON())
      running = true
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ pid: running ? PID : null }) })
  })

  const taskPosts: string[] = []
  page.on('request', (req) => {
    if (req.method() === 'POST' && new URL(req.url()).pathname === '/api/tasks')
      taskPosts.push(req.url())
  })

  await page.goto('/')
  await expect(page.getByTestId('kontor-state')).toHaveText('No session')

  const input = page.getByTestId('mission-input')
  await input.fill('Plan phase 4 of the dashboard')
  await expect(page.getByTestId('mission-reading-label')).toHaveText('START KONTOR')
  await input.press('Enter')

  await expect(page.getByTestId('kontor-state')).toHaveText('Running')
  await expect(input).toHaveValue('')
  expect(prompts).toEqual([{ prompt: 'Plan phase 4 of the dashboard' }])
  expect(taskPosts).toEqual([])
})
