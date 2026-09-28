<script setup lang="ts">
import type { ApiKey, McpScope } from '@/types'
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref, watch } from 'vue'
import AuditLogTab from '@/components/AuditLogTab.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import { useClipboardCopy } from '@/composables/useCopyId'
import { useHistoryImport } from '@/composables/useHistoryImport'
import { useOnboarding } from '@/composables/useOnboarding'
import { usePermissionPresets } from '@/composables/usePermissionPresets'
import { useServerConfig } from '@/composables/useServerConfig'
import { useTheme } from '@/composables/useTheme'
import { toast } from '@/composables/useToast'
import { useUser } from '@/composables/useUser'
import GrantSettings from '@/features/settings/components/GrantSettings.vue'
import ObsidianSettings from '@/features/settings/components/ObsidianSettings.vue'
import PipelineConfigSettings from '@/features/settings/components/PipelineConfigSettings.vue'
import ProjectSettings from '@/features/settings/components/ProjectSettings.vue'
import RemoteSettings from '@/features/settings/components/RemoteSettings.vue'
import SpawnerSettings from '@/features/settings/components/SpawnerSettings.vue'
import SystemPromptSettings from '@/features/settings/components/SystemPromptSettings.vue'
import { errorMessage } from '@/utils/errorMessage'
import { maskToken } from '@/utils/format'
import { buildMcpAddCommand, buildMcpJsonConfig } from '@/utils/mcpCommand'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()
// Lazy like its neighbours below: one settings section renders at a time behind
// a modal, and a static import puts it in the entry chunk, which has a budget.
const GitHubSettings = defineAsyncComponent(() => import('@/features/settings/components/GitHubSettings.vue'))
const NotificationSettings = defineAsyncComponent(() => import('@/features/settings/components/NotificationSettings.vue'))
const PluginSettings = defineAsyncComponent(() => import('@/features/plugins').then(m => m.PluginSettings))
const ProviderSettings = defineAsyncComponent(() => import('@/features/settings/components/ProviderSettings.vue'))
const TrackerSettingsPanel = defineAsyncComponent(() => import('@/features/settings/components/TrackerSettingsPanel.vue'))
const AppSettings = defineAsyncComponent(() => import('@/features/settings/components/AppSettings.vue'))
const MemorySettings = defineAsyncComponent(() => import('@/features/settings/components/MemorySettings.vue'))
const ResourceSettings = defineAsyncComponent(() => import('@/features/settings/components/ResourceSettings.vue'))
const ApplicationSettings = defineAsyncComponent(() => import('@/features/settings/components/ApplicationSettings.vue'))

const { preference: themePref, setTheme } = useTheme()
const { authEnabled } = useUser()
const { mcpServerName, mcpEndpoint, loadServerConfig } = useServerConfig()
const { show: showOnboarding } = useOnboarding()

function reopenOnboarding() {
  showOnboarding()
  emit('close')
}

// --- Nav ---
type Section = 'appearance' | 'apiKeys' | 'grants' | 'registry' | 'applications' | 'memory' | 'remotes' | 'permissionPresets' | 'analytics' | 'systemPrompts' | 'plugins' | 'notifications' | 'providers' | 'tracker' | 'projects' | 'spawners' | 'pipelineConfig' | 'server' | 'obsidian' | 'github'
const activeSection = ref<Section>('appearance')

// Nav model — drives the sidebar list. requiresAuth items only show when auth is on.
const SECTIONS: readonly { id: Section, icon: string, label: string, requiresAuth?: boolean }[] = [
  { id: 'appearance', icon: '◑', label: 'Appearance' },
  { id: 'apiKeys', icon: '⬡', label: 'API Keys' },
  { id: 'grants', icon: '🛡', label: 'Grants' },
  { id: 'registry', icon: '▤', label: 'Registry' },
  { id: 'applications', icon: '⧉', label: 'Applications' },
  { id: 'memory', icon: '🧠', label: 'Memory' },
  { id: 'obsidian', icon: '🗒', label: 'Obsidian' },
  { id: 'github', icon: '⑂', label: 'GitHub' },
  { id: 'remotes', icon: '⌂', label: 'My Remotes', requiresAuth: true },
  { id: 'permissionPresets', icon: '⚿', label: 'Permissions' },
  { id: 'analytics', icon: '📊', label: 'Analytics' },
  { id: 'systemPrompts', icon: '✦', label: 'System Prompts' },
  { id: 'plugins', icon: '🔌', label: 'Plugins' },
  { id: 'notifications', icon: '🔔', label: 'Notifications' },
  { id: 'providers', icon: '🧩', label: 'Providers' },
  { id: 'tracker', icon: '🔗', label: 'Tracker' },
  { id: 'projects', icon: '◫', label: 'Projects' },
  { id: 'spawners', icon: '⚙', label: 'Spawners' },
  { id: 'pipelineConfig', icon: '⛓', label: 'Pipeline' },
  { id: 'server', icon: '🖳', label: 'Server' },
]
const visibleSections = computed(() => SECTIONS.filter(s => !s.requiresAuth || authEnabled.value))

