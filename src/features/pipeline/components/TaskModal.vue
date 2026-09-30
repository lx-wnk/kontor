<script setup lang="ts">
import type { Agent, PipelineTask } from '@/types'
import { useIntervalFn } from '@vueuse/core'
import { computed, provide, ref, watch } from 'vue'
import AuditLogTab from '@/components/AuditLogTab.vue'
import AppChip from '@/components/ui/AppChip.vue'
import AppModal from '@/components/ui/AppModal.vue'
import { useCheckpoints } from '@/composables/useCheckpoints'
import { useCopyId } from '@/composables/useCopyId'
import { useRovingTabList } from '@/composables/useRovingTabList'
import CheckpointTimeline from '@/features/pipeline/components/task/CheckpointTimeline.vue'
import CoordinationTab from '@/features/pipeline/components/task/CoordinationTab.vue'
import TaskCostTab from '@/features/pipeline/components/task/TaskCostTab.vue'
import TaskDependenciesTab from '@/features/pipeline/components/task/TaskDependenciesTab.vue'
import TaskFooter from '@/features/pipeline/components/task/TaskFooter.vue'
import TaskOverviewTab from '@/features/pipeline/components/task/TaskOverviewTab.vue'
import TaskPermissionsTab from '@/features/pipeline/components/task/TaskPermissionsTab.vue'
import TaskStagesTab from '@/features/pipeline/components/task/TaskStagesTab.vue'
import { TaskActionsKey, TaskDetailsKey, TaskRefKey } from '@/features/pipeline/composables/taskModalContext'
import { usePipelineConfig } from '@/features/pipeline/composables/usePipelineConfig'
import { useTaskActions } from '@/features/pipeline/composables/useTaskActions'
import { useTaskDetails } from '@/features/pipeline/composables/useTaskDetails'
import { PluginSlot } from '@/features/plugins'
import { secondsUntil } from '@/utils/retryCountdown'
import { STAGE_LABELS } from '@/utils/stageLabels'
import { stageTone } from '@/utils/statusColors'

const props = defineProps<{ task: PipelineTask | null }>()
const emit = defineEmits<{ close: [], navigate: [agent: Agent], navigateTask: [taskId: string], openChat: [task: PipelineTask] }>()

const task = computed(() => props.task)
const details = useTaskDetails(task)
const actions = useTaskActions(task, details)
provide(TaskRefKey, task)
provide(TaskDetailsKey, details)
provide(TaskActionsKey, actions)

const { stageRuns, permissions, isFailedRun } = details

const { copy: copyTaskId, copied: modalCopiedId } = useCopyId(() => props.task?.id ?? '')

const { retryChip } = usePipelineConfig()
const modalRetrySecondsLeft = ref(0)
useIntervalFn(() => {
  modalRetrySecondsLeft.value = secondsUntil(props.task?.nextRetryAt)
}, 1000, { immediate: true })
const modalRetryChipInfo = computed(() => task.value?.autoRetryCount == null
  ? null
  : retryChip(task.value.latestStageRunStatus, task.value.autoRetryCount, modalRetrySecondsLeft.value))

const TABS = ['overview', 'stages', 'cost', 'permissions', 'dependencies', 'audit', 'coordination', 'checkpoints'] as const
const TAB_LABELS: Record<typeof TABS[number], string> = {
  overview: 'Overview',
  stages: 'Stages',
  cost: 'Cost',
  permissions: 'Permissions',
  dependencies: 'Dependencies',
  audit: 'Audit',
  coordination: 'Coordination',
  checkpoints: 'Checkpoints',
}

const { checkpoints, loading: checkpointsLoading, reverting: checkpointReverting, revert: revertCheckpoint } = useCheckpoints(computed(() => task.value?.id ?? null))
const { activeTab, tabAttrs, panelAttrs, onKeydown, select } = useRovingTabList(TABS, { idPrefix: 'task-modal', initial: 'overview' })

