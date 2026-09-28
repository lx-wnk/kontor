import type { Agent } from '../../types'
import { describe, expect, it } from 'vitest'
import { agentGroupOptions, groupAgents, resolveGroup, sortAgents } from '../agentGroup'

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    pid: 1,
    sessionId: 'sess-1',
    provider: 'claude',
    projectName: 'test-project',
    projectPath: '/test',
    status: 'active',
    uptime: 0,
    lastActivity: new Date(Date.now() - 10_000).toISOString(),
    model: 'claude-opus-4',
    costEstimate: 0,
    currentAction: null,
    errorState: null,
    costUnknown: false,
    pendingPermissions: [],
    channelAvailable: false,
    pipelineTaskId: null,
    machine: null,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    tasks: [],
    subagents: [],
    meta: null,
    ...overrides,
  } as unknown as Agent
}

const NOW_MS = Date.now()

describe('sortAgents', () => {
  it('returns a new array, not the same reference', () => {
    const list = [makeAgent()]
    const result = sortAgents(list, 'latest', NOW_MS)
    expect(result).not.toBe(list)
  })

  it('latest: sorts by ascending seconds-since-activity (most recent first)', () => {
    const recent = makeAgent({ pid: 1, lastActivity: new Date(NOW_MS - 5_000).toISOString() })
    const older = makeAgent({ pid: 2, lastActivity: new Date(NOW_MS - 60_000).toISOString() })
    const result = sortAgents([older, recent], 'latest', NOW_MS)
    expect(result[0].pid).toBe(1)
    expect(result[1].pid).toBe(2)
  })

  it('longest: sorts descending by uptime', () => {
    const short = makeAgent({ pid: 1, uptime: 100 })
    const long = makeAgent({ pid: 2, uptime: 500 })
    const result = sortAgents([short, long], 'longest', NOW_MS)
    expect(result[0].pid).toBe(2)
    expect(result[1].pid).toBe(1)
  })

  it('expensive: sorts descending by costEstimate', () => {
    const cheap = makeAgent({ pid: 1, costEstimate: 0.1 })
    const costly = makeAgent({ pid: 2, costEstimate: 5.0 })
    const result = sortAgents([cheap, costly], 'expensive', NOW_MS)
    expect(result[0].pid).toBe(2)
    expect(result[1].pid).toBe(1)
  })

  it('does not mutate the input array', () => {
    const list = [makeAgent({ pid: 1, uptime: 10 }), makeAgent({ pid: 2, uptime: 20 })]
    const original = [...list]
    sortAgents(list, 'longest', NOW_MS)
    expect(list.map(a => a.pid)).toEqual(original.map(a => a.pid))
  })
})

