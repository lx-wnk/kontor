import type { AvailableAction } from '../types'

export type ActionVariant = 'primary' | 'secondary' | 'info' | 'danger'

interface ActionMeta {
  label: string
  variant: ActionVariant
  endpoint: (taskId: string) => string
}

const ACTION_META: Record<string, ActionMeta> = {
  advance: { label: 'Advance →', variant: 'primary', endpoint: id => `/api/tasks/${id}/advance` },
  retry: { label: 'Retry Stage', variant: 'info', endpoint: id => `/api/tasks/${id}/retry` },
  resume: { label: 'Resume', variant: 'secondary', endpoint: id => `/api/tasks/${id}/resume` },
  cancel: { label: 'Cancel Task', variant: 'danger', endpoint: id => `/api/tasks/${id}/cancel` },
  complete: { label: 'Mark Done', variant: 'secondary', endpoint: id => `/api/tasks/${id}/complete` },
  hold: { label: 'Hold', variant: 'secondary', endpoint: id => `/api/tasks/${id}/hold` },
  approve_all_pending: { label: 'Approve All Pending', variant: 'info', endpoint: id => `/api/tasks/${id}/approve-all-pending` },
  approve_spec: { label: 'Approve Spec', variant: 'primary', endpoint: id => `/api/refine/${id}/confirm` },
  approve_plan: { label: 'Approve Plan', variant: 'primary', endpoint: id => `/api/plan/${id}/approve` },
  reject_plan: { label: 'Reject Plan', variant: 'secondary', endpoint: id => `/api/plan/${id}/reject` },
}

/**
 * Resolve an action's endpoint URL. Lets other callers reuse the canonical
 *  endpoint without re-defining the path (e.g. the refinement confirm flow).
 */
export function actionEndpoint(action: string, taskId: string): string | undefined {
  return ACTION_META[action]?.endpoint(taskId)
}

export function actionLabel(action: string): string {
  return ACTION_META[action]?.label ?? action
}

export function actionVariant(action: string): ActionVariant {
  return ACTION_META[action]?.variant ?? 'secondary'
}

export async function runAction(taskId: string, action: string, body?: Record<string, unknown>): Promise<void> {
  const meta = ACTION_META[action]
  if (!meta)
    throw new Error(`Unknown action: ${action}`)

  const res = await fetch(meta.endpoint(taskId), {
    method: 'POST',
    ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error?: string }).error || `Action "${action}" failed`)
  }
}

/** Returns only the enabled actions that have metadata (renderable by the UI). */
export function renderableActions(actions: AvailableAction[] | undefined): AvailableAction[] {
  if (!actions)
    return []
  return actions.filter(a => ACTION_META[a.action] !== undefined)
}
