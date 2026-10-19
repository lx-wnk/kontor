<script setup lang="ts">
import type { PanelState } from '../panelState'
import type { Agent } from '@/types'
import { computed } from 'vue'
import { useAgents } from '@/features/agents'
import CockpitPanel from './CockpitPanel.vue'

// autoStart: false — useAgents holds module-level state, so this is the stream
// App.vue already started, never a second one.
const { agents, isLoading, error, selectAgent } = useAgents({ autoStart: false })

function agentLabel(a: Agent) {
  return a.sessionTitle ? `${a.projectName} - ${a.sessionTitle}` : a.projectName
}

// No denied and no notAsked: the agent stream is not gated, and it streams
// from mount. Rendering branches that cannot occur would make the five-state
// assertion meaningless for this panel.
const state = computed<PanelState>(() => {
  if (error.value)
    return 'failed'
  if (isLoading.value)
    return 'loading'
  return agents.value.length === 0 ? 'empty' : 'ready'
})
</script>

<template>
  <CockpitPanel id="agents" title="Agents" icon="◉" :state="state" :message="error ?? 'No agent is running right now.'">
    <ul class="flex flex-col gap-1.5">
      <li v-for="a in agents.slice(0, 6)" :key="a.sessionId">
        <button
          type="button"
          class="flex items-center justify-between gap-2 text-[12px] min-w-0 w-full text-left rounded px-1 -mx-1 hover:bg-raised focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent cursor-pointer"
          :title="agentLabel(a)"
          :aria-label="`${agentLabel(a)}, ${a.status}`"
          :data-testid="`cockpit-agent-${a.sessionId}`"
          @click="selectAgent(a)"
        >
          <span class="truncate text-fg">{{ agentLabel(a) }}</span>
          <span class="shrink-0 text-fg-mute">{{ a.status }}</span>
        </button>
      </li>
    </ul>
  </CockpitPanel>
</template>
