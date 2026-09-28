import type { PipelineTask } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import LiveWorkRail from '../components/LiveWorkRail.vue'

function task(slug: string, currentStage: string): PipelineTask {
  return { id: slug, slug, title: slug, currentStage, cwd: '/repo' } as PipelineTask
}

function barStates(wrapper: ReturnType<typeof mount>, slug: string): string[] {
  return wrapper.get(`[data-testid="live-${slug}"]`).findAll('span.h-1').map((s) => {
    const c = s.classes()
    if (c.includes('bg-success'))
      return 'done'
    if (c.includes('bg-accent'))
      return 'now'
    return 'todo'
  })
}

describe('liveWorkRail', () => {
  it('marks the stages behind the current one done and the rest not started', () => {
    const wrapper = mount(LiveWorkRail, { props: { tasks: [task('a', 'self_review')] } })
    expect(barStates(wrapper, 'a')).toEqual(['done', 'done', 'now', 'todo'])
    wrapper.unmount()
  })

  // A cancelled task is off the track. The first version treated anything off
  // the track as finished, so ten cancelled tasks drew four full bars each and
  // read as completed work.
  it('never draws a task that is off the track as finished', () => {
    const wrapper = mount(LiveWorkRail, { props: { tasks: [task('c', 'cancelled')] } })
    expect(barStates(wrapper, 'c')).toEqual(['todo', 'todo', 'todo', 'todo'])
    wrapper.unmount()
  })

  it('says so plainly when nothing is running', () => {
    const wrapper = mount(LiveWorkRail, { props: { tasks: [] } })
    expect(wrapper.text()).toContain('Nothing is running')
    wrapper.unmount()
  })
})
