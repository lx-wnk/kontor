<script setup lang="ts">
import type { UseTerminalSocket } from '@/composables/useTerminalSocket'
import type { DetectedConfirm, DetectedQuestion } from '@/utils/askQuestionScreen'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import QuestionOverlay from '@/components/QuestionOverlay.vue'
import { useTerminalSocket } from '@/composables/useTerminalSocket'
import { detectScreen, screenSignature } from '@/utils/askQuestionScreen'
import '@xterm/xterm/css/xterm.css'

const props = defineProps<{
  pid: number
}>()

// How often the visible screen is re-parsed for an AskUserQuestion screen.
// Throttled rather than reactive to xterm's own render events, since those can
// fire many times per burst of output.
const QUESTION_POLL_MS = 200

const containerRef = ref<HTMLElement | null>(null)
const detectedQuestion = ref<DetectedQuestion | null>(null)
const detectedConfirm = ref<DetectedConfirm | null>(null)
const overlayOpen = computed(() => detectedQuestion.value !== null || detectedConfirm.value !== null)
// Signature of the currently-shown screen. The poll replaces the refs only when
// this changes, so an unchanged screen never re-creates the objects and the
// card's local answer state (selections, typed text) survives.
let currentSig: string | null = null
let term: Terminal | null = null
let socket: UseTerminalSocket | null = null
let resizeObserver: ResizeObserver | null = null
let questionPollTimer: ReturnType<typeof setInterval> | null = null

function readVisibleRows(t: Terminal): string[] {
  const buffer = t.buffer.active
  const rows: string[] = []
  for (let i = 0; i < t.rows; i++) {
    const line = buffer.getLine(buffer.viewportY + i)
    rows.push(line ? line.translateToString(true) : '')
  }
  return rows
}

function pollForScreen() {
  if (!term)
    return
  const { question, confirm } = detectScreen(readVisibleRows(term))
  const sig = `${screenSignature(question)}|${screenSignature(confirm)}`
  if (sig === currentSig)
    return
  currentSig = sig
  detectedQuestion.value = question
  detectedConfirm.value = confirm
}

function sendToTerminal(bytes: Uint8Array) {
  socket?.send(bytes)
}

// Keep keyboard focus out of the xterm textarea while the overlay owns
// input, and return it once the overlay clears.
watch(overlayOpen, (open) => {
  if (open)
    term?.blur()
  else
    term?.focus()
})

onMounted(() => {
  const container = containerRef.value
  if (!container)
    return

  term = new Terminal({
    convertEol: false,
    cursorBlink: true,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    screenReaderMode: true,
  })
  const fit = new FitAddon()
  term.loadAddon(fit)
  term.open(container)
  fit.fit()
  // After the hosting modal focused its panel: keys must reach the picker, and xterm keeps Esc from closing the modal.
  void nextTick(() => term?.focus())

  socket = useTerminalSocket(props.pid, {
    onData: bytes => term?.write(bytes),
  })

  term.onData((data) => {
    // While a question overlay is showing, the user answers through the
    // overlay's controls — raw keystrokes must not race those encoded bytes
    // onto the same pty.
    if (overlayOpen.value)
      return
    socket?.send(new TextEncoder().encode(data))
  })

  socket.resize(term.cols, term.rows)

  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(() => {
      fit.fit()
      if (term)
        socket?.resize(term.cols, term.rows)
    })
    resizeObserver.observe(container)
  }

  questionPollTimer = setInterval(pollForScreen, QUESTION_POLL_MS)
})

onBeforeUnmount(() => {
  if (questionPollTimer !== null)
    clearInterval(questionPollTimer)
  resizeObserver?.disconnect()
  socket?.close()
  term?.dispose()
})
</script>

<template>
  <div class="agent-terminal-wrapper">
    <div ref="containerRef" class="agent-terminal" />
    <QuestionOverlay :question="detectedQuestion" :confirm="detectedConfirm" :send="sendToTerminal" />
  </div>
</template>

<style scoped>
.agent-terminal-wrapper {
  position: relative;
  width: 100%;
  height: 100%;
}

.agent-terminal {
  width: 100%;
  height: 100%;
}
</style>
