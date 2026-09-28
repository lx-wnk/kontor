<script setup lang="ts">
import type { SettingView } from '@/features/settings/composables/useSettings'
import { computed, onMounted, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { toast } from '@/composables/useToast'
import { useGrants } from '@/features/settings/composables/useGrants'
import { useSettings } from '@/features/settings/composables/useSettings'
import { errorMessage } from '@/utils/errorMessage'

const emit = defineEmits<{ openGrants: [] }>()

const KEY_BASE_URL = 'obsidian.baseURL'
const KEY_VAULT_ROOT = 'obsidian.vaultRoot'
const KEY_API_KEY = 'obsidian.apiKey'
const KEY_TLS_MODE = 'obsidian.tlsMode'

// Fallback only for the brief window before the first GET /api/settings
// resolves; the registry's real enum (server/internal/settings/registry.go)
// is what actually renders once items load, so this list can't drift from it.
const FALLBACK_TLS_MODES = ['verify', 'pinned', 'insecure-loopback']

interface ObsidianFormState {
  baseURL: string
  vaultRoot: string
  apiKey: string
  tlsMode: string
}

const { items, loading, refetch, update } = useSettings()

const form = ref<ObsidianFormState>({ baseURL: '', vaultRoot: '', apiKey: '', tlsMode: 'verify' })
const saving = ref(false)
const vaultRootError = ref<string | null>(null)

const tlsModeOptions = computed(() => {
  const enumValues = items.value.find(i => i.key === KEY_TLS_MODE)?.enum ?? FALLBACK_TLS_MODES
  return enumValues.map(value => ({ value, label: value }))
})

// Seeded exactly once from whatever the server first reports — items updates
// again after every save (useSettings.update patches the array in place),
// and re-seeding on that would clobber whatever the user is mid-typing.
const seeded = ref(false)
watch(items, (list: SettingView[]) => {
  if (seeded.value)
    return
  const byKey = new Map(list.map(i => [i.key, i]))
  const baseURL = byKey.get(KEY_BASE_URL)
  if (!baseURL)
    return
  form.value = {
    baseURL: baseURL.value,
    vaultRoot: byKey.get(KEY_VAULT_ROOT)?.value ?? '',
    apiKey: byKey.get(KEY_API_KEY)?.value ?? '',
    tlsMode: byKey.get(KEY_TLS_MODE)?.value ?? 'verify',
  }
  seeded.value = true
}, { immediate: true })

onMounted(refetch)

// baseURL, vaultRoot and apiKey are a required trio server-side
// (serverapp.buildObsidianClient) — some-but-not-all set is a state the
// server refuses to BOOT with, and the dashboard is what dies, so the save
// is blocked rather than warned about. All three empty stays allowed: that
// is the working "off" switch, and clearing the API key field really does
// clear the stored secret (settings.Service.Set treats "" on a secret as a
// clear, and the mask sentinel as "leave unchanged").
const trioComplete = computed(() => {
  const setCount = [form.value.baseURL, form.value.vaultRoot, form.value.apiKey].filter(v => v !== '').length
  return setCount === 0 || setCount === 3
})

const configured = ref(false)
const reachable = ref<boolean | null>(null)
const statusError = ref<string | null>(null)
const statusHint = ref<string | null>(null)

async function fetchStatus() {
  try {
    const res = await fetch('/api/obsidian/status')
    if (!res.ok)
      return
    const data = await res.json() as { configured: boolean, reachable?: boolean, error?: string, hint?: string }
    configured.value = data.configured
    reachable.value = data.reachable ?? null
    statusError.value = data.error ?? null
    statusHint.value = data.hint ?? null
  }
  catch {
    // Status is advisory (only gates the Index now button); a network hiccup
    // leaves it at its previous value instead of surfacing a toast.
  }
}

onMounted(fetchStatus)

async function save() {
  if (!trioComplete.value)
    return
  saving.value = true
  vaultRootError.value = null
  try {
    // obsidian.apiKey always reads back as the mask sentinel once it is set;
    // sending it back untouched is how the server knows to leave it alone,
    // and sending an empty string is how the user clears it — which is the
    // only way the trio gets back to all-empty, since the field can never
    // show the real key to leave behind.
    const pairs: Array<[string, string]> = [
      [KEY_BASE_URL, form.value.baseURL],
      [KEY_VAULT_ROOT, form.value.vaultRoot],
      [KEY_API_KEY, form.value.apiKey],
      [KEY_TLS_MODE, form.value.tlsMode],
    ]

    let applied: 'live' | 'restart' = 'live'
    for (const [key, value] of pairs) {
      try {
        const result = await update(key, value)
        if (result === 'restart')
          applied = 'restart'
      }
      catch (e) {
        if (key === KEY_VAULT_ROOT) {
          vaultRootError.value = errorMessage(e, 'Invalid vault root')
          return
        }
        throw e
      }
    }
    toast.success(applied === 'restart' ? 'Saved — applies after a server restart.' : 'Saved.')
    await fetchStatus()
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to save Obsidian settings'))
  }
  finally {
    saving.value = false
  }
}

const indexing = ref(false)
const indexMessage = ref<string | null>(null)
const indexDenied = ref(false)

// All three are readable, expected states, not errors: a missing grant, an
// unconfigured vault, and a run already in flight (server-side single-flight
// in internal/api/obsidian/handler.go) are worth explaining in the panel,
// not flashing as a transient toast the way an unexpected failure is.
const INDEX_STATUS_MESSAGES: Record<number, string> = {
  403: 'Indexing was denied — grant obsidian.search, obsidian.read, and memory.write to allow it.',
  503: 'The Obsidian vault is not configured yet — fill in the settings above and save.',
  409: 'An index run is already in progress — try again shortly.',
}

const REQUIRED_CAPABILITIES = ['obsidian.search', 'obsidian.read', 'memory.write'] as const
const GRANT_REASON = 'Obsidian indexing (created from the Obsidian settings)'

const { grants, loading: grantsLoading, error: grantsError, createGrant } = useGrants()

const missingCapabilities = computed(() =>
  REQUIRED_CAPABILITIES.filter(cap => !grants.value.some(g =>
    g.capabilityName === cap && g.contextKind === 'global' && g.mode === 'allow' && !g.revokedAt,
  )),
)

// Reactive to the 403 path (indexDenied) as well as the proactive path (a
// missing grant found on mount) — a failed grants fetch falls back to 403-only
// rather than guessing the panel into a false positive or negative.
const showAllowButton = computed(() =>
  indexDenied.value || (!grantsLoading.value && !grantsError.value && missingCapabilities.value.length > 0),
)
const grantHintTitle = computed(() => `Grants ${missingCapabilities.value.join(', ')} as a global, unlimited allow.`)

const granting = ref(false)
const grantConfirmation = ref<string | null>(null)
const grantErrorMessage = ref<string | null>(null)

async function allowIndexing() {
  granting.value = true
  grantErrorMessage.value = null
  try {
    for (const capabilityName of missingCapabilities.value) {
      await createGrant({
        capabilityName,
        contextKind: 'global',
        contextRef: '',
        pattern: '',
        mode: 'allow',
        limitCount: 0,
        limitWindowSeconds: 0,
        reason: GRANT_REASON,
      })
    }
    indexDenied.value = false
    indexMessage.value = null
    grantConfirmation.value = 'Indexing allowed. Run Index now.'
  }
  catch (e) {
    grantErrorMessage.value = errorMessage(e, 'Failed to allow indexing')
  }
  finally {
    granting.value = false
  }
}

async function runIndex() {
  indexing.value = true
  indexMessage.value = null
  indexDenied.value = false
  try {
    const res = await fetch('/api/obsidian/index', { method: 'POST' })
    const knownMessage = INDEX_STATUS_MESSAGES[res.status]
    if (knownMessage) {
      indexMessage.value = knownMessage
      indexDenied.value = res.status === 403
      return
    }
    if (!res.ok) {
      const body = await res.json().catch(() => ({})) as { error?: string }
      throw new Error(body.error ?? `HTTP ${res.status}`)
    }
    const data = await res.json() as { indexed: number, matched: number }
    indexMessage.value = `Indexed ${data.indexed} new note${data.indexed === 1 ? '' : 's'} (${data.matched} found).`
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to run indexing'))
  }
  finally {
    indexing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <div>
      <h3 class="text-[17px] font-bold text-fg mb-1">
        Obsidian
      </h3>
      <p class="text-xs text-fg-mute">
        Connect a local Obsidian vault via its Local REST API plugin. All four settings apply as soon as they are saved.
      </p>
    </div>

    <div v-if="loading" class="text-center py-12 text-fg-mute text-sm">
      Loading...
    </div>

    <template v-else>
      <div
        v-if="!trioComplete"
        data-testid="obsidian-trio-warning"
        class="text-xs rounded-md px-3 py-2 bg-warning-soft text-warning-text"
      >
        Base URL, vault root, and API key are a required trio. With only some of them set the server <strong>refuses to start</strong> at the next restart, so saving is blocked: fill all three in, or clear all three (the API key field included) to turn the vault off.
      </div>

      <div class="grid grid-cols-1 gap-3 max-w-md">
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="obsidian-baseurl">Base URL</label>
          <input
            id="obsidian-baseurl"
            v-model="form.baseURL"
            data-testid="obsidian-baseurl"
            type="url"
            placeholder="https://127.0.0.1:27124"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
          >
        </div>
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="obsidian-vaultroot">Vault root</label>
          <input
            id="obsidian-vaultroot"
            v-model="form.vaultRoot"
            data-testid="obsidian-vaultroot"
            type="text"
            placeholder="claude-memory"
            class="w-full bg-card border rounded px-2.5 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
            :class="vaultRootError ? 'border-danger-text' : 'border-line'"
            @input="vaultRootError = null"
          >
          <p v-if="vaultRootError" data-testid="obsidian-vaultroot-error" class="mt-1 text-xs text-danger-text">
            {{ vaultRootError }}
          </p>
        </div>
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="obsidian-apikey">API key</label>
          <input
            id="obsidian-apikey"
            v-model="form.apiKey"
            data-testid="obsidian-apikey"
            type="password"
            autocomplete="off"
            placeholder="Local REST API key"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
          >
        </div>
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="obsidian-tlsmode">TLS mode</label>
          <AppSelect
            id="obsidian-tlsmode"
            v-model="form.tlsMode"
            data-testid="obsidian-tlsmode"
            :options="tlsModeOptions"
            class="w-full"
          />
        </div>
      </div>

      <div>
        <AppButton variant="info" data-testid="obsidian-save" :disabled="saving || !trioComplete" @click="save">
          {{ saving ? 'Saving…' : 'Save' }}
        </AppButton>
      </div>

      <div class="flex items-center gap-3 pt-3 border-t border-line flex-wrap">
        <AppButton variant="secondary" data-testid="obsidian-index" :disabled="indexing || !configured || reachable === false" @click="runIndex">
          {{ indexing ? 'Indexing…' : 'Index now' }}
        </AppButton>
        <AppButton
          v-if="showAllowButton"
          variant="secondary"
          size="sm"
          data-testid="obsidian-grant-indexing"
          :disabled="granting"
          :title="grantHintTitle"
          @click="allowIndexing"
        >
          {{ granting ? 'Allowing…' : 'Allow indexing' }}
        </AppButton>
        <span v-if="!configured" data-testid="obsidian-index-unconfigured-hint" class="text-xs text-fg-mute">Save a base URL, vault root and API key first.</span>
        <span v-else-if="reachable === false" data-testid="obsidian-unreachable-hint" class="text-xs text-danger-text">
          {{ statusError }} — {{ statusHint }}
        </span>
        <span v-else-if="indexMessage" data-testid="obsidian-index-result" class="text-xs text-fg-mute flex items-center gap-2">
          {{ indexMessage }}
          <AppButton v-if="indexDenied" variant="ghost" size="sm" data-testid="obsidian-open-grants" @click="emit('openGrants')">
            Open grants
          </AppButton>
        </span>
      </div>
      <div v-if="grantConfirmation" data-testid="obsidian-grant-confirmation" class="text-xs text-success-text">
        {{ grantConfirmation }}
      </div>
      <div v-if="grantErrorMessage" data-testid="obsidian-grant-error" class="text-xs text-danger-text">
        {{ grantErrorMessage }}
      </div>
    </template>
  </div>
</template>
