import type { NextThing } from '../composables/useNextThing'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { computed, nextTick, ref } from 'vue'
import { NEEDS_YOU, OPEN_TASK, PENDING_PERMISSIONS } from '@/composables/openTask'

const items = ref<NextThing[]>([
  { kind: 'permission', taskId: 't1', taskTitle: 'First', projectName: 'Dashboard', stage: 'implementation', title: 'First', why: 'w' },
  { kind: 'permission', taskId: 't2', taskTitle: 'Second', projectName: 'Dashboard', stage: 'implementation', title: 'Second', why: 'w' },
  { kind: 'permission', taskId: 't3', taskTitle: 'Third', projectName: 'Dashboard', stage: 'implementation', title: 'Third', why: 'w' },
])

vi.mock('../components/NextThing.vue', () => ({
  default: {
    props: ['next'],
    emits: ['open', 'resolved'],
    template: '<div>'
      + '<p data-testid="stub-next">{{ next ? next.title : "calm" }}</p>'
      + '<button type="button" data-testid="stub-open" @click="$emit(\'open\', \'t1\')">open</button>'
      + '<button type="button" data-testid="stub-resolved" @click="$emit(\'resolved\')">resolved</button>'
      + '</div>',
  },
}))

const { default: NeedsYouQueue } = await import('../components/NeedsYouQueue.vue')

// App.vue is the one owner of usePendingPermissions(tasks) (SSOT) — every
// mount here provides a fresh stand-in instead of letting the component reach
// for a second, out-of-sync cache.
function mountQueue(variant: 'docked' | 'strip', overrides: { openTask?: (taskId: string) => void, refresh?: () => void, kinds?: NextThing['kind'][] } = {}) {
  return mount(NeedsYouQueue, {
    props: { variant, kinds: overrides.kinds },
    global: {
      provide: {
        [NEEDS_YOU]: computed(() => items.value),
        [PENDING_PERMISSIONS]: { items: ref([]), refresh: overrides.refresh ?? vi.fn() },
        [OPEN_TASK]: overrides.openTask ?? vi.fn(),
      },
    },
  })
}

describe('needsYouQueue', () => {
  it('shows the first item and its position, and pages with the arrows', async () => {
    const w = mountQueue('docked')
    expect(w.get('[data-testid="stub-next"]').text()).toBe('First')
    expect(w.get('[data-testid="needs-you-position"]').text()).toBe('1 of 3')
    await w.get('[data-testid="needs-you-next"]').trigger('click')
    expect(w.get('[data-testid="stub-next"]').text()).toBe('Second')
    await w.get('[data-testid="needs-you-prev"]').trigger('click')
    await w.get('[data-testid="needs-you-prev"]').trigger('click')
    expect(w.get('[data-testid="stub-next"]').text()).toBe('Third')
    w.unmount()
  })

  // Calm when empty: docked it says so in one line; a strip on another page
  // would be noise on every screen, so it renders nothing.
  it('is one calm line docked and nothing as a strip when empty', async () => {
    items.value = []
    const docked = mountQueue('docked')
    expect(docked.get('[data-testid="stub-next"]').text()).toBe('calm')
    const strip = mountQueue('strip')
    expect(strip.find('[data-testid="needs-you"]').exists()).toBe(false)
    docked.unmount()
    strip.unmount()
  })

  it('announces the strip variant as a status region, unlike the docked one', () => {
    items.value = [{ kind: 'permission', taskId: 't1', taskTitle: 'First', projectName: 'Dashboard', stage: 'implementation', title: 'First', why: 'w' }]
    const strip = mountQueue('strip')
    expect(strip.get('[data-testid="needs-you"]').attributes()).toMatchObject({ 'role': 'status', 'aria-live': 'polite' })
    const docked = mountQueue('docked')
    expect(docked.get('[data-testid="needs-you"]').attributes('role')).toBeUndefined()
    strip.unmount()
    docked.unmount()
  })

  it('shows only the given kinds, and nothing as a strip when none of them waits', async () => {
    const permission: NextThing = { kind: 'permission', taskId: 't1', taskTitle: 'Perm', projectName: 'Dashboard', stage: 'implementation', title: 'Perm', why: 'w' }
    const plan: NextThing = { kind: 'plan', taskId: 't2', taskTitle: 'Plan', projectName: '', stage: 'plan_review', title: 'Plan', why: 'w' }
    items.value = [permission, plan]
    const w = mountQueue('strip', { kinds: ['plan'] })
    expect(w.get('[data-testid="stub-next"]').text()).toBe('Plan')
    expect(w.find('[data-testid="needs-you-position"]').exists()).toBe(false)
    items.value = [permission]
    await nextTick()
    expect(w.find('[data-testid="needs-you"]').exists()).toBe(false)
    w.unmount()
  })

  it('keeps a valid position when the list shrinks', async () => {
    items.value = [
      { kind: 'permission', taskId: 't1', taskTitle: 'First', projectName: 'Dashboard', stage: 'implementation', title: 'First', why: 'w' },
      { kind: 'permission', taskId: 't2', taskTitle: 'Second', projectName: 'Dashboard', stage: 'implementation', title: 'Second', why: 'w' },
      { kind: 'permission', taskId: 't3', taskTitle: 'Third', projectName: 'Dashboard', stage: 'implementation', title: 'Third', why: 'w' },
    ]
    const w = mountQueue('docked')
    await w.get('[data-testid="needs-you-next"]').trigger('click')
    await w.get('[data-testid="needs-you-next"]').trigger('click')
    items.value = items.value.slice(0, 1)
    await nextTick()
    expect(w.get('[data-testid="stub-next"]').text()).toBe('First')
    w.unmount()
  })

  it('opens a task through the injected OPEN_TASK and refreshes the injected instance on resolved', async () => {
    items.value = [
      { kind: 'permission', taskId: 't1', taskTitle: 'First', projectName: 'Dashboard', stage: 'implementation', title: 'First', why: 'w' },
    ]
    const openTask = vi.fn()
    const refresh = vi.fn()
    const w = mountQueue('docked', { openTask, refresh })
    await w.get('[data-testid="stub-open"]').trigger('click')
    expect(openTask).toHaveBeenCalledWith('t1')
    await w.get('[data-testid="stub-resolved"]').trigger('click')
    expect(refresh).toHaveBeenCalledOnce()
    w.unmount()
  })
})
