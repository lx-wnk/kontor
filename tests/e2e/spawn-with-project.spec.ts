import process from 'node:process'
import { expect, test } from '@playwright/test'
import { openListboxOptions, selectListboxOption } from './helpers'
import { APP_BASE_URL } from './servers'

// Allow the dev environment to override the dashboard URL — e.g. point at the
// Vite dev server (`http://localhost:5173`) when the Go backend on 13199 has
// no built frontend to serve. Defaults to the playwright.config.ts baseURL.
const overrideBaseUrl = process.env.DASHBOARD_E2E_BASE_URL
if (overrideBaseUrl) {
  test.use({ baseURL: overrideBaseUrl })
}

// ---------------------------------------------------------------------------
// Spawn dialog: project picker hydrates cwd from default folder
//
// Pre-seeds a project + a default folder via the REST API, then drives the
// SpawnDialog through the project picker and asserts that the working
// directory input is populated from the project's default folder. The dev
// server runs with `cfg.Auth == "none"` so unauthenticated requests are
// accepted. See dashboard.spec.ts for the broader auth/setup pattern.
// ---------------------------------------------------------------------------

test('spawn dialog shows project picker and hydrates cwd from default folder', async ({ page, request, baseURL }) => {
  const slug = `e2e-${Date.now()}`

  // The server's CSRF guard rejects unsafe-method requests without an Origin
  // header (`missing Origin header`, 403). Browser requests get this header
  // for free; APIRequestContext does not, so we set it explicitly for every
  // mutating call.
  const csrfHeaders = { Origin: baseURL ?? APP_BASE_URL }

  // 1. Pre-seed a project.
  const projectRes = await request.post('/api/projects', {
    headers: csrfHeaders,
    data: { name: `E2E ${slug}`, slug },
  })
  // toBeOK() includes the response body in failure messages — surfaces the
  // server's actual error text instead of a bare `expected true, got false`.
  await expect(projectRes).toBeOK()
  const project = await projectRes.json() as { id: string }

  // 2. Pre-seed a default folder. /tmp is safe (exists on every dev box) and
  //    must be an absolute path per the server's folder validator.
  const folderRes = await request.post(`/api/projects/${project.id}/folders`, {
    headers: csrfHeaders,
    data: { path: '/tmp', isDefault: true },
  })
  await expect(folderRes).toBeOK()

  try {
    // 3. Navigate.
    await page.goto('/')

    // 4. Open the spawn modal. The header button text is "+ New Agent".
    await page.getByRole('button', { name: '+ New Agent' }).click()

    // 5. Wait for the project select to render.
    await expect(page.locator('#spawn-project')).toBeVisible()

    // 6. Select the pre-seeded project. The composable's selectProject() is
    //    async (it may fetch folders) so we rely on Playwright auto-retry
    //    in the next assertion. Label is the project name (see
    //    projectOptions in SpawnDialog.vue); a substring match tolerates the
    //    " — no folder, add one in /settings/projects" suffix that would be
    //    appended for a project without a default folder.
    await selectListboxOption(page, page.locator('#spawn-project'), `E2E ${slug}`)

    // 7. Assert cwd is NOT a free-text field — the manual working directory
    //    input was removed. cwd now flows exclusively from the selected
    //    project folder, so [data-testid="spawn-cwd-wrap"] must not exist.
    await expect(page.locator('[data-testid="spawn-cwd-wrap"]')).toHaveCount(0)

    // Assert the folder picker is not shown (single folder → no picker).
    await expect(page.locator('#spawn-folder')).toHaveCount(0)

    // 8. Assert spawner picker is present.
    await expect(page.locator('[data-testid="spawn-spawner"]')).toBeVisible()

    // 9. Assert model select is NOT present.
    await expect(page.locator('#spawn-model')).toHaveCount(0)

    // 10. Assert channel checkbox is NOT present.
    await expect(page.locator('#spawn-channel')).toHaveCount(0)

    // 11. Assert permission-mode select is present and offers (among others)
    //    the three modes this suite cares about — options carry no `value`
    //    attribute any more, so match by their accessible name instead (see
    //    permissionModeOptions in SpawnDialog.vue for the value→label map).
    const permSelect = page.locator('[data-testid="spawn-permission-mode"]')
    await expect(permSelect).toBeVisible()
    const permissionListbox = await openListboxOptions(page, permSelect)
    await expect(permissionListbox.getByRole('option', { name: 'Ask for permission (default)', exact: true })).toHaveCount(1)
    await expect(permissionListbox.getByRole('option', { name: 'Auto-accept edits', exact: true })).toHaveCount(1)
    await expect(permissionListbox.getByRole('option', { name: 'Bypass all permissions (dangerous)', exact: true })).toHaveCount(1)
    // Close the panel again — it must not intercept the Cancel click below.
    await page.keyboard.press('Escape')
    await permissionListbox.waitFor({ state: 'detached' })

    // 12. Cancel — we don't want to actually spawn a Claude process. Scope to
    //    the modal (AppModal renders role="dialog") so we don't accidentally
    //    match a Cancel button elsewhere on the page.
    await page.getByRole('dialog').getByRole('button', { name: /^cancel$/i }).click()
  }
  finally {
    // 13. Cleanup, even if an assertion above failed.
    await request.delete(`/api/projects/${project.id}`, { headers: csrfHeaders })
  }
})

