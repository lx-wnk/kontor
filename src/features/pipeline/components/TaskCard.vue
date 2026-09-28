<script setup lang="ts">
import type { Agent, PipelineStage, PipelineTask, Project, Spawner } from '@/types'
import { useIntervalFn } from '@vueuse/core'
import { computed, ref } from 'vue'
import AppBadge from '@/components/ui/AppBadge.vue'
import AppCard from '@/components/ui/AppCard.vue'
import AppChip from '@/components/ui/AppChip.vue'
import WorktreePill from '@/components/WorktreePill.vue'
import { shortId, useCopyId } from '@/composables/useCopyId'
import { useAgentIdentity } from '@/features/agents'
import { usePipelineConfig } from '@/features/pipeline/composables/usePipelineConfig'
import { PluginSlot } from '@/features/plugins'
import { formatCost, formatDuration } from '@/utils/format'
import { secondsUntil } from '@/utils/retryCountdown'
import { STAGE_LABELS } from '@/utils/stageLabels'
import { agentStatusTone, runStatusLabel, runStatusTone, stageTone } from '@/utils/statusColors'

const props = withDefaults(defineProps<{
  task: PipelineTask
  project?: Project | null
  spawner?: Spawner | null
  workingAgent?: Agent | null
  sortable?: boolean
  isFirst?: boolean
  isLast?: boolean
}>(), {
  sortable: true,
  isFirst: false,
  isLast: false,
})
const emit = defineEmits<{
  select: [task: PipelineTask]
  openChat: [task: PipelineTask]
  navigateAgent: [sessionId: string]
  moveUp: [task: PipelineTask]
  moveDown: [task: PipelineTask]
}>()

const { getIdentity } = useAgentIdentity()

const agentIdentity = computed(() =>
  props.workingAgent ? getIdentity(props.workingAgent.projectPath) : null,
)

const agentBadgeVariant = computed(() => {
  const tone = agentStatusTone(props.workingAgent?.status ?? '')
  if (tone === 'success')
    return 'active'
  if (tone === 'warning')
    return 'waiting'
  return 'idle'
})

const { copy: copyId, copied: idCopied } = useCopyId(props.task.id)

function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function stageLabel(stage: PipelineStage): string {
  return STAGE_LABELS[stage] || stage
}

const { maxAutoRetries } = usePipelineConfig()

const retrySecondsLeft = ref(0)

function refreshCountdown() {
  retrySecondsLeft.value = secondsUntil(props.task.nextRetryAt)
}

const isRequeued = computed(() => props.task.autoRetryCount != null)
const prError = computed(() => {
  const value = props.task.metadata?.pr_error
  return typeof value === 'string' && value !== '' ? value : null
})

useIntervalFn(refreshCountdown, 1000, { immediate: true })

const activeChildOutputExpanded = ref(false)
</script>

