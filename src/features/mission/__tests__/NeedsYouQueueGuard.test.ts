import type { NextThing } from '../composables/useNextThing'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, nextTick, ref } from 'vue'
import { NEEDS_YOU, NEEDS_YOU_FOCUS, NEEDS_YOU_QUEUES, OPEN_TASK, PENDING_PERMISSIONS } from '@/composables/openTask'
import NeedsYouQueue from '../components/NeedsYouQueue.vue'
import { markSeen } from '../composables/useAttentionMarks'

vi.mock('@/features/pipeline', () => ({ resolvePermissionRequest: vi.fn() }))
vi.mock('@/utils/answerQuestion', () => ({ sendQuestionAnswer: vi.fn() }))
vi.mock('@/features/agents', () => ({
  useCapabilityDecisions: () => ({ resolvingIds: ref({}), resolve: vi.fn() }),
  useAgents: () => ({ agents: ref([]), selectAgent: vi.fn() }),
  AgentTerminalModal: { template: '<div />' },
}))
vi.mock('../composables/useAttentionMarks', () => ({ markSeen: vi.fn() }))

const base = { since: '2026-11-19T10:00:00Z', taskTitle: 'T', projectName: 'Dashboard', stage: 'implementation', why: 'w' }
const perm = (id: string): NextThing => ({ ...base, kind: 'permission', key: `permission:${id}`, taskId: id, title: id })

describe('needsYouQueue put away guard', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(markSeen).mockClear()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('disables Put away on the card that moves under the cursor until the guard window passes', async () => {
    const items = ref<NextThing[]>([perm('t0'), perm('t1')])
    const w = mount(NeedsYouQueue, {
      props: { variant: 'docked' },
      attachTo: document.body,
      global: { provide: {
        [NEEDS_YOU]: computed(() => items.value),
        [PENDING_PERMISSIONS]: { items: ref([]), refresh: vi.fn() },
        [OPEN_TASK]: vi.fn(),
        [NEEDS_YOU_FOCUS]: ref(null),
        [NEEDS_YOU_QUEUES]: ref(0),
      } },
    })
    await vi.advanceTimersByTimeAsync(500)
    const firstPutAway = () => w.findAll('[data-testid="needs-you-put-away"]')[0]!

    await firstPutAway().trigger('click')
    expect(markSeen).toHaveBeenCalledTimes(1)
    expect(markSeen).toHaveBeenCalledWith('permission:t0', base.since)

    items.value = [perm('t1')]
    await nextTick()
    expect(firstPutAway().attributes('disabled')).toBeDefined()
    await firstPutAway().trigger('click')
    expect(markSeen).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(500)
    expect(firstPutAway().attributes('disabled')).toBeUndefined()
    w.unmount()
  })
})
