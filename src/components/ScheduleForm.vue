<script setup lang="ts">
import type { CreateScheduleBody, RunMode, SchedulePreview, ScheduleView, UpdateScheduleBody } from '../composables/useSchedules'
import { onMounted, ref, watch } from 'vue'
import { useApplications } from '@/features/settings/composables/useApplications'
import { createSchedule, previewSchedule, updateSchedule } from '../composables/useSchedules'
import { useSpawners } from '../composables/useSpawners'
import { formatDateTime } from '../utils/format'
import { nextRadioGroupName } from '../utils/radioGroup'
import { TASK_PRIORITY_OPTIONS } from '../utils/taskOptions'
import AppButton from './ui/AppButton.vue'
import AppInput from './ui/AppInput.vue'
import AppSelect from './ui/AppSelect.vue'

const props = defineProps<{
  schedule?: ScheduleView | null
}>()

const emit = defineEmits<{
  saved: [schedule: ScheduleView]
  cancel: []
}>()

const name = ref(props.schedule?.name ?? '')
const nlText = ref(props.schedule?.nlText ?? '')
const timezone = ref(props.schedule?.timezone ?? 'UTC')
const catchup = ref<'none' | 'once'>(props.schedule?.catchup ?? 'none')
const runMode = ref<RunMode>(props.schedule?.runMode ?? 'job')
const runModeGroup = nextRadioGroupName('schedule-run-mode')
const slugPrefix = ref(props.schedule?.slugPrefix ?? '')
const title = ref(props.schedule?.title ?? '')
const description = ref(props.schedule?.description ?? '')
const cwd = ref(props.schedule?.cwd ?? '')
const priority = ref(props.schedule?.priority ?? 'medium')
const maxIterations = ref(props.schedule?.maxIterations ?? 20)
const permissionTemplate = ref(props.schedule?.permissionTemplate ?? '')

const { applications, fetchApplications } = useApplications()
const selectedApplications = ref<string[]>([...(props.schedule?.applications ?? [])])
const { spawners, refetch: refetchSpawners } = useSpawners({ autoStart: false })
onMounted(() => {
  void fetchApplications()
  void refetchSpawners()
})

const STALE_ID_PREFIX_LENGTH = 8

function limitLine(ids: string[]) {
  const names = ids.map(id => spawners.value.find(s => s.id === id)?.name ?? `Removed spawner (${id.slice(0, STALE_ID_PREFIX_LENGTH)})`)
  return `Limited to: ${names.join(', ')}`
}

const CATCHUP_OPTIONS: Array<{ value: 'none' | 'once', label: string }> = [
  { value: 'none', label: 'None' },
  { value: 'once', label: 'Once' },
]

const preview = ref<SchedulePreview | null>(null)
const previewError = ref<string | null>(null)
const submitError = ref<string | null>(null)
const isSaving = ref(false)

let debounceTimer: ReturnType<typeof setTimeout> | null = null

function schedulePreview() {
  previewError.value = null
  preview.value = null
  if (debounceTimer)
    clearTimeout(debounceTimer)
  const phrase = nlText.value.trim()
  if (!phrase)
    return
  debounceTimer = setTimeout(async () => {
    try {
      preview.value = await previewSchedule({ nlText: phrase, timezone: timezone.value || undefined })
      previewError.value = null
    }
    catch {
      previewError.value = 'Couldn\'t understand that phrase'
      preview.value = null
    }
  }, 400)
}

// Both the phrase and the timezone affect the parsed cron + next runs.
watch([nlText, timezone], schedulePreview)

async function onSubmit() {
  submitError.value = null
  isSaving.value = true
  try {
    const body: CreateScheduleBody = {
      name: name.value.trim(),
      nlText: nlText.value.trim() || undefined,
      timezone: timezone.value || undefined,
      catchup: catchup.value,
      runMode: runMode.value,
      slugPrefix: slugPrefix.value.trim(),
      title: title.value.trim(),
      description: description.value.trim() || undefined,
      cwd: cwd.value.trim(),
      priority: priority.value || undefined,
      maxIterations: maxIterations.value || undefined,
      permissionTemplate: permissionTemplate.value.trim() || undefined,
      applications: selectedApplications.value,
    }

    let saved: ScheduleView
    if (props.schedule) {
      saved = await updateSchedule(props.schedule.id, body as UpdateScheduleBody)
    }
    else {
      saved = await createSchedule(body)
    }
    emit('saved', saved)
  }
  catch (err) {
    submitError.value = (err as Error).message
  }
  finally {
    isSaving.value = false
  }
}
</script>

