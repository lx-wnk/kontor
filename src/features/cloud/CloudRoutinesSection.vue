<script setup lang="ts">
import type { CloudRoutineView } from './composables/useCloudRoutines'
import type { CloudSessionView } from './composables/useCloudSessions'
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import AppBadge from '../../components/ui/AppBadge.vue'
import AppButton from '../../components/ui/AppButton.vue'
import AppCard from '../../components/ui/AppCard.vue'
import AppFieldLabel from '../../components/ui/AppFieldLabel.vue'
import AppInput from '../../components/ui/AppInput.vue'
import AppSelect from '../../components/ui/AppSelect.vue'
import { useSpawners } from '../../composables/useSpawners'
import { errorMessage } from '../../utils/errorMessage'
import { useClaudeSpawnerPicker } from './claudeSpawners'
import { isCloudUrl } from './cloudUrl'
import { FireFailedError, useCloudRoutines } from './composables/useCloudRoutines'

const BODY_PREVIEW_CHARS = 200
const TOKEN_INPUT_CLASS = 'w-full bg-app border border-line rounded-md px-3 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent font-sans'

interface FireFailure { status: number, body: string }

const { routines, isLoading, error: loadError, refetch, create, update, remove, fire } = useCloudRoutines()
const { spawners } = useSpawners()

onMounted(refetch)

const { options: spawnerOptions, selectedId: createSpawnerId } = useClaudeSpawnerPicker(spawners)

function spawnerName(id: string): string {
  return spawners.value.find(s => s.id === id)?.name ?? id
}

const newName = ref('')
const newFireUrl = ref('')
const newToken = ref('')
const creating = ref(false)
const createError = ref('')

const canCreate = computed(() =>
  !creating.value && !!createSpawnerId.value && !!newName.value.trim() && !!newFireUrl.value.trim() && !!newToken.value,
)

async function runCreate() {
  if (!canCreate.value)
    return
  creating.value = true
  createError.value = ''
  try {
    await create({
      name: newName.value.trim(),
      fireUrl: newFireUrl.value.trim(),
      spawnerId: createSpawnerId.value,
      token: newToken.value,
    })
    newName.value = ''
    newFireUrl.value = ''
    newToken.value = ''
  }
  catch (err) {
    createError.value = errorMessage(err)
  }
  finally {
    creating.value = false
  }
}

const editingId = ref<string | null>(null)
const edit = reactive({ name: '', fireUrl: '', token: '' })
const saving = ref(false)
const editError = ref('')

const editButtonId = (id: string) => `routine-edit-btn-${id}`
const editFormId = (id: string) => `routine-edit-form-${id}`

async function startEdit(routine: CloudRoutineView) {
  editingId.value = routine.id
  Object.assign(edit, { name: routine.name, fireUrl: routine.fireUrl, token: '' })
  editError.value = ''
  await nextTick()
  document.getElementById(editFormId(routine.id))?.querySelector('input')?.focus()
}

async function closeEdit() {
  const id = editingId.value
  editingId.value = null
  Object.assign(edit, { name: '', fireUrl: '', token: '' })
  editError.value = ''
  if (!id)
    return
  await nextTick()
  document.getElementById(editButtonId(id))?.focus()
}

async function saveEdit(id: string) {
  if (saving.value || !edit.name.trim() || !edit.fireUrl.trim())
    return
  saving.value = true
  editError.value = ''
  try {
    await update(id, { name: edit.name.trim(), fireUrl: edit.fireUrl.trim(), token: edit.token })
    await closeEdit()
  }
  catch (err) {
    editError.value = errorMessage(err)
  }
  finally {
    saving.value = false
  }
}

const fireTexts = reactive<Record<string, string>>({})
const firing = reactive<Record<string, boolean>>({})
const fired = reactive<Record<string, CloudSessionView>>({})
const failures = reactive<Record<string, FireFailure>>({})
const fireErrors = reactive<Record<string, string>>({})

async function runFire(id: string) {
  if (firing[id])
    return
  firing[id] = true
  delete fired[id]
  delete failures[id]
  delete fireErrors[id]
  try {
    fired[id] = await fire(id, fireTexts[id]?.trim() || undefined)
    fireTexts[id] = ''
  }
  catch (err) {
    if (err instanceof FireFailedError)
      failures[id] = { status: err.status, body: err.body }
    else
      fireErrors[id] = errorMessage(err)
  }
  finally {
    firing[id] = false
  }
}

const confirmDeleteId = ref<string | null>(null)
const deleteErrors = reactive<Record<string, string>>({})
const confirmButton = ref<HTMLElement | null>(null)

async function askDelete(id: string) {
  confirmDeleteId.value = id
  await nextTick()
  confirmButton.value?.focus()
}

