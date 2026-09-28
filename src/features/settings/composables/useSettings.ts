import { ref } from 'vue'
import { errorMessage } from '@/utils/errorMessage'

export interface SettingView {
  key: string
  type: string
  value: string
  default: string
  apply: 'live' | 'restart'
  category: string
  enum?: string[]
}

export function useSettings() {
  const items = ref<SettingView[]>([])
  const loading = ref(true)
  const error = ref<string | null>(null)

  async function refetch() {
    loading.value = true
    error.value = null
    try {
      const res = await fetch('/api/settings')
      if (!res.ok)
        throw new Error(`Failed to load settings (HTTP ${res.status})`)
      items.value = await res.json()
    }
    catch (e) {
      error.value = errorMessage(e, 'Failed to load settings')
    }
    finally {
      loading.value = false
    }
  }

  // update returns the apply-semantics so the caller can raise the right toast.
  async function update(key: string, value: string): Promise<'live' | 'restart'> {
    const res = await fetch(`/api/settings/${key}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value }),
    })
    if (!res.ok) {
      const body = await res.json().catch(() => ({})) as { error?: string }
      throw new Error(body.error || `HTTP ${res.status}`)
    }
    const saved = await res.json() as { key: string, value: string, applied: 'live' | 'restart' }
    items.value = items.value.map(i => (i.key === saved.key ? { ...i, value: saved.value } : i))
    return saved.applied
  }

  return { items, loading, error, refetch, update }
}
