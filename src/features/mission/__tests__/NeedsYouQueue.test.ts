import type { NextThing } from '../composables/useNextThing'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { computed, nextTick, ref } from 'vue'
import { NEEDS_YOU, NEEDS_YOU_FOCUS, OPEN_TASK, PENDING_PERMISSIONS } from '@/composables/openTask'

const items = ref<NextThing[]>([
  { kind: 'permission', key: 'permission:t1', since: '2026-11-19T10:00:00Z', taskId: 't1', taskTitle: 'First', projectName: 'Dashboard', stage: 'implementation', title: 'First', why: 'w' },
  { kind: 'permission', key: 'permission:t2', since: '2026-11-19T10:00:00Z', taskId: 't2', taskTitle: 'Second', projectName: 'Dashboard', stage: 'implementation', title: 'Second', why: 'w' },
  { kind: 'permission', key: 'permission:t3', since: '2026-11-19T10:00:00Z', taskId: 't3', taskTitle: 'Third', projectName: 'Dashboard', stage: 'implementation', title: 'Third', why: 'w' },
])

vi.mock('../components/NextThing.vue', () => ({
  default: {
    props: ['next', 'position'],
    emits: ['open', 'resolved'],
    template: '<div>'
      + '<p data-testid="stub-next" :data-position="position">{{ next ? next.title : "calm" }}</p>'
      + '<button type="button" data-testid="stub-open" @click="$emit(\'open\', \'t1\')">open</button>'
      + '<button type="button" data-testid="stub-resolved" @click="$emit(\'resolved\')">resolved</button>'
      + '</div>',
  },
}))

vi.mock('../composables/useAttentionMarks', () => ({ markSeen: vi.fn() }))

const { markSeen } = await import('../composables/useAttentionMarks')
const { default: NeedsYouQueue } = await import('../components/NeedsYouQueue.vue')

// App.vue is the one owner of usePendingPermissions(tasks) (SSOT) — every
// mount here provides a fresh stand-in instead of letting the component reach
// for a second, out-of-sync cache.
function mountQueue(variant: 'docked' | 'strip', overrides: { openTask?: (taskId: string) => void, refresh?: () => void, kinds?: NextThing['kind'][], focused?: string | null } = {}) {
  return mount(NeedsYouQueue, {
    props: { variant, kinds: overrides.kinds },
    global: {
      provide: {
        [NEEDS_YOU]: computed(() => items.value),
        [PENDING_PERMISSIONS]: { items: ref([]), refresh: overrides.refresh ?? vi.fn() },
        [OPEN_TASK]: overrides.openTask ?? vi.fn(),
        [NEEDS_YOU_FOCUS]: ref(overrides.focused ?? null),
      },
    },
  })
}

const base = { since: '2026-11-19T10:00:00Z', taskTitle: 'T', projectName: 'Dashboard', stage: 'implementation', why: 'w' }
const perm = (id: string, since = base.since): NextThing => ({ ...base, kind: 'permission', key: `permission:${id}`, since, taskId: id, title: id })
const plan: NextThing = { ...base, kind: 'plan', key: 'plan:t9', taskId: 't9', title: 'Plan' }

