<script setup lang="ts">
import type { Agent, OutputMessage, SubAgent } from '@/types'
import { computed, nextTick, ref, watch } from 'vue'
import CrossLinkBanner from '@/components/CrossLinkBanner.vue'
import HookEventList from '@/components/HookEventList.vue'
import MachineBadge from '@/components/MachineBadge.vue'
import PromptInput from '@/components/PromptInput.vue'
import TaskList from '@/components/TaskList.vue'
import ToolTimeline from '@/components/ToolTimeline.vue'
import AppBadge from '@/components/ui/AppBadge.vue'
import AppModal from '@/components/ui/AppModal.vue'
import { usePermissionResolve } from '@/composables/usePermissionResolve'
import { toast } from '@/composables/useToast'
import { useAgentIdentity } from '@/features/agents/composables/useAgentIdentity'
import { PluginSlot } from '@/features/plugins'
import { formatCost, formatTokens, formatUptime, shortModel, totalTokenCount } from '@/utils/format'
import { agentDisplayStatus } from '@/utils/statusColors'
import AgentChatStream from './AgentChatStream.vue'
import MetricsPopover from './MetricsPopover.vue'
import SubAgentList from './SubAgentList.vue'

const props = defineProps<{ agent: Agent | null }>()

const emit = defineEmits<{ close: [], navigate: [taskId: string] }>()

const localMessages = ref<OutputMessage[]>([])
const promptInputRef = ref<InstanceType<typeof PromptInput> | null>(null)
const chatStreamRef = ref<InstanceType<typeof AgentChatStream> | null>(null)
const showMetrics = ref(false)

const hasContext = computed(() => {
  const a = props.agent
  if (!a)
    return false
  return a.tasks.length > 0 || a.subagents.length > 0 || a.lastTools.length > 0 || (a.recentHookEvents?.length ?? 0) > 0
})

// A subagent opened from the context panel takes over the transcript area; the
// modal otherwise belongs to its parent agent. Switching agents drops it, or the
// next session would open on someone else's subagent.
const openSubagent = ref<SubAgent | null>(null)
watch(() => props.agent?.sessionId, () => {
  openSubagent.value = null
})

const { getIdentity } = useAgentIdentity()
const { resolveAgent } = usePermissionResolve()

const totalTokens = computed(() => props.agent ? totalTokenCount(props.agent.tokenUsage) : 0)

async function handleApprove() {
  if (!props.agent)
    return
  const err = await resolveAgent(props.agent, 'granted')
  if (err)
    toast.error(err)
}

const approveHandler = computed(() =>
  props.agent?.pipelineTaskId && props.agent?.pendingPermissions?.length
    ? handleApprove
    : null,
)

function onMessageSent(msg: OutputMessage) {
  localMessages.value.push(msg)
  nextTick(() => chatStreamRef.value?.scrollToBottom())
}

// Reset local messages when the agent changes, focus prompt input
watch(() => props.agent?.sessionId, (sessionId) => {
  localMessages.value = []
  if (sessionId && !props.agent?.machine)
    nextTick(() => promptInputRef.value?.focus())
})

// Escape is handled by AppModal's @keydown.escape on its backdrop — no window listener needed.
</script>

