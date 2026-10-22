import type { CloudSessionView } from './useCloudSessions'
import { ref, shallowRef } from 'vue'
import { errorMessage, readErrorMessage } from '../../../utils/errorMessage'
import { upsertSession } from './useCloudSessions'

export interface CloudRoutineView {
  id: string
  name: string
  fireUrl: string
  spawnerId: string
  hasToken: boolean
  createdAt: string
  updatedAt: string
}

export interface CreateCloudRoutineInput {
  name: string
  fireUrl: string
  spawnerId: string
  token: string
}

export interface UpdateCloudRoutineInput {
  name?: string
  fireUrl?: string
  token?: string
}

export class FireFailedError extends Error {
  constructor(readonly status: number, readonly body: string) {
    super(`Routine fire failed: HTTP ${status}`)
    this.name = 'FireFailedError'
  }
}

const routines = shallowRef<CloudRoutineView[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)

async function fetchRoutines(): Promise<void> {
  try {
    const res = await fetch('/api/cloud-routines')
    if (!res.ok)
      throw new Error(await readErrorMessage(res, `HTTP ${res.status}`))
    routines.value = await res.json() as CloudRoutineView[]
    error.value = null
  }
  catch (err) {
    error.value = errorMessage(err)
  }
  isLoading.value = false
}

async function send(url: string, method: string, body?: unknown): Promise<void> {
  const res = await fetch(url, body === undefined
    ? { method }
    : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!res.ok)
    throw new Error(await readErrorMessage(res, `HTTP ${res.status}`))
}

async function create(input: CreateCloudRoutineInput): Promise<void> {
  await send('/api/cloud-routines', 'POST', input)
  await fetchRoutines()
}

async function update(id: string, input: UpdateCloudRoutineInput): Promise<void> {
  const { token, ...rest } = input
  await send(`/api/cloud-routines/${id}`, 'PATCH', token ? { ...rest, token } : rest)
  await fetchRoutines()
}

async function remove(id: string): Promise<void> {
  await send(`/api/cloud-routines/${id}`, 'DELETE')
  await fetchRoutines()
}

async function fire(id: string, text?: string): Promise<CloudSessionView> {
  const res = await fetch(`/api/cloud-routines/${id}/fire`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(text ? { text } : {}),
  })
  if (res.ok)
    return upsertSession(await res.json() as CloudSessionView)
  const body = await res.json().catch(() => ({})) as { error?: string, status?: number, body?: string }
  // Only a FireError carries the upstream status; other 502s have just `error`.
  if (res.status === 502 && typeof body.status === 'number')
    throw new FireFailedError(body.status, body.body ?? '')
  throw new Error(body.error || `HTTP ${res.status}`)
}

export function useCloudRoutines() {
  return { routines, isLoading, error, refetch: fetchRoutines, create, update, remove, fire }
}
