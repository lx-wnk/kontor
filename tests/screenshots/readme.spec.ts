import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'
import {
  assertNoNetworkLeaks,
  DEMO_NOW,
  DETAIL_AGENT,
  DETAIL_TRANSCRIPT,
  installDemoRoutes,
  installNetworkGuard,
} from './fixtures'

const ASSETS_DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), '../../docs/assets')

function assetPath(name: string): string {
  return path.join(ASSETS_DIR, name)
}

/** Pins the shell to a core view before the app boots, mirroring dashboard.spec.ts / hub.spec.ts. */
async function pinView(page: import('@playwright/test').Page, view: string): Promise<void> {
  await page.addInitScript((v) => {
    localStorage.setItem('agent-active-view', v)
  }, view)
}

test.describe('README screenshots', () => {
  test('hero — Zentrale hub with agents, sectors and needs-you items', async ({ page, baseURL }) => {
    const guard = installNetworkGuard(page, new URL(baseURL!).origin)
    await installDemoRoutes(page)
    await page.clock.setFixedTime(DEMO_NOW)

    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Zentrale')
    await expect(page.getByTestId('hub-stage')).toBeVisible()
    await expect(page.locator('[data-testid^="hub-agent-"]').first()).toBeVisible()
    await expect(page.getByTestId('needs-you')).toBeVisible()
    await expect(page.getByTestId('workspace-tile-cost-today')).toBeVisible()

    await page.screenshot({ path: assetPath('hero.png') })
    await assertNoNetworkLeaks(guard)
  })

  test('dashboard — agent roster and permission triage band', async ({ page, baseURL }) => {
    const guard = installNetworkGuard(page, new URL(baseURL!).origin)
    await installDemoRoutes(page)
    await page.clock.setFixedTime(DEMO_NOW)
    await pinView(page, 'dashboard')

    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Dashboard')
    await expect(page.locator('[data-testid="agent-card-body"]').first()).toBeVisible()

    await page.screenshot({ path: assetPath('dashboard.png') })
    await assertNoNetworkLeaks(guard)
  })

  test('pipeline — board with tasks across every stage', async ({ page, baseURL }) => {
    const guard = installNetworkGuard(page, new URL(baseURL!).origin)
    await installDemoRoutes(page)
    await page.clock.setFixedTime(DEMO_NOW)
    await pinView(page, 'pipeline')

    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Pipeline')
    await expect(page.getByRole('button', { name: /^Open task/ }).first()).toBeVisible()
    await expect(page.getByText('Implementation', { exact: true }).first()).toBeVisible()

    await page.screenshot({ path: assetPath('pipeline.png') })
    await assertNoNetworkLeaks(guard)
  })

  test('cost — analytics view with populated charts', async ({ page, baseURL }) => {
    const guard = installNetworkGuard(page, new URL(baseURL!).origin)
    await installDemoRoutes(page)
    await page.clock.setFixedTime(DEMO_NOW)
    await pinView(page, 'cost')

    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('heading', { name: 'Cost Analytics' })).toBeVisible()
    await expect(page.getByText('Spend by Model')).toBeVisible()
    await expect(page.getByText('Spend by Project')).toBeVisible()

    await page.screenshot({ path: assetPath('cost.png') })
    await assertNoNetworkLeaks(guard)
  })

  test('agent detail — modal open with transcript and tools', async ({ page, baseURL }) => {
    const guard = installNetworkGuard(page, new URL(baseURL!).origin)
    await installDemoRoutes(page)
    // This screenshot's dashboard shows exactly one agent — the detail agent's
    // own transcript — mirroring agent-detail-modal.spec.ts, which also stubs
    // a single-agent /api/agents response. Registered after installDemoRoutes
    // so these two overrides win over the shared multi-agent fixture.
    await page.route('**/api/agents', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'x-screenshot-mock': '1' },
      body: JSON.stringify([DETAIL_AGENT]),
    }))
    await page.route('**/api/agents/stream', route => route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      headers: { 'x-screenshot-mock': '1' },
      body: `data: ${JSON.stringify({ agents: [DETAIL_AGENT] })}\n\n`,
    }))
    await page.clock.setFixedTime(DEMO_NOW)
    await pinView(page, 'dashboard')

    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await page.getByTestId('agent-card-body').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByLabel('Chat transcript')).toBeVisible()
    await expect(dialog.getByText(DETAIL_TRANSCRIPT[0].content)).toBeVisible()

    await page.screenshot({ path: assetPath('agent-detail.png') })
    await assertNoNetworkLeaks(guard)
  })
})
