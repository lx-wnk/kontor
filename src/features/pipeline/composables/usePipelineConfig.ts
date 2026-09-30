import { ref } from 'vue'

type SpawnerStage = 'implementation' | 'self_review' | 'finalization'

export interface PipelineConfig {
  maxParallelOrchestrators: number
  stageTimeoutSeconds: number
  maxAutoRetries: number
  maxRateLimitRetries: number
  retryBackoffSeconds: number
  stageModels: Record<SpawnerStage, string>
  stageSpawners: Record<SpawnerStage, string>
}

interface PartialPipelineConfig {
  maxParallelOrchestrators?: number
  stageTimeoutSeconds?: number
  stageModels?: Partial<PipelineConfig['stageModels']>
  stageSpawners?: Partial<PipelineConfig['stageSpawners']>
}

const config = ref<PipelineConfig | null>(null)
const maxAutoRetries = ref(3)
const maxRateLimitRetries = ref(36)
const loading = ref(false)
const error = ref<string | null>(null)
let configPromise: Promise<void> | null = null

function fetchConfig(): Promise<void> {
  if (configPromise)
    return configPromise
  loading.value = true
  error.value = null
  configPromise = (async () => {
    try {
      const res = await fetch('/api/pipeline/config')
      if (!res.ok)
        throw new Error(`HTTP ${res.status}`)
      config.value = await res.json() as PipelineConfig
      maxAutoRetries.value = config.value.maxAutoRetries
      maxRateLimitRetries.value = config.value.maxRateLimitRetries
    }
    catch (err) {
      error.value = (err as Error).message
      configPromise = null // allow a later retry after a failed fetch
    }
    finally {
      loading.value = false
    }
  })()
  return configPromise
}

async function saveConfig(partial: PartialPipelineConfig): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const res = await fetch('/api/pipeline/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(partial),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
      throw new Error((err as { error: string }).error || `HTTP ${res.status}`)
    }
    configPromise = null // force a fresh fetch so callers see the saved values
    await fetchConfig()
  }
  catch (err) {
    error.value = (err as Error).message
    loading.value = false
  }
}

function retryBudgetFor(status: string | null | undefined): number {
  return status === 'rate_limited' ? maxRateLimitRetries.value : maxAutoRetries.value
}

function isRetryQueued(status: string | null | undefined): boolean {
  return status === 'requeued' || status === 'rate_limited'
}

interface RetryChip {
  label: string
  title: string
}

function retryChip(status: string | null | undefined, count: number, secondsLeft: number): RetryChip {
  const budget = retryBudgetFor(status)
  if (isRetryQueued(status)) {
    return {
      label: `Retrying · ${count}/${budget}${secondsLeft > 0 ? ` · ${secondsLeft}s` : ''}`,
      title: `Auto-retry queued (attempt ${count} of ${budget})`,
    }
  }
  return {
    label: `Retry ${count}/${budget}`,
    title: `Auto-retry attempt ${count} of ${budget} in progress`,
  }
}

export function usePipelineConfig() {
  fetchConfig()
  return { config, maxAutoRetries, maxRateLimitRetries, retryBudgetFor, isRetryQueued, retryChip, loading, error, fetchConfig, saveConfig }
}
