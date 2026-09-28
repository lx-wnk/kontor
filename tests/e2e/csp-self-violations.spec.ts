import { expect, test } from '@playwright/test'
import { stubAuthDisabled } from './helpers'

/**
 * The CSP is sent by the Go middleware, so this only means anything against the
 * real binary the E2E server runs — a component test would never see the header.
 * Both classes of violation are silent: nothing breaks visibly, a capability is
 * just missing, which is why they survived unnoticed until they were measured.
 */
test.describe('the app does not violate its own CSP', () => {
  test('no blocked inline script and no blocked font on first load', async ({ page }) => {
    const violations: string[] = []

    page.on('console', (message) => {
      if (message.type() === 'error' && /Content Security Policy/i.test(message.text()))
        violations.push(`console: ${message.text().slice(0, 160)}`)
    })
    page.on('requestfailed', (request) => {
      if (request.failure()?.errorText === 'csp')
        violations.push(`blocked request: ${request.url()}`)
    })

    await stubAuthDisabled(page)
    await page.goto('/')
    await expect(page.getByTestId('cockpit')).toBeVisible()

    expect(violations, violations.join('\n')).toEqual([])
  })
})
