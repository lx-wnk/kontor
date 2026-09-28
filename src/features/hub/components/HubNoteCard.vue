<script setup lang="ts">
import type { HubNote } from '../composables/useObsidianGraph'
import { computed, onMounted, ref } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import { formatRelativeThenDate } from '@/utils/format'
import { useObsidianGraph } from '../composables/useObsidianGraph'

const props = defineProps<{
  note: HubNote
  notes: ReadonlyArray<HubNote>
  sectorLabel: string
  // Why Kontor cannot be asked, straight from the hub; undefined means it can.
  kontorBlocked?: string
}>()

const emit = defineEmits<{ fly: [index: number], ask: [prefill: string], close: [] }>()

const CHIP_LIMIT = 8
const MD_EXTENSION = /\.md$/
const { openInObsidian } = useObsidianGraph()
const openError = ref<string | null>(null)
const opening = ref(false)
const panel = ref<HTMLElement | null>(null)

onMounted(() => panel.value?.querySelector('button')?.focus())

const changed = computed(() => formatRelativeThenDate(new Date(props.note.mtimeMs).toISOString()))
const chipGroups = computed(() => [
  { kind: 'link', label: 'Links', notes: props.note.links.slice(0, CHIP_LIMIT).map(i => props.notes[i]) },
  { kind: 'backlink', label: 'Backlinks', notes: props.note.backlinks.slice(0, CHIP_LIMIT).map(i => props.notes[i]) },
].filter(g => g.notes.length > 0))

function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? '' : 's'}`
}

function askKontor() {
  emit('ask', `[[${props.note.path.replace(MD_EXTENSION, '')}]] `)
}

async function openNote() {
  opening.value = true
  try {
    openError.value = await openInObsidian(props.note.path)
  }
  finally {
    opening.value = false
  }
}
</script>

<template>
  <div
    ref="panel"
    data-hub-layer
    role="dialog"
    :aria-label="note.title"
    class="absolute right-[50px] top-[92px] z-30 w-[270px] rounded-[10px] border border-line-strong bg-card px-3 py-2.5 shadow-lg"
  >
    <button type="button" aria-label="Close card" class="absolute right-2 top-1.5 cursor-pointer text-fg-mute hover:text-fg" @click="emit('close')">
      ✕
    </button>
    <h4 class="mb-0.5 pr-5 text-[13px] font-semibold text-fg">
      {{ note.title }}
    </h4>
    <p data-testid="hub-note-path" class="mb-1.5 break-all font-mono text-[10px] text-fg-faint">
      {{ note.path }}
    </p>
    <p class="text-[12px] text-fg-mute">
      {{ sectorLabel }}
    </p>
    <p data-testid="hub-note-meta" class="text-[12px] text-fg-mute">
      changed {{ changed }} · {{ count(note.links.length, 'link') }} · {{ count(note.backlinks.length, 'backlink') }}
    </p>
    <div v-for="group in chipGroups" :key="group.kind" role="group" :aria-label="group.label" class="my-1.5 flex flex-wrap items-center gap-1">
      <span class="text-[10.5px] text-fg-faint">{{ group.label }}</span>
      <button
        v-for="target in group.notes"
        :key="target.index"
        type="button"
        :data-testid="`hub-note-${group.kind}`"
        class="cursor-pointer rounded-full border border-line-strong px-[7px] text-[10.5px] text-fg-soft hover:bg-raised"
        @click="emit('fly', target.index)"
      >
        {{ target.title }}
      </button>
    </div>
    <div class="mt-2 flex flex-wrap gap-1.5">
      <AppButton
        variant="primary"
        size="sm"
        :disabled="!!kontorBlocked"
        :title="kontorBlocked"
        @click="askKontor"
      >
        Ask Kontor about this
      </AppButton>
      <AppButton size="sm" :disabled="opening" @click="openNote">
        Open in Obsidian
      </AppButton>
    </div>
    <p v-if="openError" role="alert" class="mt-1.5 text-[12px] text-danger-text">
      {{ openError }}
    </p>
  </div>
</template>