// --- State ---
const keys = ref<ApiKey[]>([])
const isLoading = ref(true)

// Create key dialog
const showCreateDialog = ref(false)
const newKeyName = ref('')
type KeyGroup = 'viewer' | 'operator' | 'developer' | 'admin'
const newKeyGroup = ref<KeyGroup>('viewer')
const isCreating = ref(false)

// Token reveal modal
const revealedToken = ref<string | null>(null)
const revealedScopes = ref<McpScope[]>([])
const copiedTarget = ref<'token' | 'cli' | 'json' | null>(null)
const errorTarget = ref<'token' | 'cli' | 'json' | null>(null)
const tokenVisible = ref(false)

const mcpAddCommand = computed(() =>
  revealedToken.value && mcpServerName.value ? buildMcpAddCommand(window.location.origin, revealedToken.value, mcpServerName.value, mcpEndpoint.value) : '',
)
const mcpJsonConfig = computed(() =>
  revealedToken.value && mcpServerName.value ? buildMcpJsonConfig(window.location.origin, revealedToken.value, mcpServerName.value, mcpEndpoint.value) : '',
)
const mcpBlocks = computed(() => [
  { key: 'cli' as const, label: 'CLI command', labelId: 'mcp-cli-label', value: mcpAddCommand.value, extraClass: 'break-all' },
  { key: 'json' as const, label: 'JSON config', labelId: 'mcp-json-label', value: mcpJsonConfig.value, extraClass: 'whitespace-pre overflow-x-auto' },
])
const canAuthorTasks = computed(() => revealedScopes.value.includes('tasks:write'))

// Revoke / regenerate confirmation
const confirmRevokeId = ref<string | null>(null)
const confirmRegenerateId = ref<string | null>(null)
const isRegenerating = ref(false)

// Permission presets
const { presets, load: loadPresetsData, revoke: revokePreset } = usePermissionPresets()
const presetsLoading = ref(false)
const confirmResetCwd = ref<string | null>(null)

// --- Group → scopes mapping ---
const GROUP_SCOPES: Record<string, McpScope[]> = {
  viewer: ['tasks:read'],
  operator: ['tasks:read', 'pipeline:control'],
  developer: ['tasks:read', 'tasks:write', 'pipeline:control'],
  admin: ['tasks:read', 'tasks:write', 'pipeline:control', 'keys:manage'],
}

const KEY_GROUP_OPTIONS: Array<{ value: KeyGroup, label: string }> = [
  { value: 'viewer', label: 'Viewer — tasks:read' },
  { value: 'operator', label: 'Operator — tasks:read, pipeline:control' },
  { value: 'developer', label: 'Developer — tasks:read, tasks:write, pipeline:control' },
  { value: 'admin', label: 'Admin — all scopes' },
]

// --- Load keys ---
async function loadKeys() {
  isLoading.value = true
  try {
    const res = await fetch('/api/settings/api-keys')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    keys.value = await res.json()
  }
  catch (e) {
    toast.error(errorMessage(e))
  }
  finally {
    isLoading.value = false
  }
}

watch(() => props.open, (val) => {
  if (val)
    loadKeys()
})

// --- Permission presets ---
async function loadPresets() {
  presetsLoading.value = true
  try {
    await loadPresetsData()
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to load'))
  }
  finally {
    presetsLoading.value = false
  }
}

async function resetPresets(projectCwd: string) {
  try {
    await revokePreset(projectCwd)
    confirmResetCwd.value = null
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to reset'))
  }
}

function basename(path: string): string {
  return path.split('/').filter(Boolean).pop() ?? path
}

watch(activeSection, (val, oldVal) => {
  if (oldVal === 'apiKeys')
    dismissReveal()
  if (val === 'permissionPresets')
    void loadPresets()
  if (val === 'analytics')
    void loadPatterns()
})

