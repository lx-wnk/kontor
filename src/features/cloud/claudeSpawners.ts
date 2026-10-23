import type { Ref } from 'vue'
import type { SelectOption } from '@/components/ui/selectOption'
import type { Spawner } from '@/types'
import { computed, ref } from 'vue'

// Client mirror of cloudsessions.RunsClaude.
export function isClaudeSpawner(spawner: Pick<Spawner, 'adapterType'>): boolean {
  return !spawner.adapterType || spawner.adapterType === 'claude'
}

export function claudeSpawnerOptions(spawners: Spawner[]): SelectOption<string>[] {
  return spawners.filter(isClaudeSpawner).map(s => ({ value: s.id, label: s.name }))
}

export function useClaudeSpawnerPicker(spawners: Ref<Spawner[]>) {
  const options = computed(() => claudeSpawnerOptions(spawners.value))
  const chosenId = ref('')
  const selectedId = computed({
    get: () => options.value.some(o => o.value === chosenId.value)
      ? chosenId.value
      : (options.value[0]?.value ?? ''),
    set: (value: string) => { chosenId.value = value },
  })
  return { options, selectedId }
}
