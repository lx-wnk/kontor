<script setup lang="ts">
import type { PipelineTask } from '@/types'
import { computed } from 'vue'
import { STAGE_LABELS } from '@/utils/stageLabels'

const props = defineProps<{ tasks: PipelineTask[] }>()

// The stages a pipeline task walks while work is in flight. The bar is one
// segment each, so "how far along" is answerable at a glance — which a spinner
// never answers.
const TRACK = ['plan_review', 'implementation', 'self_review', 'finalization'] as const

interface Segment { key: string, state: 'done' | 'now' | 'todo' }

function segments(task: PipelineTask): Segment[] {
  const at = TRACK.indexOf(task.currentStage as typeof TRACK[number])
  return TRACK.map((key, i) => ({
    key,
    // A stage off the track reads as NOT started, never as finished. The first
    // version treated it as finished, and a cancelled task — which is off the
    // track — drew four full bars as if it had completed.
    state: at < 0 ? 'todo' : i < at ? 'done' : i === at ? 'now' : 'todo',
  }))
}

const rows = computed(() => props.tasks.map(t => ({
  task: t,
  segments: segments(t),
  label: STAGE_LABELS[t.currentStage] ?? t.currentStage,
})))
</script>

<template>
  <aside class="flex flex-col min-h-0" aria-label="Live work">
    <div v-if="rows.length === 0" class="px-5 py-4 text-[12.5px] text-fg-mute leading-relaxed">
      Nothing is running. A task started from the pipeline appears here while it works.
    </div>

    <div v-else class="flex-grow overflow-y-auto p-4 flex flex-col gap-3">
      <article
        v-for="r in rows"
        :key="r.task.id"
        :data-testid="`live-${r.task.slug}`"
        class="rounded-xl border border-line bg-card p-3 flex flex-col gap-2.5"
      >
        <div class="flex items-center gap-2">
          <span class="flex-grow text-[13.5px] text-fg truncate">{{ r.task.slug }}</span>
        </div>

        <div class="flex gap-1" role="img" :aria-label="`Stage ${r.label}`">
          <span
            v-for="s in r.segments"
            :key="s.key"
            class="h-1 flex-grow rounded-sm"
            :class="{
              'bg-success': s.state === 'done',
              'bg-accent': s.state === 'now',
              'bg-line-strong': s.state === 'todo',
            }"
          />
        </div>

        <div class="text-[12px] text-fg-mute">
          {{ r.label }}
        </div>
      </article>
    </div>

    <p class="px-5 py-3 border-t border-line text-[12px] text-fg-faint leading-snug">
      Reports only. A run that needs you moves to the centre.
    </p>
  </aside>
</template>
