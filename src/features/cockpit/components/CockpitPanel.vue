<script setup lang="ts">
import type { PanelState } from '../panelState'

const props = defineProps<{
  id: string
  title: string
  state: PanelState
  icon?: string
  /** The server's own words for denied and failed; the fallback line for the rest. */
  message?: string
}>()

// One place decides which state marker exists, so five panels cannot drift.
const testid = (state: PanelState) => `cockpit-${props.id}-${state}`
</script>

<template>
  <section
    class="bg-card border border-line rounded-xl p-4 flex flex-col gap-3 min-w-0 h-full min-h-0 overflow-hidden"
    :data-testid="`cockpit-panel-${id}`"
    :aria-busy="state === 'loading'"
  >
    <header class="flex items-center gap-1.5">
      <span v-if="icon" aria-hidden="true" class="text-[11px] text-fg-mute">{{ icon }}</span>
      <h2 class="text-[10px] font-semibold uppercase tracking-[0.08em] text-fg-mute">
        {{ title }}
      </h2>
      <span class="flex-1" />
      <slot name="action" />
    </header>
    <div v-if="state === 'ready' && $slots.figure" class="text-[24px] font-semibold leading-none text-fg">
      <slot name="figure" />
    </div>

    <div v-if="state === 'loading'" :data-testid="testid('loading')" class="text-[12px] text-fg-mute" role="status">
      Loading…
    </div>
    <div v-else-if="state === 'notAsked'" :data-testid="testid('notAsked')" class="text-[12px] text-fg-mute">
      {{ message ?? 'Not configured yet.' }}
    </div>
    <div v-else-if="state === 'denied'" :data-testid="testid('denied')" class="text-[12px] rounded-md px-3 py-2 bg-warning-soft text-warning-text">
      {{ message ?? 'This read was refused (HTTP 403).' }}
    </div>
    <div v-else-if="state === 'empty'" :data-testid="testid('empty')" class="text-[12px] text-fg-mute">
      {{ message ?? 'Nothing here yet.' }}
    </div>
    <div v-else-if="state === 'failed'" :data-testid="testid('failed')" class="text-[12px] rounded-md px-3 py-2 bg-danger-soft text-danger-text" role="alert">
      {{ message ?? 'This panel could not load.' }}
    </div>
    <div v-else class="min-w-0 min-h-0 overflow-y-auto">
      <slot />
    </div>
  </section>
</template>
