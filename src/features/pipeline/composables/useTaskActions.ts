import type { Ref } from 'vue'
import type { SlashCommand } from '@/features/pipeline/components/TaskSlashCommandMenu.vue'
import type { UseTaskDetails } from '@/features/pipeline/composables/useTaskDetails'
import type { PermissionRequest, PipelineTask } from '@/types'
import { onUnmounted, ref } from 'vue'
import {
  analyzeTask,
  bulkResolvePermissionRequests,
  cancelTask,
  completeTask,
  fetchTaskPermissions,
  grantTaskPermission,
  resolvePermissionRequest,
  retryTask,
} from '@/features/pipeline/composables/useTasks'

export const TASK_SLASH_COMMANDS: SlashCommand[] = [
  { name: '/retry', description: 'Retry the current stage' },
  { name: '/grant', description: 'Grant all pending permissions' },
  { name: '/cancel', description: 'Cancel this task' },
  { name: '/status', description: 'Show current stage status' },
  { name: '/help', description: 'List available commands' },
]

// Irreversible actions run on the second click; the armed state lapses after 5 seconds.
function useTwoStepConfirm(run: () => void) {
  const armed = ref(false)
  let timer: ReturnType<typeof setTimeout> | undefined
  function click(): void {
    if (!armed.value) {
      armed.value = true
      timer = setTimeout(() => {
        armed.value = false
      }, 5000)
      return
    }
    clearTimeout(timer)
    armed.value = false
    run()
  }
  onUnmounted(() => clearTimeout(timer))
  return { armed, click }
}

export function useTaskActions(task: Ref<PipelineTask | null>, details: UseTaskDetails) {
  const additionalPrompt = ref('')
  const analysisInfo = ref<{ pid: number, cwd: string } | null>(null)

  const isGranting = ref(false)
  const permError = ref('')

  const cancel = useTwoStepConfirm(() => void details.handleAction(() => cancelTask(task.value!.id)))
  const complete = useTwoStepConfirm(() => void details.handleAction(() => completeTask(task.value!.id), 'Task marked done'))

  function onResolve(req: PermissionRequest, outcome: 'granted' | 'denied'): Promise<void> {
    return details.handleAction(() => resolvePermissionRequest(task.value!.id, req.id, outcome === 'granted' ? 'allow_once' : 'deny_once'))
  }

  function onResolveAll(stageRunId: string, outcome: 'granted' | 'denied'): Promise<void> {
    const group = details.pendingByStageRun.value.find(g => g.stageRunId === stageRunId)
    const ids = group ? group.requests.map(r => r.id) : []
    return details.handleAction(async () => {
      const { errors } = await bulkResolvePermissionRequests(task.value!.id, ids, outcome === 'granted' ? 'allow_once' : 'deny_once')
      if (errors?.length)
        throw new Error(`${errors.length} request(s) failed: ${errors.join('; ')}`)
    })
  }

  async function onGrantPermission(tool: string, pattern: string | null): Promise<boolean> {
    if (!tool.trim()) {
      permError.value = 'Tool name is required'
      return false
    }
    isGranting.value = true
    permError.value = ''
    try {
      await grantTaskPermission(task.value!.id, tool.trim(), pattern?.trim() || null)
      details.permissions.value = await fetchTaskPermissions(task.value!.id)
      return true
    }
    catch (e) {
      permError.value = (e as Error).message
      return false
    }
    finally {
      isGranting.value = false
    }
  }

  async function onAnalyze(): Promise<void> {
    if (!task.value)
      return
    analysisInfo.value = null
    await details.handleAction(async () => {
      analysisInfo.value = await analyzeTask(task.value!.id)
    })
  }

  function onRetry(): Promise<void> {
    return details.handleAction(
      () => retryTask(task.value!.id, additionalPrompt.value || undefined),
      'Stage re-queued — will run when a slot is free',
    )
  }

  async function onSlashSelect(cmd: { name: string }): Promise<void> {
    additionalPrompt.value = ''
    if (!task.value)
      return
    switch (cmd.name) {
      case '/retry':
        await onRetry()
        break
      case '/grant':
        await details.handleAction(async () => {
          for (const group of details.pendingByStageRun.value) {
            const { errors } = await bulkResolvePermissionRequests(task.value!.id, group.requests.map(r => r.id), 'allow_once')
            if (errors?.length)
              throw new Error(`${errors.length} request(s) failed: ${errors.join('; ')}`)
          }
        })
        break
      case '/cancel':
        await details.handleAction(() => cancelTask(task.value!.id))
        break
    }
  }

  return {
    additionalPrompt,
    analysisInfo,
    isGranting,
    permError,
    cancelConfirm: cancel.armed,
    completeConfirm: complete.armed,
    slashCommands: TASK_SLASH_COMMANDS,
    onCancelClick: cancel.click,
    onCompleteClick: complete.click,
    onResolve,
    onResolveAll,
    onGrantPermission,
    onAnalyze,
    onRetry,
    onSlashSelect,
  }
}

export type UseTaskActions = ReturnType<typeof useTaskActions>
