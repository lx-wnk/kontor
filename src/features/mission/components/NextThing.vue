<script setup lang="ts">
import type { NextThing } from '../composables/useNextThing'
import type { PermissionDecision } from '@/features/pipeline'
import type { AnswerIntent } from '@/utils/answerKeys'
import { computed, onUnmounted, ref, watch } from 'vue'
import ConfirmCard from '@/components/ConfirmCard.vue'
import QuestionCard from '@/components/QuestionCard.vue'
import { toast } from '@/composables/useToast'
import { useCapabilityDecisions } from '@/features/agents'
import { resolvePermissionRequest } from '@/features/pipeline'
import { sendQuestionAnswer } from '@/utils/answerQuestion'
import { capabilityValueLabel, elidedTitle } from '@/utils/capabilityDecision'
import { errorMessage } from '@/utils/errorMessage'

const props = defineProps<{ next: NextThing | null }>()
const emit = defineEmits<{ resolved: [], open: [taskId: string] }>()

const busy = ref<PermissionDecision | null>(null)
const answering = ref(false)
const capabilityBusy = ref<'allow' | 'deny' | null>(null)
const problem = ref('')
const { resolve: resolveCapability } = useCapabilityDecisions()

// Identifies which item is on screen so the card below can remount on
// change: a permission's own id, or the agent holding a question/confirm
// screen, or the task for a plan.
const itemKey = computed(() => {
  const n = props.next
  if (!n)
    return undefined
  if (n.kind === 'permission')
    return `permission:${n.request?.id ?? ''}`
  if (n.kind === 'capability')
    return `capability:${n.decision?.id ?? ''}`
  if (n.pid !== undefined)
    return `question:${n.pid}`
  return `task:${n.taskId}`
})

// Guards the decision buttons for a short window after the shown item
// changes, so a click already in flight for the previous item (e.g. a
// double-click or a repeated Enter) cannot land on the new one.
const DECISION_GUARD_MS = 500
const guarding = ref(true)
let guardTimer: ReturnType<typeof setTimeout> | null = null

watch(itemKey, () => {
  guarding.value = true
  if (guardTimer)
    clearTimeout(guardTimer)
  guardTimer = setTimeout(() => {
    guarding.value = false
  }, DECISION_GUARD_MS)
}, { immediate: true })

onUnmounted(() => {
  if (guardTimer)
    clearTimeout(guardTimer)
})

// The four decisions the needs-you band already offers. The centre must not
// invent a fifth, or a decision made here would mean something else there.
const DECISIONS: Array<{ value: PermissionDecision, label: string, primary?: boolean }> = [
  { value: 'allow_once', label: 'Allow once', primary: true },
  { value: 'allow_routine', label: 'Allow for this routine' },
  { value: 'deny_once', label: 'Deny once' },
]

async function decide(decision: PermissionDecision) {
  const n = props.next
  if (!n?.request || busy.value || guarding.value)
    return
  busy.value = decision
  problem.value = ''
  try {
    await resolvePermissionRequest(n.taskId, n.request.id, decision)
    emit('resolved')
  }
  catch (e) {
    problem.value = errorMessage(e, 'Could not record that decision')
    toast.error(problem.value)
  }
  finally {
    busy.value = null
  }
}

// Mirrors AgentTriageBand's handleCapabilityDecision: same three outcomes, read the same way.
async function decideCapability(choice: 'allow' | 'deny') {
  const n = props.next
  if (!n?.decision || capabilityBusy.value || guarding.value)
    return
  capabilityBusy.value = choice
  problem.value = ''
  try {
    const result = await resolveCapability(n.decision.id, choice)
    if (result.outcome === 'error') {
      problem.value = result.message
      toast.error(problem.value)
      return
    }
    if (result.outcome === 'already-resolved')
      toast.info('Too late — that ask already expired or was answered elsewhere')
    emit('resolved')
  }
  finally {
    capabilityBusy.value = null
  }
}

async function answer(intent: AnswerIntent) {
  const n = props.next
  if (n?.pid === undefined || answering.value || guarding.value)
    return
  answering.value = true
  problem.value = ''
  try {
    await sendQuestionAnswer(n.pid, intent)
  }
  catch (e) {
    problem.value = errorMessage(e, 'Couldn\'t send that answer')
    toast.error(problem.value)
  }
  finally {
    answering.value = false
  }
}
</script>

