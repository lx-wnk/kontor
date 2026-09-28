import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchWithRateLimitRetry } from './fetchWithRateLimitRetry'

function tooManyRequests(retryAfter?: string) {
  return new Response('', { status: 429, headers: retryAfter === undefined ? {} : { 'Retry-After': retryAfter } })
}

describe('fetchWithRateLimitRetry', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.useFakeTimers()
  })
  afterEach(() => vi.useRealTimers())

  it('caps a huge Retry-After at 5 seconds', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(tooManyRequests('99999'))
      .mockResolvedValueOnce(new Response('', { status: 200 }))
    const promise = fetchWithRateLimitRetry('/api/settings')
    await vi.advanceTimersByTimeAsync(4999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
    await expect(promise).resolves.toMatchObject({ status: 200 })
  })

  it('falls back to the 1 second default on a negative Retry-After', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(tooManyRequests('-5'))
      .mockResolvedValueOnce(new Response('', { status: 200 }))
    const promise = fetchWithRateLimitRetry('/api/settings')
    await vi.advanceTimersByTimeAsync(999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
    await expect(promise).resolves.toMatchObject({ status: 200 })
  })

  it('falls back to the 1 second default on a non-numeric Retry-After', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(tooManyRequests('soon'))
      .mockResolvedValueOnce(new Response('', { status: 200 }))
    const promise = fetchWithRateLimitRetry('/api/settings')
    await vi.advanceTimersByTimeAsync(999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
    await expect(promise).resolves.toMatchObject({ status: 200 })
  })

  it('returns the response once a retry succeeds', async () => {
    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(tooManyRequests('0'))
      .mockResolvedValueOnce(tooManyRequests('0'))
      .mockResolvedValueOnce(tooManyRequests('0'))
      .mockResolvedValueOnce(new Response('ok', { status: 200 }))
    const promise = fetchWithRateLimitRetry('/api/settings')
    await vi.runAllTimersAsync()
    await expect(promise).resolves.toMatchObject({ status: 200 })
  })

  it('returns the last 429 after exhausting the retries', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(tooManyRequests('0'))
    const promise = fetchWithRateLimitRetry('/api/settings')
    await vi.runAllTimersAsync()
    await expect(promise).resolves.toMatchObject({ status: 429 })
    expect(fetch).toHaveBeenCalledTimes(4)
  })
})
