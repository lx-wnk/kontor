import { expect, test } from '@playwright/test'

// Regression: a fenced code block whose <pre> has horizontal overflow (long
// line + overflow-x:auto scrollbar) followed directly by a heading left the
// heading partly hidden under the scrollbar. Root cause: Tailwind preflight
// zeros heading margins and font sizes, so h2 has margin-top:0 and collapses
// into the pre's scrollbar area.

const LONG_LINE = 'x'.repeat(300)

/** Rendered HTML that mirrors what renderMarkdown() produces for:
 *  ```bash
 *  <300 chars>
 *  ```
 *  ## Heading
 */
const MARKDOWN_HTML = `
<pre style="width:200px;overflow-x:auto"><code>${LONG_LINE}</code></pre>
<h2>Heading after code block</h2>
`

test.describe('markdown code block + heading overlap', () => {
  test('h2 must not overlap pre — bounding box separation', async ({ page }) => {
    await page.goto('/')

    // Inject a .markdown-body div into the page so the real CSS applies.
    await page.evaluate((html) => {
      const div = document.createElement('div')
      div.className = 'markdown-body'
      div.style.width = '300px'
      div.style.padding = '20px'
      div.innerHTML = html
      document.body.appendChild(div)
    }, MARKDOWN_HTML)

    // Screenshot: RED evidence before fix
    await page.screenshot({ path: 'tests/e2e/screenshots/chat-markdown-overflow.png' })

    // Measure: h2's top must be at or below pre's bottom (no overlap).
    const gap = await page.evaluate(() => {
      const pre = document.querySelector('.markdown-body pre')!
      const h2 = document.querySelector('.markdown-body h2')!
      const preRect = pre.getBoundingClientRect()
      const h2Rect = h2.getBoundingClientRect()
      return { preBottom: preRect.bottom, h2Top: h2Rect.top, gap: h2Rect.top - preRect.bottom }
    })

    // The heading must sit below the pre (including its scrollbar).
    // A positive gap means clear separation; zero means flush; negative means overlap.
    expect(gap.h2Top).toBeGreaterThanOrEqual(gap.preBottom)
  })

  test('heading has visible font size and weight', async ({ page }) => {
    await page.goto('/')

    await page.evaluate((html) => {
      const div = document.createElement('div')
      div.className = 'markdown-body'
      div.innerHTML = html
      document.body.appendChild(div)
    }, MARKDOWN_HTML)

    const styles = await page.evaluate(() => {
      const h2 = document.querySelector('.markdown-body h2')!
      const cs = window.getComputedStyle(h2)
      return {
        fontSize: Number.parseFloat(cs.fontSize),
        fontWeight: Number(cs.fontWeight),
        marginTop: Number.parseFloat(cs.marginTop),
      }
    })

    // h2 must have a visible font size (not the preflight-reset 1em/16px-or-less)
    expect(styles.fontSize).toBeGreaterThan(16)
    // h2 must be bold
    expect(styles.fontWeight).toBeGreaterThanOrEqual(600)
    // h2 must have margin-top separating it from the preceding element
    expect(styles.marginTop).toBeGreaterThan(0)
  })
})
