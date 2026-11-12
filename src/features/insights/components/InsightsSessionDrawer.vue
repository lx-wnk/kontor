<script setup lang="ts">
import type { InsightsSessionRow } from '../insightsApi'
import type { DAGData } from '@/sdk.generated'
import { computed, onUnmounted, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppModal from '@/components/ui/AppModal.vue'
import { SessionDagChart } from '@/features/workflows'
import { formatCost, formatTokens, formatUptime } from '@/utils/format'
import { estimateMark } from '../estimateMark'

const props = defineProps<{ row: InsightsSessionRow | null, canOpen: boolean }>()
const emit = defineEmits<{ close: [], navigate: [sessionId: string] }>()

const TITLE_ID = 'insights-drawer-title'

const dag = ref<DAGData | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
let controller: AbortController | null = null

async function loadDag(sessionId: string) {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  dag.value = null
  error.value = null
  loading.value = true
  try {
    const res = await fetch(`/api/visualizations/dag?${new URLSearchParams({ session: sessionId })}`, { signal: ctrl.signal })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    dag.value = await res.json() as DAGData
    loading.value = false
  }
  catch (e) {
    if (ctrl.signal.aborted)
      return
    error.value = e instanceof Error ? e.message : String(e)
    loading.value = false
  }
}

watch(() => props.row?.sessionId, (id) => {
  if (id)
    loadDag(id)
  else
    controller?.abort()
}, { immediate: true })

onUnmounted(() => controller?.abort())

const costMark = computed(() => props.row ? estimateMark(props.row.costSource, props.row.costUnknownModel) : estimateMark(''))
const agentMark = computed(() => props.row ? estimateMark(props.row.agentTimeSource) : estimateMark(''))
const modelCosts = computed(() => Object.entries(props.row?.modelCosts ?? {}))
const stamp = (iso: string | null) => iso ? iso.replace('T', ' ').slice(0, 16) : '—'
</script>

<template>
  <AppModal :open="row !== null" size="drawer" width="720px" :labelled-by="TITLE_ID" @close="emit('close')">
    <template v-if="row">
      <header class="flex items-start justify-between gap-3 p-4 border-b border-line">
        <div class="min-w-0">
          <h2 :id="TITLE_ID" class="text-base font-semibold text-fg break-words">
            {{ row.title || row.sessionId }}
          </h2>
          <p class="text-xs text-fg-mute">
            {{ row.projectName }}
          </p>
        </div>
        <AppButton size="sm" variant="ghost" aria-label="Close" @click="emit('close')">
          ✕
        </AppButton>
      </header>
      <div class="flex-1 overflow-y-auto p-4 space-y-4 text-sm">
        <dl class="grid grid-cols-2 gap-x-4 gap-y-2">
          <div>
            <dt class="text-xs text-fg-mute">
              Started
            </dt>
            <dd>{{ stamp(row.startedAt) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Last activity
            </dt>
            <dd>{{ stamp(row.recordedAt) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Cost
            </dt>
            <dd data-testid="drawer-cost" :title="costMark.title">
              {{ costMark.prefix }}<span v-if="costMark.srLabel" class="sr-only">{{ costMark.srLabel }}</span>{{ formatCost(row.costUsd) }}
            </dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Agent time
            </dt>
            <dd data-testid="drawer-agent" :title="agentMark.title">
              {{ agentMark.prefix }}<span v-if="agentMark.srLabel" class="sr-only">{{ agentMark.srLabel }}</span>{{ row.agentTimeSource === 'legacy' ? '—' : formatUptime(Math.floor(row.agentMs / 1000)) }} ({{ row.agentTimeSource }})
            </dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Turns
            </dt>
            <dd>{{ row.turns }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Lines
            </dt>
            <dd><span class="text-success">+{{ row.linesAdded }}</span> / <span class="text-danger">−{{ row.linesRemoved }}</span></dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Input tokens
            </dt>
            <dd>{{ formatTokens(row.inputTokens) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Output tokens
            </dt>
            <dd>{{ formatTokens(row.outputTokens) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Cache read
            </dt>
            <dd>{{ formatTokens(row.cacheReadTokens) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-fg-mute">
              Cache write
            </dt>
            <dd>{{ formatTokens(row.cacheCreationTokens) }}</dd>
          </div>
        </dl>
        <div>
          <div class="text-xs text-fg-mute">
            Model
          </div>
          <ul v-if="modelCosts.length" class="space-y-0.5">
            <li v-for="[model, cost] in modelCosts" :key="model" class="flex justify-between">
              <span>{{ model }}</span><span class="tabular-nums">{{ formatCost(cost) }}</span>
            </li>
          </ul>
          <div v-else>
            {{ row.model || '—' }}
          </div>
        </div>
        <SessionDagChart :data="dag" :loading="loading" :error="error" />
      </div>
      <footer v-if="canOpen" class="p-4 border-t border-line">
        <AppButton variant="primary" data-testid="open-session" @click="emit('navigate', row.sessionId)">
          Open session
        </AppButton>
      </footer>
    </template>
  </AppModal>
</template>
