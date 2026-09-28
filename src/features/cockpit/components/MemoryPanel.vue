<script setup lang="ts">
import type { PanelState } from '../panelState'
import { computed, onMounted } from 'vue'
import { toast } from '@/composables/useToast'
import { focusInHub, NO_HUB_PAGE_MESSAGE, useObsidianGraph } from '@/features/hub'
import { useResources } from '@/features/settings'
import { formatRelativeThenDate } from '@/utils/format'
import CockpitPanel from './CockpitPanel.vue'

// A fresh useResources per panel: it is not a singleton, and each panel names
// its kind here so the composable's mount fetch asks for it directly.
const { resources, loading, error, denied } = useResources('memory_space')
const { status: graphStatus, recentNotes, refresh: refreshGraph } = useObsidianGraph()

const RECENT_NOTE_COUNT = 5

// kind=memory_space gates on memory.read (api/resources/handler.go), so on a
// fresh install this panel is denied and must say so rather than reporting an
// empty store.
const state = computed<PanelState>(() => {
  if (loading.value)
    return 'loading'
  if (denied.value)
    return 'denied'
  if (error.value)
    return 'failed'
  return resources.value.length === 0 ? 'empty' : 'ready'
})

const recentTouched = computed(() => graphStatus.value === 'ready' ? recentNotes(RECENT_NOTE_COUNT) : [])

function touched(mtimeMs: number): string {
  return formatRelativeThenDate(new Date(mtimeMs).toISOString())
}

function openNote(path: string) {
  if (!focusInHub({ kind: 'note', path }))
    toast.info(NO_HUB_PAGE_MESSAGE)
}

// The 60s rule makes this free whenever the hub already fetched.
onMounted(() => refreshGraph())
</script>

<template>
  <CockpitPanel
    id="memory"
    title="Memory"
    icon="✎"
    :state="state"
    :message="denied ?? error ?? 'No memory space is defined in this scope.'"
  >
    <ul class="flex flex-col gap-1.5">
      <li v-for="r in resources.slice(0, 6)" :key="r.id" class="flex items-center justify-between gap-2 text-[12px] min-w-0" :data-testid="`cockpit-memoryspace-${r.id}`">
        <span class="truncate text-fg">{{ r.name }}</span>
        <span class="shrink-0 text-fg-mute">{{ r.state }}</span>
      </li>
    </ul>
    <template v-if="recentTouched.length > 0">
      <h3 class="mb-1 mt-3 text-[11px] font-semibold uppercase tracking-wider text-fg-mute">
        Recently touched
      </h3>
      <ul class="flex flex-col gap-1">
        <li v-for="note in recentTouched" :key="note.path">
          <button
            type="button"
            data-testid="cockpit-memory-recent-note"
            class="flex w-full cursor-pointer items-center justify-between gap-2 rounded-md px-1.5 py-1 text-left text-[12px] text-fg hover:bg-raised focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent"
            @click="openNote(note.path)"
          >
            <span class="truncate">{{ note.title }}</span>
            <span class="shrink-0 text-fg-mute">{{ touched(note.mtimeMs) }}</span>
          </button>
        </li>
      </ul>
    </template>
  </CockpitPanel>
</template>
