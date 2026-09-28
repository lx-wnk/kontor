import type { Ref, ShallowRef } from 'vue'
import type { GraphResponse } from '../graphApi'
import { ref, shallowRef } from 'vue'
import { errorMessage, readErrorMessage } from '@/utils/errorMessage'
import { fetchGraph, openNoteRequest } from '../graphApi'

export type GraphStatus = 'idle' | 'loading' | 'ready' | 'unconfigured' | 'denied' | 'failed'
export interface HubNote { index: number, path: string, title: string, mtimeMs: number, links: number[], backlinks: number[] }

const REFRESH_INTERVAL_MS = 60_000

const status = ref<GraphStatus>('idle')
const message = ref('')
const notes = shallowRef<HubNote[]>([])
let lastFetchMs = 0
let inFlight: Promise<void> | null = null

function titleOf(path: string): string {
  const name = path.slice(path.lastIndexOf('/') + 1)
  return name.endsWith('.md') ? name.slice(0, -3) : name
}

function buildNotes(body: GraphResponse): HubNote[] {
  const built = (body.notes ?? []).map(([path, mtimeMs], index): HubNote => ({ index, path, title: titleOf(path), mtimeMs, links: [], backlinks: [] }))
  for (const [from, to] of body.links ?? []) {
    if (!built[from] || !built[to])
      continue
    built[from].links.push(to)
    built[to].backlinks.push(from)
  }
  return built
}

async function doFetch(): Promise<void> {
  status.value = 'loading'
  try {
    const res = await fetchGraph()
    if (res.status === 403) {
      message.value = await readErrorMessage(res, 'memory.read denied')
      status.value = 'denied'
      return
    }
    if (!res.ok) {
      message.value = await readErrorMessage(res, `HTTP ${res.status}`)
      status.value = 'failed'
      return
    }
    const body = await res.json() as GraphResponse
    if (!body.configured) {
      notes.value = []
      status.value = 'unconfigured'
      return
    }
    notes.value = buildNotes(body)
    status.value = 'ready'
  }
  catch (e) {
    message.value = errorMessage(e, 'network error')
    status.value = 'failed'
  }
}

// Every request books a memory.Gate rate-limit use: never poll, fetch on mount/focus at most once per interval unless forced.
async function refresh(force = false): Promise<void> {
  if (!force && Date.now() - lastFetchMs < REFRESH_INTERVAL_MS)
    return
  if (inFlight)
    return inFlight
  inFlight = doFetch().finally(() => {
    if (status.value !== 'failed')
      lastFetchMs = Date.now()
    inFlight = null
  })
  return inFlight
}

function recentNotes(count: number): HubNote[] {
  return [...notes.value].sort((a, b) => b.mtimeMs - a.mtimeMs).slice(0, count)
}

function noteByPath(path: string): HubNote | undefined {
  return notes.value.find(n => n.path === path)
}

async function openInObsidian(path: string): Promise<string | null> {
  try {
    const res = await openNoteRequest(path)
    return res.ok ? null : await readErrorMessage(res, `HTTP ${res.status}`)
  }
  catch (e) {
    return errorMessage(e, 'network error')
  }
}

export function useObsidianGraph(): {
  status: Readonly<Ref<GraphStatus>>
  message: Readonly<Ref<string>>
  notes: Readonly<ShallowRef<HubNote[]>>
  refresh: (force?: boolean) => Promise<void>
  recentNotes: (count: number) => HubNote[]
  noteByPath: (path: string) => HubNote | undefined
  openInObsidian: (path: string) => Promise<string | null>
} {
  return { status, message, notes, refresh, recentNotes, noteByPath, openInObsidian }
}
