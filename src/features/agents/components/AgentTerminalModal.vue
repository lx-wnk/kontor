<script setup lang="ts">
import { defineAsyncComponent, useId } from 'vue'
import AppModal from '@/components/ui/AppModal.vue'

defineProps<{ open: boolean, pid: number, title: string }>()
const emit = defineEmits<{ close: [] }>()

const AgentTerminal = defineAsyncComponent(() => import('./AgentTerminal.vue'))
const titleId = useId()
</script>

<template>
  <AppModal :open="open" :z-index="1100" :labelled-by="titleId" @close="emit('close')">
    <div class="bg-raised px-4 py-2.5 flex justify-between items-center flex-shrink-0" @click.stop>
      <span :id="titleId" class="font-semibold text-sm text-fg">
        Terminal — {{ title }}
      </span>
      <button
        type="button"
        aria-label="Close terminal"
        class="bg-transparent border-none text-fg-mute text-base cursor-pointer px-2 py-1 rounded hover:bg-raised hover:text-fg"
        @click.stop="emit('close')"
      >
        ✕
      </button>
    </div>
    <div data-testid="agent-terminal-modal" class="flex-1 min-h-0" @click.stop>
      <AgentTerminal v-if="open" :key="pid" :pid="pid" />
    </div>
  </AppModal>
</template>
