<script setup lang="ts">
import type { CloudAccount, CloudSessionView } from './composables/useCloudSessions'
import { computed, reactive, ref } from 'vue'
import AppBadge from '../../components/ui/AppBadge.vue'
import AppButton from '../../components/ui/AppButton.vue'
import AppCard from '../../components/ui/AppCard.vue'
import AppChip from '../../components/ui/AppChip.vue'
import AppFieldLabel from '../../components/ui/AppFieldLabel.vue'
import AppInput from '../../components/ui/AppInput.vue'
import AppSelect from '../../components/ui/AppSelect.vue'
import { useSpawners } from '../../composables/useSpawners'
import { errorMessage } from '../../utils/errorMessage'
import { formatRelativeThenDate } from '../../utils/format'
import { importSession, removeSession, sendMessage, useCloudAccounts, useCloudSessions } from './composables/useCloudSessions'

interface AccountGroup {
  key: string
  account: CloudAccount | null
  sessions: CloudSessionView[]
}

const UNKNOWN_KEY = '__unknown__'

const { sessions } = useCloudSessions()
const { accounts, refresh } = useCloudAccounts()
const { spawners } = useSpawners()

const groups = computed<AccountGroup[]>(() => {
  const accountBySpawner = new Map<string, CloudAccount>()
  for (const account of accounts.value) {
    for (const spawner of account.spawners)
      accountBySpawner.set(spawner.id, account)
  }
  const byKey = new Map<string, AccountGroup>()
  for (const session of sessions.value) {
    const account = accountBySpawner.get(session.spawnerId) ?? null
    const key = account?.configDir ?? UNKNOWN_KEY
    const group = byKey.get(key) ?? { key, account, sessions: [] }
    group.sessions.push(session)
    byKey.set(key, group)
  }
  return [...byKey.values()].sort((a, b) => Number(a.key === UNKNOWN_KEY) - Number(b.key === UNKNOWN_KEY))
})

function isReady(account: CloudAccount | null): boolean {
  return !!account?.auth?.loggedIn && account.auth.authMethod === 'claude.ai'
}

function spawnerNames(account: CloudAccount): string {
  return account.spawners.map(s => s.name).join(', ')
}

const drafts = reactive<Record<string, string>>({})
const sending = reactive<Record<string, boolean>>({})
const rowErrors = reactive<Record<string, string>>({})
const confirmRemoveId = ref<string | null>(null)

async function send(id: string) {
  const text = (drafts[id] ?? '').trim()
  if (!text || sending[id])
    return
  sending[id] = true
  rowErrors[id] = ''
  try {
    await sendMessage(id, text)
    drafts[id] = ''
  }
  catch (err) {
    rowErrors[id] = errorMessage(err)
  }
  finally {
    sending[id] = false
  }
}

async function remove(id: string) {
  rowErrors[id] = ''
  try {
    await removeSession(id)
  }
  catch (err) {
    rowErrors[id] = errorMessage(err)
  }
  finally {
    confirmRemoveId.value = null
  }
}

const claudeSpawnerOptions = computed(() =>
  spawners.value
    .filter(s => !s.adapterType || s.adapterType === 'claude')
    .map(s => ({ value: s.id, label: s.name })),
)
const chosenSpawnerId = ref('')
const importSpawnerId = computed({
  get: () => claudeSpawnerOptions.value.some(o => o.value === chosenSpawnerId.value)
    ? chosenSpawnerId.value
    : (claudeSpawnerOptions.value[0]?.value ?? ''),
  set: (value: string) => { chosenSpawnerId.value = value },
})
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
    <h1 class="text-lg font-semibold text-fg">
      Cloud sessions
    </h1>

    <AppCard>
      <div class="flex flex-col gap-3 p-3">
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
            data-testid="import-submit"
            @click="runImport"
          >
            {{ importing ? 'Importing…' : 'Import session' }}
          </AppButton>
          <p v-if="importError" role="status" class="text-danger-text text-sm">
            {{ importError }}
          </p>
        </div>
      </div>
    </AppCard>

    <p v-if="groups.length === 0" class="text-fg-faint text-sm">
      No cloud sessions yet. Import one by URL above.
    </p>

    <AppCard v-for="group in groups" :key="group.key" data-testid="cloud-account">
      <template #header>
        <div class="min-w-0">
          <div class="font-mono text-sm text-fg truncate">
            {{ group.account?.configDir ?? 'Unknown account' }}
          </div>
          <div v-if="group.account" class="text-xs text-fg-mute truncate">
            {{ spawnerNames(group.account) }}
          </div>
        </div>
        <div v-if="group.account" class="flex items-center gap-2 shrink-0">
          <AppBadge v-if="isReady(group.account)" variant="active" label="Ready" />
          <AppBadge v-else variant="waiting" label="Not signed in" />
          <AppButton variant="ghost" size="sm" data-testid="cloud-refresh" @click="refresh()">
            Refresh
          </AppButton>
        </div>
      </template>

      <div v-if="group.account && !isReady(group.account)" class="px-3 py-2 text-xs text-fg-mute border-b border-line">
        Not signed in with claude.ai. Run: <code class="font-mono">CLAUDE_CONFIG_DIR={{ group.account.configDir }} claude auth login</code>
      </div>
      <div v-if="group.account?.error" class="px-3 py-2 text-xs text-danger-text border-b border-line">
        {{ group.account.error }}
      </div>

      <ul class="divide-y divide-line">
        <li v-for="session in group.sessions" :key="session.id" class="flex flex-col gap-2 px-3 py-3" data-testid="cloud-session">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="font-semibold text-fg truncate">{{ session.title }}</span>
            <AppChip uppercase>
              {{ session.origin }}
            </AppChip>
            <span v-if="session.lastError" :title="session.lastError">
              <AppBadge variant="error" label="Error" />
            </span>
            <a
              :href="session.url"
              target="_blank"
              rel="noopener"
              class="ml-auto text-xs text-accent hover:underline"
            >Open in claude.ai</a>
          </div>

          <p v-if="session.lastMessage" class="text-sm text-fg-soft">
            {{ session.lastMessage }}
            <span class="text-xs text-fg-faint ml-1">{{ formatRelativeThenDate(session.lastMessageAt) }}</span>
          </p>

          <div class="flex items-start gap-2">
            <AppInput
              v-model="drafts[session.id]"
              type="textarea"
              :rows="2"
              placeholder="Send a message to this session"
              :disabled="sending[session.id]"
              :error="rowErrors[session.id]"
              :data-testid="`send-box-${session.id}`"
            />
            <AppButton
              variant="primary"
              size="sm"
              :disabled="sending[session.id] || !drafts[session.id]?.trim()"
              :data-testid="`send-${session.id}`"
              @click="send(session.id)"
            >
              {{ sending[session.id] ? 'Sending…' : 'Send' }}
            </AppButton>
          </div>

          <div class="flex items-center gap-2">
            <template v-if="confirmRemoveId === session.id">
              <span class="text-xs text-fg-mute">Removes the session from Kontor only. It stays on claude.ai.</span>
              <AppButton variant="danger" size="sm" :data-testid="`remove-confirm-${session.id}`" @click="remove(session.id)">
                Confirm
              </AppButton>
              <AppButton variant="secondary" size="sm" :data-testid="`remove-cancel-${session.id}`" @click="confirmRemoveId = null">
                Cancel
              </AppButton>
            </template>
            <AppButton v-else variant="ghost" size="sm" :data-testid="`remove-${session.id}`" @click="confirmRemoveId = session.id">
              Remove from Kontor
            </AppButton>
          </div>
        </li>
      </ul>
    </AppCard>
  </div>
</template>
