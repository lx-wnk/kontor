import { onUnmounted, ref, shallowRef } from 'vue'
import { createSseResource } from '../../../composables/useSseResource'
import { errorMessage } from '../../../utils/errorMessage'

export type CloudSessionOrigin = 'started' | 'imported' | 'routine'

export interface CloudSessionView {
  id: string
  sessionId: string
  url: string
  spawnerId: string
  title: string
  origin: CloudSessionOrigin
  lastMessage: string
  lastMessageAt: string | null
  lastError: string
  createdAt: string
  updatedAt: string
}

export interface CloudAccountAuth {
  loggedIn: boolean
  authMethod: string
  apiProvider: string
  subscriptionType: string
}

export interface CloudAccount {
  configDir: string
  spawners: { id: string, name: string }[]
  auth: CloudAccountAuth | null
  ready: boolean
  loginHint: string
  error?: string
}

export interface CloudSessionEvent {
  type: 'cloud_session_created' | 'cloud_session_updated' | 'cloud_session_deleted'
  cloudSessionId: string
  payload?: unknown
}

export interface ImportCloudSessionInput {
  spawnerId: string
  url: string
  title?: string
}

const sessions = shallowRef<CloudSessionView[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)

async function fetchSessions(): Promise<void> {
  try {
    const res = await fetch('/api/cloud-sessions')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    sessions.value = await res.json() as CloudSessionView[]
    error.value = null
  }
  catch (err) {
    error.value = errorMessage(err)
  }
  isLoading.value = false
}

function applyEvent(event: CloudSessionEvent): void {
  switch (event.type) {
    case 'cloud_session_created': {
      const session = event.payload as CloudSessionView
      if (session && !sessions.value.some(s => s.id === session.id))
        sessions.value = [session, ...sessions.value]
      break
    }
    case 'cloud_session_updated': {
      const session = event.payload as CloudSessionView
      if (session)
        sessions.value = sessions.value.map(s => s.id === session.id ? session : s)
      break
    }
    case 'cloud_session_deleted': {
      sessions.value = sessions.value.filter(s => s.id !== event.cloudSessionId)
      break
    }
  }
}

function upsertSession(session: CloudSessionView): CloudSessionView {
  sessions.value = sessions.value.some(s => s.id === session.id)
    ? sessions.value.map(s => s.id === session.id ? session : s)
    : [session, ...sessions.value]
  return session
}

function handleSseMessage(data: string): void {
  try {
    applyEvent(JSON.parse(data) as CloudSessionEvent)
  }
  catch {
    // ignore malformed messages
  }
}

const sse = createSseResource({
  streamUrl: '/api/cloud-sessions/stream',
  fetchInitial: fetchSessions,
  onMessage: handleSseMessage,
  onConnected: fetchSessions,
})

// Surfaces the server's `{error}` text so the view can show it verbatim.
async function throwServerError(res: Response): Promise<never> {
  const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` })) as { error?: string }
  throw new Error(body.error || `HTTP ${res.status}`)
}

async function postJson(url: string, body: unknown): Promise<CloudSessionView> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok)
    return throwServerError(res)
  return res.json() as Promise<CloudSessionView>
}

export function importSession(input: ImportCloudSessionInput): Promise<CloudSessionView> {
  return postJson('/api/cloud-sessions/import', input).then(upsertSession)
}

export function sendMessage(id: string, text: string): Promise<CloudSessionView> {
  return postJson(`/api/cloud-sessions/${id}/messages`, { text }).then(upsertSession)
}

export async function removeSession(id: string): Promise<void> {
  const res = await fetch(`/api/cloud-sessions/${id}`, { method: 'DELETE' })
  if (!res.ok)
    await throwServerError(res)
}

export function useCloudSessions(options?: { autoStart?: boolean }) {
  // Only a caller that raised the shared subscriber count may lower it.
  if (options?.autoStart !== false) {
    sse.startStream()
    onUnmounted(sse.stopStream)
  }

  return {
    sessions,
    isLoading,
    error,
    refetch: fetchSessions,
    startStream: sse.startStream,
  }
}

const accounts = shallowRef<CloudAccount[]>([])
const accountsLoading = ref(true)
const accountsError = ref<string | null>(null)

async function fetchAccounts(url: string): Promise<void> {
  try {
    const res = await fetch(url)
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    accounts.value = await res.json() as CloudAccount[]
    accountsError.value = null
  }
  catch (err) {
    accountsError.value = errorMessage(err)
  }
  accountsLoading.value = false
}

export function useCloudAccounts() {
  void fetchAccounts('/api/cloud-accounts')

  return {
    accounts,
    isLoading: accountsLoading,
    error: accountsError,
    refetch: () => fetchAccounts('/api/cloud-accounts'),
    refresh: () => fetchAccounts('/api/cloud-accounts?refresh=1'),
  }
}
