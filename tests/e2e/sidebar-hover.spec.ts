import type { Page } from '@playwright/test'
import { expect, test } from '@playwright/test'
import { parkPointerOffNav, stubAuthDisabled } from './helpers'

interface Metrics {
  tops: number[]
  iconLefts: number[]
  brandHeight: number
}

interface Sample {
  navWidths: number[]
  brandHeights: number[]
  tops: number[][]
  iconLefts: number[][]
}

/** Synchronous single-frame read, used for the pre-hover / post-collapse baseline. */
function readMetrics(): Metrics {
  const NAV_LABELS = ['Mission', 'Cockpit', 'Dashboard']
  const navEl = document.querySelector('nav[aria-label="Primary"]') as HTMLElement
  const brand = document.querySelector('[data-testid="sidebar-brand"]') as HTMLElement
  const buttons = NAV_LABELS.map(label =>
    Array.from(navEl.querySelectorAll('button')).find(b => b.textContent?.includes(label)) as HTMLElement)
  return {
    tops: buttons.map(b => b.getBoundingClientRect().top),
    iconLefts: buttons.map(b => (b.querySelector('span') as HTMLElement).getBoundingClientRect().left),
    brandHeight: brand.getBoundingClientRect().height,
  }
}

/** Samples layout every animation frame for ~400ms — long enough to cover the 200ms width transition. */
function sampleFrames(): Promise<Sample> {
  const NAV_LABELS = ['Mission', 'Cockpit', 'Dashboard']
  const navEl = document.querySelector('nav[aria-label="Primary"]') as HTMLElement
  const brand = document.querySelector('[data-testid="sidebar-brand"]') as HTMLElement
  const buttons = NAV_LABELS.map(label =>
    Array.from(navEl.querySelectorAll('button')).find(b => b.textContent?.includes(label)) as HTMLElement)

  const navWidths: number[] = []
  const brandHeights: number[] = []
  const tops: number[][] = []
  const iconLefts: number[][] = []

  return new Promise((resolve) => {
    const start = performance.now()
    function frame() {
      navWidths.push(navEl.getBoundingClientRect().width)
      brandHeights.push(brand.getBoundingClientRect().height)
      tops.push(buttons.map(b => b.getBoundingClientRect().top))
      iconLefts.push(buttons.map(b => (b.querySelector('span') as HTMLElement).getBoundingClientRect().left))
      if (performance.now() - start < 400)
        requestAnimationFrame(frame)
      else
        resolve({ navWidths, brandHeights, tops, iconLefts })
    }
    requestAnimationFrame(frame)
  })
}

function assertStable(sample: Sample, baseline: Metrics): void {
  for (const frameTops of sample.tops) {
    for (let i = 0; i < frameTops.length; i++)
      expect(Math.abs(frameTops[i] - baseline.tops[i])).toBeLessThanOrEqual(1)
  }
  for (const frameLefts of sample.iconLefts) {
    for (let i = 0; i < frameLefts.length; i++)
      expect(Math.abs(frameLefts[i] - baseline.iconLefts[i])).toBeLessThanOrEqual(1)
  }
  for (const h of sample.brandHeights)
    expect(h).toBe(baseline.brandHeight)
}

test.describe('sidebar hover expansion', () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('agent-sidebar-pinned', 'false'))
    await stubAuthDisabled(page)
    await page.goto('/')
    await page.waitForSelector('[aria-label="Primary"]', { timeout: 10_000 })
    await parkPointerOffNav(page)
  })

  test('expanding on hover moves only the right edge — items and icons hold position, the brand block never resizes', async ({ page }: { page: Page }) => {
    const baseline = await page.evaluate(readMetrics)

    const nav = page.getByRole('navigation', { name: 'Primary' })
    await nav.hover()
    const sample = await page.evaluate(sampleFrames)

    assertStable(sample, baseline)
    expect(Math.max(...sample.navWidths)).toBeGreaterThanOrEqual(219)
  })

  test('collapsing on mouse-leave moves only the right edge — items and icons hold position, the brand block never resizes', async ({ page }: { page: Page }) => {
    const nav = page.getByRole('navigation', { name: 'Primary' })
    await nav.hover()
    await expect.poll(() => nav.evaluate(el => el.getBoundingClientRect().width)).toBeGreaterThanOrEqual(219)

    const expandedBaseline = await page.evaluate(readMetrics)

    await parkPointerOffNav(page)
    const sample = await page.evaluate(sampleFrames)

    assertStable(sample, expandedBaseline)
    expect(Math.min(...sample.navWidths)).toBeLessThanOrEqual(57)
  })

  test('collapsed nav cannot scroll sideways — the always-rendered labels clip inside their buttons instead of widening the scroll containers', async ({ page }: { page: Page }) => {
    const overflow = await page.evaluate(() => {
      const readOverflow = (el: HTMLElement) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth })
      const navItems = document.querySelector('[data-testid="nav-items"]') as HTMLElement
      const footerActions = document.querySelector('[data-testid="footer-actions"]') as HTMLElement
      return { navItems: readOverflow(navItems), footerActions: readOverflow(footerActions) }
    })

    expect(overflow.navItems.scrollWidth).toBeLessThanOrEqual(overflow.navItems.clientWidth)
    expect(overflow.footerActions.scrollWidth).toBeLessThanOrEqual(overflow.footerActions.clientWidth)
  })
})
