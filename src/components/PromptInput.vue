<script setup lang="ts">
import type { DynamicCommandSet } from '../composables/useSlashCommands'
import type { Agent, OutputMessage } from '../types'
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useAgentPrompt } from '@/features/agents/composables/useAgentPrompt'
import { emptyCommandSet, fetchDynamicCommands, SLASH_COMMAND_DEFS } from '../composables/useSlashCommands'
import TemplatePicker from './TemplatePicker.vue'

const props = withDefaults(defineProps<{
  agent: Agent | null
  variant?: 'compact' | 'full'
  /** When provided and agent has a pending permission, a green ✓ button appears. */
  approveHandler?: (() => void) | null
}>(), {
  variant: 'compact',
  approveHandler: null,
})

const emit = defineEmits<{ messageSent: [msg: OutputMessage] }>()

// UX-10: unique ID per component instance so multiple PromptInput cards don't share the same DOM id
const hintId = useId()
const listboxId = useId()

// Absolute paths of uploaded images, injected into the sent prompt as
// "@<path>" tokens by useAgentPrompt (full variant only).
const attachments = ref<string[]>([])

const { promptInput, isSending, sendStatus, sendError, handleSend, resumeConfirm, confirmResume, cancelResume } = useAgentPrompt(
  () => props.agent,
  msg => emit('messageSent', msg),
  {
    get taskId() { return props.agent?.pipelineTaskId },
    get cwd() { return props.agent?.cwd },
  },
  attachments,
)

const isUploading = ref(false)
const uploadError = ref('')

async function uploadFiles(files: File[]) {
  const pid = props.agent?.pid
  if (files.length === 0 || pid == null)
    return
  isUploading.value = true
  uploadError.value = ''
  try {
    for (const file of files) {
      const form = new FormData()
      form.append('image', file)
      const res = await fetch(`/api/agents/${pid}/upload-image`, { method: 'POST', body: form })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error || `Upload failed (${res.status})`)
      }
      const { path } = await res.json() as { path: string }
      if (path)
        attachments.value.push(path)
    }
  }
  catch (err) {
    uploadError.value = err instanceof Error ? err.message : 'Upload failed'
  }
  finally {
    isUploading.value = false
  }
}

async function onFileChange(e: Event) {
  const input = e.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = '' // reset so the same file can be re-picked
  await uploadFiles(files)
}

// Paste an image straight from the clipboard (Cmd/Ctrl+V) into the prompt.
// Only intercepts when the clipboard carries image files — plain-text paste
// falls through to the default handler unchanged.
function onPaste(e: ClipboardEvent) {
  const imageFiles = Array.from(e.clipboardData?.items ?? [])
    .filter(it => it.kind === 'file' && it.type.startsWith('image/'))
    .map(it => it.getAsFile())
    .filter((f): f is File => f !== null)
  if (imageFiles.length === 0)
    return
  e.preventDefault()
  void uploadFiles(imageFiles)
}

function removeAttachment(path: string) {
  attachments.value = attachments.value.filter(p => p !== path)
}

function attachmentName(path: string): string {
  return path.split('/').pop() || path
}

const inputEl = ref<HTMLInputElement | HTMLTextAreaElement | null>(null)
const fileInputEl = ref<HTMLInputElement | null>(null)
const selectedIndex = ref(0)
const commandSet = ref<DynamicCommandSet>(emptyCommandSet())

const hasPendingApproval = computed(() =>
  !!props.approveHandler
  && !!props.agent?.pipelineTaskId
  && !!props.agent?.pendingPermissions?.length,
)

// Prefer sessionId so suggestions reflect the running session's actual
// CLAUDE_CONFIG_DIR (spawner-dependent); fall back to cwd for project-local commands.
async function loadCommands() {
  const sessionId = props.agent?.sessionId
  const cwd = props.agent?.cwd
  commandSet.value = (sessionId || cwd)
    ? await fetchDynamicCommands({ sessionId: sessionId || undefined, cwd: cwd || undefined })
    : emptyCommandSet()
}

