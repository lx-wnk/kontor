import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ScheduleForm from '@/components/ScheduleForm.vue'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('scheduleForm — applications', () => {
  it('sends the applications the user ticked', async () => {
    const calls: Array<{ url: string, init?: RequestInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init })
      if (url === '/api/applications') {
        return { ok: true, status: 200, json: async () => [
          { resourceId: 'res-mail', serverName: 'mail', attachAll: false, allowedSpawners: [], requiredEnv: [], secrets: [], tools: [] },
          { resourceId: 'res-notes', serverName: 'notes', attachAll: true, allowedSpawners: [], requiredEnv: [], secrets: [], tools: [] },
        ] }
      }
      if (url === '/api/spawners')
        return { ok: true, status: 200, json: async () => [] }
      return { ok: true, status: 200, json: async () => ({ id: 's1', applications: ['res-mail'] }) }
    }))

    const wrapper = mount(ScheduleForm)
    await flushPromises()

    expect(wrapper.find('[data-testid="schedule-application-res-notes"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-testid="schedule-application-res-mail"]').setValue(true)
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const post = calls.find(c => c.url === '/api/schedules' && c.init?.method === 'POST')
    expect(JSON.parse(String(post?.init?.body)).applications).toEqual(['res-mail'])
    wrapper.unmount()
  })

  describe('spawner limit line', () => {
    function stubFetch(allowedSpawners: string[]) {
      vi.stubGlobal('fetch', vi.fn(async (url: string) => {
        if (url === '/api/applications') {
          return { ok: true, status: 200, json: async () => [
            { resourceId: 'res-mail', serverName: 'mail', attachAll: false, allowedSpawners, requiredEnv: [], secrets: [], tools: [] },
          ] }
        }
        if (url === '/api/spawners') {
          return { ok: true, status: 200, json: async () => [
            { id: 'sp-1', name: 'Claude (default)' },
            { id: 'sp-2', name: 'Claude Work' },
          ] }
        }
        return { ok: true, status: 200, json: async () => ({}) }
      }))
    }

    it('names the spawners a restricted application is limited to, in stored order', async () => {
      stubFetch(['sp-1', 'sp-2'])
      const wrapper = mount(ScheduleForm)
      await flushPromises()

      expect(wrapper.find('[data-testid="schedule-application-limit-res-mail"]').text()).toBe('Limited to: Claude (default), Claude Work')
      wrapper.unmount()
    })

    it('labels an unknown spawner id as removed', async () => {
      stubFetch(['gone-1234-abcd'])
      const wrapper = mount(ScheduleForm)
      await flushPromises()

      expect(wrapper.find('[data-testid="schedule-application-limit-res-mail"]').text()).toBe('Limited to: Removed spawner (gone-123)')
      wrapper.unmount()
    })

    it('shows no limit line for an unrestricted application', async () => {
      stubFetch([])
      const wrapper = mount(ScheduleForm)
      await flushPromises()

      expect(wrapper.find('[data-testid="schedule-application-res-mail"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="schedule-application-limit-res-mail"]').exists()).toBe(false)
      wrapper.unmount()
    })
  })
})
