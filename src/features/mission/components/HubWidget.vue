<script setup lang="ts">
import { computed } from 'vue'
import { useAgents } from '@/features/agents'
import { STATUS_ORDER } from '@/utils/agentSort'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { agentDisplayStatus } from '@/utils/statusColors'
import NeedsYouQueue from './NeedsYouQueue.vue'

const { agents } = useAgents({ autoStart: false })

const rows = computed(() =>
  agents.value
    .filter(a => a.status !== 'finished')
    .map(a => ({ a, state: agentDisplayStatus(a) }))
    .sort((x, y) => {
      const xOrder = x.a.working ? -1 : STATUS_ORDER[x.a.status]
      const yOrder = y.a.working ? -1 : STATUS_ORDER[y.a.status]
      return xOrder - yOrder
    }),
)
</script>

<template>
  <section data-testid="hub" aria-label="Zentrale" class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto rounded-xl border border-line bg-card p-4">
    <NeedsYouQueue variant="docked" class="shrink-0" />
    <ul data-testid="hub-list">
      <li v-for="{ a, state } in rows" :key="a.pid" data-testid="hub-agent" class="flex items-center justify-between gap-3 border-b border-line py-1.5 text-[12.5px]">
        <span class="truncate text-fg">{{ friendlyProjectName(a.projectName) }}</span>
        <span :class="state === 'working' || state === 'active' ? 'text-success-text' : 'text-fg-mute'">{{ state }}</span>
      </li>
    </ul>
  </section>
</template>
