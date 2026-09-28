<script setup lang="ts">
import type { PipelineStage, PipelineTask, TaskAutonomy } from '@/types'
import { computed, ref, watch } from 'vue'
import GitStatusPanel from '@/components/GitStatusPanel.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppChip from '@/components/ui/AppChip.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import WorktreeCommandRunner from '@/components/WorktreeCommandRunner.vue'
import WorktreePanel from '@/components/WorktreePanel.vue'
import { runAction } from '@/composables/useRunAction'
import { toast } from '@/composables/useToast'
import { AgentChatStream } from '@/features/agents'
import RefineStatusPanel from '@/features/pipeline/components/RefineStatusPanel.vue'
import StageOutputView from '@/features/pipeline/components/StageOutputView.vue'
import TaskPendingRequests from '@/features/pipeline/components/task/TaskPendingRequests.vue'
import { useInjectedTask, useInjectedTaskDetails } from '@/features/pipeline/composables/taskModalContext'
import { completedPhasesFromTurns, fetchRefineTurns, lastAssistantContent } from '@/features/pipeline/composables/useRefinementChat'
import { useTaskAssignment } from '@/features/pipeline/composables/useTaskAssignment'
import { refreshTask } from '@/features/pipeline/composables/useTasks'
import { formatDateTime } from '@/utils/format'
import { STAGE_LABELS } from '@/utils/stageLabels'
import { runStatusLabel, runStatusTone } from '@/utils/statusColors'
import { activeRuntime, formatCents, taskRuntime } from '@/utils/taskFormat'
import { TASK_AUTONOMY_OPTIONS } from '@/utils/taskOptions'

const emit = defineEmits<{ openChat: [task: PipelineTask] }>()

const task = useInjectedTask()

// Worktree create/remove changes task.worktreePath; refetch so the panel and
// editor link reflect it immediately instead of waiting for the next SSE push.
async function onWorktreeChange(): Promise<void> {
  if (task.value)
    await refreshTask(task.value.id)
}

const {
  stageRuns,
  pendingRequests,
  latestStageRun,
  latestRunError,
  latestRunAgentMessage,
  sessionAgentText,
  sessionAgentTextLoading,
  pipelineAgent,
  totalTokensUsed,
  totalCostCents,
} = useInjectedTaskDetails()
const {
  projects,
  spawners,
  currentProject,
  effectiveSpawner,
  isAssigningProject,
  isAssigningSpawner,
  assignError,
  onProjectChange,
  onSpawnerChange,
} = useTaskAssignment(task)

// Surface project/spawner assignment failures as toasts.
watch(assignError, (msg) => {
  if (msg)
    toast.error(msg)
})

const runtime = computed(() => (task.value ? taskRuntime(task.value) : '—'))
const active = computed(() => activeRuntime(stageRuns.value))

