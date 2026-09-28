import { computed, onMounted, ref } from 'vue'
import { errorMessage, readErrorMessage } from '@/utils/errorMessage'

// Mirrors resourceView in server/internal/api/resources/handler.go — a
// hand-written camelCase DTO, not an ent row, so these names are the
// codebase's normal convention rather than useGrants' snake_case exception.
export interface ResourceView {
  id: string
  kind: string
  slug: string
  name: string
  scopeKind: string
  scopeRef: string
  nodeId: string
  state: string
  version: string
  origin: string
  originRef: string
  createdAt: string
  updatedAt: string
}

// Mirrors resourceKinds in server/internal/api/resources/handler.go, same
// order. The route answers 400 for anything else.
export const RESOURCE_KINDS = ['application', 'routine', 'skill', 'memory_space'] as const
export type ResourceKind = typeof RESOURCE_KINDS[number]

export const RESOURCE_KIND_LABELS: Record<ResourceKind, string> = {
  application: 'Applications',
  routine: 'Routines',
  skill: 'Skills',
  memory_space: 'Memory spaces',
}

// Mirrors memory.ScopeKinds (server/internal/memory/authorize.go:16), which is
// the accepted set for every transport's scope/scopeRef pair.
export const RESOURCE_SCOPE_KINDS = ['global', 'project', 'application'] as const
export type ResourceScopeKind = typeof RESOURCE_SCOPE_KINDS[number]

export interface ResourceQuery {
  kind: ResourceKind
  scopeKind: ResourceScopeKind
  scopeRef: string
}

// States only what is known. The route maps every Gate.Authorize failure to 403
// — a missing grant, a rate limit and a failed read of the grant store alike —
// so the fallback names no cause. The likely one is named once, by the notice
// that renders this (cf. useMemory's READ_DENIED_FALLBACK).
const DENIED_FALLBACK = 'The registry route refused this read (HTTP 403) without giving a reason.'

// The kind is taken here rather than on the mount fetch because this composable
// owns the one request it fires on mount. A caller that asked for its kind
// afterwards issued a second request for a different kind, and the response that
// landed last decided what the caller rendered.
export function useResources(initialKind: ResourceKind = 'application') {
  const resources = ref<ResourceView[]>([])
  const query = ref<ResourceQuery>({ kind: initialKind, scopeKind: 'global', scopeRef: '' })
  const loading = ref(true)
  const error = ref<string | null>(null)
  // Held apart from `error`: `kind=memory_space` is gated on memory.read, so on
  // a fresh install a refusal is the ordinary first-run answer for that kind and
  // has a different fix from a broken read.
  const denied = ref<string | null>(null)

  // True while a non-global scope has no ref yet — the request below is
  // deliberately held rather than fired (see the guard in fetchResources).
  // Exposed here, not re-derived at the call site, so the "should we hold"
  // condition has exactly one owner.
  const held = computed(() => query.value.scopeKind !== 'global' && query.value.scopeRef.trim() === '')

  async function fetchResources(next?: Partial<ResourceQuery>): Promise<void> {
    if (next)
      query.value = { ...query.value, ...next }

    // The server refuses a non-global scope with no ref (memory.ParseScope) —
    // hold the request rather than firing a known-400 on every keystroke.
    // Reads `held`, the same computed the panel renders from, so there is
    // exactly one place that decides whether this query is fireable.
    if (held.value) {
      resources.value = []
      loading.value = false
      error.value = null
      denied.value = null
      return
    }

    const { kind, scopeKind, scopeRef } = query.value
    loading.value = true
    error.value = null
    denied.value = null
    try {
      const params = new URLSearchParams({ kind, scope: scopeKind })
      if (scopeKind !== 'global')
        params.set('scopeRef', scopeRef.trim())
      const res = await fetch(`/api/resources?${params.toString()}`)
      if (res.status === 403) {
        denied.value = await readErrorMessage(res, DENIED_FALLBACK)
        resources.value = []
        return
      }
      if (!res.ok)
        throw new Error(`Failed to load ${kind} resources (HTTP ${res.status})`)
      resources.value = await res.json()
    }
    catch (e) {
      // Clear on failure: keeping the previous kind's rows on screen under a
      // new kind's heading would misreport what the registry holds.
      resources.value = []
      error.value = errorMessage(e, 'Failed to load resources')
    }
    finally {
      loading.value = false
    }
  }

  onMounted(() => {
    void fetchResources()
  })

  return { resources, query, loading, error, denied, held, fetchResources }
}
