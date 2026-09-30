import type { Page } from '@playwright/test'
import type { GraphResponse } from '../../src/features/hub/graphApi'
import { expect, test } from '@playwright/test'

// The unit tests hand HubBrainCanvas a `hoveredNote` prop directly; only a real pointer over a real
// dot proves the HubWidget wiring (pointermove → hitNote → prop) reaches the canvas.
const DAY_MS = 24 * 60 * 60 * 1000

// Two cross-project links, none of the notes touched today, so no halo or link sits at full alpha
// before the hover.
function fakeGraph(): GraphResponse {
  const now = Date.now()
  return {
    configured: true,
    notes: [
      ['work/alpha/source-note.md', now - 3 * DAY_MS],
      ['private/beta/target-note.md', now - 10 * DAY_MS],
      ['work/gamma/other-from.md', now - 20 * DAY_MS],
      ['private/delta/other-to.md', now - 40 * DAY_MS],
    ],
    links: [[0, 1], [2, 3]],
  }
}

async function mockSources(page: Page): Promise<void> {
  await page.addInitScript(() => localStorage.setItem('agent-theme', 'light'))
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.route(/\/api\/resources(\?.*)?$/, route => route.fulfill({ json: [] }))
  await page.route('/api/agents', route => route.fulfill({ json: [] }))
  await page.route('/api/agents/stream', route => route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
}

interface Ring { x: number, y: number, inner: number, outer: number }

// Highest alpha (0–255) among canvas pixels painted in the --accent colour, optionally only within a
// ring around a stage point (CSS px). Links are the only accent strokes at the notes level: a plain
// cross link is 0.7px at 0.6 alpha, a hovered one 1.4px at 0.95. A 1.4px stroke covers at least 70%
// of some pixel even when it runs level with the pixel grid, so only the hovered state passes 0.6.
function maxAccentAlpha(page: Page, ring?: Ring): Promise<number> {
  return page.evaluate((ring) => {
    const canvas = document.querySelector<HTMLCanvasElement>('[data-testid="hub-stage"] canvas')!
    const probe = document.createElement('canvas').getContext('2d')!
    probe.fillStyle = getComputedStyle(canvas).getPropertyValue('--accent').trim()
    probe.fillRect(0, 0, 1, 1)
    const [r, g, b] = probe.getImageData(0, 0, 1, 1).data
    const px = canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height).data
    const dpr = canvas.width / canvas.clientWidth
    let max = 0
    for (let i = 0; i < px.length; i += 4) {
      if (ring) {
        const d = Math.hypot((i / 4) % canvas.width / dpr - ring.x, Math.floor(i / 4 / canvas.width) / dpr - ring.y)
        if (d < ring.inner || d > ring.outer)
          continue
      }
      if (Math.abs(px[i] - r) <= 16 && Math.abs(px[i + 1] - g) <= 16 && Math.abs(px[i + 2] - b) <= 16)
        max = Math.max(max, px[i + 3])
    }
    return max
  }, ring)
}

function cameraTransform(page: Page): Promise<string | null> {
  return page.getByTestId('hub-stage').locator('svg g').first().getAttribute('transform')
}

test('hovering a note dot at the notes level highlights its cross-project link', async ({ page }) => {
  await mockSources(page)
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  const stage = page.getByTestId('hub-stage')
  await expect(stage).toBeVisible()

  // Picking the note from the list flies the camera to centre it at the notes level.
  await page.getByRole('button', { name: 'List' }).click()
  await page.getByTestId('hub-list-note').filter({ hasText: 'source-note' }).click()
  await expect(stage).toHaveAttribute('data-level', '2')
  let last: string | null = null
  await expect.poll(async () => {
    const now = await cameraTransform(page)
    const settled = now === last
    last = now
    return settled
  }, { intervals: [150] }).toBe(true)

  const box = (await stage.boundingBox())!
  const cx = box.x + box.width / 2
  const cy = box.y + box.height / 2
  // Just outside the source note's own dot, halo and selection ring: only its link passes here. The
  // link leaves along the note's title label, which is drawn over its first stretch, so the ring
  // reaches well past the label.
  const nearSource: Ring = { x: box.width / 2, y: box.height / 2, inner: 16, outer: 160 }
  await page.mouse.move(box.x + 4, box.y + box.height - 4)
  await expect.poll(() => maxAccentAlpha(page, nearSource), { message: 'the source note\'s link leaves its dot' }).toBeGreaterThan(0)
  expect(await maxAccentAlpha(page), 'unhovered links stay below the hover alpha').toBeLessThan(0.6 * 255)

  const onStage = await page.evaluate(([x, y]) => !!document.elementFromPoint(x, y)?.closest('[data-testid="hub-stage"]'), [cx, cy])
  expect(onStage, 'the centred note is not covered by an overlay').toBe(true)
  await page.mouse.move(cx, cy)
  await expect.poll(() => maxAccentAlpha(page, nearSource), { message: 'the hovered note\'s own link outshines any unhovered one' }).toBeGreaterThan(0.6 * 255)
})
