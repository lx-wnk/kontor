<script setup lang="ts">
import type { Project, ProjectFolder } from '@/types'
import { computed, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import {
  createFolder,
  deleteFolder,
  fetchProjectFolders,
  updateFolder,
} from '@/composables/useProjectFolders'
import {
  createProject,
  deleteProject,
  updateProject,
  useProjects,
} from '@/composables/useProjects'
import { useSpawners } from '@/composables/useSpawners'
import { toast } from '@/composables/useToast'
import { useProjectPipelineConfig } from '@/features/pipeline'
import { errorMessage } from '@/utils/errorMessage'
import { AVAILABLE_MODELS } from '@/utils/models'
import { derivedSlugHint, SLUG_FORMAT_HINT } from '@/utils/slugHint'
import { STAGE_LABELS } from '@/utils/stageLabels'
import { isAbsolutePath, MAX_DESCRIPTION_CHARS, MAX_PROJECT_NAME_CHARS, slugFollowingName } from '@/utils/validation'

withDefaults(defineProps<{ hideTitle?: boolean }>(), { hideTitle: false })

// Projects composable — auto-loads on mount
const { projects, isLoading, error, refetch } = useProjects()
// Spawner list — feeds the default-spawner dropdown and per-stage spawner pickers
const { spawners } = useSpawners()

// Per-project pipeline config composable (instantiated once; re-fetched on project open)
const { config: projectPipelineConfig, loading: pipelineLoading, error: pipelineError, fetch: fetchProjectPipeline, save: saveProjectPipeline } = useProjectPipelineConfig()

// Surface async load/save failures as toasts; the view keeps its empty/loading state.
watch(error, (msg) => {
  if (msg)
    toast.error(msg)
})
watch(pipelineError, (msg) => {
  if (msg)
    toast.error(msg)
})

const PIPELINE_STAGES = ['implementation', 'self_review', 'finalization'] as const

const spawnerOptions = computed(() => [
  { value: '', label: 'None (use deployment default)' },
  ...spawners.value.map(s => ({ value: s.id, label: `${s.name}${s.builtIn ? ' (built-in)' : ''}` })),
])

const stageSpawnerOptions = computed(() => [
  { value: '', label: 'Inherit global' },
  ...spawners.value.map(s => ({ value: s.id, label: `${s.name}${s.builtIn ? ' (built-in)' : ''}` })),
])

const stageModelOptions = computed(() => [
  { value: '', label: 'Inherit global' },
  ...AVAILABLE_MODELS.map(model => ({ value: model, label: model })),
])

// Draft for per-project pipeline settings
const pipelineDraft = ref<{ stageModels: Record<string, string>, stageSpawners: Record<string, string> } | null>(null)
const pipelineSaved = ref(false)

watch(projectPipelineConfig, (val) => {
  if (val)
    pipelineDraft.value = JSON.parse(JSON.stringify(val))
})

async function handlePipelineSave(projectId: string) {
  if (!pipelineDraft.value)
    return
  pipelineSaved.value = false
  await saveProjectPipeline(projectId, {
    stageModels: { ...pipelineDraft.value.stageModels } as Record<'implementation' | 'self_review' | 'finalization', string>,
    stageSpawners: { ...pipelineDraft.value.stageSpawners } as Record<'implementation' | 'self_review' | 'finalization', string>,
  })
  if (!pipelineError.value) {
    pipelineSaved.value = true
    setTimeout(() => {
      pipelineSaved.value = false
    }, 2500)
  }
}

// ── Edit / create project form ──────────────────────────────────────────────
interface ProjectFormState {
  slug: string
  name: string
  description: string
  color: string
  defaultSpawnerId: string
  setupCommand: string
  vaultFolder: string
}

const editingProject = ref<Project | null>(null)
const isCreating = ref(false)
const formVisible = ref(false)
const formSaving = ref(false)
const form = ref<ProjectFormState>({ slug: '', name: '', description: '', color: '#3b82f6', defaultSpawnerId: '', setupCommand: '', vaultFolder: '' })
const slugTouched = ref(false)
const folderRows = ref<FolderRow[]>([])

function openCreate() {
  editingProject.value = null
  form.value = { slug: '', name: '', description: '', color: '#3b82f6', defaultSpawnerId: '', setupCommand: '', vaultFolder: '' }
  slugTouched.value = false
  formVisible.value = true
  isCreating.value = true
}

function openEdit(project: Project) {
  editingProject.value = project
  form.value = {
    slug: project.slug,
    name: project.name,
    description: project.description ?? '',
    color: project.color ?? '#3b82f6',
    defaultSpawnerId: project.defaultSpawnerId ?? '',
    setupCommand: project.setupCommand ?? '',
    vaultFolder: project.vaultFolder ?? '',
  }
  formVisible.value = true
  isCreating.value = false
  void loadFolders(project.id)
  pipelineDraft.value = null
  void fetchProjectPipeline(project.id)
}

// Watching the flag too is what hands the slug back when the field is emptied.
// openEdit() clears isCreating before this pre-flush watcher runs, so opening an
// existing project never re-derives its slug.
watch([() => form.value.name, slugTouched], ([name, touched]) => {
  if (isCreating.value)
    form.value.slug = slugFollowingName(name, form.value.slug, touched)
})

function onSlugInput(e: Event): void {
  slugTouched.value = (e.target as HTMLInputElement).value.length > 0
}

const slugHint = computed(() => isCreating.value
  ? derivedSlugHint('name')
  : `This project's lookup key — other records already point at it. ${SLUG_FORMAT_HINT}`)

function closeForm() {
  formVisible.value = false
  editingProject.value = null
  folderRows.value = []
  pipelineDraft.value = null
}

async function handleSave() {
  if (!form.value.name.trim() || !form.value.slug.trim()) {
    toast.error('Name and slug are required.')
    return
  }
  formSaving.value = true
  try {
    if (isCreating.value) {
      const project = await createProject({
        slug: form.value.slug.trim(),
        name: form.value.name.trim(),
        description: form.value.description.trim() || undefined,
        color: form.value.color || undefined,
        defaultSpawnerId: form.value.defaultSpawnerId || null,
        setupCommand: form.value.setupCommand.trim() || null,
        vaultFolder: form.value.vaultFolder.trim() || null,
      })
      // Switch to edit mode so folders can be managed
      editingProject.value = project
      isCreating.value = false
      await refetch()
    }
    else if (editingProject.value) {
      await updateProject(editingProject.value.id, {
        slug: form.value.slug.trim(),
        name: form.value.name.trim(),
        description: form.value.description.trim() || undefined,
        color: form.value.color || undefined,
        defaultSpawnerId: form.value.defaultSpawnerId || null,
        setupCommand: form.value.setupCommand.trim() || null,
        vaultFolder: form.value.vaultFolder.trim() || null,
      })
      await refetch()
    }
  }
  catch (e) {
    toast.error(errorMessage(e))
  }
  finally {
    formSaving.value = false
  }
}

// ── Delete confirmation ─────────────────────────────────────────────────────
const confirmDeleteId = ref<string | null>(null)

async function handleDelete(id: string) {
  try {
    await deleteProject(id)
    confirmDeleteId.value = null
    await refetch()
    if (editingProject.value?.id === id)
      closeForm()
  }
  catch (e) {
    toast.error(errorMessage(e))
    confirmDeleteId.value = null
  }
}

// ── Folder management ───────────────────────────────────────────────────────
interface FolderRow extends Partial<ProjectFolder> {
  _key: string
  path: string
  label: string
  isDefault: boolean
  isDirty: boolean
  isNew: boolean
  saving: boolean
  saveError: string | null
}

const folderLoading = ref(false)
let _rowKey = 0

async function loadFolders(projectId: string) {
  folderLoading.value = true
  try {
    const folders = await fetchProjectFolders(projectId)
    folderRows.value = folders.map(f => ({
      ...f,
      _key: String(_rowKey++),
      label: f.label ?? '',
      isDirty: false,
      isNew: false,
      saving: false,
      saveError: null,
    }))
  }
  catch (e) {
    toast.error(errorMessage(e))
  }
  finally {
    folderLoading.value = false
  }
}

function addFolderRow() {
  folderRows.value.push({
    _key: String(_rowKey++),
    path: '',
    label: '',
    isDefault: folderRows.value.length === 0,
    isDirty: true,
    isNew: true,
    saving: false,
    saveError: null,
  })
}

async function saveFolderRow(row: FolderRow) {
  if (!editingProject.value)
    return
  if (!row.path.trim()) {
    row.saveError = 'Path is required.'
    return
  }
  if (!isAbsolutePath(row.path.trim())) {
    row.saveError = 'Path must be absolute (starts with /) and must not contain ".." segments.'
    return
  }
  row.saveError = null
  row.saving = true
  try {
    const input = { path: row.path.trim(), label: row.label.trim() || undefined, isDefault: row.isDefault }
    if (row.isNew) {
      const created = await createFolder(editingProject.value.id, input)
      Object.assign(row, created, { _key: row._key, label: created.label ?? '', isDirty: false, isNew: false, saving: false, saveError: null })
    }
    else if (row.id) {
      const updated = await updateFolder(editingProject.value.id, row.id, input)
      Object.assign(row, updated, { _key: row._key, label: updated.label ?? '', isDirty: false, isNew: false, saving: false, saveError: null })
    }
  }
  catch (e) {
    row.saveError = errorMessage(e)
    row.saving = false
  }
}

async function removeFolderRow(row: FolderRow) {
  if (!editingProject.value)
    return
  if (row.isNew) {
    folderRows.value = folderRows.value.filter(r => r._key !== row._key)
    return
  }
  if (!row.id)
    return
  row.saving = true
  row.saveError = null
  try {
    await deleteFolder(editingProject.value.id, row.id)
    folderRows.value = folderRows.value.filter(r => r._key !== row._key)
  }
  catch (e) {
    row.saveError = errorMessage(e)
    row.saving = false
  }
}

function setDefault(targetRow: FolderRow) {
  for (const r of folderRows.value)
    r.isDefault = r._key === targetRow._key
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- Header -->
    <div class="flex items-start justify-between gap-3">
      <div v-if="!hideTitle">
        <h3 class="text-[17px] font-bold text-fg mb-1">
          Projects
        </h3>
        <p class="text-xs text-fg-mute">
          Group tasks under named projects. Each project can have default folders (working directories) and a spawner override.
        </p>
      </div>
      <AppButton variant="info" class="ml-auto" data-testid="proj-new" @click="openCreate">
        + New Project
      </AppButton>
    </div>

    <!-- Loading -->
    <div v-if="isLoading" class="text-center py-12 text-fg-mute text-sm">
      Loading projects...
    </div>

    <!-- Empty -->
    <div v-else-if="!projects.length && !formVisible" class="text-center py-8 text-fg-mute text-sm">
      No projects yet. Create one to group tasks and set default working directories.
    </div>

    <!-- Project list -->
    <table v-else-if="!formVisible" class="w-full border-collapse text-[13px]">
      <thead>
        <tr>
          <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
            Name
          </th>
          <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
            Slug
          </th>
          <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
            Folders
          </th>
          <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-3 py-2 border-b border-line">
            Actions
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="project in projects" :key="project.id" class="last:[&>td]:border-b-0">
          <td class="px-3 py-2.5 border-b border-line">
            <div class="flex items-center gap-2">
              <span
                v-if="project.color"
                class="inline-block w-2.5 h-2.5 rounded-full flex-shrink-0"
                :style="{ backgroundColor: project.color }"
                aria-hidden="true"
              />
              <span class="font-semibold text-fg">{{ project.name }}</span>
            </div>
            <p v-if="project.description" class="text-[11px] text-fg-mute mt-0.5 line-clamp-1">
              {{ project.description }}
            </p>
          </td>
          <td class="px-3 py-2.5 border-b border-line font-mono text-xs text-fg-mute">
            {{ project.slug }}
          </td>
          <td class="px-3 py-2.5 border-b border-line text-fg-mute">
            {{ project.folderCount ?? 0 }}
          </td>
          <td class="px-3 py-2.5 border-b border-line whitespace-nowrap">
            <template v-if="confirmDeleteId === project.id">
              <AppButton variant="danger" size="sm" class="mr-1" @click="handleDelete(project.id)">
                Confirm Delete
              </AppButton>
              <AppButton variant="secondary" size="sm" @click="confirmDeleteId = null">
                Cancel
              </AppButton>
            </template>
            <template v-else>
              <button
                type="button"
                class="bg-transparent border-none text-fg-mute cursor-pointer text-sm px-2 py-1 rounded hover:bg-blue-50 dark:hover:bg-blue-950/30 hover:text-blue-600 dark:hover:text-blue-400 mr-1"
                :data-testid="`proj-edit-${project.slug}`"
                @click="openEdit(project)"
              >
                Edit
              </button>
              <button
                type="button"
                class="bg-transparent border-none text-fg-mute cursor-pointer text-sm px-2 py-1 rounded hover:bg-red-50 dark:hover:bg-red-950/30 hover:text-red-600 dark:hover:text-red-400"
                @click="confirmDeleteId = project.id"
              >
                Delete
              </button>
            </template>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- Project create/edit form -->
    <div v-if="formVisible" class="flex flex-col gap-4">
      <div class="flex items-center justify-between">
        <h4 class="text-sm font-semibold text-fg">
          {{ isCreating ? 'New Project' : `Edit: ${editingProject?.name}` }}
        </h4>
        <button type="button" class="bg-transparent border-none text-fg-mute text-lg cursor-pointer px-1 leading-none hover:text-fg" @click="closeForm">
          &times;
        </button>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-name">Name</label>
          <input
            id="proj-name"
            v-model="form.name"
            data-testid="proj-name"
            type="text"
            required
            :maxlength="MAX_PROJECT_NAME_CHARS"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
            placeholder="My Project"
          >
        </div>
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-slug">Slug</label>
          <input
            id="proj-slug"
            v-model="form.slug"
            data-testid="proj-slug"
            type="text"
            required
            aria-describedby="proj-slug-hint"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg font-mono focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
            placeholder="my-project"
            @input="onSlugInput"
          >
          <p id="proj-slug-hint" data-testid="proj-slug-hint" class="text-[11px] text-fg-faint mt-0.5">
            {{ slugHint }}
          </p>
        </div>
        <div class="col-span-2">
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-desc">Description (optional)</label>
          <input
            id="proj-desc"
            v-model="form.description"
            type="text"
            :maxlength="MAX_DESCRIPTION_CHARS"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
            placeholder="Short description"
          >
        </div>
        <div class="col-span-2">
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-setup">Setup command (optional)</label>
          <input
            id="proj-setup"
            v-model="form.setupCommand"
            type="text"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg font-mono focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
            placeholder="pnpm install"
          >
          <p class="text-[11px] text-fg-faint mt-0.5">
            Run once in the worktree after it is created. Empty = skip.
          </p>
        </div>
        <div class="col-span-2">
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-vault-folder">Vault folder (optional)</label>
          <input
            id="proj-vault-folder"
            v-model="form.vaultFolder"
            data-testid="proj-vault-folder"
            type="text"
            class="w-full bg-card border border-line rounded px-2.5 py-1.5 text-sm text-fg font-mono focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
            placeholder="work/my-project"
          >
          <p class="text-[11px] text-fg-faint mt-0.5">
            Hub circle for this project's agents, as category/project. Empty = match by name, then repo folder.
          </p>
        </div>
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-color">Color</label>
          <div class="flex items-center gap-2">
            <input
              id="proj-color"
              v-model="form.color"
              type="color"
              class="h-8 w-12 rounded border border-line bg-card cursor-pointer p-0.5"
            >
            <span class="text-xs font-mono text-fg-mute">{{ form.color }}</span>
          </div>
        </div>
        <div>
          <label class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1" for="proj-spawner">Default Spawner (optional)</label>
          <AppSelect
            id="proj-spawner"
            v-model="form.defaultSpawnerId"
            :options="spawnerOptions"
            class="w-full"
          />
        </div>
      </div>

      <div class="flex gap-2">
        <AppButton variant="info" :disabled="formSaving" @click="handleSave">
          {{ formSaving ? 'Saving…' : (isCreating ? 'Create Project' : 'Save Changes') }}
        </AppButton>
        <AppButton variant="secondary" @click="closeForm">
          Cancel
        </AppButton>
      </div>

      <!-- Per-project pipeline config (only shown when editing an existing project) -->
      <template v-if="!isCreating && editingProject">
        <div class="border-t border-line pt-4 mt-1">
          <div class="mb-3">
            <h5 class="text-xs font-semibold uppercase tracking-wider text-fg-mute mb-0.5">
              Pipeline per stage
            </h5>
            <p class="text-[11px] text-fg-mute">
              Override spawner and model per stage. Leave empty to inherit the global setting. The model applies only to Claude-native spawners.
            </p>
          </div>

          <div v-if="pipelineLoading && !pipelineDraft" class="text-xs text-fg-mute py-3">
            Loading pipeline config...
          </div>

          <div v-else-if="pipelineDraft" class="grid grid-cols-1 gap-4">
            <div v-for="stage in PIPELINE_STAGES" :key="stage" class="grid grid-cols-2 gap-3 items-end">
              <div>
                <label
                  class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1"
                  :for="`pp-spawner-${stage}-${editingProject.id}`"
                >
                  {{ STAGE_LABELS[stage] }} — Spawner
                </label>
                <AppSelect
                  :id="`pp-spawner-${stage}-${editingProject.id}`"
                  v-model="pipelineDraft.stageSpawners[stage]"
                  :options="stageSpawnerOptions"
                  class="w-full"
                />
              </div>
              <div>
                <label
                  class="block text-[10px] font-semibold uppercase tracking-wider text-fg-mute mb-1"
                  :for="`pp-model-${stage}-${editingProject.id}`"
                >
                  {{ STAGE_LABELS[stage] }} — Model
                </label>
                <AppSelect
                  :id="`pp-model-${stage}-${editingProject.id}`"
                  v-model="pipelineDraft.stageModels[stage]"
                  :options="stageModelOptions"
                  class="w-full"
                />
              </div>
            </div>
          </div>

          <div class="flex items-center gap-3 mt-3">
            <AppButton variant="secondary" size="sm" :disabled="pipelineLoading || !pipelineDraft" @click="handlePipelineSave(editingProject.id)">
              {{ pipelineLoading ? 'Saving…' : 'Save pipeline config' }}
            </AppButton>
            <span v-if="pipelineSaved" class="text-xs text-emerald-600 dark:text-emerald-400">Saved.</span>
          </div>
        </div>
      </template>

      <!-- Folder management (only shown when editing an existing project) -->
      <template v-if="!isCreating && editingProject">
        <div class="border-t border-line pt-4 mt-1">
          <div class="flex items-center justify-between mb-3">
            <h5 class="text-xs font-semibold uppercase tracking-wider text-fg-mute">
              Folders (Working Directories)
            </h5>
            <AppButton variant="secondary" size="sm" @click="addFolderRow">
              + Add Folder
            </AppButton>
          </div>
          <div v-if="folderLoading" class="text-xs text-fg-mute py-3">
            Loading folders...
          </div>
          <div v-else-if="folderRows.length === 0" class="text-xs text-fg-mute py-3">
            No folders yet. Add one to provide working-directory suggestions when creating tasks.
          </div>
          <table v-else class="w-full border-collapse text-[12px]">
            <thead>
              <tr>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-2 py-1.5 border-b border-line">
                  Path
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-2 py-1.5 border-b border-line">
                  Label
                </th>
                <th class="text-center text-[10px] uppercase tracking-wide text-fg-mute px-2 py-1.5 border-b border-line">
                  Default
                </th>
                <th class="text-left text-[10px] uppercase tracking-wide text-fg-mute px-2 py-1.5 border-b border-line">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in folderRows" :key="row._key" class="last:[&>td]:border-b-0">
                <td class="px-2 py-1.5 border-b border-line">
                  <input
                    v-model="row.path"
                    type="text"
                    class="w-full bg-card border border-line rounded px-2 py-1 text-xs font-mono text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
                    placeholder="/absolute/path"
                    @input="row.isDirty = true"
                  >
                </td>
                <td class="px-2 py-1.5 border-b border-line">
                  <input
                    v-model="row.label"
                    type="text"
                    class="w-full bg-card border border-line rounded px-2 py-1 text-xs text-fg focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:border-accent"
                    placeholder="Optional label"
                    @input="row.isDirty = true"
                  >
                </td>
                <td class="px-2 py-1.5 border-b border-line text-center">
                  <input
                    type="radio"
                    :name="`folder-default-${editingProject.id}`"
                    :checked="row.isDefault"
                    class="accent-blue-600"
                    @change="setDefault(row)"
                  >
                </td>
                <td class="px-2 py-1.5 border-b border-line whitespace-nowrap">
                  <div class="flex items-center gap-1">
                    <button
                      v-if="row.isDirty"
                      type="button"
                      :disabled="row.saving"
                      class="text-[11px] px-2 py-0.5 rounded bg-blue-600 text-white disabled:opacity-50 hover:bg-blue-700"
                      @click="saveFolderRow(row)"
                    >
                      {{ row.saving ? '…' : 'Save' }}
                    </button>
                    <button
                      type="button"
                      :disabled="row.saving"
                      class="text-[11px] px-2 py-0.5 rounded bg-transparent border-none text-red-400 hover:text-red-600 disabled:opacity-50 cursor-pointer"
                      @click="removeFolderRow(row)"
                    >
                      Remove
                    </button>
                  </div>
                  <p v-if="row.saveError" class="text-[10px] text-danger-text mt-0.5">
                    {{ row.saveError }}
                  </p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </div>
</template>
