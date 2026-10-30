import { MAX_LOOKBACK_HOURS } from '@/utils/evalMetrics'

// Hand-kept parity with server/internal/api/insights/handler.go.
export type CostSource = 'cost_state' | 'estimate'
export type Preset = '7d' | '30d' | '90d' | 'all'
export type InsightsSort = 'cost' | 'agent_ms' | 'tokens' | 'turns' | 'started_at' | 'recorded_at' | 'lines'

export interface InsightsFilters {
  preset: Preset
  from?: string
  to: string
  project?: string
}

export interface InsightsTotals {
  costUsd: number
  agentMs: number
  tokens: number
  sessions: number
  estimated: number
}

export interface InsightsSummary {
  from: string
  to: string
  current: InsightsTotals
  previous: InsightsTotals | null
}

export interface InsightsSessionRow {
  sessionId: string
  title: string
  projectPath: string
  projectName: string
  model: string
  startedAt: string | null
  recordedAt: string
  costUsd: number
  costSource: CostSource
  costUnknownModel: boolean
  agentMs: number
  agentTimeSource: string
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheCreationTokens: number
  turns: number
  linesAdded: number
  linesRemoved: number
  modelCosts: Record<string, number>
}

export interface InsightsSessionsPage {
  rows: InsightsSessionRow[]
  total: number
  limit: number
  offset: number
}

export interface InsightsProject {
  projectPath: string
  projectName: string
  sessions: number
}

export interface InsightsSessionsQuery {
  sort: InsightsSort
  order: 'asc' | 'desc'
  limit: number
  offset: number
}

const PRESET_DAYS: Record<Exclude<Preset, 'all'>, number> = { '7d': 7, '30d': 30, '90d': 90 }
const DAY_MS = 86_400_000

const isoDay = (d: Date): string => d.toISOString().slice(0, 10)

export function presetRange(p: Preset, today: Date): { from?: string, to: string } {
  const to = isoDay(today)
  if (p === 'all')
    return { to }
  return { from: isoDay(new Date(today.getTime() - PRESET_DAYS[p] * DAY_MS)), to }
}

export function presetHours(p: Preset): number {
  return p === 'all' ? MAX_LOOKBACK_HOURS : PRESET_DAYS[p] * 24
}

function rangeParams(f: Pick<InsightsFilters, 'from' | 'to'>): URLSearchParams {
  const params = new URLSearchParams()
  if (f.from)
    params.set('from', f.from)
  params.set('to', f.to)
  return params
}

async function getJson<T>(path: string, params: URLSearchParams, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${path}?${params}`, { signal })
  if (!res.ok)
    throw new Error(`HTTP ${res.status}`)
  return await res.json() as T
}

function filterParams(f: InsightsFilters): URLSearchParams {
  const params = rangeParams(f)
  if (f.project)
    params.set('project', f.project)
  return params
}

export function fetchSummary(f: InsightsFilters, signal?: AbortSignal): Promise<InsightsSummary> {
  return getJson('/api/insights/summary', filterParams(f), signal)
}

export function fetchSessions(f: InsightsFilters, q: InsightsSessionsQuery, signal?: AbortSignal): Promise<InsightsSessionsPage> {
  const params = filterParams(f)
  params.set('sort', q.sort)
  params.set('order', q.order)
  params.set('limit', String(q.limit))
  params.set('offset', String(q.offset))
  return getJson('/api/insights/sessions', params, signal)
}

export function fetchProjects(f: Pick<InsightsFilters, 'from' | 'to'>, signal?: AbortSignal): Promise<InsightsProject[]> {
  return getJson('/api/insights/projects', rangeParams(f), signal)
}
