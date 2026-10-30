import { defineAsyncComponent } from 'vue'

export { default as SessionDagChart } from './components/visualizations/SessionDagChart.vue'
export const WorkflowsView = defineAsyncComponent(() => import('./components/WorkflowsView.vue'))
