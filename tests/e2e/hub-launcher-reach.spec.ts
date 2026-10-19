import type { Page } from '@playwright/test'
import type { GraphResponse } from '../../src/features/hub/graphApi'
import { expect, test } from '@playwright/test'
import { parkPointerOffNav, storeLayout, stubAgents } from './helpers'

test.afterEach(async ({ request, baseURL }) => {
  await storeLayout(request, baseURL, '')
})

const WHEEL_STEP = 8
const CORNER_INSET_PX = 12
const MIN_REL = 0.6
const DOCK_REL = 1.5

const asking = {
  pid: 5010,
  sessionId: 'sess-asking',
  provider: 'claude',
  projectName: 'proj-asking',
  projectPath: '/repo/proj-asking',
  cwd: '/repo/proj-asking',
  status: 'waiting',
  working: false,
  liveInjectable: true,
  channelAvailable: true,
  lastActivity: new Date().toISOString(),
  uptime: 120,
  tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
  costEstimate: 0,
  lastTools: [],
  tasks: [],
  subagents: [],
  pendingQuestion: {
    header: 'Launch',
    question: 'Launch failed after start, which option?',
    multiSelect: false,
    options: [1, 2, 3].map(index => ({ index, label: `Option ${index}`, description: 'A description long enough to wrap over two lines in the docked queue card' })),
    typeSomethingIndex: 4,
    chatAboutIndex: 5,
  },
}

// Any non-empty vault renders the memory-lens row (Unlinked / Stale) beside the legend button.
const vaultGraph: GraphResponse = {
  configured: true,
  notes: [['work/alpha/topic.md', Date.now()], ['work/alpha/linked.md', Date.now()], ['private/beta/other.md', Date.now()]],
  links: [[0, 1]],
}

interface Obscured { launcher: string, point: string, by: string }

// A launcher is reachable when its centre and four inner corner points all resolve to itself.
function obscuredLaunchers(page: Page): Promise<Obscured[]> {
  return page.evaluate((inset) => {
    const own = '[data-testid^="hub-launcher-"]'
    const describe = (el: Element | null) => {
      const named = el?.closest('[data-testid],[aria-label]')
      return named?.getAttribute('data-testid') ?? named?.getAttribute('aria-label') ?? el?.tagName ?? 'nothing (off the viewport)'
    }
    return [...document.querySelectorAll<HTMLElement>(own)].flatMap((launcher) => {
      launcher.scrollIntoView({ block: 'nearest', inline: 'nearest' })
      const r = launcher.getBoundingClientRect()
      const points: Array<[string, number, number]> = [
        ['centre', r.left + r.width / 2, r.top + r.height / 2],
        ['top-left', r.left + inset, r.top + inset],
        ['top-right', r.right - inset, r.top + inset],
        ['bottom-left', r.left + inset, r.bottom - inset],
        ['bottom-right', r.right - inset, r.bottom - inset],
      ]
      return points.flatMap(([point, x, y]) => {
        const hit = document.elementFromPoint(x, y)
        return hit?.closest(own) === launcher ? [] : [{ launcher: launcher.getAttribute('data-testid')!, point, by: describe(hit) }]
      })
    })
  }, CORNER_INSET_PX)
}

// Opaque chrome pinned to the stage's corners.
const CORNER_CHROME = ['[data-testid="hub-lenses"]', 'button[aria-label="Show map legend"]', '[role="group"][aria-label="Zoom level"]', 'svg[aria-label="Overview map"]']

// A launcher is a circle: it sits under a box when its centre is nearer to it than its radius. Panned off
// the stage it is clipped, so the pointer probe above only holds for the unpanned map; this one does not.
function launchersUnderCornerChrome(page: Page): Promise<string[]> {
  return page.evaluate((selectors) => {
    const boxes = selectors.flatMap(selector => [...document.querySelectorAll(selector)].map(el => ({ selector, rect: el.getBoundingClientRect() })))
    return [...document.querySelectorAll<HTMLElement>('[data-testid^="hub-launcher-"]')].flatMap((launcher) => {
      const r = launcher.getBoundingClientRect()
      const cx = r.left + r.width / 2
      const cy = r.top + r.height / 2
      return boxes
        .filter(({ rect }) => Math.hypot(Math.max(rect.left - cx, 0, cx - rect.right), Math.max(rect.top - cy, 0, cy - rect.bottom)) < r.width / 2)
        .map(({ selector }) => `${launcher.getAttribute('data-testid')} under ${selector}`)
    })
  }, CORNER_CHROME)
}

// The map moved by this many keyboard pan steps, right and down positive.
type Pan = [x: number, y: number]
const PANS: Pan[] = [[0, 0], [2, 0], [0, 2], [-2, 2]]

