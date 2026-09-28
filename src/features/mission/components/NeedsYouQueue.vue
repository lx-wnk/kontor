<script setup lang="ts">
import type { NextKind } from '../composables/useNextThing'
import { computed, inject, ref, watch } from 'vue'
import { NEEDS_YOU, OPEN_TASK, PENDING_PERMISSIONS } from '@/composables/openTask'
import NextThing from './NextThing.vue'

const props = defineProps<{ variant: 'docked' | 'strip', kinds?: NextKind[] }>()

// App.vue ranks once per tick and provides the list, and owns navigation —
// both are provided by App for every place a queue renders.
const needsYou = inject(NEEDS_YOU)
const openTask = inject(OPEN_TASK)
if (!needsYou || !openTask)
  throw new Error('NeedsYouQueue requires NEEDS_YOU and OPEN_TASK from App.vue')
// App.vue provides the one usePendingPermissions(tasks) instance — a local
// call here would open a second, out-of-sync cache.
const pendingPermissions = inject(PENDING_PERMISSIONS)
if (!pendingPermissions)
  throw new Error('NeedsYouQueue requires PENDING_PERMISSIONS to be provided by App.vue')
const { refresh } = pendingPermissions

const ranked = computed(() => props.kinds ? needsYou.value.filter(t => props.kinds!.includes(t.kind)) : needsYou.value)
const index = ref(0)
watch(() => ranked.value.length, (n) => {
  if (index.value >= n)
    index.value = 0
})
const current = computed(() => ranked.value[index.value] ?? null)

function step(by: number) {
  const n = ranked.value.length
  index.value = (index.value + by + n) % n
}
</script>

<template>
  <section
    v-if="variant === 'docked' || ranked.length > 0"
    data-testid="needs-you"
    :data-variant="variant"
    aria-label="Needs you"
    :role="variant === 'strip' ? 'status' : undefined"
    :aria-live="variant === 'strip' ? 'polite' : undefined"
    :class="variant === 'strip' ? 'rounded-xl border border-warning-line bg-warning-soft px-4 py-3' : ''"
  >
    <div v-if="ranked.length > 1" class="mb-2 flex items-center gap-2 text-[11px] uppercase tracking-widest text-warning-text">
      <span>Needs you</span>
      <span data-testid="needs-you-position">{{ index + 1 }} of {{ ranked.length }}</span>
      <span class="flex-grow" />
      <button type="button" data-testid="needs-you-prev" aria-label="Previous" class="rounded border border-line-strong px-1.5" @click="step(-1)">
        ‹
      </button>
      <button type="button" data-testid="needs-you-next" aria-label="Next" class="rounded border border-line-strong px-1.5" @click="step(1)">
        ›
      </button>
    </div>
    <NextThing :next="current" @resolved="refresh" @open="openTask" />
  </section>
</template>
