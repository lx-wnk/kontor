import type { NextThing } from '../composables/useNextThing'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref } from 'vue'
import { useCapabilityDecisions } from '@/features/agents'
import { resolvePermissionRequest } from '@/features/pipeline'
import { useNeedsYouTriage } from '../composables/useNeedsYouTriage'

vi.mock('@/features/pipeline', () => ({ resolvePermissionRequest: vi.fn() }))
vi.mock('@/features/agents', () => ({ useCapabilityDecisions: vi.fn() }))

const resolveCapability = vi.fn()

function perm(id: string, taskId = 't1'): NextThing {
  return { kind: 'permission', key: `permission:${id}`, taskId, request: { id } } as unknown as NextThing
}
const question = { kind: 'question', key: 'question:s1', taskId: '' } as NextThing
const capability = { kind: 'capability', key: 'capability:c1', taskId: '', decision: { id: 'c1' } } as unknown as NextThing

function setup(items: NextThing[]) {
  const list = ref(items)
  const refresh = vi.fn()
  const triage = useNeedsYouTriage(computed(() => list.value), refresh)
  return { list, refresh, ...triage }
}

beforeEach(() => {
  vi.mocked(resolvePermissionRequest).mockReset().mockResolvedValue(undefined)
  resolveCapability.mockReset().mockResolvedValue({ outcome: 'applied' })
  vi.mocked(useCapabilityDecisions).mockReturnValue({ resolvingIds: ref({}), resolve: resolveCapability })
})