// --- Analytics patterns ---
const patterns = ref<Array<{ tools: string, frequency: number }>>([])
const patternsLoading = ref(false)

async function loadPatterns() {
  try {
    const res = await fetch('/api/analytics/patterns')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    const data = await res.json() as { patterns: typeof patterns.value }
    patterns.value = data.patterns
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to load'))
  }
}

async function refreshPatterns() {
  patternsLoading.value = true
  try {
    const res = await fetch('/api/analytics/patterns/refresh', { method: 'POST' })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    await loadPatterns()
  }
  catch (e) {
    toast.error(errorMessage(e, 'Failed to refresh'))
  }
  finally {
    patternsLoading.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    // AppModal handles ESC for showCreateDialog and revealedToken dialogs.
    // Only handle inline confirmation states here; fall through to outer close.
    if (confirmRevokeId.value || confirmRegenerateId.value || confirmResetCwd.value) {
      confirmRevokeId.value = null
      confirmRegenerateId.value = null
      confirmResetCwd.value = null
    }
    else {
      emit('close')
    }
  }
}
onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  void loadServerConfig()
})
onUnmounted(() => window.removeEventListener('keydown', onKeydown))

// --- Revoke key ---
async function revokeKey(key: ApiKey) {
  confirmRevokeId.value = null

  // Optimistic update
  key.active = false

  try {
    const res = await fetch(`/api/settings/api-keys/${key.id}`, { method: 'DELETE' })
    if (!res.ok) {
      // Rollback on failure
      key.active = true
      toast.error(`Failed to revoke key: HTTP ${res.status}`)
    }
  }
  catch (e) {
    key.active = true
    toast.error(errorMessage(e))
  }
}

// --- Regenerate key ---
async function regenerateKey(key: ApiKey) {
  confirmRegenerateId.value = null
  isRegenerating.value = true
  try {
    const res = await fetch(`/api/settings/api-keys/${key.id}/regenerate`, { method: 'POST' })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error(body.error ?? `HTTP ${res.status}`)
    }
    const data = await res.json() as { key: ApiKey, token: string }
    const idx = keys.value.findIndex(k => k.id === data.key.id)
    if (idx !== -1)
      keys.value.splice(idx, 1, data.key)
    else
      keys.value.unshift(data.key)
    revealedToken.value = data.token
    revealedScopes.value = data.key.scopes
  }
  catch (e) {
    toast.error(errorMessage(e))
  }
  finally {
    isRegenerating.value = false
  }
}

// --- Create key ---
function openCreateDialog() {
  newKeyName.value = ''
  newKeyGroup.value = 'viewer'
  showCreateDialog.value = true
}

function closeCreateDialog() {
  showCreateDialog.value = false
}

async function handleCreate() {
  if (isCreating.value)
    return

  if (!newKeyName.value.trim()) {
    toast.error('Name is required')
    return
  }

  isCreating.value = true
  try {
    const res = await fetch('/api/settings/api-keys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: newKeyName.value.trim(),
        scopes: GROUP_SCOPES[newKeyGroup.value],
      }),
    })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error(body.error ?? `HTTP ${res.status}`)
    }
    const data = await res.json() as { key: ApiKey, token: string }
    keys.value.unshift(data.key)
    revealedToken.value = data.token
    revealedScopes.value = data.key.scopes
    closeCreateDialog()
  }
  catch (e) {
    toast.error(errorMessage(e))
  }
  finally {
    isCreating.value = false
  }
}

// --- Copy helpers ---
const { copy: copyToClipboard } = useClipboardCopy()

async function copyValue(target: 'token' | 'cli' | 'json', value: string) {
  if (!value)
    return
  try {
    await copyToClipboard(value)
    copiedTarget.value = target
    errorTarget.value = null
  }
  catch {
    errorTarget.value = target
    copiedTarget.value = null
  }
  setTimeout(() => {
    copiedTarget.value = null
    errorTarget.value = null
  }, 2000)
}

function copyLabel(target: 'cli' | 'json', base: string) {
  if (copiedTarget.value === target)
    return 'Copied'
  if (errorTarget.value === target)
    return 'Copy failed'
  return base
}

function dismissReveal() {
  revealedToken.value = null
  revealedScopes.value = []
  copiedTarget.value = null
  errorTarget.value = null
  tokenVisible.value = false
}

