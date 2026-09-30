import type { GraphResponse } from '../../src/features/hub/graphApi'
import { expect, test } from '@playwright/test'

// Regression: the memory-health toggles once rendered under the top-centre
// NeedsYouQueue notice ("Nothing needs you…"), which sits at the same
// left-1/2/top-2.5 stack and always renders regardless of agent state — so
// every click on a toggle hit the notice instead. jsdom lays out nothing, so
// only a real browser click (no `force`) catches an actionability failure
// like that.
const DAY_MS = 24 * 60 * 60 * 1000

// know-how notes: topic.md (no links, recent), linked.md (linked from a
// session log, recent), old.md (no links, 200 days old). sessions/s1.md is a
// session log (excluded from both lenses by its path) that links to
// linked.md, so linked.md alone is not unlinked.
// unlinked = topic.md + old.md = 2. stale (90+ days) = old.md only = 1.
function fakeGraph(): GraphResponse {
  const now = Date.now()
  return {
    configured: true,
    notes: [
      ['work/alpha/topic.md', now],
      ['work/alpha/sessions/s1.md', now],
      ['work/alpha/linked.md', now],
      ['private/beta/old.md', now - 200 * DAY_MS],
    ],
    links: [[1, 2]],
  }
}

const FAKE_MEMORY_SPACE = {
  id: 'ms-1',
  kind: 'memory_space',
  slug: 'claude-memory',
  name: 'claude-memory',
  scopeKind: 'global',
  scopeRef: '',
  nodeId: 'node-1',
  state: 'active',
  version: '1',
  origin: 'local',
  originRef: '',
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
}

async function mockMemoryLensSources(page: import('@playwright/test').Page): Promise<void> {
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  // MemoryPanel's lenses only render once its own resource list is non-empty
  // (CockpitPanel's 'ready' state) — RoutinesPanel shares useResources for a
  // different kind, so only 'memory_space' gets a fixture here.
  await page.route(/\/api\/resources(\?.*)?$/, async (route) => {
    const kind = new URL(route.request().url()).searchParams.get('kind')
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(kind === 'memory_space' ? [FAKE_MEMORY_SPACE] : []),
    })
  })
  // This machine runs real Claude sessions the dev server picks up as live
  // agents — an empty fixture keeps the hub's agent ring (and its label
  // layout, which the toggles' pointer-target check below is sensitive to)
  // out of this spec's control entirely.
  await page.route('/api/agents', route => route.fulfill({ json: [] }))
  await page.route('/api/agents/stream', route => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    body: '',
  }))
}

test('the memory lenses show fixture counts, and a real click toggles them without the top-centre notice intercepting', async ({ page }) => {
  await mockMemoryLensSources(page)
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const hubUnlinked = page.getByTestId('hub-lens-unlinked')
  const hubStale = page.getByTestId('hub-lens-stale')
  const memoryUnlinked = page.getByTestId('cockpit-memory-lens-unlinked')
  await expect(hubUnlinked).toHaveText('Unlinked 2')
  await expect(hubStale).toHaveText('Stale 1')
  await expect(memoryUnlinked).toBeVisible()

  // Belt and braces for the exact regression: the element a real click would
  // actually land on at each toggle's own centre must be the toggle itself.
  // elementFromPoint only sees the viewport, so scroll the row into view
  // first — the same thing a real click does before it lands.
  await hubUnlinked.scrollIntoViewIfNeeded()
  for (const testid of ['hub-lens-unlinked', 'hub-lens-stale']) {
    const topmost = await page.evaluate((id) => {
      const el = document.querySelector<HTMLElement>(`[data-testid="${id}"]`)!
      const box = el.getBoundingClientRect()
      const over = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)
      return !!over?.closest(`[data-testid="${id}"]`)
    }, testid)
    expect(topmost, `${testid} takes the pointer at its own centre`).toBe(true)
  }

  await hubUnlinked.click()
  await expect(hubUnlinked).toHaveAttribute('aria-pressed', 'true')
  // Shared module-level state (useHealthLens): the Memory tile's own button reflects the same toggle.
  await expect(memoryUnlinked).toHaveAttribute('aria-pressed', 'true')

  await hubUnlinked.click()
  await expect(hubUnlinked).toHaveAttribute('aria-pressed', 'false')
  await expect(memoryUnlinked).toHaveAttribute('aria-pressed', 'false')
})

test('the map legend opens on click, closes on Escape from the hub stage and reopens on ?', async ({ page }) => {
  await mockMemoryLensSources(page)
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const stage = page.getByTestId('hub-stage')
  const legend = page.getByTestId('hub-legend')
  await expect(legend).toHaveCount(0)

  await page.getByRole('button', { name: 'Show map legend' }).click()
  await expect(legend).toBeVisible()

  await stage.focus()
  await stage.press('Escape')
  await expect(legend).toHaveCount(0)

  await stage.press('?')
  await expect(legend).toBeVisible()
})