describe('useNeedsYouTriage', () => {
  it('focusNext walks the list and wraps', () => {
    const t = setup([perm('p1'), question, capability])
    const seen: Array<string | null> = []
    for (let i = 0; i < 4; i++) {
      t.focusNext()
      seen.push(t.focusedKey.value)
    }
    expect(seen).toEqual(['permission:p1', 'question:s1', 'capability:c1', 'permission:p1'])
  })

  it('focusNext on an empty list leaves null; a missing key restarts at the first item', () => {
    const empty = setup([])
    empty.focusNext()
    expect(empty.focusedKey.value).toBeNull()

    const t = setup([perm('p1'), perm('p2')])
    t.focusedKey.value = 'permission:gone'
    t.focusNext()
    expect(t.focusedKey.value).toBe('permission:p1')
  })

  it('allow on a permission resolves allow_once', async () => {
    const t = setup([perm('p1')])
    t.focusNext()
    expect(await t.decide('allow')).toBeNull()
    expect(resolvePermissionRequest).toHaveBeenCalledWith('t1', 'p1', 'allow_once')
    expect(t.refresh).toHaveBeenCalledTimes(1)
  })

  it('deny on a permission resolves deny_once', async () => {
    const t = setup([perm('p1')])
    t.focusNext()
    await t.decide('deny')
    expect(resolvePermissionRequest).toHaveBeenCalledWith('t1', 'p1', 'deny_once')
  })

  it('deny on a capability resolves it through the capability API', async () => {
    const t = setup([perm('p1'), capability])
    t.focusedKey.value = 'capability:c1'
    expect(await t.decide('deny')).toBeNull()
    expect(resolveCapability).toHaveBeenCalledWith('c1', 'deny')
    expect(resolvePermissionRequest).not.toHaveBeenCalled()
  })

  it('returns the capability error message and does not refresh', async () => {
    resolveCapability.mockResolvedValue({ outcome: 'error', message: 'boom' })
    const t = setup([capability])
    t.focusNext()
    expect(await t.decide('allow')).toBe('boom')
    expect(t.refresh).not.toHaveBeenCalled()
  })

  it('returns the permission failure message', async () => {
    vi.mocked(resolvePermissionRequest).mockRejectedValue(new Error('nope'))
    const t = setup([perm('p1')])
    t.focusNext()
    expect(await t.decide('allow')).toBe('nope')
    expect(t.refresh).not.toHaveBeenCalled()
  })

  it('does nothing for kinds that cannot be decided by key', async () => {
    const t = setup([question])
    t.focusNext()
    expect(await t.decide('allow')).toBeNull()
    expect(resolvePermissionRequest).not.toHaveBeenCalled()
    expect(resolveCapability).not.toHaveBeenCalled()
  })

  it('does nothing without focus', async () => {
    const t = setup([perm('p1')])
    expect(await t.decide('allow')).toBeNull()
    expect(resolvePermissionRequest).not.toHaveBeenCalled()
  })

  it('drops focus when the focused item leaves the list, so a/d never hit its replacement', async () => {
    const t = setup([perm('p1')])
    t.focusNext()
    t.list.value = [perm('p2')]
    await Promise.resolve()
    expect(t.focusedKey.value).toBeNull()
    expect(await t.decide('allow')).toBeNull()
    expect(resolvePermissionRequest).not.toHaveBeenCalled()
  })

  it('keeps focus while the focused item stays in the list', async () => {
    const t = setup([perm('p1'), perm('p2')])
    t.focusNext()
    t.list.value = [perm('p0'), perm('p1')]
    await Promise.resolve()
    expect(t.focusedKey.value).toBe('permission:p1')
  })

  it('approveAll allows every permission, never a capability, then refreshes once', async () => {
    const t = setup([perm('p1'), perm('p2', 't2'), capability])
    expect(await t.approveAll(t.list.value)).toBeNull()
    expect(resolvePermissionRequest).toHaveBeenCalledTimes(2)
    expect(resolvePermissionRequest).toHaveBeenCalledWith('t1', 'p1', 'allow_once')
    expect(resolvePermissionRequest).toHaveBeenCalledWith('t2', 'p2', 'allow_once')
    expect(resolveCapability).not.toHaveBeenCalled()
    expect(t.refresh).toHaveBeenCalledTimes(1)
  })

  it('approveAll still resolves the rest and refreshes when one fails, and reports the failure', async () => {
    vi.mocked(resolvePermissionRequest).mockRejectedValueOnce(new Error('first failed'))
    const t = setup([perm('p1'), perm('p2')])
    expect(await t.approveAll(t.list.value)).toBe('first failed')
    expect(resolvePermissionRequest).toHaveBeenCalledTimes(2)
    expect(t.refresh).toHaveBeenCalledTimes(1)
  })

  it('approveAll on a list without permissions makes no call and does not refresh', async () => {
    const t = setup([question, capability])
    expect(await t.approveAll(t.list.value)).toBeNull()
    expect(resolvePermissionRequest).not.toHaveBeenCalled()
    expect(t.refresh).not.toHaveBeenCalled()
  })

  describe('in-flight guard', () => {
    function deferred() {
      let release!: () => void
      const promise = new Promise<void>((resolve) => {
        release = resolve
      })
      return { promise, release }
    }

    it('ignores a second allow while the first is pending', async () => {
      const gate = deferred()
      vi.mocked(resolvePermissionRequest).mockReturnValue(gate.promise)
      const t = setup([perm('p1')])
      t.focusNext()
      const first = t.decide('allow')
      expect(await t.decide('allow')).toBeNull()
      gate.release()
      await first
      expect(resolvePermissionRequest).toHaveBeenCalledTimes(1)
      expect(t.refresh).toHaveBeenCalledTimes(1)
    })

    it('ignores deny after allow while the first is pending', async () => {
      const gate = deferred()
      vi.mocked(resolvePermissionRequest).mockReturnValue(gate.promise)
      const t = setup([perm('p1')])
      t.focusNext()
      const first = t.decide('allow')
      await t.decide('deny')
      gate.release()
      await first
      expect(resolvePermissionRequest).toHaveBeenCalledTimes(1)
      expect(resolvePermissionRequest).toHaveBeenCalledWith('t1', 'p1', 'allow_once')
    })

    it('resolves each permission once when approveAll overlaps itself', async () => {
      const gate = deferred()
      vi.mocked(resolvePermissionRequest).mockReturnValue(gate.promise)
      const t = setup([perm('p1'), perm('p2', 't2')])
      const first = t.approveAll(t.list.value)
      const second = t.approveAll(t.list.value)
      gate.release()
      await Promise.all([first, second])
      expect(resolvePermissionRequest).toHaveBeenCalledTimes(2)
    })

    it('approveAll skips a permission that decide is resolving', async () => {
      const gate = deferred()
      vi.mocked(resolvePermissionRequest).mockReturnValueOnce(gate.promise)
      const t = setup([perm('p1'), perm('p2', 't2')])
      t.focusNext()
      const pending = t.decide('deny')
      await t.approveAll(t.list.value)
      gate.release()
      await pending
      expect(resolvePermissionRequest).toHaveBeenCalledTimes(2)
      expect(resolvePermissionRequest).toHaveBeenCalledWith('t1', 'p1', 'deny_once')
      expect(resolvePermissionRequest).toHaveBeenCalledWith('t2', 'p2', 'allow_once')
    })

    it('approveAll acts only on the list it is given, permissions only', async () => {
      const t = setup([perm('p9'), capability])
      expect(await t.approveAll([capability, perm('p1')])).toBeNull()
      expect(resolvePermissionRequest).toHaveBeenCalledTimes(1)
      expect(resolvePermissionRequest).toHaveBeenCalledWith('t1', 'p1', 'allow_once')
      expect(resolveCapability).not.toHaveBeenCalled()
    })

    it('allows the same item again after the first call settled', async () => {
      const t = setup([perm('p1')])
      t.focusNext()
      await t.decide('allow')
      await t.decide('allow')
      expect(resolvePermissionRequest).toHaveBeenCalledTimes(2)
    })
  })
})
