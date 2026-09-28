<script setup lang="ts">
import type { Agent, PipelineTask } from './types'
import { computed, defineAsyncComponent, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useAgents } from '@/features/agents/composables/useAgents'
import BacklogForm from '@/features/pipeline/components/BacklogForm.vue'
import { useTasks } from '@/features/pipeline/composables/useTasks'
import ApiKeySettings from '@/features/settings/components/ApiKeySettings.vue'
import LoginPage from './components/LoginPage.vue'
import OnboardingFlow from './components/onboarding/OnboardingFlow.vue'
import ServerReconnectOverlay from './components/ServerReconnectOverlay.vue'
import SessionList from './components/SessionList.vue'
import AppShell from './components/shell/AppShell.vue'
import AppSidebar from './components/shell/AppSidebar.vue'
import AppStatusBar from './components/shell/AppStatusBar.vue'
import AppTopbar from './components/shell/AppTopbar.vue'
import SkeletonCard from './components/shell/SkeletonCard.vue'
import SpawnDialog from './components/SpawnDialog.vue'
import SpotlightSearch from './components/SpotlightSearch.vue'
import ToastHost from './components/ToastHost.vue'
import AppModal from './components/ui/AppModal.vue'
import AppModalHeader from './components/ui/AppModalHeader.vue'
import { useInstallPrompt } from './composables/useInstallPrompt'
import { useOnboarding } from './composables/useOnboarding'
import { usePendingPermissions } from './composables/usePendingPermissions'
import { usePermissionResolve } from './composables/usePermissionResolve'
import { useServerConfig } from './composables/useServerConfig'
import { useSidebar } from './composables/useSidebar'
import { useTheme } from './composables/useTheme'
import { toast } from './composables/useToast'
import { useTodayCost } from './composables/useTodayCost'
import { useUsage } from './composables/useUsage'
import { useUser } from './composables/useUser'
import { useViewState } from './composables/useViewState'
import MissionControlView from './features/mission/MissionControlView.vue'
import { formatCost } from './utils/format'

// PERF-BUNDLE1: AgentModal is only ever rendered on agent selection — split into its own chunk
const AgentModal = defineAsyncComponent(() => import('@/features/agents/components/AgentModal.vue'))
// F-PERF-019: top-level heavy views loaded on demand — each becomes its own chunk
const CockpitView = defineAsyncComponent(() => import('@/features/cockpit/components/CockpitView.vue'))
// Async since the cockpit took over as the landing view: the dashboard is no
// longer on the first-paint path, and a static import keeps its whole subtree
// in the entry chunk, which has a size budget CI enforces.
const DashboardView = defineAsyncComponent(() => import('@/features/cockpit/components/DashboardView.vue'))
const CostAnalyticsView = defineAsyncComponent(() => import('@/features/analytics/components/CostAnalyticsView.vue'))
const EvalView = defineAsyncComponent(() => import('@/features/analytics/components/EvalView.vue'))
const PipelineBoard = defineAsyncComponent(() => import('@/features/pipeline/components/PipelineBoard.vue'))
const WorkflowsView = defineAsyncComponent(() => import('@/features/workflows/components/WorkflowsView.vue'))
const SchedulesView = defineAsyncComponent(() => import('./components/SchedulesView.vue'))
// Heavy modal loaded on demand — split into its own chunk (includes DependencyGraph + StageCostWaterfall).
const TaskModal = defineAsyncComponent(() => import('@/features/pipeline/components/TaskModal.vue'))
// Modal/panel components that drag in marked + dompurify (RefinementChat) and diff (EditGateModal) —
// load on demand so those libs stay out of the first-load entry chunk.
const RefinementChat = defineAsyncComponent(() => import('@/features/pipeline/components/RefinementChat.vue'))
const PlanReviewPanel = defineAsyncComponent(() => import('@/features/pipeline/components/PlanReviewPanel.vue'))
const EditGateModal = defineAsyncComponent(() => import('./components/EditGateModal.vue'))

const { user, authEnabled, loaded, loadUser } = useUser()
const { homedir, loadServerConfig } = useServerConfig()
const { status: onboardingStatus, fetchStatus: fetchOnboardingStatus, visible: showOnboarding, show: showOnboardingFlow, hide: hideOnboardingFlow } = useOnboarding()
const showLogin = computed(() => authEnabled.value && !user.value)
const loginPageRef = ref<InstanceType<typeof LoginPage> | null>(null)
// Move focus to the login control when the auth gate appears (SC 2.4.3)
watch(showLogin, (visible) => {
  if (visible)
    nextTick(() => loginPageRef.value?.focusLogin())
})
const { canInstall, promptInstall } = useInstallPrompt()
const { theme, toggleTheme } = useTheme()

