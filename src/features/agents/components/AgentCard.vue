<script setup lang="ts">
import type { Agent } from '@/types'
import { computed, ref } from 'vue'
import MachineBadge from '@/components/MachineBadge.vue'
import PromptInput from '@/components/PromptInput.vue'
import ProviderBadge from '@/components/ProviderBadge.vue'
import AppBadge from '@/components/ui/AppBadge.vue'
import AppCard from '@/components/ui/AppCard.vue'
import { useNow } from '@/composables/useNow'
import { toast } from '@/composables/useToast'
import AgentTerminalModal from '@/features/agents/components/AgentTerminalModal.vue'
import MetricsPopover from '@/features/agents/components/MetricsPopover.vue'
import { useAgentIdentity } from '@/features/agents/composables/useAgentIdentity'
import { formatCost, formatDuration, formatTokens, formatUptime, isAwaitingInput, isStalled, secondsSince, shortModel, totalTokenCount } from '@/utils/format'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { agentDisplayStatus } from '@/utils/statusColors'

const props = defineProps<{ agent: Agent }>()
const emit = defineEmits<{ select: [agent: Agent], dismiss: [pid: number] }>()

const isFinished = computed(() => props.agent.status === 'finished')

// The 'waiting' relabel lives in statusLabel() (src/utils/statusColors.ts, SSOT),
// so the card, the roster row and the modal cannot disagree about the same
// agent. Only the explanatory tooltip is card-local.
const displayStatus = computed(() => agentDisplayStatus(props.agent))
const statusBadgeTitle = computed(() => displayStatus.value === 'waiting'
  ? 'No new activity for a bit — the agent process is still alive, not waiting on you'
  : undefined)

async function dismiss() {
  const pid = props.agent.pid
  try {
    await fetch(`/api/agents/${pid}/channel`, { method: 'DELETE', credentials: 'same-origin' })
  }
  catch {
    // best-effort: the next SSE frame still reflects server truth
  }
  emit('dismiss', pid)
}

const { getIdentity } = useAgentIdentity()
const { nowMs } = useNow()

const totalTokens = computed(() => totalTokenCount(props.agent.tokenUsage))
const projectLabel = computed(() => friendlyProjectName(props.agent.projectName))

const activityFallback = computed(() => props.agent.currentAction || props.agent.lastTools?.at(-1)?.name || '')

const healthChipClass = computed(() => {
  const s = props.agent.healthScore
  if (s >= 75)
    return 'bg-success-soft text-success-text'
  if (s >= 40)
    return 'bg-warning-soft text-warning-text'
  return 'bg-danger-soft text-danger-text'
})

const secSince = computed(() => secondsSince(props.agent.lastActivity, nowMs.value))
const stalled = computed(() => isStalled(props.agent.status, secSince.value))
const awaitingInput = computed(() => isAwaitingInput(props.agent))

const activeSubagents = computed(() => props.agent.subagents.filter(s => s.status === 'active'))

const expandedSubagentIds = ref<Set<string>>(new Set())
function toggleSubagentExpand(id: string) {
  const next = new Set(expandedSubagentIds.value)
  if (next.has(id))
    next.delete(id)
  else
    next.add(id)
  expandedSubagentIds.value = next
}

const showMetrics = ref(false)
// The live terminal used to be a tab at the bottom of the agent modal. It is
// reached from the card now, so the modal stays a place to read and reply.
// xterm.js is ~490KB — keep it in its own chunk, loaded on first open.
const arming = ref(false)
// Arming is per session and lives on the bridge, not in the client: the hook
// asks the server on every gated tool call, and the server has to know before
// the card is even on screen.
async function toggleArmed() {
  arming.value = true
  try {
    const res = await fetch('/api/hooks/permission/arm', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId: props.agent.sessionId, armed: !props.agent.permissionBridgeArmed }),
    })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
  }
  catch (e) {
    toast.error(`Could not change interception: ${e instanceof Error ? e.message : String(e)}`)
  }
  finally {
    arming.value = false
  }
}

const showTerminal = ref(false)
</script>