// --- Helpers ---
function formatDate(iso: string | null) {
  if (!iso)
    return '—'
  return new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

// --- Historical data import ---
const { isImporting, importStatus, start: startImport } = useHistoryImport()
</script>

<template>
  <AppModal :open="open" size="auto" @close="emit('close')">
    <div class="bg-card rounded-xl border border-line shadow-modal w-[975px] max-w-[calc(100vw-2rem)] h-[700px] max-h-[85vh] flex overflow-hidden">
      <!-- ── Sidebar ──────────────────────────────── -->
      <nav class="w-[260px] flex-shrink-0 bg-app border-r border-line px-3 py-5 flex flex-col">
        <div class="flex items-center justify-between px-1 pb-1 mb-2">
          <h2 class="text-base font-bold text-fg">
            Settings
          </h2>
          <button type="button" class="bg-transparent border-none text-fg-mute text-2xl cursor-pointer px-1 leading-none hover:text-fg" @click="emit('close')">
            &times;
          </button>
        </div>
        <ul class="list-none p-0 m-0 flex flex-col gap-0.5">
          <li v-for="s in visibleSections" :key="s.id">
            <button
              type="button"
              class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-md border-none font-sans text-[13px] cursor-pointer text-left transition-colors"
              :class="activeSection === s.id
                ? 'bg-raised text-fg font-semibold'
                : 'bg-transparent text-fg-mute hover:bg-raised/50 hover:text-fg'"
              :aria-current="activeSection === s.id ? 'page' : undefined"
              @click="activeSection = s.id"
            >
              <span class="text-sm flex-shrink-0">{{ s.icon }}</span> {{ s.label }}
            </button>
          </li>
        </ul>
        <div class="mt-auto pt-3 border-t border-line">
          <a
            class="text-xs text-fg-mute no-underline block px-1.5 py-1 rounded hover:text-slate-500 dark:hover:text-slate-400 transition-colors"
            href="https://github.com/lx-wnk/Agent-Dashboard/issues/new/choose"
            target="_blank"
            rel="noopener noreferrer"
          >Report an issue</a>
        </div>
      </nav>

      <!-- ── Content ─────────────────────────────── -->
      <div class="flex-1 overflow-y-auto px-7 py-7">
        <!-- Appearance -->
        <section v-if="activeSection === 'appearance'">
          <h3 class="text-[17px] font-bold text-fg mb-1">
            Themes
          </h3>
          <p class="text-xs text-fg-mute mb-5">
            Choose your preferred color scheme. Tip: press <kbd class="px-1 py-0.5 rounded bg-raised font-mono text-[11px]">Shift+D</kbd> anywhere to toggle dark/light mode.
          </p>
          <div class="flex gap-3.5 flex-wrap">
            <button
              v-for="opt in ([
                { value: 'light', label: 'Light Mode' },
                { value: 'dark', label: 'Dark Mode' },
                { value: 'system', label: 'System' },
              ] as const)"
              :key="opt.value"
              type="button"
              class="w-40 border-2 rounded-lg overflow-hidden cursor-pointer bg-transparent p-0 transition-all font-sans"
              :class="themePref === opt.value
                ? 'border-accent shadow-[0_0_0_3px_var(--accent-soft)]'
                : 'border-line hover:border-accent'"
              :aria-label="opt.value === 'dark' ? `${opt.label} (keyboard shortcut: Shift+D)` : opt.label"
              :aria-pressed="themePref === opt.value"
              @click="setTheme(opt.value)"
            >
              <div
                class="h-[100px] flex flex-col"
                :class="{
                  'bg-[#f0f4f8]': opt.value === 'light',
                  'bg-[#1a2235]': opt.value === 'dark',
                  'bg-[linear-gradient(135deg,#f0f4f8_50%,#1a2235_50%)]': opt.value === 'system',
                }"
              >
                <div
                  class="h-3.5 mx-2 mt-2 mb-1.5 rounded-sm"
                  :class="{
                    'bg-[#cbd5e1]': opt.value === 'light',
                    'bg-[#2d3f5a]': opt.value === 'dark',
                    'bg-[color-mix(in_srgb,#cbd5e1_50%,#2d3f5a)]': opt.value === 'system',
                  }"
                />
                <div class="flex flex-1 gap-1.5 px-2 pb-2">
                  <div
                    class="w-7 rounded-sm"
                    :class="{
                      'bg-[#cbd5e1]': opt.value === 'light',
                      'bg-[#243248]': opt.value === 'dark',
                      'bg-[color-mix(in_srgb,#cbd5e1_50%,#243248)]': opt.value === 'system',
                    }"
                  />
                  <div class="flex-1 flex flex-col gap-1 justify-center">
                    <div
                      class="h-1.5 rounded-sm"
                      :class="{
                        'bg-[#cbd5e1]': opt.value === 'light',
                        'bg-[#2d3f5a]': opt.value === 'dark',
                        'bg-[color-mix(in_srgb,#cbd5e1_50%,#2d3f5a)]': opt.value === 'system',
                      }"
                    />
                    <div
                      class="h-1.5 w-3/5 rounded-sm"
                      :class="{
                        'bg-[#cbd5e1]': opt.value === 'light',
                        'bg-[#2d3f5a]': opt.value === 'dark',
                        'bg-[color-mix(in_srgb,#cbd5e1_50%,#2d3f5a)]': opt.value === 'system',
                      }"
                    />
                    <div
                      class="h-1.5 rounded-sm"
                      :class="{
                        'bg-[#cbd5e1]': opt.value === 'light',
                        'bg-[#2d3f5a]': opt.value === 'dark',
                        'bg-[color-mix(in_srgb,#cbd5e1_50%,#2d3f5a)]': opt.value === 'system',
                      }"
                    />
                  </div>
                </div>
              </div>
              <div class="px-2.5 py-2 text-xs font-medium text-fg-mute border-t border-line flex items-center gap-1.5 bg-card">
                <span class="w-3.5 text-accent font-bold text-[13px]">{{ themePref === opt.value ? '✓' : '' }}</span>
                {{ opt.label }}
              </div>
            </button>
          </div>

          <div class="mt-8 pt-6 border-t border-line">
            <h3 class="text-[17px] font-bold text-fg mb-1">
              First-run setup
            </h3>
            <p class="text-xs text-fg-mute mb-3">
              Re-run the guided setup for the Claude CLI, dashboard connection, and session control.
            </p>
            <AppButton size="sm" variant="secondary" @click="reopenOnboarding">
              Re-run first-run setup
            </AppButton>
          </div>
        </section>

        <!-- API Keys -->
        <section v-else-if="activeSection === 'apiKeys'">
          <div class="flex items-start justify-between gap-3 mb-4">
            <div class="flex-1">
              <h3 class="text-[17px] font-bold text-fg mb-1">
                API Keys
              </h3>
              <p class="text-xs text-fg-mute">
                Manage MCP API keys for external access to this dashboard.
              </p>
            </div>
            <AppButton variant="info" @click="openCreateDialog">
              + Add Key
            </AppButton>
          </div>
          <div v-if="isLoading" class="text-center py-12 text-fg-mute text-sm">
            Loading keys...
          </div>
          <div v-else-if="keys.length === 0" class="text-center py-8 text-fg-mute text-sm">
            No API keys yet. Create one to allow MCP clients to connect.
          </div>
          <table v-else class="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Name
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Scopes
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Created
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Last Used
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Status
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="key in keys" :key="key.id" :class="{ 'opacity-45': !key.active }">
                <td class="px-3 py-2.5 border-b border-line text-fg font-medium whitespace-nowrap">
                  {{ key.name }}
                </td>
                <td class="px-3 py-2.5 border-b border-line">
                  <div class="flex flex-wrap gap-1">
                    <span v-for="scope in key.scopes" :key="scope" class="text-[10px] font-semibold uppercase tracking-wider text-fg-mute bg-raised px-1.5 py-px rounded font-mono">{{ scope }}</span>
                  </div>
                </td>
                <td class="px-3 py-2.5 border-b border-line text-fg-mute font-mono text-xs">
                  {{ formatDate(key.createdAt) }}
                </td>
                <td class="px-3 py-2.5 border-b border-line text-fg-mute font-mono text-xs">
                  {{ formatDate(key.lastUsedAt) }}
                </td>
                <td class="px-3 py-2.5 border-b border-line">
                  <span v-if="key.active" class="inline-block rounded px-2 py-0.5 text-[11px] font-semibold bg-success-soft text-success-text">Active</span>
                  <span v-else class="inline-block rounded px-2 py-0.5 text-[11px] font-semibold bg-raised text-fg-mute">Revoked</span>
                </td>
                <td class="px-3 py-2.5 border-b border-line">
                  <template v-if="key.active">
                    <template v-if="confirmRevokeId === key.id">
                      <AppButton variant="danger" size="sm" class="mr-1" @click="revokeKey(key)">
                        Confirm
                      </AppButton>
                      <AppButton variant="secondary" size="sm" @click="confirmRevokeId = null">
                        Cancel
                      </AppButton>
                    </template>
                    <template v-else-if="confirmRegenerateId === key.id">
                      <AppButton variant="danger" size="sm" class="mr-1" :disabled="isRegenerating" @click="regenerateKey(key)">
                        Confirm
                      </AppButton>
                      <AppButton variant="secondary" size="sm" @click="confirmRegenerateId = null">
                        Cancel
                      </AppButton>
                    </template>
                    <template v-else>
                      <button type="button" class="bg-transparent border-none text-fg-mute cursor-pointer text-sm px-2 py-1 rounded hover:bg-amber-50 dark:hover:bg-amber-950/30 hover:text-amber-600 dark:hover:text-amber-400 mr-1" @click="confirmRegenerateId = key.id">
                        Regenerate
                      </button>
                      <button type="button" class="bg-transparent border-none text-fg-mute cursor-pointer text-sm px-2 py-1 rounded hover:bg-red-50 dark:hover:bg-red-950/30 hover:text-red-600 dark:hover:text-red-400" @click="confirmRevokeId = key.id">
                        Revoke
                      </button>
                    </template>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
          <div class="mt-4 border-t border-line pt-4">
            <h3 class="text-sm font-semibold mb-2 text-fg-soft">
              Historical Data
            </h3>
            <button
              type="button"
              :disabled="isImporting"
              class="text-sm px-3 py-1.5 rounded bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
              @click="startImport"
            >
              Import Session History
            </button>
            <p v-if="importStatus" class="text-xs text-slate-500 mt-1">
              {{ importStatus }}
            </p>
          </div>
        </section>

        <!-- Grants -->
        <section v-else-if="activeSection === 'grants'">
          <GrantSettings />
        </section>

        <!-- Registry -->
        <section v-else-if="activeSection === 'registry'">
          <ResourceSettings />
        </section>

        <!-- Applications -->
        <section v-else-if="activeSection === 'applications'">
          <ApplicationSettings />
        </section>

        <!-- Memory -->
        <section v-else-if="activeSection === 'memory'">
          <MemorySettings />
        </section>

        <!-- Obsidian -->
        <section v-else-if="activeSection === 'obsidian'">
          <ObsidianSettings @open-grants="activeSection = 'grants'" />
        </section>

        <!-- GitHub -->
        <section v-else-if="activeSection === 'github'">
          <GitHubSettings />
        </section>

        <!-- Remotes -->
        <section v-else-if="activeSection === 'remotes' && authEnabled">
          <RemoteSettings />
        </section>

        <!-- Permission presets -->
        <section v-else-if="activeSection === 'permissionPresets'">
          <h3 class="text-[17px] font-bold text-fg mb-1">
            Permissions
          </h3>
          <p class="text-xs text-fg-mute mb-5">
            Auto-saved tool permissions per project. Reset removes all stored permissions for this project.
          </p>
          <div v-if="presetsLoading" class="text-center py-12 text-fg-mute text-sm">
            Loading...
          </div>
          <div v-else-if="presets.length === 0" class="text-center py-8 text-fg-mute text-sm">
            No saved permissions.
          </div>
          <table v-else class="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Project
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Count
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in presets" :key="p.projectCwd">
                <td class="px-3 py-2.5 border-b border-line text-fg font-mono text-xs" :title="p.projectCwd">
                  {{ basename(p.projectCwd) }}
                </td>
                <td class="px-3 py-2.5 border-b border-line text-fg-mute whitespace-nowrap">
                  {{ p.entries.length }} {{ p.entries.length === 1 ? 'Tool' : 'Tools' }}
                </td>
                <td class="px-3 py-2.5 border-b border-line whitespace-nowrap">
                  <template v-if="confirmResetCwd === p.projectCwd">
                    <AppButton variant="danger" size="sm" class="mr-1" @click="resetPresets(p.projectCwd)">
                      Yes, reset
                    </AppButton>
                    <AppButton variant="secondary" size="sm" @click="confirmResetCwd = null">
                      Cancel
                    </AppButton>
                  </template>
                  <button v-else type="button" class="bg-transparent border-none text-fg-mute cursor-pointer text-sm px-2 py-1 rounded hover:bg-red-50 dark:hover:bg-red-950/30 hover:text-red-600 dark:hover:text-red-400" @click="confirmResetCwd = p.projectCwd">
                    Reset
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- System Prompts -->
        <section v-else-if="activeSection === 'systemPrompts'">
          <SystemPromptSettings />
        </section>

        <!-- Plugins -->
        <section v-else-if="activeSection === 'plugins'">
          <PluginSettings />
        </section>

        <!-- Notifications -->
        <section v-else-if="activeSection === 'notifications'">
          <NotificationSettings />
        </section>

        <!-- Providers -->
        <section v-else-if="activeSection === 'providers'">
          <ProviderSettings />
        </section>

        <!-- Tracker -->
        <section v-else-if="activeSection === 'tracker'">
          <TrackerSettingsPanel />
        </section>

        <!-- Projects -->
        <section v-else-if="activeSection === 'projects'">
          <ProjectSettings />
        </section>

        <!-- Spawners -->
        <section v-else-if="activeSection === 'spawners'">
          <SpawnerSettings />
        </section>

        <!-- Pipeline Configuration -->
        <section v-else-if="activeSection === 'pipelineConfig'">
          <PipelineConfigSettings />
        </section>

        <!-- Server -->
        <section v-else-if="activeSection === 'server'">
          <AppSettings />
        </section>

        <!-- Analytics -->
        <section v-else-if="activeSection === 'analytics'">
          <h3 class="text-[17px] font-bold text-fg mb-1">
            Workflow Patterns
          </h3>
          <p class="text-xs text-fg-mute mb-5">
            Top 3-tool sequences discovered across all sessions.
          </p>
          <div v-if="patterns.length === 0" class="text-sm text-fg-mute">
            No patterns discovered yet.
          </div>
          <ul v-else class="space-y-1 mb-4">
            <li
              v-for="p in patterns"
              :key="p.tools"
              class="text-xs font-mono bg-raised px-2 py-1 rounded flex justify-between"
            >
              <span class="text-fg-soft">{{ p.tools }}</span>
              <span class="text-slate-400">×{{ p.frequency }}</span>
            </li>
          </ul>
          <button
            type="button"
            class="text-xs px-2 py-1 rounded border border-line-strong text-fg-mute hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="patternsLoading"
            @click="refreshPatterns"
          >
            {{ patternsLoading ? 'Scanning…' : 'Refresh' }}
          </button>

          <div class="mt-8 pt-6 border-t border-line">
            <h3 class="text-[17px] font-bold text-fg mb-1">
              Audit Log
            </h3>
            <p class="text-xs text-fg-mute mb-4">
              Recent dashboard events — permission grants, task transitions, spawn actions.
            </p>
            <AuditLogTab :limit="100" hide-title />
          </div>
        </section>
      </div>
    </div>

    <!-- Create key dialog -->
    <AppModal :open="showCreateDialog" size="auto" :z-index="300" labelled-by="create-key-dialog-title" @close="closeCreateDialog">
      <div class="bg-card border border-line rounded-xl w-full max-w-[480px] max-h-[90vh] overflow-y-auto shadow-modal">
        <header class="flex justify-between items-center px-5 py-4 border-b border-line">
          <h2 id="create-key-dialog-title" class="text-lg font-semibold text-fg">
            Create API Key
          </h2>
          <button type="button" class="bg-transparent border-none text-fg-mute text-2xl cursor-pointer px-1 leading-none hover:text-fg" @click="closeCreateDialog">
            &times;
          </button>
        </header>
        <form class="p-5" @submit.prevent="handleCreate">
          <div class="flex flex-col gap-1 mb-3.5">
            <label for="key-name" class="text-[10px] font-semibold uppercase tracking-wider text-fg-mute">Name</label>
            <input
              id="key-name"
              v-model="newKeyName"
              class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
              type="text"
              required
              placeholder="e.g. CI pipeline key"
              autofocus
            >
          </div>
          <div class="flex flex-col gap-1 mb-3.5">
            <label for="key-group" class="text-[10px] font-semibold uppercase tracking-wider text-fg-mute">Role / Scope Group</label>
            <AppSelect
              id="key-group"
              v-model="newKeyGroup"
              :options="KEY_GROUP_OPTIONS"
              class="w-full"
            />
          </div>
        </form>
        <footer class="flex justify-end gap-2 px-5 py-3 border-t border-line">
          <AppButton variant="secondary" @click="closeCreateDialog">
            Cancel
          </AppButton>
          <AppButton variant="info" :disabled="isCreating || !newKeyName.trim()" @click="handleCreate">
            {{ isCreating ? 'Creating...' : 'Create Key' }}
          </AppButton>
        </footer>
      </div>
    </AppModal>

    <!-- Token reveal dialog -->
    <AppModal :open="!!revealedToken" size="auto" :z-index="300" labelled-by="token-reveal-dialog-title" @close="dismissReveal">
      <div class="bg-card border border-line rounded-xl w-full max-w-[480px] max-h-[90vh] overflow-y-auto shadow-modal">
        <header class="flex justify-between items-center px-5 py-4 border-b border-line">
          <h2 id="token-reveal-dialog-title" class="text-lg font-semibold text-fg">
            Your new API key
          </h2>
          <button type="button" aria-label="Close" class="bg-transparent border-none text-fg-mute text-2xl cursor-pointer px-1 leading-none hover:text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent rounded" @click="dismissReveal">
            ✕
          </button>
        </header>
        <div class="p-5">
          <p class="text-[13px] text-fg-mute mb-3">
            Save this token now — it will <strong class="text-warning-text">never be shown again</strong>.
          </p>
          <div class="relative font-mono text-xs bg-success-soft text-success-text p-3 pr-10 rounded border border-success-line break-all mb-3">
            {{ tokenVisible ? revealedToken : maskToken(revealedToken ?? '') }}
            <button
              type="button"
              class="absolute right-2 top-1/2 -translate-y-1/2 p-1 rounded hover:brightness-105 text-success-text transition-[filter]"
              :aria-label="tokenVisible ? 'Hide token' : 'Show token'"
              :aria-pressed="tokenVisible"
              @click="tokenVisible = !tokenVisible"
            >
              <span aria-hidden="true" class="text-sm leading-none">{{ tokenVisible ? 'Hide' : 'Show' }}</span>
            </button>
          </div>
          <div class="flex justify-end">
            <AppButton variant="info" @click="copyValue('token', revealedToken ?? '')">
              <span v-if="errorTarget === 'token'">Copy failed</span>
              <span v-else-if="copiedTarget === 'token'">Copied!</span>
              <span v-else>Copy to clipboard</span>
            </AppButton>
          </div>

          <div class="mt-5 border-t border-line pt-4">
            <p class="text-[13px] text-fg-mute mb-1">
              {{ canAuthorTasks ? "Connect a Claude Code session to this dashboard's task tools:" : "Connect a Claude Code session (this key has read-only access to task tools):" }}
            </p>
            <p v-if="!canAuthorTasks" class="text-[11px] text-warning-text mb-3">
              Read-only key — creating or refining tasks needs the Developer or Admin role.
            </p>

            <template v-for="b in mcpBlocks" :key="b.key">
              <span :id="b.labelId" class="text-[10px] font-semibold uppercase tracking-wider text-fg-mute">{{ b.label }}</span>
              <div
                role="region"
                :aria-labelledby="b.labelId"
                tabindex="0"
                class="relative font-mono text-xs bg-raised text-fg-soft p-3 pr-10 rounded border border-line mt-1 mb-3"
                :class="b.extraClass"
              >
                {{ b.value }}
                <button
                  type="button"
                  class="absolute right-1.5 top-1.5 p-1.5 rounded hover:bg-app text-fg-mute hover:text-fg transition-colors focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:ring-offset-1"
                  :aria-label="copyLabel(b.key, `Copy ${b.label}`)"
                  @click="copyValue(b.key, b.value)"
                >
                  <span v-if="copiedTarget === b.key" aria-hidden="true" class="text-sm leading-none text-success-text">✓</span>
                  <span v-else-if="errorTarget === b.key" aria-hidden="true" class="text-sm leading-none text-danger-text">✕</span>
                  <span v-else aria-hidden="true" class="text-sm leading-none">⧉</span>
                </button>
              </div>
            </template>
            <p class="text-[11px] text-fg-mute mt-3">
              Contains your secret token — don't share it. The CLI command writes it to <code class="font-mono">~/.claude.json</code>; keep that file out of version control.
            </p>
          </div>
        </div>
        <footer class="flex justify-end gap-2 px-5 py-3 border-t border-line">
          <AppButton variant="secondary" @click="dismissReveal">
            Done — I have saved the token
          </AppButton>
        </footer>
      </div>
    </AppModal>
  </AppModal>
</template>