<template>
  <div v-if="next" data-testid="mission-next" class="flex flex-col gap-4">
    <div class="flex items-center gap-3">
      <span class="text-[11px] font-mono tracking-widest text-accent">NEXT</span>
      <span class="h-px flex-grow bg-line" />
    </div>

    <p data-testid="mission-why" class="text-[13px] text-fg-mute">
      {{ next.why }}
    </p>

    <div :key="itemKey" class="rounded-2xl border border-line-strong bg-card p-7 flex flex-col gap-4">
      <div class="flex items-center gap-2.5">
        <span class="font-mono text-[10px] uppercase rounded px-1.5 py-0.5 border border-warning-line text-warning-text">
          {{ next.kind }}
        </span>
        <span data-testid="mission-context" class="text-[12.5px] text-fg-mute">
          <template v-if="next.kind === 'capability'">{{ next.decision?.context ?? '' }}<span
            v-if="next.decision?.contextElided"
            data-testid="mission-capability-context-elided"
            :title="elidedTitle(next.decision.contextElided)"
          >…<span class="sr-only"> ({{ elidedTitle(next.decision.contextElided) }})</span></span></template>
          <template v-else>{{ [next.projectName || next.taskTitle, next.stage].filter(Boolean).join(' · ') }}</template>
        </span>
      </div>

      <h2 v-if="next.kind !== 'question'" class="text-[23px] font-medium leading-snug text-fg">
        <template v-if="next.kind === 'permission'">
          Let this agent run <span class="font-mono text-[20px] text-accent">{{ next.title }}</span>?
        </template>
        <template v-else-if="next.kind === 'capability' && next.decision">
          <span class="font-mono text-[20px] text-accent">{{ next.decision.capability }}({{ capabilityValueLabel(next.decision) }}<span
            v-if="next.decision.valueElided"
            data-testid="mission-capability-value-elided"
            :title="elidedTitle(next.decision.valueElided)"
          >…<span class="sr-only"> ({{ elidedTitle(next.decision.valueElided) }})</span></span>)</span>
        </template>
        <template v-else>
          {{ next.title }}
        </template>
      </h2>

      <p v-if="next.request?.outsideSafeList" class="text-[13px] text-warning-text leading-relaxed">
        This command is outside the server's safe list — allowing it is a deliberate override, not a formality.
      </p>

      <p v-if="next.kind === 'capability' && next.decision?.reason" data-testid="mission-capability-reason" class="text-[13px] text-fg-mute leading-relaxed">
        {{ next.decision.reason }}
      </p>

      <p v-if="problem" data-testid="mission-problem" role="alert" class="text-[13px] text-warning-text">
        {{ problem }}
      </p>

      <div v-if="next.kind === 'question'" data-testid="mission-question" :class="(answering || guarding) ? 'opacity-60 pointer-events-none' : ''">
        <QuestionCard v-if="next.question" :detected-question="next.question" :disabled="guarding" @answer="answer" />
        <ConfirmCard v-else-if="next.confirm" :detected-confirm="next.confirm" :disabled="guarding" @answer="answer" />
      </div>

      <div v-else-if="next.kind === 'permission'" class="flex flex-wrap gap-2 pt-1">
        <button
          v-for="d in DECISIONS"
          :key="d.value"
          type="button"
          :data-testid="`mission-decide-${d.value}`"
          :disabled="busy !== null || guarding"
          class="h-9 rounded-lg px-4 text-[13.5px] disabled:opacity-60"
          :class="d.primary ? 'bg-accent text-accent-contrast font-semibold' : 'border border-line-strong text-fg-soft'"
          @click="decide(d.value)"
        >
          {{ busy === d.value ? 'Working…' : d.label }}
        </button>
        <span class="flex-grow" />
        <button
          type="button"
          data-testid="mission-open"
          class="h-9 rounded-lg border border-line-strong px-3 text-[13.5px] text-fg-mute"
          @click="emit('open', next.taskId)"
        >
          Open agent
        </button>
      </div>
      <div v-else-if="next.kind === 'capability'" class="flex flex-wrap gap-2 pt-1">
        <button
          type="button"
          data-testid="mission-capability-allow"
          :disabled="capabilityBusy !== null || guarding"
          class="h-9 rounded-lg bg-accent px-4 text-[13.5px] font-semibold text-accent-contrast disabled:opacity-60"
          @click="decideCapability('allow')"
        >
          {{ capabilityBusy === 'allow' ? 'Working…' : 'Allow' }}
        </button>
        <button
          type="button"
          data-testid="mission-capability-deny"
          :disabled="capabilityBusy !== null || guarding"
          class="h-9 rounded-lg border border-line-strong px-4 text-[13.5px] text-fg-soft disabled:opacity-60"
          @click="decideCapability('deny')"
        >
          {{ capabilityBusy === 'deny' ? 'Working…' : 'Deny' }}
        </button>
      </div>
      <div v-else class="flex gap-2 pt-1">
        <button
          type="button"
          data-testid="mission-open"
          class="h-9 rounded-lg bg-accent px-4 text-[13.5px] font-semibold text-accent-contrast"
          @click="emit('open', next.taskId)"
        >
          Open
        </button>
      </div>
    </div>
  </div>

  <p v-else-if="!next" data-testid="mission-calm" class="flex items-center gap-2 text-[12.5px] text-fg-faint">
    <span aria-hidden="true" class="h-1.5 w-1.5 rounded-full bg-success" />
    Nothing needs you — agents interrupt here if they get stuck.
  </p>
</template>