<template>
  <AppCard surface="card" radius="lg" interactive class="group relative flex flex-col h-[260px] overflow-hidden cursor-pointer" @click="emit('select', agent)">
    <div class="bg-raised px-3 pt-2 pb-1.5 flex flex-col gap-1">
      <button
        type="button"
        data-testid="agent-card-open"
        :aria-label="`Open details for ${projectLabel}`"
        class="flex items-center gap-2 min-w-0 w-full bg-transparent border-none p-0 text-left cursor-pointer rounded focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-[-2px]"
        @click.stop="emit('select', agent)"
      >
        <AppBadge :variant="displayStatus" :title="statusBadgeTitle" />
        <AppBadge
          v-if="agent.internalProcess"
          variant="info"
          label="internal"
          title="Claude Code's own internal daemon process — not a session you can message"
          data-testid="agent-card-internal-badge"
        />
        <span
          v-if="stalled"
          class="text-[10px] font-medium px-1 py-0.5 rounded bg-warning-soft text-warning-text whitespace-nowrap"
          title="Agent is active but has produced no output for 3+ minutes"
        >stalled</span>
        <span
          v-else-if="awaitingInput"
          data-testid="agent-awaiting-input"
          class="text-[10px] font-medium px-1 py-0.5 rounded bg-neutral-soft text-neutral-text whitespace-nowrap"
          title="The agent finished its turn — it will not do anything else until you send it something"
        >your turn</span>
        <span class="shrink-0" aria-hidden="true">{{ getIdentity(agent.projectPath).emoji }}</span>
        <span
          class="font-semibold text-[13px] text-fg flex-1 min-w-0 whitespace-nowrap overflow-hidden text-ellipsis"
          data-testid="agent-card-project"
          :title="projectLabel"
        >{{ projectLabel }}</span>
        <ProviderBadge :provider="agent.provider" />
        <span
          class="text-[10px] font-mono text-fg-mute whitespace-nowrap shrink-0"
          :title="agent.model ? `Model: ${agent.model}` : 'Model unknown'"
        >{{ shortModel(agent.model ?? null) }}</span>
        <MachineBadge v-if="agent.machine" :machine="agent.machine" />
      </button>

      <div class="flex items-center gap-2 text-[11px] font-mono text-fg-mute min-w-0">
        <span class="whitespace-nowrap" title="Total estimated cost">
          <span v-if="agent.costUnknown" title="Cost unknown — no pricing data for this provider/model">?</span>
          <template v-else>{{ formatCost(agent.costEstimate) }}</template>
        </span>
        <span aria-hidden="true">·</span>
        <span class="whitespace-nowrap" title="Tokens used">{{ formatTokens(totalTokens) }} tok</span>
        <span aria-hidden="true">·</span>
        <span class="whitespace-nowrap" title="Uptime">{{ formatUptime(agent.uptime) }}</span>

        <span class="ml-auto flex items-center gap-1.5 shrink-0">
          <span
            class="text-[10px] font-mono px-1.5 py-0.5 rounded"
            :class="healthChipClass"
            :title="`Health score: ${agent.healthScore}/100`"
          >{{ agent.healthScore }}</span>
          <span
            class="relative z-10"
            @mouseenter="showMetrics = true"
            @mouseleave="showMetrics = false"
            @focusin="showMetrics = true"
            @focusout="showMetrics = false"
          >
            <button
              type="button"
              class="inline-flex items-center justify-center min-w-6 min-h-6 text-fg-mute hover:text-fg-soft text-[11px] leading-none focus-visible:outline-2 focus-visible:outline-ring rounded"
              aria-label="Show more metrics"
              data-testid="agent-card-info"
              @click.stop="showMetrics = !showMetrics"
            >ⓘ</button>
            <MetricsPopover v-if="showMetrics" :agent="agent" @click.stop />
          </span>
          <button
            v-if="agent.terminalAttachable"
            type="button"
            class="inline-flex items-center justify-center min-w-6 min-h-6 text-fg-mute hover:text-fg-soft text-[11px] leading-none focus-visible:outline-2 focus-visible:outline-ring rounded"
            aria-label="Open terminal"
            title="Open the live terminal for this session"
            data-testid="agent-card-terminal"
            @click.stop="showTerminal = true"
          >⌨</button>
          <button
            v-if="!isFinished"
            type="button"
            class="inline-flex items-center justify-center min-w-6 min-h-6 text-[11px] leading-none focus-visible:outline-2 focus-visible:outline-ring rounded"
            :class="agent.permissionBridgeArmed ? 'text-warning-text' : 'text-fg-mute hover:text-fg-soft'"
            :aria-pressed="agent.permissionBridgeArmed ? 'true' : 'false'"
            :aria-label="agent.permissionBridgeArmed
              ? 'Stop answering this session\'s permission prompts here'
              : 'Answer this session\'s permission prompts here instead of in its terminal'"
            :title="agent.permissionBridgeArmed
              ? 'Permission prompts from this session are answered here. Click to hand them back to its terminal.'
              : 'Answer this session\'s permission prompts here instead of in its terminal. Requires the hooks to be installed.'"
            :disabled="arming"
            data-testid="agent-card-arm-permissions"
            @click.stop="toggleArmed"
          >{{ agent.permissionBridgeArmed ? '🔒' : '🔓' }}</button>
          <button
            v-if="isFinished"
            type="button"
            class="inline-flex items-center justify-center min-w-6 min-h-6 text-fg-mute hover:text-danger-text text-sm leading-none focus-visible:outline-2 focus-visible:outline-ring rounded"
            aria-label="Dismiss finished agent"
            data-testid="agent-card-dismiss"
            @click.stop="dismiss"
          >✕</button>
        </span>
      </div>
    </div>

    <div class="relative flex-1 min-h-0 px-3 py-3 overflow-hidden text-[13px] leading-relaxed text-fg-mute font-mono" data-testid="agent-card-body">
      <template v-if="agent.lastOutput">
        {{ agent.lastOutput }}
      </template>
      <span v-else-if="activityFallback" class="text-fg-soft">▶ running: {{ activityFallback }}</span>
      <span v-else class="text-fg-mute italic">No output yet</span>
      <div class="absolute bottom-0 left-0 right-0 h-10 bg-gradient-to-t from-card to-transparent pointer-events-none" />
    </div>

    <div
      v-if="activeSubagents.length"
      data-testid="active-subagents-block"
      class="border-t border-line px-3 py-2 flex flex-col gap-1 shrink-0"
    >
      <span class="text-[10px] font-semibold uppercase tracking-wider text-fg-mute">
        {{ activeSubagents.length }} active subagent{{ activeSubagents.length !== 1 ? 's' : '' }}
      </span>
      <div class="flex flex-col gap-1 max-h-[120px] overflow-y-auto overflow-x-hidden">
        <div v-for="sa in activeSubagents" :key="sa.id" class="flex flex-col gap-0.5">
          <div class="flex items-center gap-1.5 flex-wrap">
            <AppBadge variant="active" />
            <span class="font-mono text-[11px] text-fg-soft">{{ sa.type }}</span>
            <span v-if="sa.currentAction" class="text-[10px] text-fg-mute">· {{ sa.currentAction }}</span>
            <span class="text-[10px] font-mono text-fg-mute ml-auto whitespace-nowrap">
              {{ formatDuration(sa.durationSeconds) }} · {{ Math.round(sa.tokensUsed / 1000) }}k tok
            </span>
          </div>
          <div v-if="sa.latestOutput" class="flex items-start gap-1">
            <span
              class="font-mono text-[11px] text-fg-mute leading-snug"
              :class="expandedSubagentIds.has(sa.id) ? 'whitespace-pre-wrap break-words' : 'truncate'"
              data-testid="subagent-latest-output"
            >{{ sa.latestOutput }}</span>
            <button
              type="button"
              class="inline-flex items-center justify-center min-w-6 min-h-6 flex-shrink-0 text-[10px] text-fg-mute hover:text-fg-soft focus-visible:outline-none focus-visible:ring-[2px] focus-visible:ring-accent rounded"
              :aria-label="expandedSubagentIds.has(sa.id) ? 'Collapse subagent output' : 'Expand subagent output'"
              data-testid="subagent-expand-toggle"
              @click.stop="toggleSubagentExpand(sa.id)"
            >
              {{ expandedSubagentIds.has(sa.id) ? '▲' : '▼' }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="agent.lastBtw" class="border-t border-line px-3 py-2 flex flex-col gap-1 text-[12px] font-mono shrink-0">
      <div class="text-fg-mute border-l-2 border-warning-line pl-2 whitespace-nowrap overflow-hidden text-ellipsis">
        {{ agent.lastBtw.message }}
      </div>
      <div v-if="agent.lastBtw.response" class="text-fg-mute border-l-2 border-warning-line pl-2 whitespace-nowrap overflow-hidden text-ellipsis">
        {{ agent.lastBtw.response }}
      </div>
      <div v-else class="text-fg-mute pl-2.5" style="animation: pulse 2s ease-in-out infinite;">
        ...
      </div>
    </div>

    <div
      v-if="!agent.machine"
      class="shrink-0 max-h-0 opacity-0 overflow-hidden transition-[max-height,opacity] duration-150 ease-out group-hover:max-h-40 group-hover:opacity-100 focus-within:max-h-40 focus-within:opacity-100"
      @click.stop
      @keydown.enter.stop
      @keydown.space.stop
    >
      <PromptInput :agent="agent" variant="compact" @interactive-command="showTerminal = agent.terminalAttachable === true" />
    </div>

    <AgentTerminalModal :open="showTerminal" :pid="agent.pid" :title="projectLabel" @close="showTerminal = false" />
  </AppCard>
</template>
