<script setup lang="ts">
import type { SettingView } from '@/features/settings/composables/useSettings'
import { computed, onMounted, ref } from 'vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { RestartError, useServerReconnect } from '@/composables/useServerReconnect'
import { toast } from '@/composables/useToast'
import { useSettings } from '@/features/settings/composables/useSettings'
import { errorMessage } from '@/utils/errorMessage'

const { items, loading, refetch, update } = useSettings()
const { triggerRestart } = useServerReconnect()
const saving = ref<string | null>(null)
const rebuilding = ref(false)
const rebuildOutput = ref<string | null>(null)

const notice = ref<{ kind: 'success' | 'warning', text: string } | null>(null)
let noticeTimer: ReturnType<typeof setTimeout> | null = null

function showNotice(kind: 'success' | 'warning', text: string) {
  notice.value = { kind, text }
  if (noticeTimer)
    clearTimeout(noticeTimer)
  noticeTimer = setTimeout(() => (notice.value = null), 5000)
}

// Categories edited on their own surface. A raw JSON field for the workspace
// here would be a second editor that bypasses the page's placement rules.
const HIDDEN_CATEGORIES = new Set(['workspace'])

const groups = computed(() => {
  const byCategory = new Map<string, SettingView[]>()
  for (const item of items.value.filter(i => !HIDDEN_CATEGORIES.has(i.category))) {
    const list = byCategory.get(item.category) ?? []
    list.push(item)
    byCategory.set(item.category, list)
  }
  return [...byCategory.entries()]
    .map(([category, settings]) => ({ category, settings }))
    .sort((a, b) => a.category.localeCompare(b.category))
})

function restartNoticeText(key: string, value: string): string {
  if (key === 'auth.mode' && value === 'plugin')
    return `${key}: applies after a server restart — and 'plugin' will require login`
  return `${key}: applies after a server restart`
}

async function apply(item: SettingView, value: string) {
  saving.value = item.key
  try {
    const applied = await update(item.key, value)
    if (applied === 'restart')
      showNotice('warning', restartNoticeText(item.key, value))
    else
      showNotice('success', `${item.key} updated`)
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to save setting'))
  }
  finally {
    saving.value = null
  }
}

function onCheckbox(item: SettingView, e: Event) {
  apply(item, String((e.target as HTMLInputElement).checked))
}

function onValue(item: SettingView, e: Event) {
  apply(item, (e.target as HTMLInputElement | HTMLSelectElement).value)
}

function enumOptions(item: SettingView) {
  return (item.enum ?? []).map(opt => ({ value: opt, label: opt }))
}

function onSelectChange(item: SettingView, value: string) {
  apply(item, value)
}

async function onRebuildAndRestart() {
  rebuilding.value = true
  rebuildOutput.value = null
  try {
    await triggerRestart({ rebuild: true })
  }
  catch (e) {
    if (e instanceof RestartError && e.output)
      rebuildOutput.value = e.output
    toast.error(errorMessage(e, 'Rebuild failed'))
  }
  finally {
    rebuilding.value = false
  }
}

onMounted(refetch)
</script>

<template>
  <div class="space-y-6">
    <div>
      <h3 class="text-sm font-semibold text-fg-soft">
        Server settings
      </h3>
      <p class="text-xs text-fg-mute mt-0.5">
        DB-backed runtime configuration. Some changes apply live; others take effect after a
        server restart.
      </p>
    </div>

    <div class="flex items-center gap-3">
      <button
        type="button"
        data-testid="rebuild-restart"
        class="text-xs rounded-md border border-line px-3 py-1.5 hover:bg-raised disabled:opacity-50 disabled:cursor-not-allowed"
        :disabled="rebuilding"
        @click="onRebuildAndRestart"
      >
        {{ rebuilding ? 'Rebuilding…' : 'Rebuild and restart' }}
      </button>
    </div>

    <pre
      v-if="rebuildOutput"
      data-testid="rebuild-output"
      class="text-xs rounded-md px-3 py-2 bg-danger-soft text-danger-text whitespace-pre-wrap"
    >{{ rebuildOutput }}</pre>

    <div
      v-if="notice"
      role="status"
      aria-live="polite"
      class="text-xs rounded-md px-3 py-2"
      :class="notice.kind === 'warning' ? 'bg-warning-soft text-warning-text' : 'bg-success-soft text-success-text'"
    >
      {{ notice.text }}
    </div>

    <div role="status" aria-live="polite" aria-atomic="true" class="text-xs text-fg-faint" :class="{ 'sr-only': !loading }">
      {{ loading ? 'Loading…' : '' }}
    </div>

    <div v-if="!loading" class="space-y-6">
      <div v-for="group in groups" :key="group.category">
        <h4 class="text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-2">
          {{ group.category }}
        </h4>
        <ul class="border border-line rounded-lg divide-y divide-line text-xs">
          <li
            v-for="item in group.settings"
            :key="item.key"
            class="flex items-center justify-between gap-3 px-3 py-2.5"
          >
            <div class="min-w-0">
              <p class="font-medium text-fg font-mono">
                {{ item.key }}
              </p>
              <p class="text-fg-faint text-[10px] mt-0.5">
                {{ item.apply === 'restart' ? 'Restart required' : 'Applies live' }} · default {{ item.default || '—' }}
              </p>
            </div>

            <input
              v-if="item.type === 'bool'"
              type="checkbox"
              :checked="item.value === 'true'"
              :aria-label="item.key"
              :disabled="saving === item.key"
              class="h-4 w-4 shrink-0 cursor-pointer accent-accent"
              @change="onCheckbox(item, $event)"
            >
            <AppSelect
              v-else-if="item.type === 'enum'"
              :model-value="item.value"
              :options="enumOptions(item)"
              :aria-label="item.key"
              :disabled="saving === item.key"
              class="w-44 shrink-0"
              @update:model-value="onSelectChange(item, $event)"
            />
            <input
              v-else-if="item.type === 'int' || item.type === 'float'"
              type="number"
              :value="item.value"
              :step="item.type === 'float' ? 'any' : '1'"
              :aria-label="item.key"
              :disabled="saving === item.key"
              class="w-44 shrink-0 bg-card border border-line rounded px-2 py-1 text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
              @change="onValue(item, $event)"
            >
            <input
              v-else
              type="text"
              :value="item.value"
              :aria-label="item.key"
              :disabled="saving === item.key"
              class="w-44 shrink-0 bg-card border border-line rounded px-2 py-1 text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
              @change="onValue(item, $event)"
            >
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>
