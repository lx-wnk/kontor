<script setup lang="ts">
import type { ProjectFolder } from '../../types'
import type { CloudAccount, CloudSessionView } from './composables/useCloudSessions'
import { computed, nextTick, reactive, ref, watch } from 'vue'
import AppBadge from '../../components/ui/AppBadge.vue'
import AppButton from '../../components/ui/AppButton.vue'
import AppCard from '../../components/ui/AppCard.vue'
import AppChip from '../../components/ui/AppChip.vue'
import AppFieldLabel from '../../components/ui/AppFieldLabel.vue'
import AppInput from '../../components/ui/AppInput.vue'
import AppSelect from '../../components/ui/AppSelect.vue'
import { fetchProjectFolders } from '../../composables/useProjectFolders'
import { useProjects } from '../../composables/useProjects'
import { useSpawners } from '../../composables/useSpawners'
import { errorMessage } from '../../utils/errorMessage'
import { formatRelativeThenDate } from '../../utils/format'
import { parseBool } from '../../utils/parseBool'
import { useSettings } from '../settings/composables/useSettings'
import { PREVIEW_FEATURES_KEY } from '../settings/settingLabels'
import { useClaudeSpawnerPicker } from './claudeSpawners'
import CloudRoutinesSection from './CloudRoutinesSection.vue'
import { isCloudUrl } from './cloudUrl'
import { importSession, removeSession, sendMessage, startSession, useCloudAccounts, useCloudSessions } from './composables/useCloudSessions'

interface AccountGroup {
  key: string
  label: string
  account: CloudAccount | null
  sessions: CloudSessionView[]
}

const UNKNOWN_KEY = '__unknown__'
const UNGROUPED_KEY = '__ungrouped__'
const DEFAULT_ACCOUNT_LABEL = 'default (no CLAUDE_CONFIG_DIR)'

const { sessions, error: sessionsError, refetch: refetchSessions } = useCloudSessions()
const { accounts, isLoading: accountsLoading, error: accountsError, refetch: refetchAccounts, refresh: refreshAccounts } = useCloudAccounts()
const { spawners } = useSpawners()
const { items: settingItems, loading: settingsLoading, error: settingsError, refetch: refetchSettings, savedCount } = useSettings()
const previewEnabled = computed(() => parseBool(settingItems.value.find(i => i.key === PREVIEW_FEATURES_KEY)?.value))
const showPreviewHint = computed(() => !settingsLoading.value && !settingsError.value && !previewEnabled.value)
void refetchSettings()
watch(savedCount, () => void refetchSettings())

const accountBySpawner = computed(() => {
  const bySpawner = new Map<string, CloudAccount>()
  for (const account of accounts.value) {
    for (const spawner of account.spawners)
      bySpawner.set(spawner.id, account)
  }
  return bySpawner
})

function accountLabel(account: CloudAccount): string {
  return account.configDir || DEFAULT_ACCOUNT_LABEL
}

const groups = computed<AccountGroup[]>(() => {
  if (accountsLoading.value)
    return []
  if (accountsError.value && accounts.value.length === 0) {
    return sessions.value.length > 0
      ? [{ key: UNGROUPED_KEY, label: 'Sessions', account: null, sessions: sessions.value }]
      : []
  }
  const byKey = new Map<string, AccountGroup>(
    accounts.value.map(account => [account.configDir, { key: account.configDir, label: accountLabel(account), account, sessions: [] }]),
  )
  for (const session of sessions.value) {
    const account = accountBySpawner.value.get(session.spawnerId) ?? null
    const key = account?.configDir ?? UNKNOWN_KEY
    const group = byKey.get(key) ?? { key, label: 'Unknown account', account, sessions: [] }
    group.sessions.push(session)
    byKey.set(key, group)
  }
  return [...byKey.values()].sort((a, b) => Number(a.key === UNKNOWN_KEY) - Number(b.key === UNKNOWN_KEY))
})

const refreshing = ref(false)

async function refreshAll() {
  if (refreshing.value)
    return
  refreshing.value = true
  try {
    await Promise.all([refreshAccounts(), refetchSessions()])
  }
  finally {
    refreshing.value = false
  }
}

const checkedSpawnerIds = new Set<string>()
let accountFetchRunning = false

async function refetchForUnknownSpawners() {
  if (accountFetchRunning || accountsLoading.value)
    return
  const unmapped = sessions.value
    .map(s => s.spawnerId)
    .filter(id => !accountBySpawner.value.has(id) && !checkedSpawnerIds.has(id))
  if (unmapped.length === 0)
    return
  accountFetchRunning = true
  unmapped.forEach(id => checkedSpawnerIds.add(id))
  try {
    await refetchAccounts()
  }
  finally {
    accountFetchRunning = false
  }
  await refetchForUnknownSpawners()
}