<template>
  <form class="flex flex-col gap-4" @submit.prevent="onSubmit">
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <AppInput
        v-model="name"
        label="Schedule Name"
        placeholder="Daily standup"
        required
      />
      <AppInput
        v-model="slugPrefix"
        label="Slug Prefix"
        placeholder="sched-daily"
        required
      />
    </div>

    <div class="flex flex-col gap-1">
      <AppInput
        v-model="nlText"
        label="Schedule Phrase"
        placeholder="e.g. &quot;every weekday at 9am&quot;"
      />
      <div
        v-if="previewError"
        role="status"
        aria-live="polite"
        class="text-danger-text text-sm"
      >
        {{ previewError }}
      </div>
      <div
        v-if="preview"
        class="rounded-md bg-raised border border-line p-3 text-sm flex flex-col gap-1"
        aria-live="polite"
      >
        <div class="flex items-center gap-2">
          <span class="text-fg-faint font-mono text-xs">{{ preview.cronExpr }}</span>
          <span class="text-fg-mute">—</span>
          <span class="text-fg font-medium">{{ preview.human }}</span>
        </div>
        <div class="text-fg-faint text-xs mt-1">
          Next 5 runs:
        </div>
        <ul class="flex flex-col gap-0.5">
          <li
            v-for="(run, i) in preview.nextRuns"
            :key="i"
            class="text-fg-mute text-xs font-mono"
          >
            {{ formatDateTime(run) }}
          </li>
        </ul>
      </div>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <AppInput
        v-model="timezone"
        label="Timezone"
        placeholder="UTC"
      />
      <div class="flex flex-col gap-1">
        <label for="schedule-catchup" class="text-sm font-medium text-fg-soft">Catchup</label>
        <AppSelect
          id="schedule-catchup"
          v-model="catchup"
          :options="CATCHUP_OPTIONS"
        />
      </div>
    </div>

    <AppInput
      v-model="title"
      label="Task Title"
      placeholder="Run daily standup task"
      required
    />

    <AppInput
      v-model="description"
      type="textarea"
      label="Description"
      placeholder="Optional task description"
      :rows="2"
      resize="y"
    />

    <fieldset class="flex flex-col gap-2">
      <legend class="text-sm font-medium text-fg">
        Run mode
      </legend>
      <label class="flex items-start gap-2 text-sm">
        <input
          v-model="runMode"
          type="radio"
          :name="runModeGroup"
          value="job"
          data-testid="schedule-run-mode-job"
          class="mt-0.5"
        >
        <span class="flex flex-col">
          <span class="font-medium text-fg">Job</span>
          <span class="text-xs text-fg-mute">One agent run in the working directory. Not shown on the board.</span>
        </span>
      </label>
      <label class="flex items-start gap-2 text-sm">
        <input
          v-model="runMode"
          type="radio"
          :name="runModeGroup"
          value="pipeline"
          data-testid="schedule-run-mode-pipeline"
          class="mt-0.5"
        >
        <span class="flex flex-col">
          <span class="font-medium text-fg">Pipeline task</span>
          <span class="text-xs text-fg-mute">Starts in Ready and runs the full pipeline. The working directory must be a git repository.</span>
        </span>
      </label>
    </fieldset>

    <AppInput
      v-model="cwd"
      label="Working Directory"
      placeholder="/home/user/project"
      required
    />

    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <div class="flex flex-col gap-1">
        <label for="schedule-priority" class="text-sm font-medium text-fg-soft">Priority</label>
        <AppSelect
          id="schedule-priority"
          v-model="priority"
          :options="TASK_PRIORITY_OPTIONS"
        />
      </div>
      <div class="flex flex-col gap-1">
        <label class="text-sm font-medium text-fg-soft">Max Iterations</label>
        <input
          v-model.number="maxIterations"
          type="number"
          min="1"
          class="bg-app border border-line rounded-md px-3 py-1.5 text-sm text-fg focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500"
        >
      </div>
    </div>

    <AppInput
      v-model="permissionTemplate"
      label="Permission Template"
      placeholder="Optional permission template name"
    />

    <fieldset v-if="applications.length" class="flex flex-col gap-1">
      <legend class="text-sm font-medium text-fg">
        Applications
      </legend>
      <p class="text-xs text-fg-mute">
        MCP servers this routine's tasks may use. Tools still run only where a grant allows them.
      </p>
      <div v-for="app in applications" :key="app.resourceId" class="flex flex-col">
        <label class="flex items-center gap-2 text-sm">
          <input
            v-model="selectedApplications"
            type="checkbox"
            :value="app.resourceId"
            :disabled="app.attachAll"
            :data-testid="`schedule-application-${app.resourceId}`"
          >
          {{ app.serverName }}
          <span v-if="app.attachAll" class="text-xs text-fg-mute">attached to every run</span>
        </label>
        <span
          v-if="app.allowedSpawners.length"
          class="text-xs text-fg-mute"
          :data-testid="`schedule-application-limit-${app.resourceId}`"
        >{{ limitLine(app.allowedSpawners) }}</span>
      </div>
    </fieldset>

    <div
      v-if="submitError"
      role="alert"
      class="text-danger-text text-sm"
    >
      {{ submitError }}
    </div>

    <div class="flex gap-2 justify-end pt-2">
      <AppButton
        variant="secondary"
        type="button"
        @click="emit('cancel')"
      >
        Cancel
      </AppButton>
      <AppButton
        variant="primary"
        type="submit"
        :disabled="isSaving"
      >
        {{ isSaving ? 'Saving…' : (schedule ? 'Update Schedule' : 'Create Schedule') }}
      </AppButton>
    </div>
  </form>
</template>
