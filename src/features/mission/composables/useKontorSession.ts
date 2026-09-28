import { effectScope, ref, watch } from 'vue'
import { useAgents } from '@/features/agents'
import { errorMessage, readErrorMessage } from '@/utils/errorMessage'

export type KontorStatus = 'idle' | 'starting' | 'running' | 'error'

const pid = ref<number | null>(null)
const status = ref<KontorStatus>('idle')
const error = ref('')
let agentWatchScope: ReturnType<typeof effectScope> | null = null

const SESSION_URL = '/api/kontor-session'
const JSON_HEADERS = { 'Content-Type': 'application/json' }

async function request(url: string, fallback: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(url, init)
  if (!res.ok)
    throw new Error(await readErrorMessage(res, fallback))
  return res
}

function settle(next: number | null) {
  pid.value = next
  status.value = next === null ? 'idle' : 'running'
  error.value = ''
}

async function refresh(): Promise<void> {
  const fallback = 'Could not read the Kontor session.'
  try {
    const res = await request(SESSION_URL, fallback)
    settle((await res.json() as { pid: number | null }).pid)
  }
  catch (e) {
    status.value = 'error'
    error.value = errorMessage(e, fallback)
  }
}

async function sendMessage(target: number, text: string): Promise<boolean> {
  const fallback = 'Could not reach the Kontor session.'
  error.value = ''
  try {
    await request(`/api/agents/${target}/message`, fallback, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ message: text }) })
    return true
  }
  catch (e) {
    error.value = errorMessage(e, fallback)
    return false
  }
}

async function spawn(url: string, prompt: string): Promise<{ pid: number | null, started: boolean } | null> {
  const fallback = 'Could not start a Kontor session.'
  status.value = 'starting'
  error.value = ''
  try {
    const res = await request(url, fallback, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ prompt }) })
    const json = await res.json() as { pid: number | null, started?: boolean }
    settle(json.pid)
    return { pid: json.pid, started: json.started ?? true }
  }
  catch (e) {
    pid.value = null
    status.value = 'error'
    error.value = errorMessage(e, fallback)
    return null
  }
}

async function start(text: string): Promise<boolean> {
  const result = await spawn(SESSION_URL, text)
  if (result === null || result.pid === null)
    return false
  // started: false means another window's session already ran and won the
  // race — this POST's prompt was not used, so it must go as a message.
  if (!result.started)
    return sendMessage(result.pid, text)
  return true
}

async function renew(text: string): Promise<boolean> {
  const result = await spawn(`${SESSION_URL}/renew`, text)
  return result !== null && result.pid !== null
}

async function send(text: string): Promise<boolean> {
  // POST returns an already-running session unchanged and drops the prompt, so
  // a session this window has not seen yet must be found first or the text is lost.
  if (pid.value === null) {
    await refresh()
    if (status.value === 'error')
      return false
  }
  if (pid.value === null)
    return start(text)
  return sendMessage(pid.value, text)
}

async function end(): Promise<void> {
  const fallback = 'Could not end the Kontor session.'
  error.value = ''
  try {
    await request(SESSION_URL, fallback, { method: 'DELETE' })
    settle(null)
  }
  catch (e) {
    error.value = errorMessage(e, fallback)
  }
}

function watchAgentExit() {
  // Detached so the watch outlives whichever component's setup() happens to
  // call useKontorSession() first — an ordinary watch would be torn down
  // with that component, leaving every later caller with no exit detection.
  if (agentWatchScope)
    return
  agentWatchScope = effectScope(true)
  agentWatchScope.run(() => {
    const { agents } = useAgents({ autoStart: false })
    // Only a live-then-gone flip of the SAME pid counts: a freshly spawned or
    // renewed pid is absent until the scanner lists it.
    watch(
      () => [pid.value, agents.value.some(a => a.pid === pid.value && a.status !== 'finished')] as const,
      ([now, live], [before, wasLive]) => {
        if (now !== null && now === before && wasLive && !live)
          settle(null)
      },
    )
  })
}

export function useKontorSession() {
  watchAgentExit()
  return { pid, status, error, refresh, start, send, end, renew }
}
