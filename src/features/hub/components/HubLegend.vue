<script setup lang="ts">
import type { HubLevel } from '../hubCamera'
import { computed } from 'vue'

const props = defineProps<{ open: boolean, level: HubLevel }>()
const emit = defineEmits<{ toggle: [] }>()

interface LegendRow { svg: string, text: string, minLevel?: HubLevel }

const SWATCH = 'h-3.5 w-3.5 shrink-0'
const ROWS: readonly LegendRow[] = [
  { svg: `<svg viewBox="0 0 14 14" class="${SWATCH}" aria-hidden="true"><circle cx="7" cy="7" r="4" fill="currentColor"/></svg>`, text: 'Note (know-how)' },
  { svg: `<svg viewBox="0 0 14 14" class="${SWATCH}" aria-hidden="true"><circle cx="7" cy="7" r="4" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>`, text: 'Session log' },
  { svg: `<svg viewBox="0 0 14 14" class="${SWATCH}" aria-hidden="true"><circle cx="7" cy="7" r="4" fill="currentColor"/><circle cx="7" cy="7" r="1.8" fill="var(--app)"/></svg>`, text: 'Other kind (folder or frontmatter type)' },
  { svg: `<svg viewBox="0 0 14 14" class="${SWATCH}" aria-hidden="true"><circle cx="4" cy="7" r="3" fill="currentColor"/><circle cx="11" cy="7" r="3" fill="currentColor" fill-opacity="0.35"/></svg>`, text: 'Fainter = older (untouched for longer)' },
  { svg: `<svg viewBox="0 0 14 14" class="${SWATCH}" aria-hidden="true"><path d="M1,10 Q7,2 13,10" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round"/></svg>`, text: 'Know-how links inside a project', minLevel: 1 },
]

const visibleRows = computed(() => ROWS.filter(row => row.minLevel === undefined || props.level >= row.minLevel))
</script>

<template>
  <div class="flex flex-col-reverse items-start">
    <button
      type="button"
      aria-label="Show map legend"
      :aria-expanded="open"
      class="size-[30px] cursor-pointer rounded-[7px] border border-line-strong bg-card text-fg hover:border-accent"
      @click="emit('toggle')"
    >
      ?
    </button>
    <div
      v-if="open"
      role="region"
      aria-label="Map legend"
      data-testid="hub-legend"
      class="mb-1.5 max-w-[16rem] rounded-[10px] border border-line-strong bg-card px-3 py-2.5 text-[11px] text-fg-mute shadow-lg"
    >
      <ul class="flex flex-col gap-1">
        <li v-for="row in visibleRows" :key="row.text" class="flex items-center gap-1.5">
          <span class="text-fg-soft" v-html="row.svg" />
          <span>{{ row.text }}</span>
        </li>
      </ul>
      <p class="mt-1.5">
        Circles = category › project; colour = category, shade = project
      </p>
      <p class="mt-1.5 text-fg-faint">
        Press ? to toggle
      </p>
    </div>
  </div>
</template>
