import { fetchWithRateLimitRetry } from '@/utils/fetchWithRateLimitRetry'

// Mirrors server/internal/api/obsidian/handler.go (graph); Go cannot share this type, keep the two in step by hand.
export interface GraphResponse {
  configured: boolean
  notes?: Array<[path: string, mtimeMs: number]>
  links?: Array<[from: number, to: number]>
}

export function fetchGraph(): Promise<Response> {
  return fetchWithRateLimitRetry('/api/obsidian/graph')
}

export function openNoteRequest(path: string): Promise<Response> {
  return fetchWithRateLimitRetry('/api/obsidian/open', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
}
