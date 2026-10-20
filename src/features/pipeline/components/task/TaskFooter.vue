<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import { actionLabel, actionVariant, renderableActions, runAction } from '@/composables/useRunAction'
import { toast } from '@/composables/useToast'
import TaskSlashCommandMenu from '@/features/pipeline/components/TaskSlashCommandMenu.vue'
import { useInjectedTask, useInjectedTaskActions, useInjectedTaskDetails } from '@/features/pipeline/composables/taskModalContext'

const task = useInjectedTask()
const { isActing, actionError, actionSuccess, handleAction } = useInjectedTaskDetails()
const { additionalPrompt, analysisInfo, cancelConfirm, completeConfirm, slashCommands, onCancelClick, onCompleteClick, onAnalyze, onSlashSelect } = useInjectedTaskActions()

// Surface task-action failures as toasts; success still shows inline.
watch(actionError, (msg) => {
  if (msg)
    toast.error(msg)
})

const slashMenuRef = ref<InstanceType<typeof TaskSlashCommandMenu> | null>(null)

// Spec approval is owned by the concept section in TaskOverviewTab; exclude it
// here so a backlog-stage task does not render two "Approve Spec" buttons.
const actions = computed(() =>
  renderableActions(task.value?.availableActions).filter(a => a.action !== 'approve_spec'),
)

// Show the prompt textarea when retry or resume is among the available actions.
const hasRetryOrResume = computed(() =>
  actions.value.some(a => a.action === 'retry' || a.action === 'resume'),
)

// Show analyze when any failure-related action is present (retry implies failed run).
const showAnalyze = computed(() =>
  actions.value.some(a => a.action === 'retry'),
)

// Body payload for actions that accept an additional prompt.
function bodyFor(action: string): Record<string, unknown> | undefined {
  if ((action === 'retry' || action === 'resume') && additionalPrompt.value.trim())
    return { additionalPrompt: additionalPrompt.value.trim() }
  return undefined
}

async function onActionClick(action: string): Promise<void> {
  if (action === 'cancel') {
    onCancelClick()
    return
  }
  await handleAction(() => runAction(task.value!.id, action, bodyFor(action)))
}

// Fallback: if no availableActions on the payload, detect the legacy cancel button.
const showLegacyCancel = computed(() =>
  !task.value?.availableActions
  && task.value?.currentStage !== 'done'
  && task.value?.currentStage !== 'cancelled',
)
</script>

<template>
  <footer class="px-5 py-3 border-t border-line flex-shrink-0">
    <p v-if="actionSuccess" class="text-info-text text-xs mb-2">
      {{ actionSuccess }}
    </p>
    <p v-if="analysisInfo" class="text-green-600 dark:text-green-400 text-xs mb-2">
      Analysis agent spawned · PID <code>{{ analysisInfo.pid }}</code> · look for it in the agents list.
    </p>

    <div v-if="hasRetryOrResume" class="mb-2">
      <div class="relative">
        <TaskSlashCommandMenu
          ref="slashMenuRef"
          v-model="additionalPrompt"
          :commands="slashCommands"
          @select="onSlashSelect"
        />
        <textarea
          v-model="additionalPrompt"
          rows="2"
          class="w-full bg-raised border border-line rounded px-2.5 py-1.5 text-fg text-xs resize-none focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent placeholder:text-fg-faint"
          placeholder="Optional instruction for Resume / Retry (e.g. logic change or hint)…"
          @keydown="slashMenuRef?.onKeydown($event)"
        />
      </div>
    </div>

    <div v-if="actions.length > 0" class="flex gap-2 justify-end flex-wrap">
      <template v-for="a in actions" :key="a.action">
        <!-- Cancel uses two-step confirm; delegate to existing handler. -->
        <AppButton
          v-if="a.action === 'cancel'"
          variant="danger"
          :disabled="isActing || !a.enabled"
          :title="cancelConfirm
            ? 'Click again to confirm — this stops the task and marks it cancelled'
            : (a.reason || 'Stop this task and mark it as cancelled')"
          @click="onCancelClick"
        >
          {{ cancelConfirm ? 'Confirm Cancel?' : actionLabel(a.action) }}
        </AppButton>
        <AppButton
          v-else-if="a.action === 'complete'"
          variant="secondary"
          :disabled="isActing || !a.enabled"
          :title="completeConfirm
            ? 'Click again to confirm — this stops the task and marks it done'
            : (a.reason || 'The work landed outside the pipeline — mark this task done')"
          @click="onCompleteClick"
        >
          {{ completeConfirm ? 'Confirm Done?' : actionLabel(a.action) }}
        </AppButton>
        <AppButton
          v-else
          :variant="actionVariant(a.action)"
          :disabled="isActing || !a.enabled"
          :title="a.reason"
          @click="onActionClick(a.action)"
        >
          {{ actionLabel(a.action) }}
        </AppButton>
      </template>

      <AppButton
        v-if="showAnalyze"
        variant="secondary"
        :disabled="isActing"
        title="Spawn a standalone Claude session with the failure context attached"
        @click="onAnalyze"
      >
        Analyze Failure
      </AppButton>
    </div>

    <!-- Graceful fallback for older payloads without availableActions. -->
    <div v-else-if="showLegacyCancel" class="flex gap-2 justify-end">
      <AppButton
        variant="danger"
        :disabled="isActing"
        :title="cancelConfirm ? 'Click again to confirm' : 'Stop this task and mark it as cancelled'"
        @click="onCancelClick"
      >
        {{ cancelConfirm ? 'Confirm Cancel?' : 'Cancel Task' }}
      </AppButton>
    </div>
  </footer>
</template>