describe('needsYouQueue', () => {
  it('lists every item with a count and no carousel controls', () => {
    items.value = [perm('t1'), perm('t2'), perm('t3')]
    const w = mountQueue('docked')
    expect(w.findAll('li')).toHaveLength(3)
    expect(w.get('[data-testid="needs-you-count"]').text()).toContain('3')
    for (const id of ['needs-you-next', 'needs-you-prev', 'needs-you-position'])
      expect(w.find(`[data-testid="${id}"]`).exists()).toBe(false)
    w.unmount()
  })

  it('passes each card its position so a moved card re-arms its guard', async () => {
    items.value = [perm('t1'), perm('t2')]
    const w = mountQueue('docked')
    items.value = [perm('t0'), perm('t1'), perm('t2')]
    await nextTick()
    const positions = w.findAll('[data-testid="stub-next"]').map(n => [n.text(), n.attributes('data-position')])
    expect(positions).toEqual([['t0', '0'], ['t1', '1'], ['t2', '2']])
    w.unmount()
  })

  it('puts away the clicked item through markSeen with its key and since', async () => {
    items.value = [perm('t1'), perm('t2', '2026-11-19T11:00:00Z'), perm('t3')]
    const w = mountQueue('docked')
    const button = w.findAll('li')[1]!.get('[data-testid="needs-you-put-away"]')
    expect(button.attributes('aria-label')).toBe('Put away until the agent posts something new')
    await button.trigger('click')
    expect(markSeen).toHaveBeenCalledWith('permission:t2', '2026-11-19T11:00:00Z')
    w.unmount()
  })

  it('is one calm line docked and nothing as a strip when empty', () => {
    items.value = []
    const docked = mountQueue('docked')
    expect(docked.get('[data-testid="stub-next"]').text()).toBe('calm')
    expect(docked.findAll('li')).toHaveLength(0)
    const strip = mountQueue('strip')
    expect(strip.find('[data-testid="needs-you"]').exists()).toBe(false)
    docked.unmount()
    strip.unmount()
  })

  it('announces the strip variant as a status region, unlike the docked one', () => {
    items.value = [perm('t1')]
    const strip = mountQueue('strip')
    expect(strip.get('[data-testid="needs-you"]').attributes()).toMatchObject({ 'role': 'status', 'aria-live': 'polite' })
    const docked = mountQueue('docked')
    expect(docked.get('[data-testid="needs-you"]').attributes('role')).toBeUndefined()
    strip.unmount()
    docked.unmount()
  })

  it('shows only the given kinds, and nothing as a strip when none of them waits', async () => {
    items.value = [perm('t1'), perm('t2'), plan]
    const w = mountQueue('strip', { kinds: ['plan'] })
    expect(w.findAll('li')).toHaveLength(1)
    items.value = [perm('t1')]
    await nextTick()
    expect(w.find('[data-testid="needs-you"]').exists()).toBe(false)
    w.unmount()
  })

  it('opens a task through the injected OPEN_TASK and refreshes the injected instance on resolved', async () => {
    items.value = [perm('t1')]
    const openTask = vi.fn()
    const refresh = vi.fn()
    const w = mountQueue('docked', { openTask, refresh })
    await w.get('[data-testid="stub-open"]').trigger('click')
    expect(openTask).toHaveBeenCalledWith('t1')
    await w.get('[data-testid="stub-resolved"]').trigger('click')
    expect(refresh).toHaveBeenCalledOnce()
    w.unmount()
  })

  it('marks only the focused item as current', () => {
    items.value = [perm('p1'), perm('p2')]
    const w = mountQueue('docked', { focused: 'permission:p1' })
    const lis = w.findAll('li')
    expect(lis[0]!.attributes('aria-current')).toBe('true')
    expect(lis[1]!.attributes('aria-current')).toBeUndefined()
    w.unmount()
  })

  it('scrolls the item that gains focus into view', async () => {
    items.value = [perm('p1'), perm('p2')]
    const focused = ref<string | null>(null)
    const scrollIntoView = vi.fn()
    Element.prototype.scrollIntoView = scrollIntoView
    const w = mount(NeedsYouQueue, {
      props: { variant: 'docked' },
      attachTo: document.body,
      global: { provide: { [NEEDS_YOU]: computed(() => items.value), [PENDING_PERMISSIONS]: { items: ref([]), refresh: vi.fn() }, [OPEN_TASK]: vi.fn(), [NEEDS_YOU_FOCUS]: focused } },
    })
    focused.value = 'permission:p2'
    await nextTick()
    await nextTick()
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'nearest' })
    w.unmount()
  })
})
