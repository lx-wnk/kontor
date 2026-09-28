<script setup lang="ts">
import type { Agent, OutputMessage } from '@/types'
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { toast } from '@/composables/useToast'
import { renderMarkdown } from '@/utils/markdown'
import { CHAT_REFRESH_MS } from '@/utils/sse'

// Multi-root template (transcript + sr-only live region) disables attr
// fallthrough; bind $attrs explicitly to the transcript so layout classes apply.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  agent: Agent | null
  // A transcript that belongs to no running agent — a subagent's JSONL, which
  // the output endpoint serves like any other session. Read-only: no channel
  // replies (a subagent has no channel) and no polling loop.
  sessionId?: string
  localMessages?: OutputMessage[]
  refreshIntervalMs?: number
}>()

interface ToolGroup {
  kind: 'tool_group'
  calls: Array<{ toolName: string, filePath?: string, result?: string }>
}

interface TaskGroup {
  kind: 'task_group'
  tasks: Array<{ subject: string, status: string, taskId?: string }>
}

type ChatEntry = { kind: 'message', msg: OutputMessage } | ToolGroup | TaskGroup

const sessionMessages = ref<OutputMessage[]>([])
const isLoadingOutput = ref(false)
const outputEl = ref<HTMLElement | null>(null)
let lastReplyTimestamp: string | null = null
let fetchingReplies = false
let refreshInterval: ReturnType<typeof setInterval> | null = null
const channelReplies = ref<OutputMessage[]>([])

// UX-36: helpers for <time> datetime attribute and display value.
function isoTimestamp(ts: string | undefined): string {
  if (!ts)
    return ''
  try {
    return new Date(ts).toISOString()
  }
  catch {
    return ts
  }
}

