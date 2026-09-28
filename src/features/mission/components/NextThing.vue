<script setup lang="ts">
import type { NextThing } from '../composables/useNextThing'
import type { PermissionDecision } from '@/features/pipeline'
import type { AnswerIntent } from '@/utils/answerKeys'
import { ref } from 'vue'
import ConfirmCard from '@/components/ConfirmCard.vue'
import QuestionCard from '@/components/QuestionCard.vue'
import { toast } from '@/composables/useToast'
import { resolvePermissionRequest } from '@/features/pipeline'
import { sendQuestionAnswer } from '@/utils/answerQuestion'
import { errorMessage } from '@/utils/errorMessage'

const props = defineProps<{ next: NextThing | null, remaining: number }>()
const emit = defineEmits<{ resolved: [], open: [taskId: string] }>()

const busy = ref<PermissionDecision | null>(null)
const answering = ref(false)
const problem = ref('')

// The four decisions the needs-you band already offers. The centre must not
// invent a fifth, or a decision made here would mean something else there.
const DECISIONS: Array<{ value: PermissionDecision, label: string, primary?: boolean }> = [
  { value: 'allow_once', label: 'Allow once', primary: true },
  { value: 'allow_routine', label: 'Allow for this routine' },
  { value: 'deny_once', label: 'Deny once' },
]

async function decide(decision: PermissionDecision) {
  const n = props.next
  if (!n?.request || busy.value)
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

async function answer(intent: AnswerIntent) {
  const n = props.next
  if (n?.pid === undefined || answering.value)
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
      <span v-if="remaining > 0" data-testid="mission-remaining" class="text-[12px] text-fg-mute">
        {{ remaining }} more after this
      </span>
    </div>

    <p data-testid="mission-why" class="text-[13px] text-fg-mute">
      {{ next.why }}
    </p>

    <div class="rounded-2xl border border-line-strong bg-card p-7 flex flex-col gap-4">
      <div class="flex items-center gap-2.5">
        <span class="font-mono text-[10px] uppercase rounded px-1.5 py-0.5 border border-warning-line text-warning-text">
          {{ next.kind }}
        </span>
        <span data-testid="mission-context" class="text-[12.5px] text-fg-mute">
          {{ [next.projectName || next.taskTitle, next.stage].filter(Boolean).join(' · ') }}
        </span>
      </div>

      <h2 v-if="next.kind !== 'question'" class="text-[23px] font-medium leading-snug text-fg">
        <template v-if="next.kind === 'permission'">
          Let this agent run <span class="font-mono text-[20px] text-accent">{{ next.title }}</span>?
        </template>
        <template v-else>
          {{ next.title }}
        </template>
      </h2>

      <p v-if="next.request?.outsideSafeList" class="text-[13px] text-warning-text leading-relaxed">
        This command is outside the server's safe list — allowing it is a deliberate override, not a formality.
      </p>

      <p v-if="problem" data-testid="mission-problem" role="alert" class="text-[13px] text-warning-text">
        {{ problem }}
      </p>

      <div v-if="next.kind === 'question'" data-testid="mission-question" :class="answering ? 'opacity-60 pointer-events-none' : ''">
        <QuestionCard v-if="next.question" :detected-question="next.question" @answer="answer" />
        <ConfirmCard v-else-if="next.confirm" :detected-confirm="next.confirm" @answer="answer" />
      </div>

      <div v-else-if="next.kind === 'permission'" class="flex flex-wrap gap-2 pt-1">
        <button
          v-for="d in DECISIONS"
          :key="d.value"
          type="button"
          :data-testid="`mission-decide-${d.value}`"
          :disabled="busy !== null"
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

  <div v-else data-testid="mission-calm" class="flex flex-col items-center gap-3 py-6">
    <span class="h-11 w-11 rounded-full border border-line flex items-center justify-center">
      <span class="h-2.5 w-2.5 rounded-full bg-success" />
    </span>
    <h2 class="text-[26px] font-medium text-fg">
      Nothing needs you
    </h2>
    <p class="text-[14px] text-fg-mute text-center max-w-[420px] leading-relaxed">
      Agents will interrupt here if they get stuck. Until then this stays quiet.
    </p>
  </div>
</template>
