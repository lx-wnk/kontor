import type { APIRequestContext, Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { APP_BASE_URL } from './servers'

/**
 * Mock /api/me so the app never redirects to the LoginPage — mirrors the
 * pattern used across every E2E spec (dashboard.spec.ts, question-band.spec.ts).
 */
export async function stubAuthDisabled(page: Page): Promise<void> {
  await page.route('/api/me', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ user: null, isAdmin: true, authEnabled: false }),
  }))
}

/**
 * Moves the pointer onto the content area. A pointer resting over the sidebar
 * rail expands the nav to 220px, and content under that strip stays covered
 * while the pointer moves onto it, so a click there is intercepted forever.
 */
export async function parkPointerOffNav(page: Page): Promise<void> {
  await page.mouse.move(640, 360)
}

export async function storeLayout(request: APIRequestContext, baseURL: string | undefined, value: string): Promise<void> {
  const res = await request.patch('/api/settings/workspace.layout', {
    headers: { Origin: baseURL ?? APP_BASE_URL },
    data: { value },
  })
  expect(res.ok(), `store layout request (HTTP ${res.status()})`).toBe(true)
}

export function waitForLayoutPatch(page: Page) {
  return page.waitForResponse(resp =>
    resp.url().includes('/api/settings/workspace.layout') && resp.request().method() === 'PATCH')
}

/** Stubs a GET endpoint to return a fixed JSON body. */
export async function stubJson(page: Page, path: string, body: unknown, status = 200): Promise<void> {
  // Match the exact path with an optional query string, so callers can pass the
  // bare path and still intercept requests that append params (e.g. the
  // visualization endpoints send ?from=&to=). A plain string glob would miss those.
  const escaped = path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  await page.route(new RegExp(`${escaped}(\\?.*)?$`), route => route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  }))
}

/**
 * Stubs an SSE stream endpoint (e.g. /api/projects/stream) so composables built
 * on createSseResource don't reach out to the real backend. Fulfilling with an
 * empty body closes the connection immediately — callers that need live push
 * updates should route the initial list via `stubJson` instead and treat the
 * stream as a no-op, matching how question-band.spec.ts stubs /api/agents/stream.
 */
export async function stubEmptyStream(page: Page, path: string): Promise<void> {
  await page.route(path, route => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    body: '',
  }))
}

/**
 * Closes a listbox panel that belongs to a different trigger than the one
 * about to be interacted with. AppSelect's outside-click suppressor arms
 * itself on the mousedown that dismisses a panel and eats the very next
 * click — so clicking straight into a second trigger while another select's
 * panel is still open swallows that click, leaving the second panel closed
 * and the caller's visible-wait timing out with a misleading error. Closing
 * via Escape (rather than an outside click) targets the open panel's own
 * trigger, which holds focus, and never arms the suppressor.
 */
async function closeOtherOpenListbox(page: Page, trigger: Locator): Promise<void> {
  if (await trigger.getAttribute('aria-expanded') === 'true')
    return
  const listbox = page.getByRole('listbox')
  if (await listbox.isVisible().catch(() => false)) {
    await page.keyboard.press('Escape')
    await listbox.waitFor({ state: 'detached' })
  }
}

/**
 * Opens an AppSelect trigger (role="combobox") and returns its listbox panel
 * once visible, without selecting anything — for tests that need to inspect
 * the option list (count, labels) before deciding what to do next. The panel
 * is teleported to <body>, so it must be located via page.getByRole, not as
 * a descendant of the trigger.
 */
export async function openListboxOptions(page: Page, trigger: Locator): Promise<Locator> {
  const listbox = page.getByRole('listbox')
  await closeOtherOpenListbox(page, trigger)
  // Guard on aria-expanded like selectListboxOption does — clicking
  // unconditionally would toggle an already-open panel shut and then hang
  // on the visible-wait below.
  if (await trigger.getAttribute('aria-expanded') !== 'true') {
    await trigger.click()
    await listbox.waitFor({ state: 'visible' })
  }
  return listbox
}

/**
 * Selects an option from an AppSelect listbox by its accessible name. Only
 * one listbox is open at a time, so page.getByRole('listbox') finds it even
 * though the panel is teleported outside the trigger's subtree. If the panel
 * is already open (e.g. after openListboxOptions), it is reused rather than
 * toggled shut by a redundant click on the trigger.
 */
export async function selectListboxOption(page: Page, trigger: Locator, optionName: string | RegExp): Promise<void> {
  const listbox = page.getByRole('listbox')
  await closeOtherOpenListbox(page, trigger)
  // A picker whose options are filled by an in-flight fetch (SpawnDialog's
  // project list is the known case) disables its trigger for the duration —
  // wait for that state instead of racing the arrival of the option itself,
  // so the panel is already fully populated by the time it opens.
  await expect(trigger).toBeEnabled()
  if (await trigger.getAttribute('aria-expanded') !== 'true') {
    await trigger.click()
    await listbox.waitFor({ state: 'visible' })
  }
  const option = listbox.getByRole('option', { name: optionName })
  await option.waitFor({ state: 'visible', timeout: 10_000 })
  await option.click()
  await listbox.waitFor({ state: 'detached' })
}