watch(() => [props.agent?.sessionId, props.agent?.cwd] as const, loadCommands, { immediate: true })

const slashSuggestions = computed(() => {
  const val = promptInput.value.trim()
  if (!val.startsWith('/'))
    return []
  if (val.includes(' '))
    return []
  const query = val.toLowerCase()
  const term = query.slice(1) // drop leading '/'
  // Match like Claude's slash menu: prefix OR substring on the command name
  // (so "/review" surfaces /branch-review, /security-review, …). Prefix hits
  // rank first.
  const matches = (name: string) => {
    const n = name.toLowerCase()
    return n.startsWith(query) || n.slice(1).includes(term)
  }
  const rank = (name: string) => (name.toLowerCase().startsWith(query) ? 0 : 1)

  const dashboardCmds = SLASH_COMMAND_DEFS
    .filter(c => matches(c.name))
    .map(c => ({
      ...c,
      disabled: !!c.requiresTask && !props.agent?.pipelineTaskId,
    }))

  const seen = new Set(dashboardCmds.map(c => c.name))
  const sessionCmds = commandSet.value.commands
    .filter(c => matches(c.name) && !seen.has(c.name))
    .map(c => ({ ...c, disabled: false }))

  return [...dashboardCmds, ...sessionCmds].sort((a, b) => rank(a.name) - rank(b.name))
})

const showSuggestions = computed(() => slashSuggestions.value.length > 0)

// The drift note explains why an expected command is ABSENT, so it must survive
// the zero-match case — which is exactly when no suggestion renders.
const isSlashQuery = computed(() => promptInput.value.trim().startsWith('/'))
const showStaleNote = computed(() => isSlashQuery.value && commandSet.value.builtinsMayBeStale)

// A command can be installed while this input stays mounted, so every reopen of the menu asks again — the fetch's own TTL caps the traffic.
watch(isSlashQuery, (opened) => {
  if (opened)
    void loadCommands()
})

// Only a live-injectable session (pty broker or tmux) can receive a live prompt.
// Any other session resumes as a NEW session (claude --resume) on send — surface
// that so it's not mistaken for live injection. The three causes are distinct:
// an internal process is not a session at all, a disconnected agent has no
// channel file yet, and a connected-but-not-injectable agent can only be
// resumed as a new one.
type ResumeReason = 'internal' | 'disconnected' | 'noLiveChannel'
const resumeReason = computed<ResumeReason | null>(() => {
  const a = props.agent
  if (!a || a.liveInjectable)
    return null
  if (a.internalProcess)
    return 'internal'
  if (!a.channelAvailable)
    return 'disconnected'
  return 'noLiveChannel'
})
const isResumeMode = computed(() => resumeReason.value !== null)

function selectSuggestion(cmd: { name: string, disabled?: boolean }) {
  if (cmd.disabled)
    return
  promptInput.value = `${cmd.name} `
  nextTick(() => inputEl.value?.focus())
}

function focus() {
  inputEl.value?.focus()
}

function prefill(t: string) {
  promptInput.value = t
  nextTick(() => {
    focus()
    inputEl.value?.setSelectionRange(t.length, t.length)
  })
}