const { activeView, dashboardLayout } = useViewState()
const { handleShortcut: handleSidebarShortcut } = useSidebar()
const { resolveAgent, approveAll } = usePermissionResolve()

onMounted(() => {
  loadUser()
  void loadServerConfig()
})

const { agents, costTrend, filteredAgents, attentionAgents, attentionCount, pendingCapabilityDecisions, selectedAgent, isLoading, error, selectAgent, selectAgentWhenAvailable, startStream: startAgents } = useAgents({ autoStart: false })
const { tasks, selectedTask, selectTask, startStream: startTasks } = useTasks({ autoStart: false })
const { items: permissionItems, approve: approvePermission, deny: denyPermission, decide: decidePermission } = usePendingPermissions(tasks)
const combinedAttentionCount = computed(() => attentionCount.value + permissionItems.value.length + pendingCapabilityDecisions.value.length)
// Today's persisted spend — reuses the shared cost-summary logic so the footer
// and Cost view agree. Distinct from totalCost (cost of agents running now).
const { todayUsd, start: startTodayCost } = useTodayCost()

// Start data streams and fetch onboarding status only after auth is confirmed —
// avoids 401 flood while login page is shown (onboarding status is behind the
// same JWT-protected route group).
watch(loaded, (isLoaded) => {
  if (isLoaded && !showLogin.value) {
    startAgents()
    startTasks()
    startTodayCost()
    fetchOnboardingStatus().then(() => {
      if (onboardingStatus.value && !onboardingStatus.value.completed)
        showOnboardingFlow()
    })
  }
}, { immediate: true })

// Move focus to main content on view change for keyboard/screen-reader users
watch(activeView, () => {
  nextTick(() => document.getElementById('main-content')?.focus())
})

const live = computed(() => !error.value)

// Live burn-rate over the last 5 minutes, computed time-based from the
// client-side cost-trend ring buffer (see useAgents). Falls back to null until
// there are at least two samples spanning a measurable interval.
const BURN_WINDOW_MS = 5 * 60 * 1000
const costDelta = computed(() => {
  const pts = costTrend.value
  if (pts.length < 2)
    return null
  const last = pts[pts.length - 1]
  const cutoff = last.t - BURN_WINDOW_MS
  // Earliest sample at or after the cutoff is the window baseline.
  const baseline = pts.find(p => p.t >= cutoff) ?? pts[0]
  if (baseline.t === last.t)
    return null
  return last.cost - baseline.cost
})

const todayCostLabel = computed(() => (todayUsd.value === null ? '—' : formatCost(todayUsd.value)))

const showSpawnDialog = ref(false)
const activeConceptTask = ref<PipelineTask | null>(null)
const showRefinementChat = ref(false)
const showPlanReview = ref(false)
const activePlanTask = ref<PipelineTask | null>(null)
const showBacklogForm = ref(false)
const showSessions = ref(false)
const showSettings = ref(false)

function openNewTask() {
  showBacklogForm.value = true
}

function onCreateTaskAndRefine(task: PipelineTask) {
  showBacklogForm.value = false
  activeConceptTask.value = task
  showRefinementChat.value = true
}

// Keyboard focus for the triage band: `n` cycles attention agents (wrapping).
const focusedSessionId = ref<string | null>(null)
// Guards the keyboard resolve shortcuts (a/d/⇧A) against rapid double-fire.
const kbResolving = ref(false)

function focusNextAttention() {
  const list = attentionAgents.value
  if (!list.length)
    return
  const idx = list.findIndex(a => a.sessionId === focusedSessionId.value)
  focusedSessionId.value = list[(idx + 1) % list.length].sessionId
}

function resolveFocused(outcome: 'granted' | 'denied') {
  if (kbResolving.value)
    return
  const focused = attentionAgents.value.find(a => a.sessionId === focusedSessionId.value)
  if (!focused?.pipelineTaskId || !focused.pendingPermissions?.length)
    return
  kbResolving.value = true
  void resolveAgent(focused, outcome).then((err) => {
    if (err)
      toast.error(err)
  }).finally(() => { kbResolving.value = false })
}

