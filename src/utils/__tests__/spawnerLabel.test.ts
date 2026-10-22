import { describe, expect, it } from 'vitest'
import { staleSpawnerLabel } from '../spawnerLabel'

describe('staleSpawnerLabel', () => {
  it('keeps the first 8 characters of the id', () => {
    expect(staleSpawnerLabel('gone-1234-abcd')).toBe('Removed spawner (gone-123)')
  })

  it('keeps a short id whole', () => {
    expect(staleSpawnerLabel('abc')).toBe('Removed spawner (abc)')
  })
})
