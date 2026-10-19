import type { UseTerminalSocket, UseTerminalSocketOptions } from '@/composables/useTerminalSocket'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AgentTerminal from './AgentTerminal.vue'

const mocks = vi.hoisted(() => ({
  openMock: vi.fn(),
  writeMock: vi.fn(),
  loadAddonMock: vi.fn(),
  disposeMock: vi.fn(),
  fitMock: vi.fn(),
  sendMock: vi.fn(),
  closeMock: vi.fn(),
  blurMock: vi.fn(),
  focusMock: vi.fn(),
  useTerminalSocketMock: vi.fn(),
  termOnData: { current: null as ((data: string) => void) | null },
  socketOnData: { current: null as ((bytes: Uint8Array) => void) | null },
  screenRows: { current: [] as string[] },
}))

vi.mock('@xterm/xterm', () => {
  class FakeTerminal {
    cols = 80
    rows = 24
    open = mocks.openMock
    write = mocks.writeMock
    loadAddon = mocks.loadAddonMock
    dispose = mocks.disposeMock
    blur = mocks.blurMock
    focus = mocks.focusMock
    buffer = {
      active: {
        viewportY: 0,
        getLine: (i: number) => {
          const text = mocks.screenRows.current[i]
          return text === undefined ? null : { translateToString: () => text }
        },
      },
    }

    onData(cb: (data: string) => void): void {
      mocks.termOnData.current = cb
    }
  }
  return { Terminal: FakeTerminal }
})

vi.mock('@xterm/addon-fit', () => {
  class FakeFitAddon {
    fit = mocks.fitMock
  }
  return { FitAddon: FakeFitAddon }
})

vi.mock('@/composables/useTerminalSocket', () => ({
  useTerminalSocket: (pid: number, opts: UseTerminalSocketOptions): UseTerminalSocket => {
    mocks.useTerminalSocketMock(pid, opts)
    mocks.socketOnData.current = opts.onData
    return {
      send: mocks.sendMock,
      resize: vi.fn(),
      status: { value: 'open' } as UseTerminalSocket['status'],
      close: mocks.closeMock,
    }
  },
}))