function formatMsgTime(ts: string | undefined): string {
  if (!ts)
    return ''
  try {
    return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  catch {
    return ''
  }
}

const outputMessages = computed<OutputMessage[]>(() => {
  // Deduplicate: once a human message appears in sessionMessages (from JSONL),
  // remove it from localMessages to avoid showing it twice during the poll gap.
  const inSession = new Set(
    sessionMessages.value.filter(m => m.role === 'human').map(m => m.content),
  )
  const filteredLocal = (props.localMessages ?? []).filter(
    m => m.role !== 'human' || !inSession.has(m.content),
  )
  const all = [...sessionMessages.value, ...channelReplies.value, ...filteredLocal]
  all.sort((a, b) => {
    const ta = a.timestamp ? new Date(a.timestamp).getTime() : 0
    const tb = b.timestamp ? new Date(b.timestamp).getTime() : 0
    return ta - tb
  })
  return all
})

const chatEntries = computed<ChatEntry[]>(() => {
  const entries: ChatEntry[] = []
  let currentToolGroup: ToolGroup | null = null
  let currentTaskGroup: TaskGroup | null = null

  function flushToolGroup() {
    if (currentToolGroup) {
      entries.push(currentToolGroup)
      currentToolGroup = null
    }
  }
  function flushTaskGroup() {
    if (currentTaskGroup) {
      entries.push(currentTaskGroup)
      currentTaskGroup = null
    }
  }

  for (const msg of outputMessages.value) {
    if (msg.role === 'tool_call') {
      flushTaskGroup()
      if (!currentToolGroup)
        currentToolGroup = { kind: 'tool_group', calls: [] }
      currentToolGroup.calls.push({ toolName: msg.toolName || msg.content, filePath: msg.filePath })
    }
    else if (msg.role === 'tool_result') {
      if (currentToolGroup) {
        const lastCall = currentToolGroup.calls[currentToolGroup.calls.length - 1]
        if (lastCall)
          lastCall.result = msg.content
      }
    }
    else if (msg.role === 'task') {
      flushToolGroup()
      if (!currentTaskGroup)
        currentTaskGroup = { kind: 'task_group', tasks: [] }
      if (msg.taskStatus === 'pending') {
        currentTaskGroup.tasks.push({ subject: msg.content, status: 'pending', taskId: msg.taskId })
      }
      else {
        const existing = currentTaskGroup.tasks.find(t => t.taskId === msg.taskId)
        if (existing) {
          existing.status = msg.taskStatus || 'in_progress'
        }
        else {
          currentTaskGroup.tasks.push({ subject: msg.content, status: msg.taskStatus || 'in_progress', taskId: msg.taskId })
        }
      }
    }
    else {
      flushToolGroup()
      flushTaskGroup()
      entries.push({ kind: 'message', msg })
    }
  }
  flushToolGroup()
  flushTaskGroup()

  return entries
})

function scrollToBottom() {
  requestAnimationFrame(() => {
    if (outputEl.value)
      outputEl.value.scrollTop = outputEl.value.scrollHeight
  })
}

async function fetchOutput(sessionId: string) {
  isLoadingOutput.value = true
  try {
    const res = await fetch(`/api/agents/${sessionId}/output`)
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    const data = await res.json()
    sessionMessages.value = data.messages
    await nextTick()
    scrollToBottom()
  }
  catch {
    toast.error('Failed to load session output. Reconnect or refresh.')
    sessionMessages.value = []
  }
  finally {
    isLoadingOutput.value = false
  }
}

async function fetchReplies(sessionId: string) {
  if (fetchingReplies)
    return
  fetchingReplies = true
  try {
    const url = lastReplyTimestamp
      ? `/api/agents/${sessionId}/replies?since=${encodeURIComponent(lastReplyTimestamp)}`
      : `/api/agents/${sessionId}/replies`
    const res = await fetch(url)
    if (!res.ok)
      return
    const data = await res.json()
    const replies: Array<{ message: string, timestamp: string }> = data.replies || []
    if (replies.length === 0)
      return
    for (const r of replies) {
      channelReplies.value.push({
        role: 'channel_reply',
        content: r.message,
        timestamp: r.timestamp,
      })
    }
    lastReplyTimestamp = replies[replies.length - 1].timestamp
    await nextTick()
    scrollToBottom()
  }
  catch { /* ignore */ }
  finally {
    fetchingReplies = false
  }
}

async function refreshOutput() {
  const agent = props.agent
  if (!agent || agent.status === 'idle')
    return
  try {
    const res = await fetch(`/api/agents/${agent.sessionId}/output`)
    if (!res.ok)
      return
    const data = await res.json()
    const newMessages: OutputMessage[] = data.messages
    if (newMessages.length > sessionMessages.value.length) {
      sessionMessages.value = newMessages
      await nextTick()
      scrollToBottom()
    }
  }
  catch { /* ignore */ }
  await fetchReplies(agent.sessionId)
}

function startRefresh() {
  stopRefresh()
  if (props.agent && props.agent.status !== 'idle')
    refreshInterval = setInterval(refreshOutput, props.refreshIntervalMs ?? CHAT_REFRESH_MS)
}

function stopRefresh() {
  if (refreshInterval) {
    clearInterval(refreshInterval)
    refreshInterval = null
  }
}

watch(() => props.sessionId ?? props.agent?.sessionId, (sessionId) => {
  channelReplies.value = []
  lastReplyTimestamp = null
  stopRefresh()
  if (!sessionId) {
    sessionMessages.value = []
    return
  }
  // A standalone transcript is historical: fetch it once, then leave it alone.
  if (props.sessionId) {
    fetchOutput(props.sessionId)
    return
  }
  if (!props.agent?.machine) {
    fetchOutput(sessionId)
    fetchReplies(sessionId)
    startRefresh()
  }
  else {
    sessionMessages.value = []
  }
}, { immediate: true })

watch(() => props.agent?.status, () => {
  if (props.agent && !props.sessionId)
    startRefresh()
})

onUnmounted(stopRefresh)

// Latest plain-text message for the sr-only live region — avoids re-announcing the full transcript.
const latestMessageText = computed<string>(() => {
  for (let i = chatEntries.value.length - 1; i >= 0; i--) {
    const entry = chatEntries.value[i]
    if (entry.kind === 'message')
      return entry.msg.content ?? ''
  }
  return ''
})

defineExpose({ scrollToBottom })
</script>

<template>
  <div ref="outputEl" v-bind="$attrs" class="flex flex-col gap-1.5 overflow-y-auto font-mono text-[13px] leading-relaxed" aria-label="Chat transcript">
    <div v-if="agent?.machine" class="text-fg-mute text-center py-12">
      Session output is not available for remote agents.
    </div>
    <div v-else-if="isLoadingOutput" class="text-fg-mute text-center py-12">
      Loading session output...
    </div>
    <template v-else-if="chatEntries.length > 0">
      <template v-for="(entry, i) in chatEntries" :key="i">
        <div v-if="entry.kind === 'tool_group'" class="flex justify-center">
          <details class="w-full border border-line rounded-md bg-app text-xs">
            <summary class="px-2.5 py-1 text-fg-mute cursor-pointer select-none hover:text-slate-600 dark:hover:text-slate-400">
              {{ entry.calls.length }} tool call{{ entry.calls.length > 1 ? 's' : '' }}
            </summary>
            <div class="border-t border-line py-1">
              <details v-for="(call, j) in entry.calls" :key="j" class="px-2.5">
                <summary class="py-0.5 text-fg-mute text-[11px] cursor-pointer hover:text-fg-soft">
                  {{ call.toolName }}
                  <span class="text-fg-mute ml-1.5 text-[10px]">— {{ call.filePath || 'no target' }}</span>
                  <span class="text-fg-mute ml-1.5 text-[10px]">({{ call.result ? 'with output' : 'no output' }})</span>
                </summary>
                <pre v-if="call.result" class="bg-raised rounded p-2 text-[11px] text-fg-mute max-h-[200px] overflow-y-auto mt-1 mb-1 whitespace-pre-wrap break-words">{{ call.result }}</pre>
              </details>
            </div>
          </details>
        </div>
        <div v-else-if="entry.kind === 'task_group'" class="flex justify-center">
          <div class="w-full border border-line rounded-md bg-app px-2.5 py-1.5 text-xs">
            <div
              v-for="(task, j) in entry.tasks"
              :key="j"
              class="flex items-baseline gap-1.5 py-px text-fg-mute"
            >
              <span
                class="flex-shrink-0 w-3.5 text-center font-semibold"
                :class="{
                  'text-green-600 dark:text-green-400': task.status === 'completed',
                  'text-blue-600 dark:text-blue-400': task.status === 'in_progress',
                  'text-fg-mute': task.status === 'pending',
                }"
              >{{ task.status === 'completed' ? '✓' : task.status === 'in_progress' ? '›' : '○' }}</span>
              <span
                class="font-mono"
                :class="task.status === 'completed' ? 'text-fg-mute line-through' : ''"
              >{{ task.subject }}</span>
            </div>
          </div>
        </div>
        <div
          v-else
          class="flex"
          :class="{
            'justify-end': entry.msg.role === 'human',
            'justify-start': entry.msg.role === 'channel_reply' || entry.msg.role === 'assistant',
            'justify-center': entry.msg.role === 'tool_call' || entry.msg.role === 'tool_result',
          }"
        >
          <!-- UX-36: wrap message bubbles in column so <time> sits below each bubble -->
          <div v-if="entry.msg.role === 'human'" class="flex flex-col items-end gap-0.5 max-w-[80%]">
            <div
              class="max-w-full px-3 py-2 rounded-xl rounded-br-sm text-[13px] leading-relaxed break-words whitespace-pre-wrap bg-raised text-fg-mute"
              :class="{ 'border border-yellow-400/40': entry.msg.queued }"
            >
              {{ entry.msg.content }}
            </div>
            <time
              v-if="formatMsgTime(entry.msg.timestamp)"
              :datetime="isoTimestamp(entry.msg.timestamp)"
              class="text-[10px] text-fg-mute select-none"
            >{{ formatMsgTime(entry.msg.timestamp) }}</time>
          </div>
          <div v-else-if="entry.msg.role === 'channel_reply'" class="flex flex-col items-start gap-0.5 max-w-[80%]">
            <div
              class="max-w-full px-3 py-2 rounded-xl rounded-bl-sm text-[13px] leading-relaxed break-words bg-raised text-fg-mute border-l-2 border-green-500 dark:border-green-400 markdown-body"
              v-html="renderMarkdown(entry.msg.content)"
            />
            <time
              v-if="formatMsgTime(entry.msg.timestamp)"
              :datetime="isoTimestamp(entry.msg.timestamp)"
              class="text-[10px] text-fg-mute select-none"
            >{{ formatMsgTime(entry.msg.timestamp) }}</time>
          </div>
          <div v-else-if="entry.msg.role === 'assistant'" class="flex flex-col items-start gap-0.5 max-w-[80%]">
            <div
              class="max-w-full px-3 py-2 rounded-xl rounded-bl-sm text-[13px] leading-relaxed break-words bg-raised text-fg-mute markdown-body"
              v-html="renderMarkdown(entry.msg.content)"
            />
            <time
              v-if="formatMsgTime(entry.msg.timestamp)"
              :datetime="isoTimestamp(entry.msg.timestamp)"
              class="text-[10px] text-fg-mute select-none"
            >{{ formatMsgTime(entry.msg.timestamp) }}</time>
          </div>
          <div v-else-if="entry.msg.role === 'subagent'" class="flex items-center gap-1.5 px-2.5 py-1 border border-line rounded-md bg-app text-xs text-fg-mute">
            <span class="text-[14px] text-blue-600 dark:text-blue-400">⑂</span>
            <span class="font-semibold text-fg text-[11px] uppercase tracking-wide">{{ entry.msg.subagentType }}</span>
            <span class="text-fg-mute">{{ entry.msg.content }}</span>
          </div>
        </div>
      </template>
    </template>
    <div v-else class="text-fg-mute text-center py-12">
      No output available for this session.
    </div>
    <div v-if="agent?.status === 'active' && agent.currentAction" class="text-fg-mute text-xs italic py-1" style="animation: pulse 2s ease-in-out infinite;">
      {{ agent.currentAction }}...
    </div>
  </div>
  <!-- Announces only the latest message to screen readers; placed outside the scrollable div
       so scroll events cannot re-trigger the live region. -->
  <span class="sr-only" aria-live="polite" aria-atomic="true">{{ latestMessageText }}</span>
</template>
