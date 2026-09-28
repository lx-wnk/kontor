<script setup lang="ts">
defineProps<{
  expanded: boolean
  theme: 'dark' | 'light'
  canInstall: boolean
}>()
defineEmits<{
  openSessions: []
  toggleTheme: []
  install: []
  openSettings: []
}>()
</script>

<template>
  <div class="mt-auto border-t border-line pt-2 px-1.5 flex flex-col gap-2">
    <button
      v-if="canInstall"
      type="button"
      data-testid="footer-install"
      class="flex items-center gap-2 rounded-lg px-2 min-h-[36px] text-[12px] text-fg-mute hover:text-fg hover:bg-raised transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
      :class="expanded ? 'w-full' : 'w-full justify-center'"
      :title="!expanded ? 'Install PWA' : undefined"
      @click="$emit('install')"
    >
      <span aria-hidden="true">⬇</span><span v-if="expanded">Install PWA</span><span v-else class="sr-only">Install PWA</span>
    </button>
    <!-- One column in both states. Laying these three out in a row when
         expanded made the footer ~80px shorter, and the last nav group is
         bottom-anchored (mt-auto), so every Insights item slid that far up the
         moment the panel opened — under a pointer already travelling toward it. -->
    <div data-testid="footer-actions" class="flex flex-col items-stretch gap-1">
      <button
        type="button"
        data-testid="footer-sessions"
        class="flex items-center gap-2 rounded-lg px-2 min-h-[36px] text-[12px] text-fg-mute hover:text-fg hover:bg-raised transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
        :class="expanded ? 'w-full' : 'w-full justify-center'"
        :title="!expanded ? 'Sessions' : undefined"
        @click="$emit('openSessions')"
      >
        <span aria-hidden="true">🕘</span><span v-if="expanded">Sessions</span><span v-else class="sr-only">Sessions</span>
      </button>
      <button
        type="button"
        data-testid="footer-settings"
        class="w-full flex items-center justify-center rounded-lg px-2 min-h-[36px] min-w-[36px] text-[14px] text-fg-mute hover:text-fg hover:bg-raised transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
        aria-label="Settings"
        @click="$emit('openSettings')"
      >
        <span aria-hidden="true">⚙</span>
      </button>
      <button
        type="button"
        data-testid="footer-theme"
        class="w-full flex items-center justify-center rounded-lg px-2 min-h-[36px] min-w-[36px] text-[14px] text-fg-mute hover:text-fg hover:bg-raised transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
        :aria-label="theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'"
        @click="$emit('toggleTheme')"
      >
        <span aria-hidden="true">{{ theme === 'dark' ? '☀' : '🌙' }}</span>
      </button>
    </div>
  </div>
</template>
