<script setup lang="ts">
import type { GraphStatus } from '../composables/useObsidianGraph'
import type { AgentNoteRow } from '../hubEdges'
import type { Launcher } from '../hubLaunchers'
import type { Agent, NoteTouchKind } from '@/types'
import type { AgentDisplayStatus } from '@/utils/statusColors'
import { computed, inject, onMounted, ref } from 'vue'
import AppChip from '@/components/ui/AppChip.vue'
import { OPEN_SETTINGS } from '@/composables/openTask'
import { formatRelativeThenDate } from '@/utils/format'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { agentStatusTone, statusLabel } from '@/utils/statusColors'
import { LIST_GRAPH_NOTICES } from '../hubGraphNotices'

const props = defineProps<{
  agents: ReadonlyArray<{ agent: Agent, state: AgentDisplayStatus, notes?: ReadonlyArray<AgentNoteRow> }>
  notes: ReadonlyArray<{ path: string, title: string, sector: string, mtimeMs: number }>
  graphStatus: GraphStatus
  graphMessage: string
  launchers: Launcher[]
}>()

const emit = defineEmits<{ agent: [agent: Agent], note: [path: string], launch: [launcher: Launcher], close: [] }>()

const openSettings = inject(OPEN_SETTINGS)
if (!openSettings)
  throw new Error('HubList requires OPEN_SETTINGS from App.vue')

const panel = ref<HTMLElement | null>(null)
onMounted(() => panel.value?.querySelector('button')?.focus())

function touched(mtimeMs: number): string {
  return formatRelativeThenDate(new Date(mtimeMs).toISOString())
}

const noteNotice = computed<string | null>(() => {
  if (props.graphStatus === 'ready' && props.notes.length > 0)
    return null
  return LIST_GRAPH_NOTICES[props.graphStatus] ?? null
})

const HEADING = 'mb-1 mt-3 text-[11px] font-semibold uppercase tracking-wider text-fg-mute'
const ROW = 'flex w-full cursor-pointer items-center justify-between gap-3 rounded-md px-2 py-1 text-left text-[13px] text-fg hover:bg-raised'
const TOUCH_VERB: Record<NoteTouchKind, string> = { read: 'read', write: 'wrote' }
</script>

<template>
  <div
    ref="panel"
    data-hub-layer
    role="dialog"
    aria-label="Zentrale as a list"
    class="absolute inset-x-[50px] bottom-[50px] top-[60px] z-20 overflow-auto rounded-[10px] border border-line-strong bg-card px-3.5 py-2.5 shadow-lg"
  >
    <div class="flex items-center justify-between gap-3">
      <span class="text-[12px] text-fg-mute">Zentrale as a list — the same content, for keyboard and screen readers</span>
      <button type="button" aria-label="Close list" class="cursor-pointer rounded px-1.5 text-fg-mute hover:text-fg" @click="emit('close')">
        ✕
      </button>
    </div>

    <h3 :class="HEADING">
      Agents
    </h3>
    <p v-if="agents.length === 0" class="px-2 text-[13px] text-fg-mute">
      No agents running.
    </p>
    <div v-for="{ agent, state, notes: agentNotes } in agents" :key="agent.pid" role="group" :aria-labelledby="`hub-list-agent-${agent.pid}`">
      <button
        :id="`hub-list-agent-${agent.pid}`"
        type="button"
        data-testid="hub-list-agent"
        :aria-label="`${friendlyProjectName(agent.projectName)}, ${statusLabel(state)}`"
        :class="ROW"
        @click="emit('agent', agent)"
      >
        <span>{{ friendlyProjectName(agent.projectName) }}</span>
        <AppChip :tone="agentStatusTone(state)">
          {{ statusLabel(state) }}
        </AppChip>
      </button>
      <button
        v-for="n in agentNotes ?? []"
        :key="`${n.kind}:${n.path}`"
        type="button"
        data-testid="hub-list-agent-note"
        :aria-label="`${n.title}, ${TOUCH_VERB[n.kind]} ${formatRelativeThenDate(n.at)}`"
        :class="`${ROW} pl-6`"
        @click="emit('note', n.path)"
      >
        <span>{{ n.title }}</span>
        <span class="text-fg-mute">{{ TOUCH_VERB[n.kind] }} · {{ formatRelativeThenDate(n.at) }}</span>
      </button>
    </div>

    <h3 :class="HEADING">
      Recently touched
    </h3>
    <div
      v-if="noteNotice"
      data-testid="hub-list-note-notice"
      :title="graphStatus === 'denied' ? graphMessage : undefined"
      class="flex flex-col gap-0.5 px-2 text-[13px] text-fg-mute"
    >
      <p class="flex items-center gap-2">
        {{ noteNotice }}
        <button v-if="graphStatus === 'unconfigured'" type="button" class="cursor-pointer text-accent underline-offset-2 hover:underline" @click="openSettings()">
          Open settings
        </button>
      </p>
      <p v-if="graphStatus === 'denied'" data-testid="hub-list-note-notice-detail" class="text-[11px] text-fg-faint">
        {{ graphMessage }}
      </p>
    </div>
    <button
      v-for="note in notes"
      :key="note.path"
      type="button"
      data-testid="hub-list-note"
      :aria-label="`${note.title}, ${note.sector}, ${touched(note.mtimeMs)}`"
      :class="ROW"
      @click="emit('note', note.path)"
    >
      <span>{{ note.title }}</span>
      <span class="text-fg-mute">{{ note.sector }} · {{ touched(note.mtimeMs) }}</span>
    </button>

    <h3 :class="HEADING">
      Go to
    </h3>
    <button
      v-for="launcher in launchers"
      :key="launcher.id"
      type="button"
      :class="ROW"
      @click="emit('launch', launcher)"
    >
      <span><span aria-hidden="true" class="mr-2 text-fg-mute">{{ launcher.icon }}</span>{{ launcher.label }}</span>
    </button>
  </div>
</template>
