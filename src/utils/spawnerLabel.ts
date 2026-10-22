const STALE_ID_PREFIX_LENGTH = 8

export function staleSpawnerLabel(id: string): string {
  return `Removed spawner (${id.slice(0, STALE_ID_PREFIX_LENGTH)})`
}