async function onAutonomyChange(autonomy: TaskAutonomy): Promise<void> {
  if (!task.value)
    return
  try {
    const res = await fetch(`/api/tasks/${task.value.id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ autonomy }),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
      toast.error((err as { error?: string }).error || 'Failed to update autonomy')
    }
  }
  catch {
    toast.error('Failed to update autonomy')
  }
}

const projectAssignOptions = computed(() => [
  { value: '', label: 'None' },
  ...projects.value.map(p => ({ value: p.id, label: p.name })),
])

const spawnerAssignOptions = computed(() => [
  { value: '', label: currentProject.value?.defaultSpawnerId ? 'Project default' : 'Default' },
  ...spawners.value.map(s => ({ value: s.id, label: `${s.name}${s.builtIn ? ' (built-in)' : ''}` })),
])

// The list fetch that populates the injected task strips metadata (it can run
// into tens of KB per task); spec/plan are only ever needed here, in the
// detail view, so fetch the single task on its own instead.
const conceptSpec = ref<string | null>(null)
const conceptPlan = ref<string | null>(null)
const hasConcept = computed(() => !!conceptSpec.value || !!conceptPlan.value)

async function loadConcept(taskId: string): Promise<void> {
  try {
    const res = await fetch(`/api/tasks/${taskId}`)
    if (!res.ok) {
      conceptSpec.value = null
      conceptPlan.value = null
      return
    }
    const full = await res.json() as PipelineTask
    conceptSpec.value = typeof full.metadata?.spec === 'string' ? full.metadata.spec : null
    conceptPlan.value = typeof full.metadata?.plan === 'string' ? full.metadata.plan : null
  }
  catch {
    conceptSpec.value = null
    conceptPlan.value = null
  }
}

watch(
  () => task.value?.id,
  (id) => {
    if (id) {
      void loadConcept(id)
    }
    else {
      conceptSpec.value = null
      conceptPlan.value = null
    }
  },
  { immediate: true },
)
const showApproveSpec = computed(() => task.value?.refineStatus === 'draft_ready')

const isApprovingSpec = ref(false)

async function onApproveSpec(): Promise<void> {
  if (!task.value)
    return
  isApprovingSpec.value = true
  try {
    await runAction(task.value.id, 'approve_spec')
  }
  catch (e) {
    toast.error((e as Error).message)
  }
  finally {
    isApprovingSpec.value = false
  }
}

const lastRefineOutput = ref('')
const completedRefinePhases = ref<string[]>([])

async function loadLastRefineOutput(taskId: string): Promise<void> {
  try {
    const turns = await fetchRefineTurns(taskId)
    lastRefineOutput.value = lastAssistantContent(turns)
    completedRefinePhases.value = completedPhasesFromTurns(turns)
  }
  catch { /* leave empty */ }
}

watch(
  () => [task.value?.id, task.value?.currentStage, task.value?.refineStatus] as const,
  ([id, stage]) => {
    if (id && stage === 'backlog')
      void loadLastRefineOutput(id)
  },
  { immediate: true },
)
</script>

<template>
  <section v-if="task" class="flex-1 overflow-y-auto p-5 flex flex-col gap-4 min-h-0">
    <!-- Lingering-pending gate: orchestrator refuses to respawn while pendings linger. -->
    <div
      v-if="task.blockedByPendingPermissions"
      class="bg-orange-50 dark:bg-orange-950/30 border border-orange-300 dark:border-orange-700/60 rounded-md px-3.5 py-3"
    >
      <h3 class="text-[11px] uppercase tracking-[0.5px] text-orange-700 dark:text-orange-400 font-semibold mb-1.5 flex items-center gap-1.5">
        <span aria-hidden="true">⏸</span> Respawn blocked
      </h3>
      <p class="text-[12px] text-fg-soft leading-relaxed">
        The previous stage run still has unresolved permission requests. Resolve them below — the orchestrator will spawn a fresh run automatically once the queue is clear.
      </p>
    </div>

    <div
      v-if="pendingRequests.length > 0"
      class="bg-yellow-50 dark:bg-yellow-950/30 border border-yellow-300 dark:border-yellow-700/60 rounded-md px-3.5 py-3"
    >
      <h3 class="text-[11px] uppercase tracking-[0.5px] text-yellow-700 dark:text-yellow-400 font-semibold mb-2 flex items-center gap-1.5">
        <span aria-hidden="true">⚠</span> Waiting for Permission ({{ pendingRequests.length }})
      </h3>
      <TaskPendingRequests />
    </div>

    <template v-if="task.currentStage === 'backlog'">
      <RefineStatusPanel
        :status="task.refineStatus ?? 'none'"
        :error="task.refineError ?? null"
        :last-output="lastRefineOutput"
        :completed-phases="completedRefinePhases"
        class="mb-2"
      />
      <div class="flex items-center justify-between gap-3 bg-blue-50 dark:bg-blue-950/30 border border-blue-200 dark:border-blue-800 rounded-md px-3.5 py-3 mb-2">
        <div class="flex flex-col gap-0.5">
          <span class="text-xs font-semibold text-blue-700 dark:text-blue-300">This ticket is waiting for refinement</span>
          <span class="text-[11px] text-blue-600/80 dark:text-blue-400/80">Continue the conversation with the refinement assistant</span>
        </div>
        <AppButton variant="primary" size="sm" @click="emit('openChat', task)">
          Continue Chat →
        </AppButton>
      </div>
    </template>

    <dl class="grid grid-cols-[auto_1fr] gap-y-1.5 gap-x-4 text-[13px] mb-2">
      <div class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          CWD
        </dt><dd class="font-mono text-xs text-fg truncate">
          {{ task.cwd }}
        </dd>
      </div>
      <div v-if="task.sourceBranch" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Source
        </dt><dd class="font-mono text-xs text-fg">
          {{ task.sourceBranch }}
        </dd>
      </div>
      <div v-if="task.targetBranch" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Target
        </dt><dd class="font-mono text-xs text-fg">
          {{ task.targetBranch }}
        </dd>
      </div>
      <div class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Iter
        </dt><dd class="text-fg">
          {{ task.currentIteration ?? 0 }} / {{ task.maxIterations }}
        </dd>
      </div>
      <div class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Created
        </dt><dd class="text-fg">
          {{ formatDateTime(task.createdAt) }}
        </dd>
      </div>
      <div class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Runtime
        </dt><dd class="text-fg font-mono text-xs">
          {{ runtime }}
        </dd>
      </div>
      <div v-if="active !== '—'" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Active Runtime
        </dt><dd class="text-fg font-mono text-xs">
          {{ active }}
        </dd>
      </div>
      <div v-if="totalTokensUsed > 0" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Tokens
        </dt><dd class="text-fg font-mono text-xs">
          {{ totalTokensUsed.toLocaleString() }}
        </dd>
      </div>
      <div v-if="totalCostCents > 0" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Cost
        </dt><dd class="text-fg font-mono text-xs">
          {{ formatCents(totalCostCents) }}
        </dd>
      </div>
      <div v-if="task.tokenBudget" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Token Budget
        </dt><dd class="text-fg">
          {{ task.tokenBudget.toLocaleString() }}
        </dd>
      </div>
      <div v-if="task.parentTaskId" class="contents">
        <dt class="text-fg-mute text-[11px] uppercase tracking-[0.5px]">
          Parent
        </dt><dd class="font-mono text-xs text-fg">
          {{ task.parentTaskId }}
        </dd>
      </div>
    </dl>

    <section class="border-t border-line pt-3 flex flex-col gap-1.5">
      <label class="text-[11px] uppercase tracking-[0.5px] text-fg-mute font-semibold" :for="`task-modal-autonomy-${task.id}`">
        Autonomy
      </label>
      <AppSelect
        :id="`task-modal-autonomy-${task.id}`"
        :model-value="task.autonomy ?? 'spec_gated'"
        :options="TASK_AUTONOMY_OPTIONS"
        data-testid="task-autonomy-select"
        size="compact"
        @update:model-value="onAutonomyChange"
      />
    </section>

    <section v-if="hasConcept" class="border-t border-line pt-3 flex flex-col gap-2" data-testid="concept-viewer">
      <div class="flex items-center justify-between gap-3">
        <h4 class="text-[11px] font-semibold uppercase tracking-[0.5px] text-fg-mute">
          Concept
        </h4>
        <div v-if="showApproveSpec" class="flex items-center gap-2">
          <AppButton
            variant="primary"
            size="sm"
            :disabled="isApprovingSpec"
            data-testid="approve-spec-btn"
            @click="onApproveSpec"
          >
            {{ isApprovingSpec ? 'Approving…' : 'Approve Spec' }}
          </AppButton>
        </div>
      </div>
      <div v-if="conceptSpec">
        <div class="text-[10px] uppercase tracking-[0.5px] text-fg-mute mb-1">
          Spec
        </div>
        <pre class="font-mono text-[11px] bg-card rounded px-3 py-2.5 whitespace-pre-wrap break-words max-h-[300px] overflow-y-auto text-fg leading-relaxed">{{ conceptSpec }}</pre>
      </div>
      <div v-if="conceptPlan">
        <div class="text-[10px] uppercase tracking-[0.5px] text-fg-mute mb-1">
          Plan
        </div>
        <pre class="font-mono text-[11px] bg-card rounded px-3 py-2.5 whitespace-pre-wrap break-words max-h-[300px] overflow-y-auto text-fg leading-relaxed">{{ conceptPlan }}</pre>
      </div>
    </section>

    <WorktreePanel
      :task-id="task.id"
      :worktree-path="task.worktreePath ?? null"
      :active="!!task"
      @change="onWorktreeChange"
    />

    <section class="border-t border-line pt-3 flex flex-col gap-2.5">
      <h4 class="text-[11px] font-semibold uppercase tracking-[0.5px] text-fg-mute">
        Project &amp; Spawner
      </h4>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="block text-[10px] uppercase tracking-[0.5px] text-fg-mute mb-1" :for="`task-modal-project-${task.id}`">
            Project
          </label>
          <div class="flex items-center gap-2">
            <span
              v-if="currentProject"
              class="inline-flex items-center gap-1 text-[10px] font-semibold px-1.5 py-px rounded border border-transparent flex-shrink-0"
              :style="currentProject.color ? { backgroundColor: `${currentProject.color}22`, color: currentProject.color, borderColor: `${currentProject.color}55` } : {}"
              :class="!currentProject.color ? 'bg-raised text-fg-mute border-line' : ''"
            >
              <span aria-hidden="true">◫</span>{{ currentProject.name }}
            </span>
            <AppSelect
              :id="`task-modal-project-${task.id}`"
              :model-value="task.projectId ?? ''"
              :options="projectAssignOptions"
              :disabled="isAssigningProject"
              size="compact"
              class="flex-1 min-w-0"
              @update:model-value="onProjectChange"
            />
          </div>
        </div>
        <div>
          <label class="block text-[10px] uppercase tracking-[0.5px] text-fg-mute mb-1" :for="`task-modal-spawner-${task.id}`">
            Spawner
          </label>
          <div class="flex items-center gap-2">
            <span
              v-if="effectiveSpawner"
              class="inline-flex items-center gap-1 text-[10px] font-mono px-1.5 py-px rounded bg-raised text-fg-mute border border-line flex-shrink-0"
              :title="effectiveSpawner.description"
            >{{ effectiveSpawner.name }}</span>
            <span v-else-if="!task.spawnerId && currentProject?.defaultSpawnerId" class="text-[10px] text-fg-mute italic flex-shrink-0">from project</span>
            <AppSelect
              :id="`task-modal-spawner-${task.id}`"
              :model-value="task.spawnerId ?? ''"
              :options="spawnerAssignOptions"
              :disabled="isAssigningSpawner"
              size="compact"
              class="flex-1 min-w-0"
              @update:model-value="onSpawnerChange"
            />
          </div>
        </div>
      </div>
    </section>

    <details v-if="task.description" class="mb-1 text-xs border-t border-line pt-3">
      <summary class="cursor-pointer text-fg-mute py-1.5 select-none hover:text-slate-500 dark:hover:text-slate-400">
        Origin Prompt
      </summary>
      <div class="mt-1.5 px-3 py-3 bg-app rounded-md text-[13px] leading-relaxed whitespace-pre-wrap text-fg-mute">
        {{ task.description }}
      </div>
    </details>

    <div v-if="latestStageRun" class="bg-app border border-line rounded-md px-3.5 py-3 border-t border-line pt-3">
      <div class="text-[10px] uppercase tracking-[0.5px] text-fg-mute font-semibold mb-2">
        Current Output
      </div>
      <div class="flex items-center gap-2 flex-wrap mb-2">
        <span class="font-mono text-[10px] uppercase bg-raised text-fg-mute px-2 py-0.5 rounded font-semibold">{{ STAGE_LABELS[latestStageRun.stage as PipelineStage] ?? latestStageRun.stage }}</span>
        <span class="text-[10px] text-fg-mute font-mono">iter {{ latestStageRun.iteration }}</span>
        <AppChip :tone="runStatusTone(latestStageRun.status)" mono uppercase class="ml-auto">
          {{ runStatusLabel(latestStageRun.status) }}
        </AppChip>
        <span class="text-[11px] text-fg-mute ml-auto">
          {{ formatDateTime(latestStageRun.startedAt) }}
          <template v-if="latestStageRun.endedAt"> → {{ formatDateTime(latestStageRun.endedAt) }}</template>
        </span>
      </div>
      <AgentChatStream
        v-if="latestStageRun.status === 'running' && pipelineAgent"
        :agent="pipelineAgent"
        :local-messages="[]"
        class="border-t border-line mt-2 pt-3 min-h-[200px] max-h-[40vh] px-0 py-3"
      />
      <template v-else>
        <div v-if="latestRunError" class="mt-2 rounded-md bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-800/50 px-3 py-2 flex items-start gap-2">
          <span class="text-red-500 dark:text-red-400 text-sm leading-none mt-0.5">✗</span>
          <p class="text-xs text-red-700 dark:text-red-300 font-mono leading-relaxed whitespace-pre-wrap break-words">
            {{ latestRunError }}
          </p>
        </div>

        <div v-if="latestRunAgentMessage" class="mt-2">
          <div class="text-[10px] uppercase tracking-[0.5px] text-fg-mute mb-1">
            Agent output
          </div>
          <pre class="font-mono text-[11px] bg-card rounded px-3 py-2.5 whitespace-pre-wrap break-words max-h-[300px] overflow-y-auto text-fg-mute leading-relaxed">{{ latestRunAgentMessage }}</pre>
        </div>

        <div v-else-if="sessionAgentText || sessionAgentTextLoading" class="mt-2">
          <div class="text-[10px] uppercase tracking-[0.5px] text-fg-mute mb-1">
            Agent output
          </div>
          <div v-if="sessionAgentTextLoading" class="text-[11px] text-fg-mute animate-pulse">
            Loading…
          </div>
          <pre v-else class="font-mono text-[11px] bg-card rounded px-3 py-2.5 whitespace-pre-wrap break-words max-h-[400px] overflow-y-auto text-fg-mute leading-relaxed">{{ sessionAgentText }}</pre>
        </div>

        <div v-else-if="latestStageRun.status === 'running'" class="mt-2 text-[11px] text-fg-mute animate-pulse">
          Agent is running…
        </div>

        <details v-else-if="latestStageRun.output && !latestRunError" class="mt-1.5">
          <summary class="cursor-pointer text-[11px] text-fg-mute py-0.5 select-none hover:text-slate-500">
            Stage output
          </summary>
          <StageOutputView :stage="latestStageRun.stage" :output="latestStageRun.output" :status="latestStageRun.status" />
        </details>
      </template>
    </div>

    <div class="mt-2">
      <h3 class="text-xs font-semibold text-fg-mute uppercase tracking-wide mb-2">
        Git Status
      </h3>
      <GitStatusPanel :task-id="task.id" />
    </div>

    <WorktreeCommandRunner :task-id="task.id" class="mt-2" />
  </section>
</template>
