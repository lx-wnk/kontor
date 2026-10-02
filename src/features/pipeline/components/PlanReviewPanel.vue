<script setup lang="ts">
import type { PipelineTask } from '@/types'
import { computed, onUnmounted, ref, watch } from 'vue'
import { toast } from '@/composables/useToast'
import { usePlanReview } from '@/features/pipeline/composables/usePlanReview'
import { renderMarkdown } from '@/utils/markdown'

defineOptions({ name: 'PlanReviewPanel' })

const props = defineProps<{
  open: boolean
  task: PipelineTask | null
}>()

const emit = defineEmits<{
  close: []
  approved: [task: PipelineTask]
  rejected: []
}>()

const showRejectForm = ref(false)
const feedbackText = ref('')
const isActing = ref(false)

const { gateState, approvedPlan, loading, error, start, stop, approve, reject } = usePlanReview(
  () => props.task?.id ?? null,
)

const awaitingUser = computed(() => gateState.value === 'awaiting_user')
const planReady = computed(() => awaitingUser.value && approvedPlan.value !== null)

// Surface plan-review load/action failures as toasts; the panel keeps its state.
watch(error, (msg) => {
  if (msg)
    toast.error(msg)
})

watch(
  () => [props.open, props.task?.id] as const,
  ([open, id]) => {
    if (open && id)
      void start()
    else if (!open)
      stop()
  },
  { immediate: true },
)

onUnmounted(stop)

async function handleApprove() {
  isActing.value = true
  const updated = await approve()
  isActing.value = false
  if (updated)
    emit('approved', updated)
}

async function handleReject() {
  isActing.value = true
  await reject(feedbackText.value)
  isActing.value = false
  if (!error.value) {
    feedbackText.value = ''
    showRejectForm.value = false
    emit('rejected')
  }
}

function renderedPlan(): string {
  if (!approvedPlan.value)
    return ''
  // Prefer a `content` string field; fall back to JSON dump so nothing is silently swallowed.
  const plan = approvedPlan.value
  if (typeof plan.content === 'string')
    return renderMarkdown(plan.content)
  return renderMarkdown(JSON.stringify(plan, null, 2))
}
</script>

