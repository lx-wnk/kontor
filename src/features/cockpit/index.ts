// The cockpit's public surface. The panels are exported because they are
// placements of a shared contract (panelState.ts) reused outside this feature:
// the workspace widget registry renders every panel as a widget. CockpitPanel
// is exported for the same reason — CostTodayWidget in analytics composes it
// directly.
export { default as AgentsPanel } from './components/AgentsPanel.vue'
export { default as CockpitPanel } from './components/CockpitPanel.vue'
export { default as GitHubPanel } from './components/GitHubPanel.vue'
export { default as MemoryPanel } from './components/MemoryPanel.vue'
export { default as PipelinePanel } from './components/PipelinePanel.vue'
export { default as RoutinesPanel } from './components/RoutinesPanel.vue'
export * from './panelState'
