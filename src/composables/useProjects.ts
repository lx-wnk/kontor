import type { Project } from '../types'
import { onUnmounted, ref, shallowRef } from 'vue'
import { errorMessage } from '../utils/errorMessage'
import { fetchWithRateLimitRetry } from '../utils/fetchWithRateLimitRetry'
import { createSseResource } from './useSseResource'

const projects = shallowRef<Project[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)

export interface ProjectEvent {
  type: 'project_created' | 'project_updated' | 'project_deleted'
  projectId: string
  payload?: unknown
}

async function fetchProjects(): Promise<void> {
  try {
    const res = await fetchWithRateLimitRetry('/api/projects')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    projects.value = await res.json() as Project[]
    isLoading.value = false
    error.value = null
  }
  catch (err) {
    error.value = errorMessage(err)
    isLoading.value = false
  }
}

function applyEvent(event: ProjectEvent): void {
  switch (event.type) {
    case 'project_created': {
      const project = event.payload as Project
      if (project && !projects.value.some(p => p.id === project.id))
        projects.value = [project, ...projects.value]
      break
    }
    case 'project_updated': {
      const project = event.payload as Project
      if (project)
        projects.value = projects.value.map(p => p.id === project.id ? project : p)
      break
    }
    case 'project_deleted': {
      projects.value = projects.value.filter(p => p.id !== event.projectId)
      break
    }
  }
}

function handleSseMessage(data: string): void {
  try {
    const event: ProjectEvent = JSON.parse(data)
    applyEvent(event)
  }
  catch {
    // ignore malformed messages
  }
}

// NOTE: /api/projects/stream SSE endpoint pending — the CLOSED→poll fallback
// keeps the list fresh until the backend stream ships.
const sse = createSseResource({
  streamUrl: '/api/projects/stream',
  fetchInitial: fetchProjects,
  onMessage: handleSseMessage,
})

export interface CreateProjectInput {
  slug: string
  name: string
  description?: string
  color?: string
  defaultSpawnerId?: string | null
  setupCommand?: string | null
}

export async function createProject(input: CreateProjectInput): Promise<Project> {
  const res = await fetch('/api/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to create project')
  }
  return res.json() as Promise<Project>
}

export async function updateProject(id: string, input: Partial<CreateProjectInput>): Promise<Project> {
  const res = await fetch(`/api/projects/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to update project')
  }
  return res.json() as Promise<Project>
}

export async function deleteProject(id: string): Promise<void> {
  const res = await fetch(`/api/projects/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to delete project')
  }
}

export async function fetchProject(id: string): Promise<Project> {
  const res = await fetch(`/api/projects/${id}`)
  if (!res.ok)
    throw new Error(`HTTP ${res.status}`)
  return res.json() as Promise<Project>
}

export function useProjects(options?: { autoStart?: boolean }) {
  if (options?.autoStart !== false)
    sse.startStream()

  onUnmounted(sse.stopStream)

  return {
    projects,
    isLoading,
    error,
    refetch: fetchProjects,
    startStream: sse.startStream,
  }
}
