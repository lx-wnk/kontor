<script setup lang="ts">
import type { AnswerIntent } from '../utils/answerKeys'
import type { DetectedQuestion } from '../utils/askQuestionScreen'
import { computed, ref, watch } from 'vue'
import { screenSignature } from '../utils/askQuestionScreen'
import { nextRadioGroupName } from '../utils/radioGroup'
import AppButton from './ui/AppButton.vue'

const props = defineProps<{
  detectedQuestion: DetectedQuestion
  disabled?: boolean
}>()

const emit = defineEmits<{
  answer: [intent: AnswerIntent]
}>()

// Two cards can be mounted at once (two agents with a question in the triage
// band, or a band card plus the terminal overlay); a shared radio `name` would
// make the browser uncheck the other card's radio. See nextRadioGroupName.
const optionGroupName = nextRadioGroupName('detected-question-option')

const detectedSelectedIndices = ref<number[]>([])
const detectedCustomText = ref('')
const detectedCustomOpen = ref(false)
const detectedChatOpen = ref(false)
const detectedChatText = ref('')

// Reset on a CONTENT change, never on a mere reference change. The card is fed
// straight from the SSE agent payload, which is re-deserialized on every scan
// tick (~3 s) because volatile fields like uptime keep changing — watching the
// prop object itself would therefore wipe the user's selection every few
// seconds, mid-answer.
watch(
  () => screenSignature(props.detectedQuestion),
  () => {
    detectedSelectedIndices.value = []
    detectedCustomText.value = ''
    detectedCustomOpen.value = false
    detectedChatOpen.value = false
    detectedChatText.value = ''
  },
)

// Radios bind the option through v-model rather than :checked + @change, so
// the selected value is declared once instead of being recomputed in a handler.
// Note this is NOT a safety net: vModelRadio.beforeUpdate also writes el.checked
// only when the bound value changes, so a DOM state cleared behind Vue's back
// stays cleared. What prevents that is optionGroupName — nothing outside this
// card shares its radio group anymore.
const singleSelected = computed<number | null>({
  get: () => detectedSelectedIndices.value[0] ?? null,
  set: (index) => {
    if (index !== null)
      toggleDetectedOption(index)
  },
})

const detectedAnswered = computed(() =>
  detectedSelectedIndices.value.length > 0 || detectedCustomText.value.trim().length > 0,
)
const detectedChatReady = computed(() => detectedChatText.value.trim().length > 0)

function toggleDetectedOption(index: number) {
  const question = props.detectedQuestion
  detectedCustomText.value = ''
  if (question.multiSelect) {
    const current = detectedSelectedIndices.value
    const pos = current.indexOf(index)
    detectedSelectedIndices.value = pos === -1
      ? [...current, index]
      : current.filter((_, i) => i !== pos)
  }
  else {
    detectedSelectedIndices.value = [index]
  }
}

function toggleDetectedCustomOpen() {
  detectedCustomOpen.value = !detectedCustomOpen.value
}

function onDetectedCustomInput(value: string) {
  detectedCustomText.value = value
  if (value.trim())
    detectedSelectedIndices.value = []
}

// DetectedOption.index is the 1-based on-screen digit; AnswerIntent indices are
// 0-based (encodeAnswer re-adds 1 when it renders the keystroke).
function handleDetectedSubmit() {
  const question = props.detectedQuestion
  const customText = detectedCustomText.value.trim()
  if (customText) {
    emit('answer', { mode: 'custom', optionCount: question.options.length, text: customText })
    return
  }
  if (question.multiSelect) {
    emit('answer', { mode: 'multi', indices: detectedSelectedIndices.value.map(i => i - 1) })
    return
  }
  const selectedIndex = detectedSelectedIndices.value[0]
  if (selectedIndex === undefined)
    return
  emit('answer', { mode: 'single', index: selectedIndex - 1 })
}

function handleDetectedChatSubmit() {
  const question = props.detectedQuestion
  emit('answer', { mode: 'chat', optionCount: question.options.length, text: detectedChatText.value.trim() })
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <div class="flex flex-col gap-1.5">
      <!-- No header row: what the detector puts in `header` is whatever
           scrolled above the modal (a prompt echo, the welcome box), not a
           title. The question is the card's heading. -->
      <div class="text-[12px] font-semibold text-fg leading-snug mb-0.5">
        {{ detectedQuestion.question }}
      </div>

      <fieldset class="border-none m-0 p-0 flex flex-col gap-1.5">
        <legend class="sr-only">
          {{ detectedQuestion.question }}
        </legend>
        <label
          v-for="option in detectedQuestion.options"
          :key="option.index"
          class="flex items-start gap-2 cursor-pointer"
        >
          <input
            v-if="detectedQuestion.multiSelect"
            type="checkbox"
            :checked="detectedSelectedIndices.includes(option.index)"
            class="mt-0.5 accent-accent shrink-0"
            @change="toggleDetectedOption(option.index)"
          >
          <input
            v-else
            v-model="singleSelected"
            type="radio"
            :name="optionGroupName"
            :value="option.index"
            class="mt-0.5 accent-accent shrink-0"
          >
          <span class="flex flex-col">
            <span class="text-[12px] font-medium text-fg leading-snug">{{ option.label }}</span>
            <span v-if="option.description" class="text-[11px] text-fg-faint leading-snug">{{ option.description }}</span>
          </span>
        </label>
      </fieldset>

      <div class="flex flex-col gap-1">
        <button
          type="button"
          data-testid="detected-custom-toggle"
          class="self-start text-[11px] text-accent underline decoration-dotted"
          @click="toggleDetectedCustomOpen"
        >
          {{ detectedCustomOpen ? 'Hide custom answer' : 'Type your own answer' }}
        </button>
        <textarea
          v-if="detectedCustomOpen || detectedCustomText"
          aria-label="Custom answer"
          data-testid="detected-custom-textarea"
          :value="detectedCustomText"
          rows="2"
          class="text-[12px] rounded border border-line bg-raised px-2 py-1 text-fg"
          @input="onDetectedCustomInput(($event.target as HTMLTextAreaElement).value)"
        />
      </div>
    </div>

    <div class="flex items-center gap-2 flex-wrap">
      <AppButton
        variant="primary"
        size="sm"
        data-testid="detected-send-btn"
        :disabled="!detectedAnswered || disabled"
        @click="handleDetectedSubmit"
      >
        Send answer
      </AppButton>
    </div>

    <div class="flex flex-col gap-1.5 pt-1 border-t border-line">
      <button
        type="button"
        data-testid="detected-chat-toggle"
        class="self-start text-[11px] text-accent underline decoration-dotted"
        @click="detectedChatOpen = !detectedChatOpen"
      >
        {{ detectedChatOpen ? 'Hide chat' : 'Chat about this instead' }}
      </button>
      <template v-if="detectedChatOpen">
        <textarea
          v-model="detectedChatText"
          aria-label="Chat message instead of answering"
          data-testid="detected-chat-textarea"
          rows="2"
          class="text-[12px] rounded border border-line bg-raised px-2 py-1 text-fg"
        />
        <AppButton
          variant="secondary"
          size="sm"
          data-testid="detected-chat-send-btn"
          :disabled="!detectedChatReady || disabled"
          @click="handleDetectedChatSubmit"
        >
          Send as chat
        </AppButton>
      </template>
    </div>
  </div>
</template>
