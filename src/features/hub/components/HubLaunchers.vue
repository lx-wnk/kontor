<script setup lang="ts">
import type { Camera } from '../hubCamera'
import type { Launcher } from '../hubLaunchers'
import { useTimeoutFn } from '@vueuse/core'
import { ref, watch } from 'vue'
import { toScreen } from '../hubCamera'
import { LAUNCHER_RING_RADIUS, polar } from '../hubGeometry'
import { launcherSlotDeg } from '../hubLaunchers'

const props = defineProps<{ launchers: Launcher[], cam: Camera, docked: boolean }>()
defineEmits<{ launch: [launcher: Launcher] }>()

const DOCK_X = 30
const DOCK_TOP = 150
const DOCK_STEP = 46

// Eased only across a dock switch: a standing transition would trail every pan and zoom frame.
const easing = ref(false)
const { start: stopEasing } = useTimeoutFn(() => {
  easing.value = false
}, 420, { immediate: false })
watch(() => props.docked, () => {
  easing.value = true
  stopEasing()
})

function at(i: number) {
  const [x, y] = props.docked
    ? [DOCK_X, DOCK_TOP + i * DOCK_STEP]
    : toScreen(props.cam, ...polar(LAUNCHER_RING_RADIUS, launcherSlotDeg(i)))
  return { transform: `translate(${x}px, ${y}px)` }
}
</script>

<template>
  <div class="pointer-events-none absolute inset-0">
    <button
      v-for="(launcher, i) in launchers"
      :key="launcher.id"
      type="button"
      :data-testid="`hub-launcher-${launcher.id}`"
      :aria-label="launcher.label"
      :title="`${launcher.label} (${i + 1})`"
      class="group pointer-events-auto absolute left-0 top-0 flex size-10 -translate-1/2 cursor-pointer items-center justify-center rounded-full border bg-card text-[14px] outline-none hover:border-accent focus-visible:border-accent"
      :class="[
        launcher.kind === 'new-page' ? 'border-dashed border-line-strong text-accent' : 'border-line-strong text-fg',
        easing && 'motion-safe:transition-transform motion-safe:duration-[380ms] motion-safe:ease-out',
      ]"
      :style="at(i)"
      @click="$emit('launch', launcher)"
    >
      <span aria-hidden="true">{{ launcher.icon }}</span>
      <span
        aria-hidden="true"
        class="pointer-events-none absolute hidden whitespace-nowrap rounded-[5px] border border-line-strong bg-card px-1.5 text-[10px] text-fg group-hover:block group-focus-visible:block"
        :class="docked ? 'left-[46px] top-2.5' : 'left-1/2 top-[43px] -translate-x-1/2'"
      >{{ launcher.label }}</span>
    </button>
  </div>
</template>
