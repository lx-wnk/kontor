<script setup lang="ts">
import type { KontorStatus } from '../composables/useKontorSession'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useViewState } from '@/composables/useViewState'
import { AgentSessionPane } from '@/features/agents'
import { useKontorAgent, useKontorSession } from '../composables/useKontorSession'
import { readInput, SLASH_COMMAND_REFUSAL } from '../composables/useReading'

const { status, error, refresh, send, end, renew, pendingPrompt, takePendingPrompt } = useKontorSession()
const { activeView } = useViewState()

const STATE_LABELS: Record<KontorStatus, string> = {
  idle: 'No session',
  starting: 'Starting…',
  running: 'Running',
  error: 'Failed',
}

const text = ref('')
const problem = ref('')
const running = computed(() => status.value === 'running')
const busy = computed(() => status.value === 'starting')
const reading = computed(() => readInput(text.value, running.value))
// The scanner lists a fresh pid a few seconds after start; until then the tile keeps its own input so a typed prompt still reaches the session.
const agent = useKontorAgent()
const paneRef = ref<InstanceType<typeof AgentSessionPane> | null>(null)

// Reattaches after a reload, a view switch or a server restart.
onMounted(refresh)

function forwardPendingPrompt() {
  const t = takePendingPrompt()
  if (t === null)
    return
  if (agent.value) {
    paneRef.value?.prefill(t)
  }
  else {
    text.value = t
    nextTick(() => document.getElementById('kontor-input')?.focus())
  }
}

// A watch's immediate call runs before paneRef exists, so a prompt already pending at mount is taken in onMounted instead.
onMounted(() => {
  if (pendingPrompt.value !== null)
    forwardPendingPrompt()
})
watch([pendingPrompt, agent], ([prompt], [, before]) => {
  if (prompt !== null) {
    forwardPendingPrompt()
    return
  }
  // Own input still held unsent text when the agent appeared — hand it to the pane before it unmounts.
  if (agent.value && !before && text.value) {
    const t = text.value
    text.value = ''
    nextTick(() => paneRef.value?.prefill(t))
  }
}, { flush: 'post' })

async function submit() {
  const r = reading.value
  if (r.kind === 'empty' || busy.value)
    return
  problem.value = ''

  if (r.kind === 'navigate' && r.view) {
    activeView.value = r.view
    text.value = ''
    return
  }
  if (r.kind === 'command') {
    problem.value = SLASH_COMMAND_REFUSAL
    return
  }
  if (await send(text.value.trim()))
    text.value = ''
}

async function renewSession() {
  if (await renew(text.value.trim()))
    text.value = ''
}
</script>

<template>
  <section
    data-testid="kontor-tile"
    aria-label="Kontor"
    class="relative flex h-full min-h-0 flex-col overflow-hidden rounded-xl border border-line bg-card"
  >
    <!-- Absolute, so the transcript scrolls inside the tile: the Mission column
         has no fixed height and would otherwise grow with every message. -->
    <AgentSessionPane v-if="agent" ref="paneRef" :key="agent.pid" :agent="agent" title="Kontor" class="absolute inset-0">
      <template #actions>
        <button
          type="button"
          data-testid="kontor-new"
          title="Ends this session and starts a fresh one"
          :disabled="!running"
          class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
          @click="renewSession"
        >
          New
        </button>
        <button
          type="button"
          data-testid="kontor-end"
          :disabled="!running"
          class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
          @click="end"
        >
          End
        </button>
        <slot name="actions" />
      </template>
    </AgentSessionPane>

    <div v-else class="flex min-h-0 flex-1 flex-col gap-3 p-4">
      <header class="flex items-center gap-3">
        <h2 id="kontor-title" class="text-[14px] font-medium text-fg">
          Kontor
        </h2>
        <span data-testid="kontor-state" class="font-mono text-[10px] uppercase tracking-widest text-fg-mute">
          {{ STATE_LABELS[status] }}
        </span>
        <span class="flex-grow" />
        <button
          type="button"
          data-testid="kontor-new"
          title="Ends this session and starts a fresh one; text in the input becomes its first prompt"
          :disabled="!running"
          class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
          @click="renewSession"
        >
          New
        </button>
        <button
          type="button"
          data-testid="kontor-end"
          :disabled="!running"
          class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
          @click="end"
        >
          End
        </button>
        <slot name="actions" />
      </header>

      <div class="min-h-0 flex-1 overflow-hidden rounded-lg border border-line bg-app">
        <p data-testid="kontor-empty" class="p-4 text-[12.5px] text-fg-faint">
          {{ busy || running ? 'Starting a Kontor session…' : 'No session. Ask for anything below to start one.' }}
        </p>
      </div>

      <div class="flex flex-col gap-2">
        <label for="kontor-input" class="text-[12.5px] text-fg-mute">
          Or ask for anything — tasks, this interface, the system itself
        </label>

        <div class="rounded-xl border border-line-strong bg-app overflow-hidden">
          <div class="flex items-center gap-2.5 px-3.5 h-11">
            <span aria-hidden="true" class="font-mono text-[13px] text-accent">›</span>
            <input
              id="kontor-input"
              v-model="text"
              data-testid="kontor-input"
              type="text"
              placeholder="Ask Kontor, or go to pipeline"
              class="flex-grow bg-transparent text-[14.5px] text-fg outline-none"
              @keydown.enter.prevent="submit"
            >
            <button
              type="button"
              data-testid="kontor-input-submit"
              :disabled="reading.kind === 'empty' || busy"
              class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
              @click="submit"
            >
              {{ busy ? 'Working…' : 'Enter' }}
            </button>
          </div>

          <div
            v-if="reading.kind !== 'empty'"
            data-testid="kontor-reading"
            class="border-t border-line px-3.5 py-2.5 flex items-center gap-2.5"
          >
            <span
              data-testid="kontor-reading-label"
              class="font-mono text-[10px] rounded px-1.5 py-0.5 border border-line-strong text-fg-soft shrink-0"
            >{{ reading.label }}</span>
            <!--
            Flex gap separates badge and sentence on screen but not in the text
            layer; without this, textContent read "GO TOSwitches to…".
          -->
            <span class="sr-only">: </span>
            <span data-testid="kontor-reading-will" class="text-[12.5px] text-fg-mute leading-snug">{{ reading.will }}</span>
          </div>
        </div>

        <p v-if="problem || error" data-testid="kontor-input-problem" role="alert" class="text-[12.5px] text-warning-text">
          {{ problem || error }}
        </p>
        <p v-else class="text-[12px] text-fg-faint">
          The reading above is what Enter does. Only readings this screen can carry out are offered.
        </p>
      </div>
    </div>
  </section>
</template>