function tabLabel(key: typeof TABS[number]): string {
  if (key === 'stages')
    return `Stages (${stageRuns.value.length})`
  if (key === 'permissions')
    return `Permissions (${permissions.value.length})`
  return TAB_LABELS[key]
}

// Reset to the first tab whenever a different task is opened.
watch(() => props.task?.id, (id, prevId) => {
  if (id && id !== prevId)
    select('overview')
})
</script>

<template>
  <AppModal :open="!!task" :z-index="1000" :labelled-by="task ? `task-modal-title-${task.id}` : undefined" @close="emit('close')">
    <template v-if="task">
      <header class="flex items-center justify-between px-5 py-4 border-b border-line">
        <div class="flex items-center gap-2.5 flex-wrap">
          <AppChip :tone="stageTone(task.currentStage ?? '')" mono uppercase>
            {{ task.currentStage ? (STAGE_LABELS[task.currentStage] ?? task.currentStage) : '' }}
          </AppChip>
          <AppChip v-if="isFailedRun" tone="danger" mono uppercase :bordered="false" class="ml-auto" title="Latest stage run failed">
            RUN FAILED
          </AppChip>
          <AppChip
            v-if="modalRetryChipInfo"
            tone="info"
            mono
            uppercase
            :bordered="false"
            :title="modalRetryChipInfo.title"
          >
            {{ modalRetryChipInfo.label }}
          </AppChip>
          <span class="font-mono text-xs text-info-text">{{ task.slug }}</span>
          <button
            type="button"
            class="font-mono text-[10px] px-1.5 py-px rounded border bg-raised text-fg-mute border-line hover:text-fg-soft hover:border-fg-mute transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent flex-shrink-0"
            :aria-label="`Copy task id ${task.id}`"
            :title="modalCopiedId ? 'Copied!' : task.id"
            @click="copyTaskId"
          >
            {{ modalCopiedId ? 'copied' : task.id }}
          </button>
          <h2 :id="`task-modal-title-${task.id}`" class="text-lg font-semibold text-fg">
            {{ task.title }}
          </h2>
        </div>
        <button type="button" aria-label="Close" class="bg-transparent border-none text-fg-mute text-2xl cursor-pointer px-1 leading-none hover:text-fg" title="Close (Esc)" @click="emit('close')">
          &times;
        </button>
      </header>

      <nav role="tablist" aria-label="Task details" class="flex border-b border-line flex-shrink-0" @keydown="onKeydown">
        <button
          v-for="key in TABS"
          :key="key"
          v-bind="tabAttrs(key)"
          type="button"
          class="px-4 py-2.5 text-xs font-semibold bg-transparent border-none border-b-2 border-transparent cursor-pointer hover:text-fg-soft transition-colors"
          :class="activeTab === key ? 'text-accent border-accent' : 'text-fg-mute'"
          @click="select(key)"
        >
          {{ tabLabel(key) }}
        </button>
      </nav>

      <div class="flex-1 min-h-0 overflow-y-auto" v-bind="panelAttrs(activeTab)">
        <TaskOverviewTab v-if="activeTab === 'overview'" @open-chat="t => emit('openChat', t)" />
        <TaskStagesTab v-else-if="activeTab === 'stages'" />
        <TaskCostTab v-else-if="activeTab === 'cost'" />
        <TaskPermissionsTab v-else-if="activeTab === 'permissions'" />
        <TaskDependenciesTab v-else-if="activeTab === 'dependencies'" @navigate-task="id => emit('navigateTask', id)" />
        <section v-else-if="activeTab === 'audit'" class="p-5">
          <AuditLogTab :task-id="task.id" />
        </section>
        <CoordinationTab v-else-if="activeTab === 'coordination'" />
        <CheckpointTimeline
          v-else-if="activeTab === 'checkpoints'"
          :task-id="task.id"
          :checkpoints="checkpoints"
          :loading="checkpointsLoading"
          :reverting="checkpointReverting"
          @revert="revertCheckpoint"
        />
      </div>

      <TaskFooter />
      <PluginSlot name="task-modal-footer" :ctx="{ task }" />
    </template>
  </AppModal>
</template>