describe('agentTerminal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.termOnData.current = null
    mocks.socketOnData.current = null
    mocks.screenRows.current = []
  })

  it('creates a terminal and opens it in the container', () => {
    const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })

    expect(mocks.openMock).toHaveBeenCalledTimes(1)
    expect(mocks.openMock.mock.calls[0][0]).toBeInstanceOf(HTMLElement)
    expect(mocks.useTerminalSocketMock).toHaveBeenCalledWith(1234, expect.any(Object))

    wrapper.unmount()
  })

  it('writes incoming socket bytes to the terminal', () => {
    mount(AgentTerminal, { props: { pid: 1234 } })

    const bytes = new Uint8Array([104, 105])
    mocks.socketOnData.current?.(bytes)

    expect(mocks.writeMock).toHaveBeenCalledWith(bytes)
  })

  it('sends encoded terminal input to the socket', () => {
    mount(AgentTerminal, { props: { pid: 1234 } })

    mocks.termOnData.current?.('x')

    expect(mocks.sendMock).toHaveBeenCalledWith(new TextEncoder().encode('x'))
  })

  it('disposes the terminal and closes the socket on unmount', () => {
    const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })

    wrapper.unmount()

    expect(mocks.disposeMock).toHaveBeenCalledTimes(1)
    expect(mocks.closeMock).toHaveBeenCalledTimes(1)
  })

  describe('question overlay', () => {
    const modalRows = [
      'Pick a colour',
      'What is your favourite colour?',
      '1. Red',
      '2. Green',
      '3. Type something',
      '4. Chat about this',
    ]

    afterEach(() => {
      vi.useRealTimers()
    })

    it('polls the terminal screen and shows the overlay when a question is detected', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })

      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      expect(wrapper.find('[data-testid="question-overlay"]').exists()).toBe(true)
      expect(wrapper.text()).toContain('Red')

      wrapper.unmount()
    })

    it('hides the overlay again once the question leaves the screen', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()
      expect(wrapper.find('[data-testid="question-overlay"]').exists()).toBe(true)

      mocks.screenRows.current = ['$ ']
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      expect(wrapper.find('[data-testid="question-overlay"]').exists()).toBe(false)

      wrapper.unmount()
    })

    it('sends encoded answer tokens through the socket when the overlay answer is submitted', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      await wrapper.findAll('input[type="radio"]')[0].trigger('change')
      await wrapper.find('[data-testid="detected-send-btn"]').trigger('click')

      expect(mocks.sendMock).toHaveBeenCalledWith(new TextEncoder().encode('1'))

      wrapper.unmount()
    })

    it('does not forward raw terminal keystrokes to the socket while the overlay is showing', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()
      expect(wrapper.find('[data-testid="question-overlay"]').exists()).toBe(true)

      mocks.termOnData.current?.('x')

      expect(mocks.sendMock).not.toHaveBeenCalled()

      wrapper.unmount()
    })

    it('resumes forwarding raw terminal keystrokes once the overlay clears', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      mocks.screenRows.current = ['$ ']
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()
      expect(wrapper.find('[data-testid="question-overlay"]').exists()).toBe(false)

      mocks.termOnData.current?.('x')

      expect(mocks.sendMock).toHaveBeenCalledWith(new TextEncoder().encode('x'))

      wrapper.unmount()
    })

    it('takes keyboard focus once mounted', async () => {
      mocks.focusMock.mockClear()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await wrapper.vm.$nextTick()
      expect(mocks.focusMock).toHaveBeenCalledTimes(1)
      wrapper.unmount()
    })

    it('blurs the terminal when the overlay appears and refocuses it once it clears', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()
      mocks.focusMock.mockClear()

      expect(mocks.blurMock).toHaveBeenCalledTimes(1)

      mocks.screenRows.current = ['$ ']
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      expect(mocks.focusMock).toHaveBeenCalledTimes(1)

      wrapper.unmount()
    })

    it('stops polling after unmount', () => {
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      wrapper.unmount()

      mocks.screenRows.current = modalRows

      expect(() => vi.advanceTimersByTime(1000)).not.toThrow()
    })

    it('preserves a selected option across an unchanged-screen poll', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      await wrapper.findAll('input[type="radio"]')[1].trigger('change')
      expect((wrapper.findAll('input[type="radio"]')[1].element as HTMLInputElement).checked).toBe(true)

      // Another poll tick fires WITHOUT the screen changing.
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      // Selection must survive the poll (compare-before-assign keeps the ref stable).
      expect((wrapper.findAll('input[type="radio"]')[1].element as HTMLInputElement).checked).toBe(true)
      // Answered → send button stays enabled.
      expect(wrapper.find('[data-testid="detected-send-btn"]').attributes('disabled')).toBeUndefined()

      wrapper.unmount()
    })

    it('preserves in-progress custom text across an unchanged-screen poll', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      await wrapper.find('[data-testid="detected-custom-toggle"]').trigger('click')
      await wrapper.find('[data-testid="detected-custom-textarea"]').setValue('My typed answer')

      // Another poll tick fires WITHOUT the screen changing.
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      // Typed text must survive the poll.
      expect(
        (wrapper.find('[data-testid="detected-custom-textarea"]').element as HTMLTextAreaElement).value,
      ).toBe('My typed answer')

      wrapper.unmount()
    })

    it('resets the answer when the screen changes to a different modal', async () => {
      mocks.screenRows.current = modalRows
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      await wrapper.find('[data-testid="detected-custom-toggle"]').trigger('click')
      await wrapper.find('[data-testid="detected-custom-textarea"]').setValue('Stale answer')

      // A different modal appears on screen.
      mocks.screenRows.current = [
        'Pick fruits',
        'Which fruits?',
        '1. Apples',
        '2. Bananas',
        '3. Type something',
        '4. Chat about this',
      ]
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      expect(wrapper.text()).toContain('Apples')
      // Stale custom text from the previous modal is gone.
      expect(wrapper.find('[data-testid="detected-custom-textarea"]').exists()).toBe(false)

      wrapper.unmount()
    })

    it('multi-select: toggling two options and submitting sends digits + Tab + Enter', async () => {
      mocks.screenRows.current = [
        'Pick fruits',
        'Which fruits?',
        '1. [ ] Apples',
        '2. [ ] Bananas',
        '3. [ ] Cherries',
        '4. Type something',
        '5. Chat about this',
      ]
      vi.useFakeTimers()
      const wrapper = mount(AgentTerminal, { props: { pid: 1234 } })
      await vi.advanceTimersByTimeAsync(200)
      await wrapper.vm.$nextTick()

      const checkboxes = wrapper.findAll('input[type="checkbox"]')
      await checkboxes[0].trigger('change')
      await checkboxes[2].trigger('change')
      await wrapper.find('[data-testid="detected-send-btn"]').trigger('click')

      const decoder = new TextDecoder()
      const sent = mocks.sendMock.mock.calls.map(([bytes]) => decoder.decode(bytes as Uint8Array))
      expect(sent).toEqual(['1', '3', '\t', '\r'])

      wrapper.unmount()
    })
  })
})