<template>
  <AppModal :open="!!agent" :z-index="1000" :labelled-by="agent ? `agent-modal-title-${agent.pid}` : undefined" @close="emit('close')">
    <template v-if="agent">
      <div class="bg-raised px-4 py-2.5 flex justify-between items-center flex-shrink-0">
        <div class="flex items-center gap-2.5 min-w-0">
          <AppBadge :variant="agentDisplayStatus(agent)" />
          <span class="mr-1" aria-hidden="true">{{ getIdentity(agent.projectPath).emoji }}</span>
          <span :id="`agent-modal-title-${agent.pid}`" class="font-semibold text-sm text-fg">{{ agent.projectName }}</span>
          <MachineBadge v-if="agent.machine" :machine="agent.machine" />
          <span class="text-[11px] font-mono text-fg-mute whitespace-nowrap">{{ shortModel(agent.model ?? null) }} · {{ formatCost(agent.costEstimate) }} · {{ formatTokens(totalTokens) }} tok · {{ formatUptime(agent.uptime) }}</span>
          <!-- The breakdown behind the same affordance the card uses, instead of a
               token table nested two levels deep in a drawer. -->
          <span
            class="relative"
            @mouseenter="showMetrics = true"
            @mouseleave="showMetrics = false"
            @focusin="showMetrics = true"
            @focusout="showMetrics = false"
          >
            <button
              type="button"
              class="inline-flex items-center justify-center min-w-6 min-h-6 text-fg-mute hover:text-fg-soft text-[11px] leading-none rounded focus-visible:outline-2 focus-visible:outline-ring"
              aria-label="Show token and cost breakdown"
              data-testid="agent-modal-metrics"
              @click="showMetrics = !showMetrics"
            >ⓘ</button>
            <MetricsPopover v-if="showMetrics" :agent="agent" />
          </span>
        </div>
        <div class="flex items-center gap-2 flex-shrink-0">
          <button type="button" aria-label="Close" class="bg-transparent border-none text-fg-mute text-base cursor-pointer px-2 py-1 rounded hover:bg-slate-200 dark:hover:bg-slate-700 hover:text-fg" @click="emit('close')">
            ✕
          </button>
        </div>
      </div>
      <CrossLinkBanner
        v-if="agent.pipelineTaskId"
        label="Part of"
        :target-name="agent.pipelineTaskTitle ?? `Task ${agent.pipelineTaskId.slice(0, 8)}`"
        button-text="Open →"
        @click="emit('navigate', agent.pipelineTaskId)"
      />
      <template v-if="openSubagent">
        <div class="flex items-center gap-2 px-4 py-2 border-b border-line text-xs flex-shrink-0">
          <button
            type="button"
            class="text-accent hover:underline focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent rounded px-1"
            data-testid="subagent-back"
            @click="openSubagent = null"
          >
            ← Back to session
          </button>
          <span class="font-mono text-fg-mute">Subagent {{ openSubagent.id.substring(0, 16) }}</span>
          <AppBadge :variant="openSubagent.status" />
          <span class="text-fg-faint">{{ openSubagent.type }}</span>
          <span data-testid="subagent-metrics" class="ml-auto flex items-center gap-3 text-fg-faint">
            <span>{{ formatTokens(openSubagent.tokensUsed) }} tokens</span>
            <span>{{ formatUptime(openSubagent.durationSeconds) }}</span>
          </span>
        </div>
        <AgentChatStream
          :agent="null"
          :session-id="openSubagent.id"
          data-testid="subagent-transcript"
          class="flex-1 min-h-0 overflow-y-auto p-4"
        />
        <p data-testid="subagent-readonly" class="flex-shrink-0 border-t border-line px-4 py-3 text-[11px] text-fg-faint">
          A subagent runs inside its parent's process and has no channel of its own. Go back to the session to send anything.
        </p>
      </template>
      <template v-else>
        <!-- Session context: what you read while reading the transcript. -->
        <div
          v-if="hasContext"
          data-testid="agent-context"
          class="flex-shrink-0 max-h-[170px] overflow-y-auto border-b border-line px-4 py-2 flex flex-col gap-3"
        >
          <TaskList v-if="agent.tasks.length > 0" :tasks="agent.tasks" />
          <SubAgentList v-if="agent.subagents.length > 0" :subagents="agent.subagents" @open="openSubagent = $event" />
          <ToolTimeline v-if="agent.lastTools.length > 0" :tools="agent.lastTools" />
          <HookEventList v-if="(agent.recentHookEvents?.length ?? 0) > 0" :events="agent.recentHookEvents ?? []" />
        </div>
        <AgentChatStream
          ref="chatStreamRef"
          :agent="agent"
          :local-messages="localMessages"
          class="flex-1 min-h-0 overflow-y-auto p-4"
        />
        <PromptInput v-if="!agent.machine" ref="promptInputRef" :agent="agent" variant="full" :approve-handler="approveHandler" @message-sent="onMessageSent" />
      </template>
      <PluginSlot name="agent-modal-footer" :ctx="{ agent }" />
    </template>
  </AppModal>
</template>
