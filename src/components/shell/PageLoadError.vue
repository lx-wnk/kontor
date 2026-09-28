<script setup lang="ts">
defineOptions({ inheritAttrs: false })
defineProps<{ error?: Error }>()

function reload() {
  window.location.reload()
}
</script>

<template>
  <div data-testid="page-load-error" class="flex flex-col items-center gap-3 py-12 text-center">
    <p class="text-danger-text">
      This page could not load — reload the app.
    </p>
    <button
      type="button"
      class="rounded-md bg-accent px-4 py-1.5 text-sm font-medium text-white hover:bg-accent/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2"
      @click="reload"
    >
      Reload
    </button>
  </div>
</template>