<template>
  <AppCard
    surface="app"
    radius="md"
    :interactive="!task.isBlocked"
    lift
    class="relative px-3 py-2.5 cursor-pointer flex flex-col gap-1.5"
    :class="task.isBlocked ? 'opacity-60 hover:opacity-85' : ''"
  >
    <button
      type="button"
      class="absolute inset-0 w-full h-full rounded-md focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-inset focus-visible:ring-accent"
      :aria-label="`Open task ${task.title}`"
      data-testid="task-card-open"
      @click="$emit('select', task)"
    />
    <div class="flex justify-between items-baseline gap-2">
      <span class="flex items-center gap-1 overflow-hidden">
        <span
          v-if="props.sortable"
          class="task-drag-handle relative z-10 cursor-grab active:cursor-grabbing text-fg-mute hover:text-fg-soft select-none leading-none -ml-0.5"
          title="Drag to reorder"
          aria-hidden="true"
          @click.stop
        >⠿</span>
        <span v-if="props.sortable" class="relative z-10 flex items-center gap-0.5 -ml-1">
          <button
            type="button"
            class="inline-flex items-center justify-center min-w-6 min-h-6 text-[9px] leading-none text-fg-mute hover:text-fg-soft disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:text-fg-mute focus-visible:outline-none focus-visible:ring-[2px] focus-visible:ring-accent rounded"
            :aria-label="`Move task ${task.title} up`"
            :disabled="isFirst"
            data-testid="task-move-up"
            @click.stop="emit('moveUp', task)"
          >▲</button>
          <button
            type="button"
            class="inline-flex items-center justify-center min-w-6 min-h-6 text-[9px] leading-none text-fg-mute hover:text-fg-soft disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:text-fg-mute focus-visible:outline-none focus-visible:ring-[2px] focus-visible:ring-accent rounded"
            :aria-label="`Move task ${task.title} down`"
            :disabled="isLast"
            data-testid="task-move-down"
            @click.stop="emit('moveDown', task)"
          >▼</button>
        </span>
        <span class="font-mono text-[11px] text-info-text font-semibold overflow-hidden text-ellipsis whitespace-nowrap">{{ task.slug }}</span>
        <button
          type="button"
          class="relative z-10 font-mono text-[10px] px-1 py-px rounded border bg-raised text-fg-mute border-line hover:text-fg-soft hover:border-fg-mute transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent flex-shrink-0"
          :aria-label="`Copy task id ${task.id}`"
          :title="task.id"
          @click.stop.prevent="copyId()"
        >{{ idCopied ? 'copied' : `#${shortId(task.id)}` }}</button>
      </span>
      <span class="text-[10px] font-mono text-fg-mute">{{ shortDate(task.createdAt) }}</span>
    </div>
    <div class="text-[13px] font-semibold text-fg leading-tight line-clamp-2">
      {{ task.title }}
    </div>
    <div v-if="task.description" class="text-[11px] text-fg-mute leading-snug line-clamp-2">
      {{ task.description }}
    </div>
    <button
      v-if="task.currentStage === 'backlog'"
      class="relative z-10 self-start text-[11px] font-semibold px-2 py-0.5 rounded border border-info-line bg-info-soft text-info-text hover:brightness-105 transition-[filter] focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent"
      @click.stop="emit('openChat', task)"
      @keydown.enter.stop
      @keydown.space.stop
    >
      Continue Chat →
    </button>
    <!-- Project chip -->
    <div v-if="project" class="flex items-center gap-1">
      <span
        class="inline-flex items-center gap-1 text-[10px] font-semibold px-1.5 py-px rounded border border-transparent"
        :style="project.color ? { backgroundColor: `${project.color}22`, color: project.color, borderColor: `${project.color}55` } : {}"
        :class="!project.color ? 'bg-raised text-fg-mute border-line' : ''"
        :title="`Project: ${project.name}`"
      >
        <span aria-hidden="true">◫</span>{{ project.name }}
      </span>
    </div>
    <button
      v-if="workingAgent && agentIdentity"
      type="button"
      data-testid="task-agent-chip"
      class="relative z-10 self-start flex items-center gap-1.5 px-1.5 py-px rounded border border-line bg-raised hover:bg-card transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent text-left"
      :aria-label="`Jump to agent: ${workingAgent.projectName}`"
      @click.stop="emit('navigateAgent', workingAgent.sessionId)"
    >
      <span class="text-[11px] leading-none" aria-hidden="true">{{ agentIdentity.emoji }}</span>
      <AppBadge :variant="agentBadgeVariant" />
      <span class="font-mono text-[10px] text-fg-soft truncate max-w-[120px]">{{ workingAgent.projectName }}</span>
    </button>
    <div
      v-if="(task.childCount ?? 0) > 0 && task.activeChild"
      data-testid="active-child-block"
      class="relative z-10 rounded-md border border-line bg-raised px-2 py-1.5 flex flex-col gap-1"
      @click.stop
    >
      <div class="flex items-center justify-between gap-2">
        <span class="text-[10px] font-semibold text-fg-mute">
          {{ task.activeChildCount ?? 0 }}/{{ task.childCount }} active subtask{{ (task.childCount ?? 0) !== 1 ? 's' : '' }}
        </span>
        <span class="text-[10px] font-mono text-fg-mute whitespace-nowrap">
          {{ formatDuration(task.activeChild.durationSeconds) }} · {{ Math.round(task.activeChild.tokensUsed / 1000) }}k tok · {{ formatCost(task.activeChild.costCents / 100) }}
        </span>
      </div>
      <div class="flex items-center gap-1.5 flex-wrap">
        <span class="text-[11px] font-mono text-info-text">●</span>
        <span class="font-mono text-[11px] text-fg-soft">{{ STAGE_LABELS[task.activeChild.currentStage as keyof typeof STAGE_LABELS] ?? task.activeChild.currentStage }}</span>
      </div>
      <div v-if="task.activeChild.latestOutput" class="flex items-start gap-1">
        <span
          class="font-mono text-[11px] text-fg-mute leading-snug"
          :class="activeChildOutputExpanded ? 'whitespace-pre-wrap break-words' : 'truncate'"
          data-testid="active-child-latest-output"
        >{{ task.activeChild.latestOutput }}</span>
        <button
          type="button"
          class="inline-flex items-center justify-center min-w-6 min-h-6 flex-shrink-0 text-[10px] text-fg-mute hover:text-fg-soft focus-visible:outline-none focus-visible:ring-[2px] focus-visible:ring-accent rounded"
          :aria-label="activeChildOutputExpanded ? 'Collapse child output' : 'Expand child output'"
          data-testid="active-child-expand-toggle"
          @click="activeChildOutputExpanded = !activeChildOutputExpanded"
        >
          {{ activeChildOutputExpanded ? '▲' : '▼' }}
        </button>
      </div>
    </div>
    <div class="flex flex-wrap gap-1 mt-0.5">
      <AppChip :tone="stageTone(task.currentStage)" mono>
        {{ stageLabel(task.currentStage) }}
      </AppChip>
      <AppChip
        v-if="task.latestStageRunStatus"
        :tone="runStatusTone(task.latestStageRunStatus)"
        mono
        uppercase
        :title="`Latest stage run: ${runStatusLabel(task.latestStageRunStatus)}`"
      >
        {{ runStatusLabel(task.latestStageRunStatus) }}
      </AppChip>
      <AppChip
        v-if="isRequeued"
        tone="info"
        mono
        uppercase
        :title="`Auto-retry queued (attempt ${task.autoRetryCount} of ${maxAutoRetries})`"
      >
        Retrying · {{ task.autoRetryCount }}/{{ maxAutoRetries }}{{ retrySecondsLeft > 0 ? ` · ${retrySecondsLeft}s` : '' }}
      </AppChip>
      <AppChip
        v-if="task.needsUser && task.latestStageRunStatus === 'awaiting_user'"
        tone="warning"
        mono
        uppercase
        title="Agent is paused and waiting for a permission grant"
      >
        ⚠ Needs Permission
      </AppChip>
      <AppChip
        v-if="task.blockedByPendingPermissions"
        tone="warning"
        title="Respawn blocked: previous run still has unresolved permission requests"
      >
        &#9888; blocked by permissions
      </AppChip>
      <WorktreePill v-if="task.worktreePath" :task-id="task.id" @open="$emit('select', task)" @click.stop />
      <AppChip v-if="task.sourceBranch" tone="neutral" mono>
        {{ task.sourceBranch }}
      </AppChip>
      <a
        v-if="task.draftPrUrl"
        :href="task.draftPrUrl"
        target="_blank"
        rel="noopener"
        class="relative z-10"
        @click.stop
      >
        <AppChip tone="success" mono>
          PR #{{ task.draftPrNumber }}
        </AppChip>
      </a>
      <AppChip v-if="prError" tone="warning" :title="prError" data-testid="task-card-pr-error">
        &#9888; PR not created
      </AppChip>
      <AppChip v-if="task.parentTaskId" tone="info" mono title="Follow-up task">
        ↳
      </AppChip>
      <AppChip v-if="task.isUnsatisfiable" tone="warning" mono title="Unsatisfiable dep">
        ⚠ Unsatisfiable dep
      </AppChip>
      <AppChip v-else-if="task.isBlocked" tone="neutral" mono title="Waiting for prerequisite">
        🔒 Blocked
      </AppChip>
      <AppChip v-if="task.currentStage === 'implementation'" tone="warning" mono>
        max iter {{ task.maxIterations }}
      </AppChip>
      <PluginSlot name="kanban-card-badge" :ctx="{ task }" />
    </div>
  </AppCard>
</template>
