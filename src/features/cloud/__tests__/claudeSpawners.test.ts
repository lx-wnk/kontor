import type { Spawner } from '@/types'
import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { claudeSpawnerOptions, isClaudeSpawner, useClaudeSpawnerPicker } from '../claudeSpawners'

function makeSpawner(id: string, adapterType: string): Spawner {
  return {
    id,
    name: `Spawner ${id}`,
    slug: id,
    command: 'claude',
    args: [],
    env: {},
    adapterType: adapterType as Spawner['adapterType'],
    adapterConfig: {},
    builtIn: false,
    isDefault: false,
    createdAt: '',
    updatedAt: '',
  }
}

describe('isClaudeSpawner', () => {
  it('accepts adapter type claude and an empty adapter type', () => {
    expect(isClaudeSpawner(makeSpawner('a', 'claude'))).toBe(true)
    expect(isClaudeSpawner(makeSpawner('b', ''))).toBe(true)
  })

  it('rejects every other adapter type', () => {
    expect(isClaudeSpawner(makeSpawner('c', 'ollama'))).toBe(false)
    expect(isClaudeSpawner(makeSpawner('d', 'Claude'))).toBe(false)
  })
})

describe('claudeSpawnerOptions', () => {
  it('lists only claude spawners as select options', () => {
    const options = claudeSpawnerOptions([makeSpawner('a', 'claude'), makeSpawner('b', 'ollama'), makeSpawner('c', '')])

    expect(options).toEqual([
      { value: 'a', label: 'Spawner a' },
      { value: 'c', label: 'Spawner c' },
    ])
  })

  it('is empty when no spawner runs claude', () => {
    expect(claudeSpawnerOptions([makeSpawner('b', 'ollama')])).toEqual([])
  })
})

describe('useClaudeSpawnerPicker', () => {
  it('falls back to the first claude spawner until one is chosen', () => {
    const spawners = ref([makeSpawner('x', 'ollama'), makeSpawner('a', 'claude'), makeSpawner('b', 'claude')])
    const { options, selectedId } = useClaudeSpawnerPicker(spawners)

    expect(options.value.map(o => o.value)).toEqual(['a', 'b'])
    expect(selectedId.value).toBe('a')
    selectedId.value = 'b'
    expect(selectedId.value).toBe('b')
  })

  it('ignores a choice that is not a claude spawner and falls back to the first', () => {
    const spawners = ref([makeSpawner('x', 'ollama'), makeSpawner('a', 'claude')])
    const { selectedId } = useClaudeSpawnerPicker(spawners)

    selectedId.value = 'x'
    expect(selectedId.value).toBe('a')
  })

  it('is empty without a claude spawner and follows the list when it changes', () => {
    const spawners = ref([makeSpawner('x', 'ollama')])
    const { selectedId } = useClaudeSpawnerPicker(spawners)
    expect(selectedId.value).toBe('')

    spawners.value = [makeSpawner('a', 'claude')]
    expect(selectedId.value).toBe('a')
  })
})