// Shift+D theme + Cmd/Ctrl+B sidebar are global; n/a/d/⇧A/c act on the triage
// band and are gated to the dashboard with no overlay open and no field focused.
function handleKeydown(e: KeyboardEvent) {
  handleSidebarShortcut(e)

  const tag = (e.target as HTMLElement)?.tagName
  const isTyping = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (e.target as HTMLElement).isContentEditable
  // key normalised so CapsLock doesn't break the lower-case shortcuts
  const key = e.key.toLowerCase()

  if (key === 'd' && e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey && !isTyping) {
    toggleTheme()
    return
  }

  // Any open modal/dialog (incl. Spotlight) suppresses the single-key shortcuts.
  const overlayOpen = selectedAgent.value || showSettings.value || showSpawnDialog.value
    || showBacklogForm.value || showSessions.value || showRefinementChat.value || activeConceptTask.value || showPlanReview.value || activePlanTask.value
    || document.querySelector('[role="dialog"], [aria-modal="true"]') !== null
  if (activeView.value !== 'dashboard' || overlayOpen || isTyping || e.ctrlKey || e.metaKey || e.altKey)
    return

  if (key === 'n') {
    e.preventDefault()
    focusNextAttention()
  }
  else if (key === 'a' && e.shiftKey) {
    e.preventDefault()
    if (kbResolving.value)
      return
    kbResolving.value = true
    void approveAll(attentionAgents.value.filter(a => a.pipelineTaskId && a.pendingPermissions?.length)).then((err) => {
      if (err)
        toast.error(err)
    }).finally(() => { kbResolving.value = false })
  }
  else if (key === 'a') {
    e.preventDefault()
    resolveFocused('granted')
  }
  else if (key === 'd') {
    e.preventDefault()
    resolveFocused('denied')
  }
  else if (key === 'c') {
    e.preventDefault()
    dashboardLayout.value = dashboardLayout.value === 'cards' ? 'list' : 'cards'
  }
}

onMounted(() => window.addEventListener('keydown', handleKeydown))
onUnmounted(() => window.removeEventListener('keydown', handleKeydown))

function navigateTo(target: { agent?: Agent, taskId?: string }) {
  selectAgent(null)
  selectTask(null)
  nextTick(() => {
    if (target.agent)
      selectAgent(target.agent)
    if (target.taskId) {
      const t = tasks.value.find(t => t.id === target.taskId)
      if (t) {
        openTask(t)
      }
      else {
        console.warn('[navigateTo] task not found locally:', target.taskId)
        toast.error('Task not found — it may belong to a different machine.')
      }
    }
  })
}

// Single routing rule: plan_review tasks open the plan panel, all others the generic modal.
function openTask(t: PipelineTask) {
  if (t.currentStage === 'plan_review') {
    activePlanTask.value = t
    showPlanReview.value = true
    return
  }
  selectTask(t)
}

const usageComposable = useUsage()
onMounted(() => usageComposable.start())
</script>

