<script setup lang="ts">
import type { PermissionItem } from '@/composables/usePendingPermissions'
import type { PermissionDecision } from '@/features/pipeline'
import { computed, ref, watch } from 'vue'
import AutoApprovingStrip from '@/components/AutoApprovingStrip.vue'
import ChannelScriptCallout from '@/components/shell/ChannelScriptCallout.vue'
import DashboardToolbar from '@/components/shell/DashboardToolbar.vue'
import { useNow } from '@/composables/useNow'
import { useSpawners } from '@/composables/useSpawners'
import { useViewState } from '@/composables/useViewState'
import { AgentCardGrid, AgentTable, AgentTriageBand, EmptyAgentState, useAgents } from '@/features/agents'
import { agentProjectKey, groupAgents, sortAgents } from '@/utils/agentGroup'
import { friendlyProjectName } from '@/utils/friendlyProjectName'

defineProps<{
  permissionItems: PermissionItem[]
  focusedSessionId: string | null
}>()
const emit = defineEmits<{
  approve: [taskId: string, ids: string[], remember: boolean]
  deny: [taskId: string, ids: string[]]
  decide: [taskId: string, ids: string[], decision: PermissionDecision]
}>()

// autoStart: false, exactly as App.vue calls it — useAgents holds module-level
// state, so this is the same stream App.vue already started, not a second one.
const { agents, filteredAgents, attentionAgents, pendingCapabilityDecisions, searchQuery, selectAgent, dismissAgent } = useAgents({ autoStart: false })
const { dashboardLayout, dashboardSort, dashboardGroup, setDashboardGroup, dashboardProject, dashboardSpawner } = useViewState()
const { spawners } = useSpawners()
const { nowMs } = useNow()

const autoApprovingStrip = ref<InstanceType<typeof AutoApprovingStrip> | null>(null)

// Dashboard roster: project + spawner filter → sort → optional grouping. Project
// options list every known project (pre-filter) so the dropdown stays stable.
const rosterAgents = computed(() => {
  let base = filteredAgents.value
  if (dashboardProject.value !== 'all')
    base = base.filter(a => agentProjectKey(a) === dashboardProject.value)
  if (dashboardSpawner.value !== 'all')
    base = base.filter(a => a.spawnerId === dashboardSpawner.value)
  return sortAgents(base, dashboardSort.value, nowMs.value)
})
const rosterGroups = computed(() => groupAgents(rosterAgents.value, dashboardGroup.value))
const projectOptions = computed(() => [
  { value: 'all', label: 'All projects' },
  ...[...new Map(agents.value.map(a => [agentProjectKey(a), friendlyProjectName(a.projectName)])).entries()]
    .map(([value, label]) => ({ value, label }))
    .sort((a, b) => a.label.localeCompare(b.label)),
])
// Before project keys, the stored filter held a raw projectName; migrate it once
// agents are known, and fall back to 'all' rather than an empty roster.
let projectMigrated = false
watch(agents, (list) => {
  if (projectMigrated || list.length === 0)
    return
  projectMigrated = true
  const stored = dashboardProject.value
  if (stored === 'all' || list.some(a => agentProjectKey(a) === stored))
    return
  const legacy = list.find(a => a.projectName === stored)
  dashboardProject.value = legacy ? agentProjectKey(legacy) : 'all'
}, { immediate: true })
const spawnerOptions = computed(() => [
  { value: 'all', label: 'All spawners' },
  ...spawners.value.map(s => ({ value: s.id, label: s.name })),
])

defineExpose({ rosterAgents })
</script>

<template>
  <AgentTriageBand
    :agents="attentionAgents"
    :permission-items="permissionItems"
    :capability-decisions="pendingCapabilityDecisions"
    :focused-session-id="focusedSessionId"
    @select="selectAgent"
    @remembered="autoApprovingStrip?.load()"
    @approve="(taskId, ids, remember) => emit('approve', taskId, ids, remember)"
    @deny="(taskId, ids) => emit('deny', taskId, ids)"
    @decide="(taskId, ids, decision) => emit('decide', taskId, ids, decision)"
  />
  <AutoApprovingStrip ref="autoApprovingStrip" />
  <DashboardToolbar
    :layout="dashboardLayout"
    :spawner="dashboardSpawner"
    :project="dashboardProject"
    :sort-by="dashboardSort"
    :group-by="dashboardGroup"
    :search-query="searchQuery"
    :project-options="projectOptions"
    :spawner-options="spawnerOptions"
    :total-count="agents.length"
    :shown-count="rosterAgents.length"
    @update:layout="dashboardLayout = $event"
    @update:spawner="dashboardSpawner = $event"
    @update:project="dashboardProject = $event"
    @update:sort-by="dashboardSort = $event"
    @update:group-by="setDashboardGroup($event)"
    @update:search-query="searchQuery = $event"
  />
  <template v-if="dashboardLayout === 'list'">
    <EmptyAgentState v-if="rosterAgents.length === 0" :search-query="searchQuery" />
    <AgentTable v-else :agents="rosterAgents" :groups="rosterGroups" @select="selectAgent" />
  </template>
  <template v-else>
    <EmptyAgentState v-if="rosterAgents.length === 0" :search-query="searchQuery" />
    <AgentCardGrid v-else :agents="rosterAgents" :groups="rosterGroups" :group-by="dashboardGroup" @select="selectAgent" @dismiss="dismissAgent" />
  </template>
  <ChannelScriptCallout />
</template>
