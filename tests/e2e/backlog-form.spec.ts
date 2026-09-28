import type { Page } from '@playwright/test'
import { expect, test } from '@playwright/test'
import { slugify } from '../../src/utils/validation'
import { openListboxOptions, selectListboxOption, stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

// ---------------------------------------------------------------------------
// Backlog / "New Task" create form — single-screen flow.
//
// The form has a single primary action "Create & Refine". The working directory
// is no longer a visible field — it is auto-filled from the selected project's
// default folder. A project is mandatory: there is no "No project" option.
//
// The "+ New Task" CTA in the topbar only renders while the pipeline view is
// active, so each test pre-sets the persisted view via localStorage
// (addInitScript runs before any app script reads it).
//
// The first test below stubs every /api endpoint the create-and-refine flow
// touches instead of hitting the real Go backend — the shared dev server has
// no seeded data (and accumulates state across runs), so relying on it for
// "the first real project" was non-deterministic. Endpoints and shapes are
// sourced from src/composables/useProjects.ts, useProjectFolders.ts,
// useTasks.ts (createTask), and useRefinementChat.ts (loadHistory/syncStatus).
// ---------------------------------------------------------------------------

/** Persist the pipeline view so the "+ New Task" topbar CTA renders on load. */
async function selectPipelineView(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('agent-active-view', 'pipeline')
  })
}

const PROJECT_ID = 'e2e-project-1'
const PROJECT_NAME = 'E2E Project'
const TASK_ID = 'e2e-task-1'

function project() {
  return {
    id: PROJECT_ID,
    slug: 'e2e-project',
    name: PROJECT_NAME,
    folderCount: 1,
    defaultSpawnerId: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
  }
}

function task(title: string, slug: string) {
  return {
    id: TASK_ID,
    slug,
    title,
    description: null,
    cwd: '/tmp',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'backlog',
    parentTaskId: null,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
  }
}

/** Mutable box for the POST /api/tasks body, so tests can assert on it after the click resolves. */
interface CapturedPayload { value: Record<string, unknown> | null }

async function stubCreateAndRefineFlow(page: Page, title: string, slug: string, capturedTaskPayload: CapturedPayload) {
  await stubAuthDisabled(page)
  await stubJson(page, '/api/projects', [project()])
  await stubEmptyStream(page, '/api/projects/stream')
  await stubJson(page, '/api/spawners', [])
  await stubEmptyStream(page, '/api/spawners/stream')
  await stubJson(page, `/api/projects/${PROJECT_ID}/folders/suggest`, [
    { id: 'e2e-folder-1', projectId: PROJECT_ID, path: '/tmp', label: undefined, isDefault: true, createdAt: '2026-01-01T00:00:00Z' },
  ])
  await page.route('/api/tasks', async (route) => {
    capturedTaskPayload.value = route.request().postDataJSON()
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(task(title, slug)),
    })
  })
  await stubJson(page, `/api/refine/${TASK_ID}/turns`, [])
  await stubJson(page, `/api/refine/${TASK_ID}/status`, { status: 'none' })
}

test('create & refine with a project auto-fills cwd and opens the refinement chat', async ({ page }) => {
  const slug = `e2e-create-${Date.now()}`
  const title = `E2E create task ${slug}`
  const capturedTaskPayload: CapturedPayload = { value: null }
  await stubCreateAndRefineFlow(page, title, slug, capturedTaskPayload)

  await selectPipelineView(page)
  await page.goto('/')

  // 1. Open the create modal.
  await page.getByRole('button', { name: '+ New Task' }).click()
  await expect(page.getByRole('heading', { name: 'New Task' })).toBeVisible()

  // 2. Select the stubbed project — the watch() handler fetches the suggested
  //    folder and fills cwd asynchronously (Playwright auto-retries assertions).
  await selectListboxOption(page, page.getByTestId('backlog-project-select'), PROJECT_NAME)

  // 3. Fill a unique title (slug auto-derives).
  await page.getByTestId('details-title').fill(title)

  // 4. Submit via the single primary action "Create & Refine".
  await page.getByTestId('details-submit-refine').click()

  // 5. After "Create & Refine" the backlog form closes and the refinement
  //    chat opens — assert the empty-state copy from RefinementChat.vue.
  await expect(page.getByText('What would you like to build?')).toBeVisible()

  // 6. Assert the POST /api/tasks body actually carries what the form drove:
  //    cwd auto-filled from the stubbed folders/suggest default ('/tmp'),
  //    and the typed title with its auto-derived slug (see the [title,
  //    slugTouched] watcher in BacklogForm.vue — the slug field itself was
  //    never touched, so it tracks slugify(title), not the raw `slug` local
  //    var used as a title suffix above).
  await expect.poll(() => capturedTaskPayload.value).not.toBeNull()
  const payload = capturedTaskPayload.value as Record<string, unknown>
  expect(payload.cwd).toBe('/tmp')
  expect(payload.title).toBe(title)
  expect(payload.slug).toBe(slugify(title))
  expect(payload.projectId).toBe(PROJECT_ID)
})

test('no "No project" option — project select starts empty and must be chosen', async ({ page }) => {
  await selectPipelineView(page)
  await page.goto('/')

  await page.getByRole('button', { name: '+ New Task' }).click()
  await expect(page.getByRole('heading', { name: 'New Task' })).toBeVisible()

  const trigger = page.getByTestId('backlog-project-select')
  // The option list has no empty/no-project entry — every option is a real
  // project name or "+ Create new project…", never the empty sentinel.
  const listbox = await openListboxOptions(page, trigger)
  const optionLabels = (await listbox.getByRole('option').allTextContents()).map(text => text.trim())
  expect(optionLabels).not.toContain('')

  // Close the panel again so it can't intercept the next interaction.
  await page.keyboard.press('Escape')
  await listbox.waitFor({ state: 'detached' })

  // Submit button is disabled while no title is entered.
  await expect(page.getByTestId('details-submit-refine')).toBeDisabled()
})
