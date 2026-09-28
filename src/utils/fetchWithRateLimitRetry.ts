const MAX_429_RETRIES = 3
const DEFAULT_RETRY_AFTER_MS = 1000
const MAX_RETRY_AFTER_MS = 5000

function retryDelayMs(res: Response): number {
  const seconds = Number.parseInt(res.headers.get('Retry-After') ?? '', 10)
  const ms = Number.isFinite(seconds) && seconds >= 0 ? seconds * 1000 : DEFAULT_RETRY_AFTER_MS
  return Math.min(ms, MAX_RETRY_AFTER_MS)
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms))
}

// A 429 here is typically the dashboard's own boot burst exhausting the shared per-IP limiter (server/internal/api/middleware.go) — transient, so retry before failing.
export async function fetchWithRateLimitRetry(input: string, init?: RequestInit): Promise<Response> {
  let res = await fetch(input, init)
  for (let attempt = 0; attempt < MAX_429_RETRIES && res.status === 429; attempt++) {
    await sleep(retryDelayMs(res))
    res = await fetch(input, init)
  }
  return res
}
