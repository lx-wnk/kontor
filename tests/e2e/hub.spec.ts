import type { GraphResponse } from '../../src/features/hub/graphApi'
import { expect, test } from '@playwright/test'
import { storeLayout } from './helpers'

const FOLDERS = ['Work', 'Private', 'Misc']
const NOTE_COUNT = 300
const LINK_COUNT = 30
const TWO_YEARS_MS = 2 * 365 * 24 * 60 * 60 * 1000

// 300 notes across 3 folders with mtimes spread over two years, so freshness rings and the notes level have data to draw.
function fakeGraph(): GraphResponse {
  const now = Date.now()
  const notes: GraphResponse['notes'] = Array.from({ length: NOTE_COUNT }, (_, i) => [
    `${FOLDERS[i % FOLDERS.length]}/note-${i}.md`,
    now - Math.round((i / NOTE_COUNT) * TWO_YEARS_MS),
  ])
  const links: GraphResponse['links'] = Array.from({ length: LINK_COUNT }, (_, i) => [i, (i + 7) % NOTE_COUNT])
  return { configured: true, notes, links }
}

const AGENT_COUNT = 9

// A crowd in one project: enough labels to force a cull, and enough dots to land some of them under
// the sector names the legend draws just outside the agent ring.
function fakeAgents() {
  return Array.from({ length: AGENT_COUNT }, (_, i) => ({
    pid: 6000 + i,
    sessionId: `sess-${i}`,
    provider: 'claude',
    projectName: 'Work',
    projectPath: '/repo/work',
    cwd: '/repo/work',
    status: i % 3 === 0 ? 'active' : 'idle',
    working: i % 3 === 0,
    lastActivity: new Date().toISOString(),
    uptime: 120,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    costEstimate: 0,
    lastTools: [],
    tasks: [],
    subagents: [],
  }))
}

test.afterEach(async ({ request, baseURL }) => {
  await storeLayout(request, baseURL, '')
})

// jsdom lays out nothing, so which element actually takes the pointer over a dot — the agent above
// or the sector name it overlaps — can only be settled in a real browser.
test('every agent dot takes the pointer, and hovering a culled agent reveals its label', async ({ page }) => {
  const agents = fakeAgents()
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.route('/api/agents', route => route.fulfill({ json: agents }))
  await page.route('/api/agents/stream', route => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    body: `data: ${JSON.stringify({ agents })}\n\n`,
  }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId(`hub-agent-${agents[0].pid}`)).toBeVisible()
  await expect(page.locator('[data-testid^="hub-sector-"]').first()).toBeVisible()

  const map = await page.evaluate(() => {
    const blocked: string[] = []
    const culled: string[] = []
    for (const button of document.querySelectorAll<HTMLElement>('[data-testid^="hub-agent-"]')) {
      const box = button.getBoundingClientRect()
      const over = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)
      const by = over?.closest('[data-testid]')?.getAttribute('data-testid') ?? over?.tagName ?? 'nothing'
      const takesPointer = button.contains(over)
      // Only the map's own layers count here: the launcher rail, the minimap and the controls are
      // chrome drawn over the map on purpose.
      if (!takesPointer && /^hub-(?:sector|agent)-/.test(by))
        blocked.push(`${button.dataset.testid} blocked by ${by}`)
      if (takesPointer && getComputedStyle(button.querySelector('[data-testid="hub-label"]')!).visibility === 'hidden')
        culled.push(button.dataset.testid!)
    }
    return { blocked, culled }
  })
  expect(map.blocked, 'agent dots the map layer covers').toEqual([])
  expect(map.culled.length, 'the crowd is culled, so some labels are hidden').toBeGreaterThan(0)

  const label = page.getByTestId(map.culled[0]).getByTestId('hub-label')
  await expect(label).toBeHidden()
  await page.getByTestId(map.culled[0]).hover()
  await expect(label).toBeVisible()
})

test('+ reaches the notes level within eight presses and 0 returns to the overview', async ({ page }) => {
  // '0' flies back to fit, which animates unless reduced motion is on.
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const stage = page.getByTestId('hub-stage')
  await stage.focus()
  for (let i = 0; i < 8 && await stage.getAttribute('data-level') !== '2'; i++)
    await stage.press('+')
  await expect(stage).toHaveAttribute('data-level', '2')

  await stage.press('0')
  await expect(stage).toHaveAttribute('data-level', '0')
})

test('L lists the agents and recently touched notes, and Escape dismisses the note card, then the list', async ({ page }) => {
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const stage = page.getByTestId('hub-stage')
  const list = page.getByRole('dialog', { name: 'Zentrale as a list' })
  await stage.press('l')
  await expect(list).toBeVisible()
  await expect(list.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
  await expect(list.getByRole('heading', { name: 'Recently touched', exact: true })).toBeVisible()

  const firstNote = list.getByTestId('hub-list-note').first()
  const title = (await firstNote.getAttribute('aria-label'))?.split(',')[0].trim()
  await firstNote.click()
  // Picking a note closes the list, so the card is checked as its own dialog.
  const card = page.getByRole('dialog', { name: title!, exact: true })
  await expect(card).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(card).toBeHidden()

  await stage.press('l')
  await expect(list).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(list).toBeHidden()
})

test('the Connect Obsidian notice renders when the vault is not configured', async ({ page }) => {
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: { configured: false } }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('hub-graph-notice')).toContainText('Connect Obsidian to see your notes here.')
})

test('F widens the hub tile to the page width and back', async ({ page }) => {
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const stage = page.getByTestId('hub-stage')
  const grid = page.getByTestId('workspace-grid')
  const hub = page.getByTestId('hub')
  const gridWidth = (await grid.boundingBox())!.width

  await stage.press('f')
  await expect.poll(async () => (await hub.boundingBox())!.width).toBeGreaterThanOrEqual(gridWidth * 0.95)

  await stage.press('f')
  await expect.poll(async () => (await hub.boundingBox())!.width).toBeLessThan(gridWidth * 0.95)
})

// The docked needs-you queue must stay inside the hub's box at its minimum size (6×6) on a small viewport.
test('the docked needs-you queue stays inside a 6×6 hub at 1280×700', async ({ page, request, baseURL }) => {
  await storeLayout(request, baseURL, JSON.stringify({
    version: 1,
    pages: [{ id: 'zentrale', title: 'Zentrale', tiles: [{ widget: 'hub', col: 1, row: 1, colSpan: 6, rowSpan: 6 }] }],
  }))
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.setViewportSize({ width: 1280, height: 700 })
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const hubBox = (await page.getByTestId('hub').boundingBox())!
  const queueBox = (await page.getByTestId('needs-you').boundingBox())!
  expect(queueBox.x).toBeGreaterThanOrEqual(hubBox.x - 0.5)
  expect(queueBox.y).toBeGreaterThanOrEqual(hubBox.y - 0.5)
  expect(queueBox.x + queueBox.width).toBeLessThanOrEqual(hubBox.x + hubBox.width + 0.5)
  expect(queueBox.y + queueBox.height).toBeLessThanOrEqual(hubBox.y + hubBox.height + 0.5)
})
