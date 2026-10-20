<script setup lang="ts">
import type { Agent } from '@/types'
import { computed, ref } from 'vue'
import AppBadge from '@/components/ui/AppBadge.vue'
import AppButton from '@/components/ui/AppButton.vue'
import { useNow } from '@/composables/useNow'
import { usePermissionResolve } from '@/composables/usePermissionResolve'
import { toast } from '@/composables/useToast'
import { useAgentIdentity } from '@/features/agents/composables/useAgentIdentity'
import { attentionFor } from '@/utils/attention'
import { formatBurnRate, formatCost, formatRelativeActivity, isAwaitingInput, isStalled, secondsSince, shortModel, totalTokenCount } from '@/utils/format'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { agentDisplayStatus } from '@/utils/statusColors'

const props = defineProps<{ agent: Agent }>()
const emit = defineEmits<{
  select: [agent: Agent]
}>()

const { getIdentity } = useAgentIdentity()
const { nowMs } = useNow()
const { resolving, resolveAgent } = usePermissionResolve()

const expanded = ref(false)

const secSince = computed(() => secondsSince(props.agent.lastActivity, nowMs.value))
const att = computed(() => attentionFor(props.agent, secSince.value))
const stalled = computed(() => isStalled(props.agent.status, secSince.value))
const awaitingInput = computed(() => isAwaitingInput(props.agent))
const relActivity = computed(() => formatRelativeActivity(secSince.value))
const burnRate = computed(() => formatBurnRate(props.agent.costEstimate, props.agent.uptime))
const projectTitle = computed(() => friendlyProjectName(props.agent.projectName))

const toneClass: Record<string, string> = {
  warning: 'border-warning-dot',
  danger: 'border-danger-dot',
  info: 'border-info-dot',
}

const statusDotClass = computed(() => {
  if (agentDisplayStatus(props.agent) === 'replied')
    return 'bg-accent'
  if (props.agent.status === 'active')
    return 'bg-success-dot shadow-[0_0_0_3px_color-mix(in_oklch,var(--success)_18%,transparent)]'
  if (props.agent.status === 'waiting')
    return 'bg-warning-dot'
  if (att.value?.tone === 'danger')
    return 'bg-danger-dot'
  return 'bg-fg-faint'
})

const actionText = computed(() => props.agent.currentAction || att.value?.label || 'idle')

const borderClass = computed(() => {
  if (att.value)
    return toneClass[att.value.tone] ?? 'border-line'
  return 'border-line'
})

async function handleResolve(outcome: 'granted' | 'denied') {
  const err = await resolveAgent(props.agent, outcome, false)
  if (err)
    toast.error(err)
}
</script>