// ---------------------------------------------------------------------------
// Spawn dialog: submitted payload
//
// Same project/folder pre-seed as above, but this time we intercept the real
// spawn POST (SpawnDialog.vue's handleSpawn -> POST /api/agents/spawn) before
// clicking "Spawn Agent", so no Claude process actually launches — the route
// is fulfilled with the { pid } shape the client reads (`data.pid`). Asserts
// the captured request body carries the values the form drove: cwd from the
// selected project's default folder, the chosen permission mode, and the
// typed prompt. spawnerId is asserted absent because no spawner override was
// picked and the project has no defaultSpawnerId (see useSpawnDialog.ts
// selectProject()).
// ---------------------------------------------------------------------------

test('spawn dialog submits payload with project cwd, permission mode, and prompt', async ({ page, request, baseURL }) => {
  const slug = `e2e-${Date.now()}`
  const csrfHeaders = { Origin: baseURL ?? APP_BASE_URL }

  const projectRes = await request.post('/api/projects', {
    headers: csrfHeaders,
    data: { name: `E2E ${slug}`, slug },
  })
  await expect(projectRes).toBeOK()
  const project = await projectRes.json() as { id: string }

  const folderRes = await request.post(`/api/projects/${project.id}/folders`, {
    headers: csrfHeaders,
    data: { path: '/tmp', isDefault: true },
  })
  await expect(folderRes).toBeOK()

  try {
    await page.goto('/')

    await page.getByRole('button', { name: '+ New Agent' }).click()
    await expect(page.locator('#spawn-project')).toBeVisible()
    await selectListboxOption(page, page.locator('#spawn-project'), `E2E ${slug}`)
    await expect(page.locator('[data-testid="spawn-spawner"]')).toBeVisible()

    await page.locator('#spawn-prompt').fill('Do the thing')
    // 'acceptEdits' → label 'Auto-accept edits' (permissionModeOptions in SpawnDialog.vue).
    await selectListboxOption(page, page.locator('[data-testid="spawn-permission-mode"]'), 'Auto-accept edits')

    // Register the interception BEFORE the submit click so the real request
    // never leaves the browser — route.fulfill answers with the { pid }
    // shape handleSpawn() expects, so the client believes the spawn succeeded
    // without a process ever having been created.
    let capturedPayload: Record<string, unknown> | null = null
    await page.route('/api/agents/spawn', async (route) => {
      capturedPayload = route.request().postDataJSON()
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ pid: 9999 }),
      })
    })

    await page.getByTestId('spawn-btn').click()

    await expect.poll(() => capturedPayload).not.toBeNull()
    const payload = capturedPayload as unknown as Record<string, unknown>
    expect(payload.prompt).toBe('Do the thing')
    expect(payload.cwd).toBe('/tmp')
    expect(payload.projectId).toBe(project.id)
    expect(payload.permissionMode).toBe('acceptEdits')
    expect(payload.enableChannel).toBe(true)
    // No spawner override was picked and the project has no defaultSpawnerId,
    // so handleSpawn() omits spawnerId entirely rather than sending it empty.
    expect(payload.spawnerId).toBeUndefined()
  }
  finally {
    await request.delete(`/api/projects/${project.id}`, { headers: csrfHeaders })
  }
})
