import type { Spawner, SpawnerAdapterType } from '../types'
import { onUnmounted, ref, shallowRef } from 'vue'
import { errorMessage } from '../utils/errorMessage'
import { createSseResource } from './useSseResource'

const spawners = shallowRef<Spawner[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)

export interface SpawnerEvent {
  type: 'spawner_created' | 'spawner_updated' | 'spawner_deleted'
  spawnerId: string
  payload?: unknown
}

async function fetchSpawners(): Promise<void> {
  try {
    const res = await fetch('/api/spawners')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    spawners.value = await res.json() as Spawner[]
    isLoading.value = false
    error.value = null
  }
  catch (err) {
    error.value = errorMessage(err)
    isLoading.value = false
  }
}

function applyEvent(event: SpawnerEvent): void {
  switch (event.type) {
    case 'spawner_created': {
      const spawner = event.payload as Spawner
      if (spawner && !spawners.value.some(s => s.id === spawner.id))
        spawners.value = [spawner, ...spawners.value]
      break
    }
    case 'spawner_updated': {
      const spawner = event.payload as Spawner
      if (spawner)
        spawners.value = spawners.value.map(s => s.id === spawner.id ? spawner : s)
      break
    }
    case 'spawner_deleted': {
      spawners.value = spawners.value.filter(s => s.id !== event.spawnerId)
      break
    }
  }
}

function handleSseMessage(data: string): void {
  try {
    const event: SpawnerEvent = JSON.parse(data)
    applyEvent(event)
  }
  catch {
    // ignore malformed messages
  }
}

// NOTE: /api/spawners/stream SSE endpoint pending — the CLOSED→poll fallback
// keeps the list fresh until the backend stream ships.
const sse = createSseResource({
  streamUrl: '/api/spawners/stream',
  fetchInitial: fetchSpawners,
  onMessage: handleSseMessage,
})

export interface CreateSpawnerInput {
  name: string
  slug: string
  command: string
  args?: string[]
  env?: Record<string, string>
  adapterType: SpawnerAdapterType
  adapterConfig: Record<string, string>
  modelOverride?: string
  description?: string
}

export type UpdateSpawnerInput = Partial<CreateSpawnerInput>

export async function createSpawner(input: CreateSpawnerInput): Promise<Spawner> {
  const res = await fetch('/api/spawners', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to create spawner')
  }
  return res.json() as Promise<Spawner>
}

export async function updateSpawner(id: string, input: UpdateSpawnerInput): Promise<Spawner> {
  const res = await fetch(`/api/spawners/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to update spawner')
  }
  return res.json() as Promise<Spawner>
}

export async function deleteSpawner(id: string): Promise<void> {
  const res = await fetch(`/api/spawners/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to delete spawner')
  }
}

// Mark a spawner as the deployment-wide default. The server clears the previous
// default atomically and broadcasts both rows, so the SSE/poll feed updates the
// list — no optimistic mutation needed here.
export async function setDefaultSpawner(id: string): Promise<Spawner> {
  const res = await fetch(`/api/spawners/${id}/default`, { method: 'POST' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to set default spawner')
  }
  return res.json() as Promise<Spawner>
}

export function useSpawners(options?: { autoStart?: boolean }) {
  // Only a caller that raised the shared subscriber count may lower it.
  if (options?.autoStart !== false) {
    sse.startStream()
    onUnmounted(sse.stopStream)
  }

  return {
    spawners,
    isLoading,
    error,
    refetch: fetchSpawners,
    startStream: sse.startStream,
  }
}
