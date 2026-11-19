import type { ComputedRef, Ref } from 'vue'
import type { NextThing } from './useNextThing'
import { ref, watch } from 'vue'
import { useCapabilityDecisions } from '@/features/agents'
import { resolvePermissionRequest } from '@/features/pipeline'
import { errorMessage } from '@/utils/errorMessage'

export interface NeedsYouTriage {
  focusedKey: Ref<string | null>
  focusNext: () => void
  decide: (choice: 'allow' | 'deny') => Promise<string | null>
  approveAll: (visible: NextThing[]) => Promise<string | null>
}

/** Resolves to an error message, or null on success or when nothing needed doing. */
async function resolveItem(item: NextThing, choice: 'allow' | 'deny', resolveCapability: ReturnType<typeof useCapabilityDecisions>['resolve'], inFlight: Set<string>): Promise<string | null> {
  if (inFlight.has(item.key))
    return null
  inFlight.add(item.key)
  try {
    return await dispatch(item, choice, resolveCapability)
  }
  finally {
    inFlight.delete(item.key)
  }
}

async function dispatch(item: NextThing, choice: 'allow' | 'deny', resolveCapability: ReturnType<typeof useCapabilityDecisions>['resolve']): Promise<string | null> {
  if (item.kind === 'permission' && item.request) {
    try {
      await resolvePermissionRequest(item.taskId, item.request.id, choice === 'allow' ? 'allow_once' : 'deny_once')
      return null
    }
    catch (e) {
      return errorMessage(e, 'Could not record that decision')
    }
  }
  if (item.kind === 'capability' && item.decision) {
    const result = await resolveCapability(item.decision.id, choice)
    return result.outcome === 'error' ? result.message : null
  }
  return null
}

export function useNeedsYouTriage(list: ComputedRef<NextThing[]>, refresh: () => void): NeedsYouTriage {
  const { resolve: resolveCapability } = useCapabilityDecisions()
  const focusedKey = ref<string | null>(null)
  const inFlight = new Set<string>()

  // A shifted list must never leave focus on a key whose item is gone: a/d would then act on nothing, or on a look-alike.
  watch(list, (items) => {
    if (focusedKey.value !== null && !items.some(i => i.key === focusedKey.value))
      focusedKey.value = null
  })

  function focusNext() {
    const items = list.value
    if (items.length === 0) {
      focusedKey.value = null
      return
    }
    const at = items.findIndex(i => i.key === focusedKey.value)
    focusedKey.value = items[(at + 1) % items.length].key
  }

  async function decide(choice: 'allow' | 'deny'): Promise<string | null> {
    const item = list.value.find(i => i.key === focusedKey.value)
    if (!item || inFlight.has(item.key))
      return null
    const problem = await resolveItem(item, choice, resolveCapability, inFlight)
    if (problem === null && (item.kind === 'permission' || item.kind === 'capability'))
      refresh()
    return problem
  }

  async function approveAll(visible: NextThing[]): Promise<string | null> {
    const permissions = visible.filter(i => i.kind === 'permission' && !inFlight.has(i.key))
    if (permissions.length === 0)
      return null
    const problems = await Promise.all(permissions.map(i => resolveItem(i, 'allow', resolveCapability, inFlight)))
    refresh()
    return problems.find(p => p !== null) ?? null
  }

  return { focusedKey, focusNext, decide, approveAll }
}
