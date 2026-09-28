import type { PipelineTask } from '@/types'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PipelineBoard from '@/features/pipeline/components/PipelineBoard.vue'
import SortableTaskList from '@/features/pipeline/components/SortableTaskList.vue'

// Minimal EventSource stub — useTasks() starts the SSE stream as soon as
// PipelineBoard mounts (it owns the stream by default).
class MockEventSource {
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  close() { this.readyState = 2 }
}

vi.mock('@/composables/useProjects', () => ({
  useProjects: () => ({ projects: { value: [] } }),
}))

vi.mock('@vueuse/integrations/useSortable', () => ({
  useSortable: vi.fn(),
}))

function makeTask(id: string, overrides: Partial<PipelineTask> = {}): PipelineTask {
  return {
    id,
    slug: `slug-${id}`,
    title: `Task ${id}`,
    description: null,
    cwd: '/repo',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'implementation',
    parentTaskId: null,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
    rank: null,
    needsUser: false,
    kind: 'pipeline',
    ...overrides,
  }
}

beforeEach(() => {
  vi.stubGlobal('EventSource', MockEventSource)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('pipelineBoard — routine jobs stay off the board', () => {
  it('renders the pipeline card and no card for a routine job, needs-you column included', async () => {
    const pipelineTask = makeTask('p1', { currentStage: 'implementation', kind: 'pipeline' })
    const job = makeTask('j1', { currentStage: 'job' as PipelineTask['currentStage'], kind: 'job', needsUser: true })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([pipelineTask, job]),
    }))

    const wrapper = shallowMount(PipelineBoard, { attachTo: document.body })
    await flushPromises()
    await flushPromises()

    const allRenderedIds = wrapper.findAllComponents(SortableTaskList)
      .flatMap(list => (list.props('tasks') as PipelineTask[]).map(t => t.id))

    expect(allRenderedIds).toContain('p1')
    expect(allRenderedIds).not.toContain('j1')

    wrapper.unmount()
  })
})