<template>
  <LoginPage v-if="loaded && showLogin" ref="loginPageRef" />
  <div v-else-if="loaded">
    <AppShell>
      <template #sidebar>
        <AppSidebar
          :agent-count="filteredAgents.length"
          :attention-count="combinedAttentionCount"
          :task-count="tasks.length"
          :live="live"
          :theme="theme"
          :can-install="canInstall"
          @open-sessions="showSessions = true"
          @toggle-theme="toggleTheme"
          @install="promptInstall"
          @open-settings="showSettings = true"
        />
      </template>

      <template #topbar>
        <AppTopbar :active-view="activeView">
          <template #cta>
            <button
              v-if="activeView === 'pipeline'"
              type="button"
              class="bg-accent text-white rounded-lg px-3 py-1.5 text-[13px] font-semibold hover:brightness-110 focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
              @click="openNewTask"
            >
              + New Task
            </button>
            <button
              v-else-if="activeView === 'dashboard' || activeView === 'cockpit'"
              type="button"
              class="bg-accent text-white rounded-lg px-3 py-1.5 text-[13px] font-semibold hover:brightness-110 focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card"
              @click="showSpawnDialog = true"
            >
              + New Agent
            </button>
          </template>
        </AppTopbar>
      </template>

      <div class="p-5 flex flex-col min-h-full">
        <div v-if="isLoading && activeView === 'dashboard'" class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
          <SkeletonCard v-for="n in 6" :key="n" />
        </div>
        <p v-else-if="error" class="text-center py-12 text-danger-text">
          Error: {{ error }}
        </p>

        <MissionControlView
          v-else-if="activeView === 'mission'"
          @open-task="(taskId: string) => navigateTo({ taskId })"
        />
        <CockpitView v-else-if="activeView === 'cockpit'" />

        <DashboardView
          v-else-if="activeView === 'dashboard'"
          :permission-items="permissionItems"
          :focused-session-id="focusedSessionId"
          @approve="(taskId, ids, remember) => approvePermission(taskId, ids, remember)"
          @deny="(taskId, ids) => denyPermission(taskId, ids)"
          @decide="(taskId, ids, decision) => decidePermission(taskId, ids, decision)"
        />

        <PipelineBoard
          v-else-if="activeView === 'pipeline'"
          :agents="agents"
          @select="openTask"
          @open-chat="(t) => { activeConceptTask = t; showRefinementChat = true }"
          @navigate-agent="(sessionId) => { const a = agents.find(x => x.sessionId === sessionId); if (a) selectAgent(a) }"
        />
        <CostAnalyticsView v-else-if="activeView === 'cost'" />
        <SchedulesView v-else-if="activeView === 'schedules'" />
        <EvalView v-else-if="activeView === 'eval'" />
        <WorkflowsView
          v-else-if="activeView === 'workflows'"
          @navigate="(sessionId) => { const a = agents.find(x => x.sessionId === sessionId); if (a) selectAgent(a) }"
        />
      </div>

      <template #statusbar>
        <AppStatusBar :cost-delta="costDelta" :today-cost-label="todayCostLabel" :usage-data="usageComposable.data.value" />
      </template>
    </AppShell>

    <AgentModal :agent="selectedAgent" @close="selectAgent(null)" @navigate="(taskId: string) => navigateTo({ taskId })" />
    <TaskModal
      :task="selectedTask"
      @close="selectTask(null)"
      @navigate="(agent: Agent) => navigateTo({ agent })"
      @navigate-task="(taskId: string) => navigateTo({ taskId })"
      @open-chat="(t) => { selectTask(null); activeConceptTask = t; showRefinementChat = true }"
    />

    <!-- PWA update banner: shown when a new service worker is waiting to activate. -->
    <!-- F-UIUX-016: role="status" + aria-live rendered unconditionally so screen readers
         announce the content when it is inserted (ARIA live regions must exist in the DOM
         before content changes to be reliably announced). -->
    <div role="status" aria-live="polite" aria-atomic="true" class="pointer-events-none">
      <Transition name="toast" />
    </div>
    <ToastHost />
    <SpawnDialog :open="showSpawnDialog" @close="showSpawnDialog = false" @spawned="selectAgentWhenAvailable" />
    <RefinementChat
      :open="showRefinementChat"
      :task="activeConceptTask"
      @close="showRefinementChat = false; activeConceptTask = null"
      @confirmed="showRefinementChat = false; activeConceptTask = null"
    />
    <PlanReviewPanel
      :open="showPlanReview"
      :task="activePlanTask"
      @close="showPlanReview = false; activePlanTask = null"
      @approved="showPlanReview = false; activePlanTask = null"
      @rejected="showPlanReview = false; activePlanTask = null"
    />
    <AppModal :open="showBacklogForm" width="560px" @close="showBacklogForm = false">
      <AppModalHeader title="New Task" @close="showBacklogForm = false" />
      <div class="flex-1 min-h-0 overflow-y-auto p-5">
        <BacklogForm @created-and-refine="onCreateTaskAndRefine" />
      </div>
    </AppModal>
    <SessionList :open="showSessions" :home-dir="homedir" @close="showSessions = false" />
    <ApiKeySettings :open="showSettings" @close="showSettings = false" />
    <OnboardingFlow :open="showOnboarding" @close="hideOnboardingFlow" @spawned="selectAgentWhenAvailable" />
    <EditGateModal />
    <ServerReconnectOverlay />
    <SpotlightSearch
      @navigate-task="task => openTask(task)"
      @navigate-agent="agent => selectAgent(agent)"
    />
  </div>
  <div v-else class="min-h-screen bg-app" />
</template>

<style>
.toast-enter-active, .toast-leave-active { transition: opacity 0.2s, transform 0.2s; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateX(-50%) translateY(8px); }
</style>
