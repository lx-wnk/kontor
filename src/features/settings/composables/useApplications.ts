import { ref } from 'vue'

export interface ApplicationTool {
  capability: string
  name: string
  description?: string
  readOnlyHint: boolean
  destructiveHint?: boolean
  /** What happens with nothing but the global context: denied, asks, or allowed. */
  state?: 'denied' | 'asks' | 'allowed'
  /** Contexts carrying a live allow grant, e.g. `routine:<id>`. */
  allowedIn?: string[]
}

export interface ApplicationSecret {
  envName: string
  updatedAt: string
}

export interface ApplicationEntry {
  type?: string
  command?: string
  args?: string[]
  url?: string
  env?: Record<string, string>
}

export interface ApplicationView {
  resourceId: string
  serverName: string
  attachAll: boolean
  requiredEnv: string[]
  secrets: ApplicationSecret[]
  tools: ApplicationTool[]
  catalogueError?: string
  catalogueRefreshedAt?: string
  entry: ApplicationEntry
  exportToClaude: boolean
  allowedSpawners: string[]
  /** Server name of the matched preset, empty when none matched. */
  preset: string
}

export interface ApplicationDrift {
  found: string[]
  changed: string[]
}

export interface SetupSession {
  resourceId: string
  port: number
  url: string
  startedAt: string
  warning: string
}

async function readError(res: Response, fallback: string): Promise<string> {
  const body = await res.json().catch(() => null) as { error?: string } | null
  return body?.error || fallback
}

export function useApplications() {
  const applications = ref<ApplicationView[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const drift = ref<ApplicationDrift>({ found: [], changed: [] })
  const setupSessions = ref<Record<string, SetupSession>>({})
  let eventSource: EventSource | null = null

  function replace(updated: ApplicationView) {
    applications.value = applications.value.map(a => a.resourceId === updated.resourceId ? updated : a)
  }

  async function fetchApplications(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const res = await fetch('/api/applications')
      if (!res.ok)
        throw new Error(await readError(res, `HTTP ${res.status}`))
      applications.value = await res.json() as ApplicationView[]
    }
    catch (e) {
      error.value = (e as Error).message || 'Failed to load applications'
    }
    finally {
      loading.value = false
    }
  }

  async function patch(resourceId: string, body: Record<string, unknown>): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to update application'))
    replace(await res.json() as ApplicationView)
  }

  const setAttachAll = (resourceId: string, attachAll: boolean) => patch(resourceId, { attachAll })
  const setRequiredEnv = (resourceId: string, names: string[]) => patch(resourceId, { requiredEnv: names })
  const setEntry = (resourceId: string, entry: ApplicationEntry) => patch(resourceId, { entry })
  const setExport = (resourceId: string, exportToClaude: boolean) => patch(resourceId, { exportToClaude })
  const setAllowedSpawners = (resourceId: string, ids: string[]) => patch(resourceId, { allowedSpawners: ids })

  async function createApplication(input: { name: string, command: string, args: string[], env: Record<string, string> }): Promise<void> {
    const res = await fetch('/api/applications', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to create application'))
    await fetchApplications()
  }

  async function deleteApplication(resourceId: string): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}`, { method: 'DELETE' })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to remove application'))
    await fetchApplications()
  }

  async function setSecret(resourceId: string, envName: string, value: string): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}/secrets/${encodeURIComponent(envName)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value }),
    })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to store secret'))
    await fetchApplications()
  }

  async function deleteSecret(resourceId: string, envName: string): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}/secrets/${encodeURIComponent(envName)}`, { method: 'DELETE' })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to delete secret'))
    await fetchApplications()
  }

  async function refreshCatalogue(resourceId: string): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}/refresh`, { method: 'POST' })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to read the tool list'))
    replace(await res.json() as ApplicationView)
  }

  async function fetchDrift(): Promise<void> {
    try {
      const res = await fetch('/api/applications/drift')
      if (!res.ok)
        return
      const data = await res.json() as Partial<ApplicationDrift> | null
      drift.value = { found: data?.found ?? [], changed: data?.changed ?? [] }
    }
    catch {
      // drift is supplementary information; a failed fetch must not affect the application list
    }
  }

  async function startSetup(resourceId: string): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}/setup`, { method: 'POST' })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to start setup'))
    const session = await res.json() as SetupSession
    setupSessions.value = { ...setupSessions.value, [resourceId]: session }
  }

  async function stopSetup(resourceId: string): Promise<void> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}/setup`, { method: 'DELETE' })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to stop setup'))
    const next = { ...setupSessions.value }
    delete next[resourceId]
    setupSessions.value = next
  }

  async function fetchAccountNames(resourceId: string): Promise<string[]> {
    const res = await fetch(`/api/applications/${encodeURIComponent(resourceId)}/accounts`, { method: 'POST' })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to read account names'))
    const body = await res.json() as { names: string[] }
    return body.names
  }

  async function importApplication(name: string): Promise<void> {
    const res = await fetch('/api/applications/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    })
    if (!res.ok)
      throw new Error(await readError(res, 'Failed to import application'))
    await fetchApplications()
    await fetchDrift()
  }

  function subscribe(): void {
    eventSource = new EventSource('/api/tasks/stream')
    eventSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data) as { type: string }
        if (event.type === 'applications_changed')
          void fetchDrift()
      }
      catch {
        // ignore malformed messages
      }
    }
  }

  function unsubscribe(): void {
    eventSource?.close()
    eventSource = null
  }

  return { applications, loading, error, drift, setupSessions, fetchApplications, setAttachAll, setRequiredEnv, setSecret, deleteSecret, refreshCatalogue, createApplication, deleteApplication, setEntry, setExport, setAllowedSpawners, fetchDrift, importApplication, startSetup, stopSetup, fetchAccountNames, subscribe, unsubscribe }
}