watch([sessions, accounts, accountsLoading], refetchForUnknownSpawners, { immediate: true })

function spawnerNames(account: CloudAccount): string {
  return account.spawners.map(s => s.name).join(', ')
}

const drafts = reactive<Record<string, string>>({})
const sending = reactive<Record<string, boolean>>({})
const sendErrors = reactive<Record<string, string>>({})
const removeErrors = reactive<Record<string, string>>({})
const confirmRemoveId = ref<string | null>(null)

function editDraft(id: string, text: string) {
  drafts[id] = text
  sendErrors[id] = ''
}

watch(sessions, (list) => {
  const live = new Set(list.map(s => s.id))
  for (const record of [drafts, sending, sendErrors, removeErrors]) {
    for (const id of Object.keys(record)) {
      if (!live.has(id))
        delete record[id]
    }
  }
  if (confirmRemoveId.value && !live.has(confirmRemoveId.value))
    confirmRemoveId.value = null
})

const confirmButton = ref<HTMLElement | null>(null)

async function askRemove(id: string) {
  confirmRemoveId.value = id
  await nextTick()
  confirmButton.value?.focus()
}

async function send(id: string) {
  const text = (drafts[id] ?? '').trim()
  if (!text || sending[id])
    return
  sending[id] = true
  sendErrors[id] = ''
  try {
    await sendMessage(id, text)
    drafts[id] = ''
  }
  catch (err) {
    sendErrors[id] = errorMessage(err)
  }
  finally {
    sending[id] = false
  }
}

async function remove(id: string) {
  removeErrors[id] = ''
  try {
    await removeSession(id)
  }
  catch (err) {
    removeErrors[id] = errorMessage(err)
  }
  finally {
    confirmRemoveId.value = null
  }
}

const { options: claudeSpawnerOptions, selectedId: importSpawnerId } = useClaudeSpawnerPicker(spawners)
const { selectedId: startSpawnerId } = useClaudeSpawnerPicker(spawners)
const { projects } = useProjects()
const projectOptions = computed(() => projects.value.map(p => ({ value: p.id, label: p.name })))
const startProjectId = ref('')
const startFolders = ref<ProjectFolder[]>([])
const folderOptions = computed(() => startFolders.value.map(f => ({ value: f.id, label: f.label || f.path })))
const startFolderId = ref('')
const startDescription = ref('')
const starting = ref(false)
const startError = ref('')
const canStart = computed(() => !!startSpawnerId.value && !!startFolderId.value && !!startDescription.value.trim() && !starting.value)

watch(startProjectId, async (projectId) => {
  startFolders.value = []
  startFolderId.value = ''
  startError.value = ''
  if (!projectId)
    return
  try {
    const folders = await fetchProjectFolders(projectId)
    if (startProjectId.value !== projectId)
      return
    startFolders.value = folders
    startFolderId.value = folders.find(f => f.isDefault)?.id ?? ''
  }
  catch (err) {
    if (startProjectId.value === projectId)
      startError.value = errorMessage(err)
  }
})

async function runStart() {
  if (!canStart.value)
    return
  starting.value = true
  startError.value = ''
  try {
    await startSession({
      spawnerId: startSpawnerId.value,
      folderId: startFolderId.value,
      description: startDescription.value.trim(),
    })
    startDescription.value = ''
  }
  catch (err) {
    startError.value = errorMessage(err)
  }
  finally {
    starting.value = false
  }
}

const importUrl = ref('')
const importTitle = ref('')
const importing = ref(false)
const importError = ref('')