function autoResize() {
  const el = inputEl.value as HTMLTextAreaElement | null
  if (!el)
    return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 144)}px`
}

function onKeydown(e: KeyboardEvent) {
  if (showSuggestions.value) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      selectedIndex.value = Math.min(selectedIndex.value + 1, slashSuggestions.value.length - 1)
    }
    else if (e.key === 'ArrowUp') {
      e.preventDefault()
      selectedIndex.value = Math.max(selectedIndex.value - 1, 0)
    }
    else if (e.key === 'Tab') {
      // Tab always completes the highlighted suggestion.
      e.preventDefault()
      const cmd = slashSuggestions.value[selectedIndex.value]
      if (cmd)
        selectSuggestion(cmd)
    }
    else if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      // If the input is already a complete command, send it; otherwise complete
      // the highlighted suggestion (so a fully-typed /command isn't stuck on the menu).
      const typed = promptInput.value.trim()
      const exact = slashSuggestions.value.find(c => c.name === typed)
      if (exact) {
        if (!exact.disabled)
          handleSend()
      }
      else {
        const cmd = slashSuggestions.value[selectedIndex.value]
        if (cmd)
          selectSuggestion(cmd)
      }
    }
    else if (e.key === 'Escape') {
      e.preventDefault()
      promptInput.value = ''
    }
  }
  else if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    handleSend()
  }
}

// Reset selected index when suggestions change
watch(slashSuggestions, () => {
  selectedIndex.value = 0
})

// Reset textarea height after send
watch(promptInput, (val) => {
  if (val === '') {
    nextTick(autoResize)
  }
})

function truncate(text: string, max = 80): string {
  return text.length > max ? `${text.slice(0, max)}…` : text
}

function onConfirmStripKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    cancelResume()
  }
}

defineExpose({ focus, prefill })
</script>

<template>
  <div class="relative" :class="variant">
    <span v-if="variant === 'full'" :id="hintId" class="sr-only">Press Enter to send, Shift+Enter for new line</span>
    <span v-else :id="hintId" class="sr-only">Press Enter to send</span>
    <!-- The compact variant opens DOWNWARD: on an agent card the input sits in a
         hover-revealed wrapper (`overflow-hidden`, `max-h-40` = 160px) that clips
         anything above it, so an upward listbox would be invisible. Downward it
         must also fit that budget — input (~38px) + max-h-28 (112px) stays inside
         it — hence the shorter list here; it scrolls. The full variant sits at
         the bottom of the modal, where upward is the only direction with room. -->
    <div
      v-if="showSuggestions || showStaleNote"
      :class="variant === 'full'
        ? 'bottom-full border-b-0 rounded-t-md max-h-60'
        : 'top-full border-t-0 rounded-b-md max-h-28'"
      class="absolute left-0 right-0 bg-app border border-line overflow-y-auto z-10"
    >
      <!-- role="listbox" owns only role="option" children; the drift note is a
           sibling so it stays a valid listbox and can be announced on its own. -->
      <div v-if="showSuggestions" :id="listboxId" role="listbox">
        <button
          v-for="(cmd, i) in slashSuggestions"
          :key="cmd.name"
          type="button"
          role="option"
          :aria-selected="i === selectedIndex"
          :disabled="cmd.disabled"
          class="flex items-center gap-2.5 w-full px-4 py-2 bg-transparent border-none text-fg-mute text-[13px] font-mono cursor-pointer text-left hover:bg-raised disabled:opacity-40 disabled:cursor-not-allowed"
          :class="{ 'bg-raised': i === selectedIndex }"
          @mousedown.prevent="selectSuggestion(cmd)"
        >
          <span class="text-accent font-semibold flex-shrink-0">{{ cmd.name }}</span>
          <span class="text-fg-mute text-xs">{{ cmd.description }}</span>
          <span v-if="cmd.usage" data-testid="command-usage" :title="cmd.usage" class="text-fg-faint text-[10px] ml-1">{{ truncate(cmd.usage, 60) }}</span>
          <span v-if="cmd.requiresTask && cmd.disabled" class="text-warning-text text-[10px] ml-auto">requires linked task</span>
        </button>
      </div>
      <p
        v-if="showStaleNote"
        role="status"
        data-testid="builtins-stale-note"
        class="m-0 px-4 py-1.5 text-[10px] text-fg-faint"
        :class="{ 'border-t border-line': showSuggestions }"
      >
        Built-in commands are listed for a different Claude Code version{{ commandSet.engineVersion ? ` (session runs ${commandSet.engineVersion})` : '' }} — a command missing here may still work if you type it.
      </p>
    </div>
    <input
      v-if="variant === 'full'"
      ref="fileInputEl"
      type="file"
      accept="image/*"
      multiple
      class="hidden"
      @change="onFileChange"
    >
    <!-- Attached images (full variant): injected as @<path> tokens on send -->
    <div v-if="variant === 'full' && (attachments.length || uploadError)" class="px-4 pt-2 flex flex-wrap items-center gap-1.5">
      <span
        v-for="path in attachments"
        :key="path"
        class="inline-flex items-center gap-1 rounded bg-raised border border-line px-2 py-0.5 text-[11px] text-fg-soft font-mono"
      >
        🖼 {{ attachmentName(path) }}
        <button
          type="button"
          :aria-label="`Remove ${attachmentName(path)}`"
          class="inline-flex items-center justify-center min-w-4 min-h-4 text-fg-mute hover:text-danger-text leading-none"
          @click="removeAttachment(path)"
        >✕</button>
      </span>
      <span v-if="uploadError" class="text-[11px] text-danger-text">{{ uploadError }}</span>
    </div>
    <TemplatePicker
      v-if="variant === 'full'"
      model-value=""
      class="px-4 pt-2"
      @update:model-value="(val) => { promptInput = val; nextTick(autoResize) }"
    />
    <div
      class="border-t border-line flex items-end focus-within:ring-[3px] focus-within:ring-accent"
      :class="variant === 'full' ? 'px-4 py-2.5 gap-2 flex-shrink-0' : 'px-3 py-2 gap-1.5 items-center'"
    >
      <button
        v-if="variant === 'full'"
        type="button"
        title="Attach image"
        aria-label="Attach image"
        class="w-8 h-8 flex-shrink-0 rounded-full border border-line bg-raised text-fg-mute text-base cursor-pointer flex items-center justify-center hover:border-accent hover:text-accent disabled:opacity-35 disabled:cursor-default transition-colors"
        :disabled="isSending || isUploading"
        @click="fileInputEl?.click()"
      >
        {{ isUploading ? '…' : '+' }}
      </button>
      <span
        v-else
        class="text-accent flex-shrink-0 pb-0.5 text-[13px]"
      >❯</span>
      <textarea
        v-if="variant === 'full'"
        ref="inputEl"
        v-model="promptInput"
        rows="1"
        :placeholder="isResumeMode ? 'Prompt… (resumes as a new session)' : 'Enter prompt...'"
        :disabled="isSending"
        role="combobox"
        :aria-expanded="showSuggestions"
        :aria-describedby="hintId"
        :aria-controls="showSuggestions ? listboxId : undefined"
        class="flex-1 bg-transparent border-none text-fg text-[13px] font-mono focus-visible:outline-none placeholder:text-fg-faint disabled:opacity-50 resize-none leading-snug min-h-[22px] max-h-36 overflow-y-auto"
        @keydown="onKeydown"
        @input="autoResize"
        @paste="onPaste"
      />
      <input
        v-else
        ref="inputEl"
        v-model="promptInput"
        :placeholder="isResumeMode ? 'Prompt… (resumes as a new session)' : 'Enter prompt...'"
        :disabled="isSending"
        role="combobox"
        :aria-expanded="showSuggestions"
        :aria-describedby="hintId"
        :aria-controls="showSuggestions ? listboxId : undefined"
        class="flex-1 bg-transparent border-none text-fg text-[13px] font-mono focus-visible:outline-none placeholder:text-fg-faint disabled:opacity-50"
        @keydown="onKeydown"
      >
      <button
        v-if="hasPendingApproval"
        type="button"
        title="Approve pending permission"
        aria-label="Approve pending permission"
        class="w-8 h-8 flex-shrink-0 rounded-full border-none bg-success text-white text-sm font-bold cursor-pointer flex items-center justify-center hover:brightness-110 disabled:opacity-40 disabled:cursor-not-allowed transition-all"
        :disabled="isSending"
        @click="approveHandler?.()"
      >
        ✓
      </button>
      <button
        type="button"
        :aria-label="isResumeMode ? 'Resume session with prompt (creates a new session)' : 'Send message'"
        :title="isResumeMode ? 'No live channel — resumes this session as a new session' : 'Send'"
        class="text-white border-none rounded font-bold cursor-pointer flex-shrink-0 hover:brightness-110 disabled:opacity-40 disabled:cursor-not-allowed"
        :class="[
          variant === 'full' ? 'px-3.5 py-1.5 text-[14px]' : 'px-2.5 py-1 text-[13px]',
          isResumeMode ? 'bg-amber-600' : 'bg-accent',
        ]"
        :disabled="isSending || (promptInput.trim().length === 0 && attachments.length === 0)"
        @click="handleSend"
      >
        {{ isSending ? '...' : (isResumeMode ? '⤳' : '↵') }}
      </button>
    </div>
    <!-- Resume confirmation strip — shown when a send was intercepted on a non-injectable session -->
    <div
      v-if="resumeConfirm !== null"
      role="region"
      aria-label="Confirm resume as new session"
      class="border-t border-amber-400 dark:border-amber-600 bg-amber-50 dark:bg-amber-950/40"
      :class="variant === 'full' ? 'px-4 py-2.5' : 'px-3 py-2'"
      @keydown="onConfirmStripKeydown"
    >
      <p class="text-[11px] text-amber-800 dark:text-amber-300 mb-2">
        <template v-if="resumeReason === 'internal'">
          ⚠ This is a Claude Code <strong>internal process</strong>, not a session — it cannot be messaged.
        </template>
        <template v-else-if="resumeReason === 'disconnected'">
          ⚠ <strong>Not connected</strong> to the dashboard. Confirming will resume it as a
          <strong>new detached session</strong> via <code>claude --resume</code>.
        </template>
        <template v-else>
          ⤳ This session has <strong>no live input path</strong>. Confirming will resume it as a
          <strong>new detached session</strong>. Start it via <code>agent-dashboard live</code> for
          live injection.
        </template>
        <br>
        <span class="font-mono opacity-75">{{ truncate(resumeConfirm) }}</span>
      </p>
      <div class="flex gap-2">
        <button
          type="button"
          class="text-white bg-amber-600 hover:brightness-110 border-none rounded font-bold cursor-pointer flex-shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
          :class="variant === 'full' ? 'px-3.5 py-1.5 text-[13px]' : 'px-2.5 py-1 text-[12px]'"
          :disabled="isSending"
          @click="confirmResume"
        >
          {{ isSending ? '...' : '⤳ Resume as new session' }}
        </button>
        <button
          type="button"
          class="text-fg-mute bg-transparent border border-line hover:bg-raised rounded cursor-pointer flex-shrink-0"
          :class="variant === 'full' ? 'px-3.5 py-1.5 text-[13px]' : 'px-2.5 py-1 text-[12px]'"
          :disabled="isSending"
          @click="cancelResume"
        >
          Cancel
        </button>
      </div>
    </div>
    <p
      v-if="isResumeMode && !sendStatus"
      data-testid="resume-hint"
      class="text-[11px] text-amber-700 dark:text-amber-400"
      :class="variant === 'full' ? 'px-4 pb-2' : 'px-3 pb-1.5 pt-0.5'"
    >
      <template v-if="resumeReason === 'internal'">
        ⚠ Internal Claude Code process — not a session you can message.
      </template>
      <template v-else-if="resumeReason === 'disconnected'">
        ⚠ Not connected to the dashboard — sending will not reach this session.
      </template>
      <template v-else>
        ⤳ No live input path — sending resumes this session as a <strong>new</strong> session.
      </template>
    </p>
    <p
      v-if="sendStatus"
      class="text-[11px]"
      :class="[
        variant === 'full' ? 'px-4 pb-2' : 'px-3 pb-1.5 pt-0.5',
        sendStatus === 'sent' ? 'text-success-text' : 'text-danger-text',
      ]"
    >
      {{ sendStatus === 'sent' ? 'Sent' : sendError }}
    </p>
  </div>
</template>
