<script setup lang="ts">
import type { Agent } from '@/types'
import { computed } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppChip from '@/components/ui/AppChip.vue'
import { useAgents } from '@/features/agents'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { agentDisplayStatus, agentStatusTone, statusLabel } from '@/utils/statusColors'

const props = defineProps<{ agent: Agent }>()
const emit = defineEmits<{ close: [] }>()

const { selectAgent } = useAgents({ autoStart: false })
const name = computed(() => friendlyProjectName(props.agent.projectName))
const state = computed(() => agentDisplayStatus(props.agent))
</script>

<template>
  <div
    data-hub-layer
    role="dialog"
    :aria-label="name"
    class="absolute right-[50px] top-[92px] z-30 w-[270px] rounded-[10px] border border-line-strong bg-card px-3 py-2.5 shadow-lg"
  >
    <button type="button" aria-label="Close card" class="absolute right-2 top-1.5 cursor-pointer text-fg-mute hover:text-fg" @click="emit('close')">
      ✕
    </button>
    <h4 class="mb-1 pr-5 text-[13px] font-semibold text-fg">
      {{ name }}
    </h4>
    <AppChip data-testid="hub-card-state" :tone="agentStatusTone(state)">
      {{ statusLabel(state) }}
    </AppChip>
    <p class="my-2 line-clamp-4 whitespace-pre-wrap border-l-2 border-line-strong pl-2 text-[12px] text-fg-mute">
      {{ agent.lastOutput || 'No output yet.' }}
    </p>
    <AppButton variant="primary" size="sm" @click="selectAgent(agent)">
      Open session
    </AppButton>
  </div>
</template>