async function runDelete(id: string) {
  deleteErrors[id] = ''
  try {
    await remove(id)
  }
  catch (err) {
    deleteErrors[id] = errorMessage(err)
  }
  finally {
    confirmDeleteId.value = null
  }
}

watch(routines, (list) => {
  const live = new Set(list.map(r => r.id))
  for (const record of [fireTexts, firing, fired, failures, fireErrors, deleteErrors]) {
    for (const id of Object.keys(record)) {
      if (!live.has(id))
        delete record[id]
    }
  }
  if (confirmDeleteId.value && !live.has(confirmDeleteId.value))
    confirmDeleteId.value = null
  if (editingId.value && !live.has(editingId.value))
    void closeEdit()
})
</script>

<template>
  <AppCard data-testid="routines-section">
    <template #header>
      <h2 class="text-sm font-semibold text-fg">
        Routines
      </h2>
      <AppBadge variant="info" label="Preview" />
    </template>

    <form class="flex flex-col gap-3 p-3 border-b border-line" data-testid="routine-create-form" @submit.prevent="runCreate">
      <div class="grid gap-3 sm:grid-cols-[1fr_14rem]">
        <AppInput v-model="newName" label="Name" data-testid="routine-name" />
        <div>
          <AppFieldLabel for="routine-spawner">
            Spawner
          </AppFieldLabel>
          <AppSelect
            id="routine-spawner"
            v-model="createSpawnerId"
            :options="spawnerOptions"
            placeholder="No claude spawner"
            aria-label="Routine spawner"
            class="w-full"
          />
        </div>
      </div>
      <AppInput
        v-model="newFireUrl"
        label="Fire URL"
        placeholder="https://api.anthropic.com/v1/claude_code/routines/trig_…/fire"
        data-testid="routine-url"
      />
      <div>
        <AppFieldLabel for="routine-token">
          Token
        </AppFieldLabel>
        <input
          id="routine-token"
          v-model="newToken"
          type="password"
          autocomplete="new-password"
          :class="TOKEN_INPUT_CLASS"
          data-testid="routine-token"
        >
      </div>
      <div class="flex items-center gap-3">
        <AppButton variant="primary" size="sm" type="submit" :disabled="!canCreate" data-testid="routine-create">
          {{ creating ? 'Saving…' : 'Add routine' }}
        </AppButton>
        <p v-if="createError" role="alert" class="text-danger-text text-sm" data-testid="routine-create-error">
          {{ createError }}
        </p>
      </div>
    </form>

    <p v-if="loadError" role="alert" class="px-3 py-2 text-sm text-danger-text">
      Could not load routines: {{ loadError }}
    </p>
    <p v-else-if="isLoading" class="px-3 py-3 text-sm text-fg-faint">
      Loading routines…
    </p>
    <p v-else-if="routines.length === 0" class="px-3 py-3 text-sm text-fg-faint">
      No routines yet.
    </p>
    <ul v-else class="divide-y divide-line">
      <li v-for="routine in routines" :key="routine.id" class="flex flex-col gap-2 px-3 py-3" data-testid="routine">
        <div class="flex items-center gap-2 flex-wrap">
          <span class="font-semibold text-fg truncate">{{ routine.name }}</span>
          <span class="text-xs text-fg-mute">{{ spawnerName(routine.spawnerId) }}</span>
          <span class="text-xs text-fg-mute">{{ routine.hasToken ? 'token stored' : 'no token' }}</span>
        </div>
        <div class="font-mono text-xs text-fg-mute truncate" :title="routine.fireUrl">
          {{ routine.fireUrl }}
        </div>

        <div class="flex items-start gap-2">
          <AppInput
            v-model="fireTexts[routine.id]"
            :aria-label="`Text to send when firing ${routine.name}`"
            placeholder="Optional text"
            :disabled="firing[routine.id]"
            :data-testid="`fire-text-${routine.id}`"
          />
          <AppButton
            variant="primary"
            size="sm"
            :class="firing[routine.id] && 'opacity-40 cursor-not-allowed'"
            :aria-disabled="firing[routine.id] || undefined"
            :aria-label="`Fire ${routine.name}`"
            :data-testid="`fire-${routine.id}`"
            @click="runFire(routine.id)"
          >
            {{ firing[routine.id] ? 'Firing…' : 'Fire' }}
          </AppButton>
          <span class="sr-only" role="status" aria-live="polite">{{ firing[routine.id] ? 'Firing…' : '' }}</span>
        </div>

        <p v-if="fired[routine.id]" role="status" class="text-sm text-fg-soft" :data-testid="`fire-result-${routine.id}`">
          Started <span class="font-semibold">{{ fired[routine.id]!.title }}</span>
          <a
            v-if="isCloudUrl(fired[routine.id]!.url)"
            :href="fired[routine.id]!.url"
            target="_blank"
            rel="noopener"
            class="ml-2 text-xs text-accent hover:underline"
          >Open in claude.ai</a>
        </p>
        <div v-else-if="failures[routine.id]" role="alert" class="text-danger-text text-sm" :data-testid="`fire-error-${routine.id}`">
          <div class="font-semibold">
            HTTP {{ failures[routine.id]!.status }}
          </div>
          <p class="line-clamp-3 break-words whitespace-pre-wrap" :aria-hidden="failures[routine.id]!.body.length > BODY_PREVIEW_CHARS || undefined">
            {{ failures[routine.id]!.body }}
          </p>
          <details v-if="failures[routine.id]!.body.length > BODY_PREVIEW_CHARS">
            <summary class="cursor-pointer text-xs">
              Show full response
            </summary>
            <pre class="max-h-48 overflow-auto whitespace-pre-wrap break-words text-xs">{{ failures[routine.id]!.body }}</pre>
          </details>
        </div>
        <p v-else-if="fireErrors[routine.id]" role="alert" class="text-danger-text text-sm" :data-testid="`fire-error-${routine.id}`">
          {{ fireErrors[routine.id] }}
        </p>

        <form
          v-if="editingId === routine.id"
          :id="editFormId(routine.id)"
          class="flex flex-col gap-2 border border-line rounded-md p-3"
          :data-testid="`edit-form-${routine.id}`"
          @submit.prevent="saveEdit(routine.id)"
        >
          <AppInput v-model="edit.name" label="Name" :data-testid="`edit-name-${routine.id}`" />
          <AppInput v-model="edit.fireUrl" label="Fire URL" :data-testid="`edit-url-${routine.id}`" />
          <div>
            <AppFieldLabel :for="`routine-edit-token-${routine.id}`">
              Token
            </AppFieldLabel>
            <input
              :id="`routine-edit-token-${routine.id}`"
              v-model="edit.token"
              type="password"
              autocomplete="new-password"
              :aria-describedby="`routine-token-hint-${routine.id}`"
              :class="TOKEN_INPUT_CLASS"
              :data-testid="`edit-token-${routine.id}`"
            >
          </div>
          <p :id="`routine-token-hint-${routine.id}`" class="text-xs text-fg-mute">
            Leave empty to keep the stored token.
          </p>
          <div class="flex items-center gap-2">
            <AppButton variant="primary" size="sm" type="submit" :disabled="saving" :data-testid="`edit-save-${routine.id}`">
              {{ saving ? 'Saving…' : 'Save' }}
            </AppButton>
            <AppButton variant="secondary" size="sm" :aria-label="`Cancel editing ${routine.name}`" :data-testid="`edit-cancel-${routine.id}`" @click="closeEdit()">
              Cancel
            </AppButton>
            <p v-if="editError" role="alert" class="text-danger-text text-xs">
              {{ editError }}
            </p>
          </div>
        </form>

        <div class="flex items-center gap-2">
          <AppButton
            v-if="editingId !== routine.id"
            :id="editButtonId(routine.id)"
            variant="ghost"
            size="sm"
            :aria-label="`Edit ${routine.name}`"
            :data-testid="`edit-${routine.id}`"
            @click="startEdit(routine)"
          >
            Edit
          </AppButton>
          <template v-if="confirmDeleteId === routine.id">
            <span class="text-xs text-fg-mute">Deletes the routine and its stored token from Kontor.</span>
            <AppButton
              :ref="(el: unknown) => { confirmButton = (el as { $el?: HTMLElement } | null)?.$el ?? null }"
              variant="danger"
              size="sm"
              :aria-label="`Confirm deleting ${routine.name}`"
              :data-testid="`delete-confirm-${routine.id}`"
              @click="runDelete(routine.id)"
            >
              Confirm
            </AppButton>
            <AppButton variant="secondary" size="sm" :aria-label="`Cancel deleting ${routine.name}`" :data-testid="`delete-cancel-${routine.id}`" @click="confirmDeleteId = null">
              Cancel
            </AppButton>
          </template>
          <AppButton v-else variant="ghost" size="sm" :aria-label="`Delete ${routine.name}`" :data-testid="`delete-${routine.id}`" @click="askDelete(routine.id)">
            Delete
          </AppButton>
          <span v-if="deleteErrors[routine.id]" role="alert" class="text-danger-text text-xs">{{ deleteErrors[routine.id] }}</span>
        </div>
      </li>
    </ul>
  </AppCard>
</template>
