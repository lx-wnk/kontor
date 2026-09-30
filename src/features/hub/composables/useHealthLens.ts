import type { Ref } from 'vue'
import type { HealthLens } from '../hubHealth'
import { ref } from 'vue'

const lens = ref<HealthLens | null>(null)

function toggle(l: HealthLens) {
  lens.value = lens.value === l ? null : l
}

export function useHealthLens(): { lens: Ref<HealthLens | null>, toggle: (l: HealthLens) => void } {
  return { lens, toggle }
}
