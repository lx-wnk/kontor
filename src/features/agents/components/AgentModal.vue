<script setup lang="ts">
import type { Agent } from '@/types'
import AppModal from '@/components/ui/AppModal.vue'
import { PluginSlot } from '@/features/plugins'
import AgentSessionPane from './AgentSessionPane.vue'

defineProps<{ agent: Agent | null }>()

const emit = defineEmits<{ close: [], navigate: [taskId: string] }>()

// Escape is handled by AppModal's @keydown.escape on its backdrop — no window listener needed.
</script>

<template>
  <AppModal :open="!!agent" :z-index="1000" :labelled-by="agent ? `agent-modal-title-${agent.pid}` : undefined" @close="emit('close')">
    <template v-if="agent">
      <AgentSessionPane :agent="agent" autofocus @navigate="emit('navigate', $event)">
        <template #actions>
          <button type="button" aria-label="Close" class="bg-transparent border-none text-fg-mute text-base cursor-pointer px-2 py-1 rounded hover:bg-slate-200 dark:hover:bg-slate-700 hover:text-fg" @click="emit('close')">
            ✕
          </button>
        </template>
      </AgentSessionPane>
      <PluginSlot name="agent-modal-footer" :ctx="{ agent }" />
    </template>
  </AppModal>
</template>