<template>
  <div
    class="rounded-md overflow-hidden border bg-card transition-[border-color] duration-fast"
    :class="borderClass"
  >
    <!-- Dense single-line row -->
    <button
      type="button"
      class="flex items-center gap-2.5 px-3 py-2.5 cursor-pointer min-h-[40px] w-full text-left hover:bg-app focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-inset"
      :aria-expanded="expanded"
      :aria-label="`${projectTitle} — ${agent.status}`"
      @click="expanded = !expanded"
    >
      <!-- Status dot -->
      <span
        aria-hidden="true"
        data-testid="agent-row-status-dot"
        class="w-2 h-2 rounded-full shrink-0"
        :class="statusDotClass"
      />

      <!-- Project emoji + name -->
      <span aria-hidden="true" class="text-[14px] shrink-0">{{ getIdentity(agent.projectPath).emoji }}</span>
      <span class="font-semibold text-sm text-fg shrink-0 w-[156px] overflow-hidden text-ellipsis whitespace-nowrap">
        {{ projectTitle }}
      </span>

      <!-- Model -->
      <span class="font-mono text-[11px] text-fg-faint shrink-0 w-16">{{ shortModel(agent.model ?? null) }}</span>

      <!-- Current action — calm middle column; status is shown via the dot, not by recoloring -->
      <span class="flex-1 min-w-0 text-xs text-fg-mute overflow-hidden text-ellipsis whitespace-nowrap">
        {{ actionText }}
      </span>

      <!-- Inline approve/deny for resolvable permission agents -->
      <template v-if="att?.kind === 'permission' && agent.pipelineTaskId && agent.pendingPermissions?.length">
        <span class="flex items-center gap-1.5 shrink-0" @click.stop>
          <AppButton
            variant="success"
            size="sm"
            :disabled="resolving[agent.sessionId]"
            :aria-label="`Approve permission for ${projectTitle}`"
            @click="handleResolve('granted')"
          >
            ✓ Approve
          </AppButton>
          <AppButton
            variant="danger"
            size="sm"
            :disabled="resolving[agent.sessionId]"
            :aria-label="`Deny permission for ${projectTitle}`"
            @click="handleResolve('denied')"
          >
            ✕ Deny
          </AppButton>
        </span>
      </template>

      <!-- Liveness (right rail) — always shown so every row carries a timestamp -->
      <span
        class="font-mono text-[11px] shrink-0 w-[76px] text-right"
        :class="stalled ? 'text-warning-text' : 'text-fg-faint'"
      >{{ relActivity }}</span>

      <!-- Cost -->
      <span class="font-mono text-[11px] text-success-text shrink-0 w-[52px] text-right">
        <span v-if="agent.costUnknown" class="text-fg-mute" title="Cost unknown">?</span>
        <template v-else>{{ formatCost(agent.costEstimate) }}</template>
      </span>

      <!-- Expand chevron -->
      <span
        aria-hidden="true"
        class="text-fg-faint text-[11px] shrink-0 transition-transform duration-150"
        :class="expanded ? 'rotate-90' : ''"
      >▸</span>
    </button>

    <!-- Expanded detail panel -->
    <div
      v-if="expanded"
      class="border-t border-line p-3 bg-app"
      @click.stop
    >
      <div class="flex gap-1.5 flex-wrap mb-2.5 items-center">
        <span class="font-mono text-[11px] px-1.5 py-0.5 rounded bg-raised text-fg-mute">PID {{ agent.pid }}</span>
        <span class="font-mono text-[11px] px-1.5 py-0.5 rounded bg-raised text-fg-mute">{{ totalTokenCount(agent.tokenUsage).toLocaleString() }} tok</span>
        <span v-if="burnRate !== '—'" class="font-mono text-[11px] px-1.5 py-0.5 rounded bg-raised text-fg-mute">{{ burnRate }}</span>
        <AppBadge :variant="agentDisplayStatus(agent)" />
        <AppBadge
          v-if="agent.internalProcess"
          variant="info"
          label="internal"
          title="Claude Code's own internal daemon process — not a session you can message"
          data-testid="agent-row-internal-badge"
        />
        <span
          v-if="stalled"
          class="text-[10px] font-medium px-1 py-0.5 rounded bg-warning-soft text-warning-text"
          title="Agent is active but has produced no output for 3+ minutes"
        >stalled</span>
        <span
          v-else-if="awaitingInput"
          data-testid="agent-awaiting-input"
          class="text-[10px] font-medium px-1 py-0.5 rounded bg-neutral-soft text-neutral-text"
          title="The agent finished its turn — it will not do anything else until you send it something"
        >your turn</span>
      </div>

      <pre
        v-if="agent.lastOutput"
        class="m-0 font-mono text-xs text-fg-mute leading-relaxed whitespace-pre-wrap bg-card border border-line rounded-sm p-2.5"
      >{{ agent.lastOutput }}</pre>
      <p v-else class="text-xs text-fg-faint italic m-0">
        No output yet
      </p>

      <div class="flex gap-1.5 mt-2.5">
        <AppButton
          variant="outline"
          size="sm"
          class="ml-auto"
          :aria-label="`Open details for ${projectTitle}`"
          @click="emit('select', agent)"
        >
          Open ↗
        </AppButton>
      </div>
    </div>
  </div>
</template>