describe('groupAgents', () => {
  it('project: keys by project id, so equal names of different projects stay apart', () => {
    const groups = groupAgents([
      makeAgent({ pid: 1, projectId: 'p-1', projectName: 'Website' }),
      makeAgent({ pid: 2, projectId: 'p-2', projectName: 'Website' }),
      makeAgent({ pid: 3, projectId: 'p-1', projectName: 'Website' }),
      makeAgent({ pid: 4, projectId: '', projectName: 'loose-folder' }),
    ], 'project')
    expect(groups.map(g => [g.key, g.agents.map(a => a.pid)])).toEqual([
      ['p-1', [1, 3]],
      ['p-2', [2]],
      ['loose-folder', [4]],
    ])
  })

  it('none: returns single group with null label containing all agents', () => {
    const agents = [makeAgent({ pid: 1 }), makeAgent({ pid: 2 })]
    const groups = groupAgents(agents, 'none')
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('all')
    expect(groups[0].label).toBeNull()
    expect(groups[0].agents).toHaveLength(2)
  })

  it('status: groups by status in AGENT_STATUSES order', () => {
    const active = makeAgent({ pid: 1, status: 'active' })
    const idle = makeAgent({ pid: 2, status: 'idle' })
    const waiting = makeAgent({ pid: 3, status: 'waiting' })
    const groups = groupAgents([idle, waiting, active], 'status')
    expect(groups.map(g => g.key)).toEqual(['active', 'waiting', 'idle'])
  })

  it('status: drops empty groups', () => {
    const active = makeAgent({ pid: 1, status: 'active' })
    const groups = groupAgents([active], 'status')
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('active')
  })

  it('status: a working agent bucket-folds into active regardless of its idle/waiting time bucket', () => {
    const workingIdle = makeAgent({ pid: 1, status: 'idle', working: true })
    const groups = groupAgents([workingIdle], 'status')
    expect(groups.map(g => g.key)).toEqual(['active'])
    expect(groups[0].agents.map(a => a.pid)).toEqual([1])
  })

  it('status: applies human-readable labels', () => {
    const a = makeAgent({ status: 'active' })
    const w = makeAgent({ status: 'waiting' })
    const i = makeAgent({ status: 'idle' })
    const groups = groupAgents([a, w, i], 'status')
    const labels = groups.map(g => g.label)
    expect(labels).toContain('Active')
    expect(labels).toContain('Waiting on you')
    expect(labels).toContain('Idle')
  })

  it('model: groups by shortModel, preserving first-seen order', () => {
    const a1 = makeAgent({ pid: 1, model: 'claude-opus-4' })
    const a2 = makeAgent({ pid: 2, model: 'claude-sonnet-4' })
    const a3 = makeAgent({ pid: 3, model: 'claude-opus-4' })
    const groups = groupAgents([a1, a2, a3], 'model')
    expect(groups).toHaveLength(2)
    expect(groups[0].agents.map(a => a.pid)).toEqual([1, 3])
    expect(groups[1].agents.map(a => a.pid)).toEqual([2])
  })

  it('model: uses shortModel for group label', () => {
    const a = makeAgent({ model: 'claude-opus-4' })
    const groups = groupAgents([a], 'model')
    expect(groups[0].label).toBe('opus 4')
  })

  it('spawner: buckets by the spawner the server attributed, labelled with its name', () => {
    const a1 = makeAgent({ pid: 1, spawnerId: 's1', spawnerName: 'Claude (default)' })
    const a2 = makeAgent({ pid: 2, spawnerId: 's2', spawnerName: 'Claude Work' })
    const a3 = makeAgent({ pid: 3, spawnerId: 's1', spawnerName: 'Claude (default)' })
    const groups = groupAgents([a1, a2, a3], 'spawner')
    expect(groups.map(g => [g.key, g.label])).toEqual([['s1', 'Claude (default)'], ['s2', 'Claude Work']])
    expect(groups[0].agents.map(a => a.pid)).toEqual([1, 3])
  })

  it('spawner: collects unattributed agents in a trailing "Unassigned" group', () => {
    const free = makeAgent({ pid: 1, spawnerId: undefined })
    const owned = makeAgent({ pid: 2, spawnerId: 's1', spawnerName: 'Claude Work' })
    const groups = groupAgents([free, owned], 'spawner')
    expect(groups.map(g => g.label)).toEqual(['Claude Work', 'Unassigned'])
    expect(groups[1].agents.map(a => a.pid)).toEqual([1])
  })

  it('spawner: falls back to the id when the server sent no name', () => {
    const groups = groupAgents([makeAgent({ spawnerId: 's1' })], 'spawner')
    expect(groups[0].label).toBe('s1')
  })

  it('spawner: marks a group the server derived rather than recorded', () => {
    const derived = makeAgent({ pid: 1, spawnerId: 's1', spawnerName: 'Claude Work', spawnerSource: 'env' })
    const recorded = makeAgent({ pid: 2, spawnerId: 's2', spawnerName: 'Pipeline', spawnerSource: 'task' })
    const groups = groupAgents([derived, recorded], 'spawner')
    expect(groups[0].derivedFrom).toContain('config directory')
    expect(groups[1].derivedFrom).toBeUndefined()
  })
})

describe('agentGroupOptions / resolveGroup', () => {
  it('offers spawner grouping only while no spawner is filtered', () => {
    expect(agentGroupOptions('all').map(o => o.value)).toContain('spawner')
    expect(agentGroupOptions('s1').map(o => o.value)).not.toContain('spawner')
  })

  it('falls back to "none" when spawner grouping is filtered away', () => {
    expect(resolveGroup('spawner', 'all')).toBe('spawner')
    expect(resolveGroup('spawner', 's1')).toBe('none')
    expect(resolveGroup('status', 's1')).toBe('status')
  })
})
