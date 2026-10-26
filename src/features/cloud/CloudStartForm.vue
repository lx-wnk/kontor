<script setup lang="ts">
import type { ProjectFolder, Spawner } from '../../types'
import { computed, onMounted, ref, toRef, watch } from 'vue'
import AppButton from '../../components/ui/AppButton.vue'
import AppCard from '../../components/ui/AppCard.vue'
import AppFieldLabel from '../../components/ui/AppFieldLabel.vue'
import AppInput from '../../components/ui/AppInput.vue'
import AppSelect from '../../components/ui/AppSelect.vue'
import { fetchProjectFolders } from '../../composables/useProjectFolders'
import { useProjects } from '../../composables/useProjects'
import { errorMessage } from '../../utils/errorMessage'
import { useClaudeSpawnerPicker } from './claudeSpawners'
import { isCloudUrl } from './cloudUrl'
import { startSession, StartUnconfirmedError } from './composables/useCloudSessions'

const props = defineProps<{ spawners: Spawner[] }>()

const { options: claudeSpawnerOptions, selectedId: startSpawnerId } = useClaudeSpawnerPicker(toRef(props, 'spawners'))
const { projects, refetch: refetchProjects } = useProjects({ autoStart: false })
onMounted(() => void refetchProjects())
const projectOptions = computed(() => projects.value.map(p => ({ value: p.id, label: p.name })))
const startProjectId = ref('')
const startFolders = ref<ProjectFolder[]>([])
const folderOptions = computed(() => startFolders.value.map(f => ({ value: f.id, label: f.label || f.path })))
const startFolderId = ref('')
const startDescription = ref('')
const starting = ref(false)
const startError = ref('')
const startUnconfirmed = ref<StartUnconfirmedError | null>(null)
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
  startUnconfirmed.value = null
  try {
    await startSession({
      spawnerId: startSpawnerId.value,
      folderId: startFolderId.value,
      description: startDescription.value.trim(),
    })
    startDescription.value = ''
  }
  catch (err) {
    if (err instanceof StartUnconfirmedError)
      startUnconfirmed.value = err
    else
      startError.value = errorMessage(err)
  }
  finally {
    starting.value = false
  }
}
</script>

<template>
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
        <!-- focusin, not focus: WebKit does not focus a clicked button, only the search input it opens. -->
        <div @focusin="refetchProjects">
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
      <div v-if="startUnconfirmed" role="alert" class="text-danger-text text-sm" data-testid="start-unconfirmed">
        <p>{{ startUnconfirmed.message }}</p>
        <p v-if="startUnconfirmed.url && isCloudUrl(startUnconfirmed.url)">
          <a :href="startUnconfirmed.url" target="_blank" rel="noopener noreferrer" class="underline">{{ startUnconfirmed.url }}</a>
        </p>
        <p>Import it by URL below if it is not listed.</p>
      </div>
      <p class="text-fg-faint text-xs">
        Claude must trust this folder under the spawner's account. If it does not, the error shows the command to run once.
      </p>
    </form>
  </AppCard>
</template>