async function runImport() {
  if (!importSpawnerId.value || !importUrl.value.trim() || importing.value)
    return
  importing.value = true
  importError.value = ''
  try {
    await importSession({
      spawnerId: importSpawnerId.value,
      url: importUrl.value.trim(),
      title: importTitle.value.trim() || undefined,
    })
    importUrl.value = ''
    importTitle.value = ''
  }
  catch (err) {
    importError.value = errorMessage(err)
  }
  finally {
    importing.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-4 p-4 max-w-5xl mx-auto">
    <div class="flex items-center gap-3">
      <h1 class="text-lg font-semibold text-fg">
        Cloud sessions
      </h1>
      <AppButton
        class="ml-auto"
        variant="ghost"
        size="sm"
        :disabled="refreshing"
        data-testid="cloud-refresh"
        @click="refreshAll()"
      >
        {{ refreshing ? 'Refreshing…' : 'Refresh' }}
      </AppButton>
    </div>

    <p v-if="sessionsError" role="alert" class="text-danger-text text-sm" data-testid="sessions-error">
      Could not load cloud sessions: {{ sessionsError }}
    </p>
    <p v-if="accountsError" role="alert" class="text-danger-text text-sm" data-testid="accounts-error">
      Could not load accounts: {{ accountsError }}
    </p>

    <AppCard>
      <form class="flex flex-col gap-3 p-3" data-testid="start-form" @submit.prevent="runStart">
        <h2 class="text-sm font-semibold text-fg">
          Start a cloud session
        </h2>
        <div class="grid gap-3 sm:grid-cols-3">
          <div>
            <AppFieldLabel for="cloud-start-spawner">
              Spawner
            </AppFieldLabel>
            <AppSelect
              id="cloud-start-spawner"
              v-model="startSpawnerId"
              :options="claudeSpawnerOptions"
              placeholder="No claude spawner"
              aria-label="Spawner for the new session"
              class="w-full"
            />
          </div>
          <div>
            <AppFieldLabel for="cloud-start-project">
              Project
            </AppFieldLabel>
            <AppSelect
              id="cloud-start-project"
              v-model="startProjectId"
              :options="projectOptions"
              placeholder="Select a project"
              aria-label="Project"
              class="w-full"
              data-testid="start-project"
            />
          </div>
          <div>
            <AppFieldLabel for="cloud-start-folder">
              Folder
            </AppFieldLabel>
            <AppSelect
              id="cloud-start-folder"
              v-model="startFolderId"
              :options="folderOptions"
              :disabled="!startProjectId"
              placeholder="Select a folder"
              aria-label="Folder"
              class="w-full"
              data-testid="start-folder"
            />
          </div>
        </div>
        <AppInput
          v-model="startDescription"
          type="textarea"
          :rows="3"
          label="Description"
          placeholder="What should Claude do?"
          data-testid="start-description"
        />
        <div class="flex items-center gap-3">
          <AppButton
            variant="primary"
            size="sm"
            :disabled="!canStart"
            type="submit"
            data-testid="start-submit"
          >
            {{ starting ? 'Starting…' : 'Start' }}
          </AppButton>
          <p v-if="startError" role="alert" class="text-danger-text text-sm" data-testid="start-error">
            {{ startError }}
          </p>
        </div>
        <p class="text-fg-faint text-xs">
          Claude must trust this folder: open it once with claude and accept the trust prompt.
        </p>
      </form>
    </AppCard>

    <AppCard>
      <form class="flex flex-col gap-3 p-3" data-testid="import-form" @submit.prevent="runImport">
        <div class="grid gap-3 sm:grid-cols-[14rem_1fr_14rem]">
          <div>
            <AppFieldLabel for="cloud-import-spawner">
              Spawner
            </AppFieldLabel>
            <AppSelect
              id="cloud-import-spawner"
              v-model="importSpawnerId"
              :options="claudeSpawnerOptions"
              placeholder="No claude spawner"
              aria-label="Spawner"
              class="w-full"
            />
          </div>
          <AppInput
            v-model="importUrl"
            label="Session URL"
            placeholder="https://claude.ai/code/session_…"
            data-testid="import-url"
          />
          <AppInput
            v-model="importTitle"
            label="Title (optional)"
            data-testid="import-title"
          />
        </div>
        <div class="flex items-center gap-3">
          <AppButton
            variant="primary"
            size="sm"
            :disabled="importing || !importSpawnerId || !importUrl.trim()"
            type="submit"
            data-testid="import-submit"
          >
            {{ importing ? 'Importing…' : 'Import session' }}
          </AppButton>
          <p v-if="importError" role="alert" class="text-danger-text text-sm">
            {{ importError }}
          </p>
        </div>
      </form>
    </AppCard>

    <CloudRoutinesSection v-if="previewEnabled" />
    <p v-else-if="showPreviewHint" class="text-fg-faint text-sm" data-testid="routines-preview-hint">
      Routines are a preview feature: Settings › Server › Preview features.
    </p>

    <p v-if="accountsLoading" class="text-fg-faint text-sm">
      Loading accounts…
    </p>
    <p v-else-if="groups.length === 0 && !sessionsError" class="text-fg-faint text-sm">
      No cloud sessions yet. Start one or import one by URL above.
    </p>

    <AppCard v-for="group in groups" :key="group.key" data-testid="cloud-account">
      <template #header>
        <div class="min-w-0">
          <div class="font-mono text-sm text-fg truncate">
            {{ group.label }}
          </div>
          <div v-if="group.account" class="text-xs text-fg-mute truncate">
            {{ spawnerNames(group.account) }}
          </div>
        </div>
        <div v-if="group.account" class="flex items-center gap-2 shrink-0">
          <AppBadge v-if="group.account.ready" variant="active" label="Ready" />
          <AppBadge v-else-if="group.account.error" variant="idle" label="Unknown" />
          <AppBadge v-else variant="waiting" label="Not signed in" />
        </div>
      </template>

      <div v-if="group.account && !group.account.ready && !group.account.error" class="px-3 py-2 text-xs text-fg-mute border-b border-line">
        Not signed in with claude.ai. Run: <code class="font-mono">{{ group.account.loginHint }}</code>
      </div>
      <div v-if="group.account?.error" role="alert" class="px-3 py-2 text-xs text-danger-text border-b border-line">
        {{ group.account.error }}
      </div>

      <p v-if="group.sessions.length === 0" class="px-3 py-3 text-sm text-fg-faint">
        No sessions yet.
      </p>
      <ul v-else class="divide-y divide-line">
        <li v-for="session in group.sessions" :key="session.id" class="flex flex-col gap-2 px-3 py-3" data-testid="cloud-session">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="font-semibold text-fg truncate">{{ session.title }}</span>
            <AppChip uppercase>
              {{ session.origin }}
            </AppChip>
            <span v-if="session.lastError" :title="session.lastError">
              <AppBadge variant="error" label="Error" />
              <span class="sr-only">{{ session.lastError }}</span>
            </span>
            <a
              v-if="isCloudUrl(session.url)"
              :href="session.url"
              target="_blank"
              rel="noopener"
              class="ml-auto text-xs text-accent hover:underline"
            >Open in claude.ai</a>
          </div>

          <div v-if="session.lastMessage" class="text-sm text-fg-soft">
            <p class="line-clamp-3 break-words" :title="session.lastMessage" :data-testid="`last-message-${session.id}`">
              {{ session.lastMessage }}
            </p>
            <span class="text-xs text-fg-faint">{{ formatRelativeThenDate(session.lastMessageAt) }}</span>
          </div>

          <div class="flex items-start gap-2">
            <AppInput
              :model-value="drafts[session.id]"
              type="textarea"
              :rows="2"
              :aria-label="`Message to ${session.title}`"
              placeholder="Send a message to this session"
              :disabled="sending[session.id]"
              :error="sendErrors[session.id]"
              :data-testid="`send-box-${session.id}`"
              @update:model-value="editDraft(session.id, $event)"
            />
            <AppButton
              variant="primary"
              size="sm"
              :disabled="sending[session.id] || !drafts[session.id]?.trim()"
              :aria-label="`Send message to ${session.title}`"
              :data-testid="`send-${session.id}`"
              @click="send(session.id)"
            >
              {{ sending[session.id] ? 'Sending…' : 'Send' }}
            </AppButton>
            <span class="sr-only" role="status" :data-testid="`send-status-${session.id}`">{{ sending[session.id] ? 'Sending…' : '' }}</span>
          </div>

          <div class="flex items-center gap-2">
            <template v-if="confirmRemoveId === session.id">
              <span class="text-xs text-fg-mute">Removes the session from Kontor only. It stays on claude.ai.</span>
              <AppButton
                :ref="(el: unknown) => { confirmButton = (el as { $el?: HTMLElement } | null)?.$el ?? null }"
                variant="danger"
                size="sm"
                :aria-label="`Confirm removing ${session.title}`"
                :data-testid="`remove-confirm-${session.id}`"
                @click="remove(session.id)"
              >
                Confirm
              </AppButton>
              <AppButton variant="secondary" size="sm" :aria-label="`Cancel removing ${session.title}`" :data-testid="`remove-cancel-${session.id}`" @click="confirmRemoveId = null">
                Cancel
              </AppButton>
            </template>
            <AppButton v-else variant="ghost" size="sm" :aria-label="`Remove from Kontor: ${session.title}`" :data-testid="`remove-${session.id}`" @click="askRemove(session.id)">
              Remove from Kontor
            </AppButton>
            <span v-if="removeErrors[session.id]" role="alert" class="text-danger-text text-xs" :data-testid="`remove-error-${session.id}`">{{ removeErrors[session.id] }}</span>
          </div>
        </li>
      </ul>
    </AppCard>
  </div>
</template>
