import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

let AppSettings: any
let toastMod: typeof import('@/composables/useToast')

beforeEach(async () => {
  vi.resetModules()
  toastMod = await import('@/composables/useToast')
  vi.spyOn(toastMod.toast, 'error')
  AppSettings = (await import('@/features/settings/components/AppSettings.vue')).default
})

describe('appSettings', () => {
  it('calls toast.error when save fails and renders no inline danger text', async () => {
    globalThis.fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => [] })
      .mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({}) })
    const w = mount(AppSettings)
    await flushPromises()
    await (w.vm as any).apply({ key: 'some.key', type: 'bool', value: 'false' }, 'true')
    await nextTick()
    expect(toastMod.toast.error).toHaveBeenCalled()
    expect(w.find('.text-danger-text').exists()).toBe(false)
  })

  it('posts a rebuild restart request', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => [] })
      .mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ status: 'restarting' }) })
    globalThis.fetch = fetchMock
    const w = mount(AppSettings)
    await flushPromises()
    await w.get('[data-testid="rebuild-restart"]').trigger('click')
    await flushPromises()
    expect(fetchMock).toHaveBeenLastCalledWith('/api/admin/restart', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ rebuild: true }),
    }))
  })

  // workspace.layout is edited on the page itself, never as a raw JSON field here.
  it('hides the workspace category while keeping other categories visible', async () => {
    globalThis.fetch = vi.fn().mockResolvedValueOnce({
      ok: true,
      json: async () => [
        { key: 'workspace.layout', type: 'string', value: '', default: '', apply: 'live', category: 'workspace' },
        { key: 'sse.intervalMs', type: 'int', value: '3000', default: '3000', apply: 'restart', category: 'sse' },
      ],
    })
    const w = mount(AppSettings)
    await flushPromises()
    expect(w.text()).not.toContain('workspace.layout')
    expect(w.text()).toContain('sse.intervalMs')
  })

  // A failed rebuild must show the build output, not claim the restart happened.
  it('shows the build output on a failed rebuild instead of claiming success', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => [] })
      .mockResolvedValueOnce({
        ok: false,
        status: 500,
        json: async () => ({ error: 'build failed', output: 'src/main.go:12: syntax error' }),
      })
    globalThis.fetch = fetchMock
    const w = mount(AppSettings)
    await flushPromises()
    await w.get('[data-testid="rebuild-restart"]').trigger('click')
    await flushPromises()
    const output = w.get('[data-testid="rebuild-output"]')
    expect(output.text()).toContain('src/main.go:12: syntax error')
    expect(toastMod.toast.error).toHaveBeenCalled()
  })
})
