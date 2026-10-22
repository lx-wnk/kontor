import { shortId } from '@/composables/useCopyId'

export function staleSpawnerLabel(id: string): string {
  return `Removed spawner (${shortId(id)})`
}
