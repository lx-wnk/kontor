<script setup lang="ts">
import type { NextKind } from '../composables/useNextThing'
import { computed, inject, nextTick, ref, watch } from 'vue'
import { NEEDS_YOU, NEEDS_YOU_FOCUS, OPEN_TASK, PENDING_PERMISSIONS } from '@/composables/openTask'
import { markSeen } from '../composables/useAttentionMarks'
import NextThing from './NextThing.vue'

const props = defineProps<{ variant: 'docked' | 'strip', kinds?: NextKind[] }>()

// App.vue ranks once per tick, owns navigation, and provides both to every queue via inject.
const needsYou = inject(NEEDS_YOU)
const openTask = inject(OPEN_TASK)
const focusedKey = inject(NEEDS_YOU_FOCUS)
if (!needsYou || !openTask || !focusedKey)
  throw new Error('NeedsYouQueue requires NEEDS_YOU, NEEDS_YOU_FOCUS and OPEN_TASK from App.vue')
// App.vue provides the one usePendingPermissions(tasks) instance — a local
// call here would open a second, out-of-sync cache.
const pendingPermissions = inject(PENDING_PERMISSIONS)
if (!pendingPermissions)
  throw new Error('NeedsYouQueue requires PENDING_PERMISSIONS to be provided by App.vue')
const { refresh } = pendingPermissions

const ranked = computed(() => props.kinds ? needsYou.value.filter(t => props.kinds!.includes(t.kind)) : needsYou.value)

const root = ref<HTMLElement | null>(null)
watch(focusedKey, async (key) => {
  if (!key)
    return
  await nextTick()
  Array.from(root.value?.querySelectorAll<HTMLElement>('[data-key]') ?? []).find(el => el.dataset.key === key)?.scrollIntoView({ block: 'nearest' })
})
</script>

<template>
  <section
    v-if="variant === 'docked' || ranked.length > 0"
    ref="root"
    data-testid="needs-you"
    :data-variant="variant"
    aria-label="Needs you"
    :role="variant === 'strip' ? 'status' : undefined"
    :aria-live="variant === 'strip' ? 'polite' : undefined"
    :class="variant === 'strip' ? 'rounded-xl border border-warning-line bg-warning-soft px-4 py-3' : ''"
  >
    <template v-if="ranked.length > 0">
      <h2 class="mb-2 text-[11px] uppercase tracking-widest text-warning-text">
        Needs you · <span data-testid="needs-you-count">{{ ranked.length }}</span>
      </h2>
      <ul class="flex flex-col gap-3">
        <li v-for="(t, i) in ranked" :key="t.key" :data-key="t.key" :aria-current="t.key === focusedKey ? 'true' : undefined" class="flex flex-col gap-2 rounded-lg" :class="t.key === focusedKey ? 'ring-2 ring-accent' : ''">
          <NextThing :next="t" :position="i" @resolved="refresh" @open="openTask" />
          <button
            type="button"
            data-testid="needs-you-put-away"
            aria-label="Put away until the agent posts something new"
            class="self-end rounded border border-line-strong px-2 py-1 text-[12px] text-fg-mute focus-visible:outline-2 focus-visible:outline-accent"
            @click="markSeen(t.key, t.since)"
          >
            Put away
          </button>
        </li>
      </ul>
    </template>
    <NextThing v-else :next="null" />
  </section>
</template>
