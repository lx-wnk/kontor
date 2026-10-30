import { defineAsyncComponent } from 'vue'

// CostTodayWidget and PlanUsageWidget are exported because the workspace widget
// registry places them as cross-feature widgets, same as the cockpit panels.
export { default as CostTodayWidget } from './components/CostTodayWidget.vue'
export { default as PlanUsageWidget } from './components/PlanUsageWidget.vue'
// The Insights tabs mount one view at a time; lazy wrappers keep the barrel out of the entry chunk.
export const CostAnalyticsView = defineAsyncComponent(() => import('./components/CostAnalyticsView.vue'))
export const EvalView = defineAsyncComponent(() => import('./components/EvalView.vue'))
export { useCostAnalytics } from './composables/useCostAnalytics'
export { useEvalMetrics } from './composables/useEvalMetrics'
