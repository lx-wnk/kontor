<script setup lang="ts">
import type { InsightsSessionRow, InsightsSessionsPage, InsightsSort } from '../insightsApi'
import { computed } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import { formatCost, formatTokens, formatUptime } from '@/utils/format'
import { estimateMark } from '../estimateMark'

const props = defineProps<{
  page: InsightsSessionsPage
  sort: InsightsSort
  order: 'asc' | 'desc'
}>()

const emit = defineEmits<{
  sort: [key: InsightsSort]
  page: [offset: number]
  open: [row: InsightsSessionRow, el: HTMLElement]
}>()

const columns: { label: string, sort?: InsightsSort, right?: boolean }[] = [
  { label: 'Title' },
  { label: 'Project' },
  { label: 'Start', sort: 'started_at' },
  { label: 'Cost', sort: 'cost', right: true },
  { label: 'Agent time', sort: 'agent_ms', right: true },
  { label: 'Tokens', sort: 'tokens', right: true },
  { label: 'Turns', sort: 'turns', right: true },
  { label: 'Lines', sort: 'lines', right: true },
]

function ariaSort(key?: InsightsSort): 'ascending' | 'descending' | 'none' | undefined {
  if (!key)
    return undefined
  if (key !== props.sort)
    return 'none'
  return props.order === 'asc' ? 'ascending' : 'descending'
}

const rowTokens = (r: InsightsSessionRow) => r.inputTokens + r.outputTokens + r.cacheReadTokens + r.cacheCreationTokens
const day = (iso: string | null) => iso ? iso.slice(0, 10) : '—'

const hasPrev = computed(() => props.page.offset > 0)
const hasNext = computed(() => props.page.offset + props.page.limit < props.page.total)
const from = computed(() => props.page.total === 0 ? 0 : props.page.offset + 1)
const to = computed(() => Math.min(props.page.offset + props.page.limit, props.page.total))

function open(row: InsightsSessionRow, event: Event) {
  emit('open', row, event.currentTarget as HTMLElement)
}
</script>

<template>
  <div>
    <div class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="text-left text-xs text-fg-mute border-b border-line">
            <th
              v-for="c in columns"
              :key="c.label"
              scope="col"
              class="py-2 px-2 font-medium"
              :class="c.right && 'text-right'"
              :aria-sort="ariaSort(c.sort)"
            >
              <button
                v-if="c.sort"
                type="button"
                class="cursor-pointer bg-transparent border-0 font-medium text-fg-mute hover:text-fg-soft focus-visible:ring-2 focus-visible:ring-accent"
                :data-testid="`sort-${c.sort}`"
                @click="emit('sort', c.sort)"
              >
                {{ c.label }}<span v-if="c.sort === sort" aria-hidden="true"> {{ order === 'asc' ? '▲' : '▼' }}</span>
              </button>
              <template v-else>
                {{ c.label }}
              </template>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="r in page.rows"
            :key="r.sessionId"
            tabindex="0"
            class="border-b border-line cursor-pointer hover:bg-raised focus-visible:ring-2 focus-visible:ring-accent"
            @click="open(r, $event)"
            @keydown.enter="open(r, $event)"
          >
            <td class="py-2 px-2 max-w-[22rem] truncate text-fg">
              {{ r.title || r.sessionId }}
            </td>
            <td class="py-2 px-2 text-fg-mute">
              {{ r.projectName }}
            </td>
            <td class="py-2 px-2 text-fg-mute whitespace-nowrap">
              {{ day(r.startedAt) }}
            </td>
            <td class="py-2 px-2 text-right tabular-nums" :title="estimateMark(r.costSource, r.costUnknownModel).title">
              {{ estimateMark(r.costSource, r.costUnknownModel).prefix }}<span v-if="estimateMark(r.costSource, r.costUnknownModel).srLabel" class="sr-only">{{ estimateMark(r.costSource, r.costUnknownModel).srLabel }}</span>{{ formatCost(r.costUsd) }}
            </td>
            <td class="py-2 px-2 text-right tabular-nums" :title="estimateMark(r.agentTimeSource).title">
              {{ estimateMark(r.agentTimeSource).prefix }}<span v-if="estimateMark(r.agentTimeSource).srLabel" class="sr-only">{{ estimateMark(r.agentTimeSource).srLabel }}</span>{{ formatUptime(Math.floor(r.agentMs / 1000)) }}
            </td>
            <td class="py-2 px-2 text-right tabular-nums">
              {{ formatTokens(rowTokens(r)) }}
            </td>
            <td class="py-2 px-2 text-right tabular-nums">
              {{ r.turns }}
            </td>
            <td class="py-2 px-2 text-right tabular-nums whitespace-nowrap">
              <span class="text-success">+{{ r.linesAdded }}</span> / <span class="text-danger">−{{ r.linesRemoved }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="flex items-center justify-end gap-2 mt-3 text-xs text-fg-mute">
      <span>{{ from }}–{{ to }} of {{ page.total }}</span>
      <AppButton size="sm" data-testid="page-prev" :disabled="!hasPrev" @click="emit('page', Math.max(0, page.offset - page.limit))">
        Previous
      </AppButton>
      <AppButton size="sm" data-testid="page-next" :disabled="!hasNext" @click="emit('page', page.offset + page.limit)">
        Next
      </AppButton>
    </div>
  </div>
</template>
