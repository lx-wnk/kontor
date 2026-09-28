<script setup lang="ts">
import type { WorkspacePage } from '../layout'
import { computed, ref } from 'vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { addTile, ZENTRALE_PAGE_ID } from '../layout'
import { widgetIds, WIDGETS } from '../widgetRegistry'

const props = defineProps<{ page: WorkspacePage, refusal: string | null }>()
const emit = defineEmits<{ change: [page: WorkspacePage], refuse: [reason: string], done: [], rename: [title: string], remove: [] }>()

const choice = ref('')
const confirmingDelete = ref(false)
const ownPage = computed(() => props.page.id !== ZENTRALE_PAGE_ID)
const addOptions = computed(() => [
  { value: '', label: 'Add a tile…', disabled: true },
  ...widgetIds().filter(id => !props.page.tiles.some(t => t.widget === id)).map(id => ({ value: id, label: WIDGETS[id].title })),
])

function add() {
  if (!choice.value)
    return
  const r = addTile(props.page, choice.value)
  if (r.ok)
    emit('change', r.value)
  else
    emit('refuse', r.reason)
  choice.value = ''
}

function rename(event: Event) {
  const input = event.target as HTMLInputElement
  confirmingDelete.value = false
  emit('rename', input.value)
  // A refused title reverts here; an accepted one re-renders with the stored title.
  input.value = props.page.title
}
</script>

<template>
  <div data-testid="workspace-edit-bar" class="flex flex-wrap items-center gap-2 rounded-lg border border-line bg-card px-3 py-2 text-[12.5px]">
    <label v-if="ownPage" class="flex items-center gap-1.5 font-medium text-fg">
      Editing
      <input data-testid="workspace-rename" :value="page.title" aria-label="Page title" class="rounded-md border border-line-strong bg-app px-2 py-1 font-normal" @input="confirmingDelete = false" @change="rename">
    </label>
    <span v-else class="font-medium text-fg">Editing {{ page.title }}</span>
    <span class="text-fg-mute">Arrows move · Shift+arrows resize · Delete removes</span>
    <span class="flex-grow" />
    <AppSelect v-model="choice" :options="addOptions" data-testid="workspace-add" aria-label="Tile to add" size="compact" />
    <button type="button" data-testid="workspace-add-submit" class="rounded-md border border-line-strong px-2.5 py-1" :disabled="!choice" @click="add">
      Add
    </button>
    <template v-if="ownPage">
      <template v-if="confirmingDelete">
        <button type="button" data-testid="workspace-delete-confirm" class="rounded-md bg-danger-soft px-2.5 py-1 text-danger-text" @click="emit('remove')">
          Delete {{ page.title }} and its tiles?
        </button>
        <button type="button" data-testid="workspace-delete-cancel" class="rounded-md border border-line-strong px-2.5 py-1" @click="confirmingDelete = false">
          Cancel
        </button>
      </template>
      <button v-else type="button" data-testid="workspace-delete-page" class="rounded-md border border-line-strong px-2.5 py-1" @click="confirmingDelete = true">
        Delete page
      </button>
    </template>
    <button type="button" data-testid="workspace-done" class="rounded-md bg-accent px-2.5 py-1 text-accent-contrast" @click="emit('done')">
      Done
    </button>
    <p data-testid="workspace-refusal" aria-live="polite" class="basis-full text-warning-text empty:hidden">
      {{ refusal ?? '' }}
    </p>
  </div>
</template>