async function panBy(page: Page, [x, y]: Pan) {
  for (let i = 0; i < Math.abs(x); i++)
    await page.keyboard.press(x > 0 ? 'ArrowLeft' : 'ArrowRight')
  for (let i = 0; i < Math.abs(y); i++)
    await page.keyboard.press(y > 0 ? 'ArrowUp' : 'ArrowDown')
}

const frame = (page: Page) => page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))

async function cameraScale(page: Page): Promise<number> {
  const transform = await page.getByTestId('hub-stage').locator('svg g').first().getAttribute('transform')
  return Number(/scale\(([-\d.e]+)\)/.exec(transform!)![1])
}

// Zooms in wheel steps from the fitted view down to MIN_REL and up past DOCK_REL, probing every step.
async function sweepZoom(page: Page, label: string, pan: Pan): Promise<string[]> {
  const stage = page.getByTestId('hub-stage')
  await stage.focus()
  await page.keyboard.press('0')
  await frame(page)
  const k0 = await cameraScale(page)
  const box = (await stage.boundingBox())!
  const wheel = (deltaY: number) => stage.evaluate((el, dy) => {
    const r = el.getBoundingClientRect()
    el.dispatchEvent(new WheelEvent('wheel', { deltaY: dy, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2, bubbles: true, cancelable: true }))
  }, deltaY)

  const findings: string[] = []
  const probe = async (direction: string) => {
    await frame(page)
    const rel = (await cameraScale(page)) / k0
    const docked = (await page.locator('[data-testid^="hub-launcher-"]').first().boundingBox())!.x - box.x < 60
    const where = `${label} pan=${pan} ${direction} rel=${rel.toFixed(2)} ${docked ? 'docked' : 'ring'}`
    if (pan.every(step => step === 0)) {
      for (const o of await obscuredLaunchers(page))
        findings.push(`${where}: ${o.launcher} ${o.point} hit ${o.by}`)
    }
    for (const covered of await launchersUnderCornerChrome(page))
      findings.push(`${where}: ${covered}`)
  }

  await panBy(page, pan)
  await probe('fit')
  for (const [direction, deltaY, reached] of [['out', WHEEL_STEP, (rel: number) => rel <= MIN_REL + 0.001], ['in', -WHEEL_STEP, (rel: number) => rel > DOCK_REL]] as const) {
    await page.keyboard.press('0')
    await panBy(page, pan)
    for (let rel = 1; !reached(rel);) {
      await wheel(deltaY)
      await frame(page)
      rel = (await cameraScale(page)) / k0
      await probe(direction)
    }
  }
  return findings
}

// Viewport and widened flag chosen to land the hub stage on the shapes the default tile and its widened form take.
const STAGES = [
  { width: 1280, height: 720, wide: false },
  { width: 1280, height: 720, wide: true },
  { width: 1440, height: 900, wide: true },
  { width: 1920, height: 1080, wide: false },
  { width: 1920, height: 1080, wide: true },
  { width: 1190, height: 1350, wide: true },
]

const STATES = [
  { name: 'empty queue', agents: [], vault: false },
  { name: 'pending question', agents: [asking], vault: false },
  { name: 'vault notes', agents: [], vault: true },
  { name: 'vault notes, pending question', agents: [asking], vault: true },
]

for (const viewport of STAGES) {
  for (const { name: state, agents, vault } of STATES) {
    test(`no launcher is covered at any zoom: ${viewport.width}x${viewport.height}${viewport.wide ? ' widened' : ''}, ${state}`, async ({ page }) => {
      // Reduced motion: the camera lands synchronously and the dock easing never runs.
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await page.setViewportSize(viewport)
      await stubAgents(page, [...agents])
      if (vault)
        await page.route('**/api/obsidian/graph', route => route.fulfill({ json: vaultGraph }))
      await page.goto('/', { waitUntil: 'domcontentloaded' })
      await parkPointerOffNav(page)
      const stage = page.getByTestId('hub-stage')
      await expect(stage).toBeVisible()
      await expect(page.getByTestId('needs-you')).toContainText(agents.length ? 'Launch failed' : 'Nothing needs you')
      if (vault)
        await expect(page.getByTestId('hub-lenses')).toBeVisible()
      if (viewport.wide) {
        await stage.focus()
        await page.keyboard.press('f')
        await expect(stage.getByRole('button', { name: 'Widen' })).toHaveAttribute('aria-pressed', 'true')
      }

      await expect(page.locator('[data-testid^="hub-launcher-"]')).not.toHaveCount(0)
      const stageBox = (await stage.boundingBox())!
      const label = `[${viewport.width}x${viewport.height}${viewport.wide ? ' wide' : ''} stage ${Math.round(stageBox.width)}x${Math.round(stageBox.height)} ${state}]`
      // Only a shown vault adds the lens row, so only it needs the panned sweeps.
      const findings: string[] = []
      for (const pan of vault ? PANS : PANS.slice(0, 1))
        findings.push(...await sweepZoom(page, label, pan))
      expect(findings.join('\n')).toBe('')
    })
  }
}