<template>
  <div
    v-if="open"
    class="fixed inset-0 bg-black/60 flex items-center justify-center z-[100] backdrop-blur-sm"
    data-testid="plan-review-panel"
    @click.self="emit('close')"
  >
    <div
      class="bg-card border border-line rounded-2xl flex flex-col overflow-hidden shadow-2xl"
      style="width: min(820px, 96vw); height: min(86vh, 90vh)"
    >
      <!-- Header -->
      <div class="flex justify-between items-center px-5 py-4 border-b border-line shrink-0">
        <span class="text-base font-semibold tracking-tight text-fg">Plan Review</span>
        <button
          data-testid="plan-review-close-btn"
          class="bg-transparent border-none cursor-pointer text-base text-fg-faint px-2 py-1 rounded-md transition-all hover:text-fg-soft hover:bg-raised"
          @click="emit('close')"
        >
          ✕
        </button>
      </div>

      <!-- Body -->
      <div class="flex-1 overflow-y-auto px-5 py-4 flex flex-col gap-4 min-h-0">
        <div v-if="loading" class="text-sm text-fg-mute animate-pulse">
          Loading plan…
        </div>

        <template v-else>
          <p v-if="gateState && gateState !== 'awaiting_user'" class="text-sm text-fg-mute">
            Gate state: {{ gateState }}
          </p>

          <p v-if="gateState === 'running'" data-testid="plan-review-pending-hint" class="text-sm text-fg-mute italic">
            Plan is still being written…
          </p>

          <div
            v-if="approvedPlan"
            class="assistant-bubble markdown-body"
            v-html="renderedPlan()"
          />
          <p v-else class="text-sm text-fg-mute italic">
            No plan output available yet.
          </p>
        </template>
      </div>

      <!-- Reject feedback form -->
      <div v-if="showRejectForm" class="px-5 py-3 border-t border-line shrink-0 flex flex-col gap-2">
        <label class="text-xs font-semibold text-fg-mute uppercase tracking-wide" for="plan-review-feedback">
          Feedback for revision
        </label>
        <textarea
          id="plan-review-feedback"
          v-model="feedbackText"
          data-testid="reject-feedback-textarea"
          class="w-full px-3 py-2 rounded-xl border border-line bg-raised text-fg placeholder:text-fg-faint text-[13px] font-mono leading-relaxed resize-none min-h-[80px] max-h-[160px] transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
          placeholder="Describe what should be changed…"
        />
        <div class="flex gap-2 justify-end">
          <button
            class="px-4 py-2 rounded-xl border border-line bg-raised text-fg-mute text-sm cursor-pointer hover:text-fg transition-colors"
            @click="showRejectForm = false; feedbackText = ''"
          >
            Cancel
          </button>
          <button
            data-testid="submit-reject-btn"
            class="px-4 py-2 rounded-xl bg-orange-500 text-white font-semibold text-sm border-none cursor-pointer transition-all hover:enabled:opacity-90 disabled:opacity-40 disabled:cursor-default"
            :disabled="!feedbackText.trim() || isActing"
            @click="handleReject"
          >
            {{ isActing ? 'Submitting…' : 'Submit Feedback' }}
          </button>
        </div>
      </div>

      <!-- Approval bar -->
      <div v-else class="px-5 py-3 border-t border-line shrink-0 flex gap-3">
        <button
          data-testid="reject-plan-btn"
          class="flex-1 py-3 px-4 rounded-xl bg-raised border border-line text-fg-mute font-semibold text-[0.95rem] cursor-pointer transition-all hover:enabled:border-orange-400 hover:enabled:text-orange-500 disabled:opacity-40 disabled:cursor-default"
          :disabled="!awaitingUser || isActing || loading"
          @click="showRejectForm = true"
        >
          Request Changes
        </button>
        <button
          data-testid="approve-plan-btn"
          class="flex-1 py-3 px-4 rounded-xl bg-green-500 text-black font-bold text-[0.95rem] tracking-tight border-none cursor-pointer transition-all hover:enabled:opacity-90 hover:enabled:-translate-y-px disabled:opacity-40 disabled:cursor-default"
          :disabled="!planReady || isActing || loading"
          @click="handleApprove"
        >
          {{ isActing ? 'Approving…' : 'Approve Plan →' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.assistant-bubble {
  align-self: flex-start;
  width: 100%;
  padding: 12px 16px;
  border-radius: 12px;
  line-height: 1.65;
  word-break: break-word;
  font-size: 13.5px;
  background: #f8fafc;
  color: #334155;
  border: 1px solid #e2e8f0;
}
.dark .assistant-bubble {
  background: rgba(30, 41, 59, 0.45);
  color: #cbd5e1;
  border-color: rgba(51, 65, 85, 0.5);
}

.markdown-body { white-space: normal; }
.markdown-body :deep(p) { margin: 0 0 0.5em; }
.markdown-body :deep(p:last-child) { margin-bottom: 0; }
.markdown-body :deep(code) {
  background: #f1f5f9;
  padding: 1px 5px;
  border-radius: 4px;
  font-size: 0.9em;
  font-family: var(--font-mono);
}
.dark .markdown-body :deep(code) { background: rgba(15, 23, 42, 0.8); }
.markdown-body :deep(pre) {
  background: #f1f5f9;
  padding: 10px 12px;
  border-radius: 6px;
  overflow-x: auto;
  margin: 6px 0;
}
.dark .markdown-body :deep(pre) { background: #0f172a; }
.markdown-body :deep(pre code) { background: none; padding: 0; }
.markdown-body :deep(ul), .markdown-body :deep(ol) { margin: 4px 0; padding-left: 1.4em; }
.markdown-body :deep(li) { margin: 2px 0; }
.markdown-body :deep(strong) { color: #1e293b; font-weight: 600; }
.dark .markdown-body :deep(strong) { color: #f1f5f9; }
.markdown-body :deep(a) { color: #3b82f6; }
.dark .markdown-body :deep(a) { color: #60a5fa; }
.markdown-body :deep(h1), .markdown-body :deep(h2), .markdown-body :deep(h3) {
  color: #1e293b;
  margin: 0.6em 0 0.3em;
  font-size: 1em;
  font-weight: 700;
}
.dark .markdown-body :deep(h1),
.dark .markdown-body :deep(h2),
.dark .markdown-body :deep(h3) { color: #f1f5f9; }
</style>
