import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SSE_RETRY_DELAY_MS } from '../../utils/sse'
import { useTerminalSocket } from '../useTerminalSocket'

class MockWebSocket {
  static instances: MockWebSocket[] = []
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  binaryType = 'blob'
  readyState = MockWebSocket.CONNECTING
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onclose: ((e: CloseEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  sent: (string | ArrayBufferLike | ArrayBufferView)[] = []

  constructor(public url: string) {
    MockWebSocket.instances.push(this)
  }

  open() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.({} as Event)
  }

  emit(data: unknown) {
    this.onmessage?.({ data } as MessageEvent)
  }

  send(data: string | ArrayBufferLike | ArrayBufferView) {
    this.sent.push(data)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.({} as CloseEvent)
  }
}

function lastSocket() {
  return MockWebSocket.instances.at(-1)!
}

beforeEach(() => {
  MockWebSocket.instances = []
  vi.stubGlobal('WebSocket', MockWebSocket)
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useTerminalSocket', () => {
  it('opens a WebSocket to the pid terminal endpoint', () => {
    useTerminalSocket(1234, { onData: vi.fn() })

    expect(MockWebSocket.instances).toHaveLength(1)
    expect(lastSocket().url).toMatch(/^ws:\/\/.*\/api\/agents\/1234\/terminal$/)
  })

  it('forwards binary frames to onData as Uint8Array', () => {
    const onData = vi.fn()
    useTerminalSocket(1, { onData })

    const bytes = new Uint8Array([1, 2, 3])
    lastSocket().emit(bytes.buffer)

    expect(onData).toHaveBeenCalledTimes(1)
    const received = onData.mock.calls[0][0] as Uint8Array
    expect(received).toBeInstanceOf(Uint8Array)
    expect(Array.from(received)).toEqual([1, 2, 3])
  })

  it('sends binary frames via send()', () => {
    const { send } = useTerminalSocket(1, { onData: vi.fn() })
    lastSocket().open()

    const bytes = new Uint8Array([9, 8, 7])
    send(bytes)

    expect(lastSocket().sent).toHaveLength(1)
    expect(lastSocket().sent[0]).toBe(bytes)
  })

  it('sends a resize control frame via resize()', () => {
    const { resize } = useTerminalSocket(1, { onData: vi.fn() })
    lastSocket().open()

    resize(80, 24)

    expect(lastSocket().sent).toHaveLength(1)
    expect(lastSocket().sent[0]).toBe(JSON.stringify({ resize: { cols: 80, rows: 24 } }))
  })

  it('tracks status through connecting -> open -> closed', () => {
    const { status } = useTerminalSocket(1, { onData: vi.fn() })

    expect(status.value).toBe('connecting')

    lastSocket().open()
    expect(status.value).toBe('open')

    lastSocket().close()
    expect(status.value).toBe('closed')
  })

  it('schedules a reconnect after an unexpected close', () => {
    useTerminalSocket(1, { onData: vi.fn() })
    const before = MockWebSocket.instances.length

    lastSocket().close()
    expect(MockWebSocket.instances.length).toBe(before) // not yet

    vi.advanceTimersByTime(SSE_RETRY_DELAY_MS)
    expect(MockWebSocket.instances.length).toBe(before + 1)
  })

  it('close() tears down the socket and cancels any pending reconnect', () => {
    const { close } = useTerminalSocket(1, { onData: vi.fn() })
    const socket = lastSocket()
    const before = MockWebSocket.instances.length

    socket.close() // unexpected close schedules a reconnect
    close() // explicit close should cancel it

    vi.advanceTimersByTime(SSE_RETRY_DELAY_MS)
    expect(MockWebSocket.instances.length).toBe(before)
  })

  it('close() closes the live socket', () => {
    const { close, status } = useTerminalSocket(1, { onData: vi.fn() })
    lastSocket().open()

    close()

    expect(lastSocket().readyState).toBe(MockWebSocket.CLOSED)
    expect(status.value).toBe('closed')
  })

  it('sends the last requested size on open when resize() was called before open', () => {
    const { resize } = useTerminalSocket(1, { onData: vi.fn() })

    resize(80, 24)
    expect(lastSocket().sent).toHaveLength(0)

    lastSocket().open()

    expect(lastSocket().sent).toHaveLength(1)
    expect(lastSocket().sent[0]).toBe(JSON.stringify({ resize: { cols: 80, rows: 24 } }))
  })

  it('resends the last requested size on every reconnect', () => {
    const { resize } = useTerminalSocket(1, { onData: vi.fn() })
    resize(80, 24)
    lastSocket().open()

    lastSocket().close() // unexpected close schedules a reconnect
    vi.advanceTimersByTime(SSE_RETRY_DELAY_MS)
    lastSocket().open()

    expect(lastSocket().sent).toHaveLength(1)
    expect(lastSocket().sent[0]).toBe(JSON.stringify({ resize: { cols: 80, rows: 24 } }))
  })

  it('does not duplicate the resize frame when resize() is called again after open', () => {
    const { resize } = useTerminalSocket(1, { onData: vi.fn() })
    lastSocket().open()

    resize(80, 24)

    expect(lastSocket().sent).toHaveLength(1)
    expect(lastSocket().sent[0]).toBe(JSON.stringify({ resize: { cols: 80, rows: 24 } }))
  })
})
