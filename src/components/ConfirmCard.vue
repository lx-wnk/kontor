<script setup lang="ts">
import type { AnswerIntent } from '../utils/answerKeys'
import type { DetectedConfirm } from '../utils/askQuestionScreen'
import { ref, watch } from 'vue'
import { screenSignature } from '../utils/askQuestionScreen'
import { nextRadioGroupName } from '../utils/radioGroup'
import AppButton from './ui/AppButton.vue'

const props = defineProps<{
  detectedConfirm: DetectedConfirm
  disabled?: boolean
}>()

const emit = defineEmits<{
  answer: [intent: AnswerIntent]
}>()

// Per-instance radio group name — see nextRadioGroupName.
const optionGroupName = nextRadioGroupName('detected-confirm-option')

// The TUI puts the cursor on the first option (Submit), so pre-selecting it
// mirrors what the terminal already shows and makes the common case one click.
const selectedIndex = ref<number>(props.detectedConfirm.options[0]?.index ?? 1)

// Reset on a CONTENT change only — see the same note in QuestionCard.vue: the
// SSE payload is re-deserialized every scan tick, so watching the prop object
// itself would fight the user's selection.
watch(
  () => screenSignature(props.detectedConfirm),
  () => {
    selectedIndex.value = props.detectedConfirm.options[0]?.index ?? 1
  },
)

// DetectedOption.index is the 1-based on-screen digit; AnswerIntent indices are
// 0-based (encodeAnswer re-adds 1 when it renders the keystroke). Single-select
// is the right mode here: the confirm screen submits instantly on the digit,
// exactly like a one-question modal.
function handleSubmit() {
  emit('answer', { mode: 'single', index: selectedIndex.value - 1 })
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <div class="flex flex-col gap-1.5">
      <div class="text-[12px] font-semibold text-fg">
        Review your answers
      </div>
      <div class="text-[11px] text-fg-mute leading-snug mb-0.5">
        {{ detectedConfirm.question }}
      </div>

      <fieldset class="border-none m-0 p-0 flex flex-col gap-1.5">
        <!-- Names the option group. The prompt itself is already rendered above
             as the card heading, so repeating it here would only duplicate the
             text for a screen reader. -->
        <legend class="sr-only">
          Review your answers
        </legend>
        <label
          v-for="option in detectedConfirm.options"
          :key="option.index"
          class="flex items-start gap-2 cursor-pointer"
        >
          <!-- v-model, not :checked — see the note in QuestionCard.vue. -->
          <input
            v-model="selectedIndex"
            type="radio"
            :name="optionGroupName"
            :value="option.index"
            class="mt-0.5 accent-accent shrink-0"
          >
          <span class="text-[12px] font-medium text-fg leading-snug">{{ option.label }}</span>
        </label>
      </fieldset>
    </div>

    <div class="flex items-center gap-2 flex-wrap">
      <AppButton
        variant="primary"
        size="sm"
        data-testid="detected-confirm-send-btn"
        :disabled="disabled"
        @click="handleSubmit"
      >
        Send answer
      </AppButton>
    </div>
  </div>
</template>
