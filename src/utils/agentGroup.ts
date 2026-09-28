import type { Agent } from '../types'
import { AGENT_STATUSES, SPAWNER_SOURCE_ENV } from '../types'
import { STATUS_ORDER } from './agentSort'
import { secondsSince, shortModel } from './format'
import { friendlyProjectName } from './friendlyProjectName'
import { agentDisplayStatus } from './statusColors'

export const AGENT_SORT_OPTIONS = [
  { value: 'latest', label: 'Latest active' },
  { value: 'longest', label: 'Longest running' },
  { value: 'expensive', label: 'Most expensive' },
] as const

// The control that renders these is already labelled "Group", so the options
// name the dimension alone instead of repeating "Group by …" in every row.
export const AGENT_GROUP_OPTIONS = [
  { value: 'none', label: 'No grouping' },
  { value: 'project', label: 'Project' },
  { value: 'status', label: 'Status' },
  { value: 'model', label: 'Model' },
  { value: 'spawner', label: 'Spawner' },
] as const

export type AgentSort = typeof AGENT_SORT_OPTIONS[number]['value']
export type AgentGroup = typeof AGENT_GROUP_OPTIONS[number]['value']

const UNASSIGNED_SPAWNER_KEY = '__unassigned__'

/** Grouping by spawner is redundant while a single spawner is filtered, so the option drops out. */
export function agentGroupOptions(spawnerFilter: string): ReadonlyArray<{ value: AgentGroup, label: string }> {
  return spawnerFilter === 'all'
    ? AGENT_GROUP_OPTIONS
    : AGENT_GROUP_OPTIONS.filter(o => o.value !== 'spawner')
}

/** Falls back to 'none' when the stored grouping is unavailable under the current filter. */
export function resolveGroup(groupBy: AgentGroup, spawnerFilter: string): AgentGroup {
  return agentGroupOptions(spawnerFilter).some(o => o.value === groupBy) ? groupBy : 'none'
}

export interface AgentGrouping {
  key: string
  label: string | null
  agents: Agent[]
  /** Set when the grouping was derived rather than recorded; carries the why. */
  derivedFrom?: string
}

const STATUS_LABELS: Record<string, string> = {
  active: 'Active',
  waiting: 'Waiting on you',
  idle: 'Idle',
}

export function sortAgents(list: Agent[], sortBy: AgentSort, nowMs: number): Agent[] {
  const sorted = [...list]
  if (sortBy === 'longest') {
    sorted.sort((a, b) => b.uptime - a.uptime)
  }
  else if (sortBy === 'expensive') {
    sorted.sort((a, b) => b.costEstimate - a.costEstimate)
  }
  else {
    // latest: ascending seconds-since-activity = most-recently-active first
    sorted.sort((a, b) => {
      const sa = secondsSince(a.lastActivity, nowMs) ?? Infinity
      const sb = secondsSince(b.lastActivity, nowMs) ?? Infinity
      return sa - sb
    })
  }
  return sorted
}

function bucketBy(list: Agent[], keyOf: (agent: Agent) => { key: string, label: string }): AgentGrouping[] {
  const seen = new Map<string, AgentGrouping>()
  for (const agent of list) {
    const { key, label } = keyOf(agent)
    const bucket = seen.get(key)
    if (bucket) {
      bucket.agents.push(agent)
    }
    else {
      seen.set(key, { key, label, agents: [agent] })
    }
  }
  return Array.from(seen.values())
}

// Project names are free text and need not be unique, so the Kontor project ID
// is the identity; an agent outside every project falls back to its folder name.
export function agentProjectKey(agent: Agent): string {
  return agent.projectId || agent.projectName
}

export function groupAgents(list: Agent[], groupBy: AgentGroup): AgentGrouping[] {
  if (groupBy === 'project') {
    return bucketBy(list, agent => ({
      key: agentProjectKey(agent),
      label: friendlyProjectName(agent.projectName),
    }))
  }

  if (groupBy === 'status') {
    return AGENT_STATUSES
      .slice()
      .sort((a, b) => STATUS_ORDER[a] - STATUS_ORDER[b])
      .map(s => ({
        key: s,
        label: STATUS_LABELS[s] ?? s,
        // A working agent is live regardless of its idle/waiting time bucket -
        // folds into 'active' so the band can't disagree with the Working badge.
        agents: list.filter(a => (agentDisplayStatus(a) === 'working' ? 'active' : a.status) === s),
      }))
      .filter(g => g.agents.length > 0)
  }

  if (groupBy === 'model') {
    return bucketBy(list, (agent) => {
      const key = shortModel(agent.model ?? null)
      return { key, label: key }
    })
  }

  if (groupBy === 'spawner') {
    // The server attributes each agent to a configured spawner (from its
    // pipeline task, or from the config dir its process runs on); anything it
    // could not place lands in one trailing bucket.
    const groups = bucketBy(list, agent => (
      agent.spawnerId
        ? { key: agent.spawnerId, label: agent.spawnerName || agent.spawnerId }
        : { key: UNASSIGNED_SPAWNER_KEY, label: 'Unassigned' }
    ))
    for (const group of groups) {
      if (group.agents.some(a => a.spawnerSource === SPAWNER_SOURCE_ENV))
        group.derivedFrom = 'Derived from the config directory these sessions run on'
    }
    // Unattributed agents are the residual bucket, so they trail the named ones.
    return groups.sort((a, b) =>
      Number(a.key === UNASSIGNED_SPAWNER_KEY) - Number(b.key === UNASSIGNED_SPAWNER_KEY))
  }

  return [{ key: 'all', label: null, agents: list }]
}
