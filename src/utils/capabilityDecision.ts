import type { PendingCapabilityDecision } from '@/sdk.generated'

export function capabilityValueLabel(decision: PendingCapabilityDecision): string {
  return decision.value || 'Everything'
}

// ValueElided/ContextElided are rune-cut counts, not booleans: 0 and undefined
// both mean "not cut" so the "…" marker (and the WCAG-friendly title behind
// it) never renders for whole, untouched values.
export function elidedTitle(count: number | undefined): string | undefined {
  return count ? `${count} character${count === 1 ? '' : 's'} cut off` : undefined
}
