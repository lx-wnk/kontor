import type { WorkspaceLayout, WorkspacePage } from './layout'
import { ref } from 'vue'
import { fetchWithRateLimitRetry } from '@/utils/fetchWithRateLimitRetry'
import { DEFAULT_LAYOUT, parseLayout, serializeLayout } from './layout'

const SETTING = 'workspace.layout'

// unreadable: only Reset to default may replace the stored value; unloaded: it is unknown, so a retry may still read it.
export interface WorkspaceLock { kind: 'unreadable' | 'unloaded', message: string }

const UNREADABLE: WorkspaceLock = { kind: 'unreadable', message: 'The saved layout could not be read, so the built-in one is shown. Editing is locked until you reset it.' }
const UNLOADED: WorkspaceLock = { kind: 'unloaded', message: 'The saved layout could not be loaded, so editing is locked until it loads.' }

const layout = ref<WorkspaceLayout>(DEFAULT_LAYOUT)
const loaded = ref(false)
const locked = ref<WorkspaceLock | null>(null)
const saveError = ref<string | null>(null)
const editing = ref(false)
// The widget id shown wide on the current page, per browser window — never persisted, never sent to the server.
const wide = ref<string | null>(null)
let loading: Promise<void> | null = null
let fetching = false
let writing = false
let queued: WorkspaceLayout | null = null

async function fetchLayout(): Promise<void> {
  fetching = true
  try {
    const res = await fetchWithRateLimitRetry('/api/settings')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    const items = await res.json() as Array<{ key: string, value: string }>
    const parsed = parseLayout(items.find(i => i.key === SETTING)?.value ?? '')
    if (serializeLayout(parsed.layout) !== serializeLayout(layout.value)) {
      layout.value = parsed.layout
      // A failed save's "stays on screen" promise no longer holds once the
      // screen has just been replaced by whatever the server actually has.
      saveError.value = null
    }
    locked.value = parsed.unreadable ? UNREADABLE : null
  }
  catch {
    locked.value = UNLOADED
  }
  finally {
    fetching = false
    loaded.value = true
  }
}

async function load(): Promise<void> {
  loading ??= fetchLayout()
  return loading
}

// Another window may have saved since the last read. Skipped while a write is
// queued, which would otherwise be undone on screen by the older stored value.
async function reload(): Promise<void> {
  if (loaded.value && !fetching && !writing)
    loading = fetchLayout()
  await loading
}

// Installed by the app shell, not at import — a module-level listener would outlive components and stack across test reloads.
export function watchExternalChanges(): () => void {
  const onFocus = () => void reload()
  const onVisibilityChange = () => {
    if (document.visibilityState === 'visible')
      void reload()
  }
  window.addEventListener('focus', onFocus)
  document.addEventListener('visibilitychange', onVisibilityChange)
  return () => {
    window.removeEventListener('focus', onFocus)
    document.removeEventListener('visibilitychange', onVisibilityChange)
  }
}

async function patch(next: WorkspaceLayout): Promise<void> {
  try {
    const res = await fetchWithRateLimitRetry(`/api/settings/${SETTING}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value: serializeLayout(next) }),
    })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    saveError.value = null
  }
  catch (e) {
    saveError.value = `Not saved (${e instanceof Error ? e.message : 'network error'}). The change stays on screen; the next edit tries again.`
  }
}

// One PATCH in flight at a time; edits made meanwhile collapse into the latest, sent next.
async function write(next: WorkspaceLayout): Promise<void> {
  layout.value = next
  queued = next
  if (writing)
    return
  writing = true
  while (queued) {
    const body = queued
    queued = null
    await patch(body)
  }
  writing = false
}

// Resolves once the write is queued, not once it lands; saveError reports the outcome.
async function save(next: WorkspaceLayout): Promise<boolean> {
  if (locked.value || !loaded.value || fetching)
    return false
  void write(next)
  return true
}

async function reset(): Promise<void> {
  await loading
  locked.value = null
  return write(DEFAULT_LAYOUT)
}

function page(id: string): WorkspacePage | undefined {
  return layout.value.pages.find(p => p.id === id)
}

export function useWorkspace() {
  return { layout, loaded, locked, saveError, editing, wide, load, retry: reload, save, reset, page }
}
