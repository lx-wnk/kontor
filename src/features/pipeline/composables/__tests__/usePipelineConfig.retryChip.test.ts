import { describe, expect, it } from 'vitest'
import { usePipelineConfig } from '../usePipelineConfig'

describe('retryChip', () => {
  const { retryChip } = usePipelineConfig()

  it('names a queued infra retry with its countdown', () => {
    expect(retryChip('requeued', 1, 12)).toEqual({ label: 'Retrying · 1/3 · 12s', title: 'Auto-retry queued (attempt 1 of 3)' })
  })

  it('divides a rate-limit pause by the rate-limit budget', () => {
    expect(retryChip('rate_limited', 7, 0)).toEqual({ label: 'Retrying · 7/36', title: 'Auto-retry queued (attempt 7 of 36)' })
  })

  it.each(['pending', 'running'])('names a %s retry as in progress, without a countdown', (status) => {
    expect(retryChip(status, 2, 30)).toEqual({ label: 'Retry 2/3', title: 'Auto-retry attempt 2 of 3 in progress' })
  })
})
