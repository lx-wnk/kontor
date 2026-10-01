import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const approveMock = vi.fn().mockResolvedValue({ id: 't1', currentStage: 'implementation' })
const rejectMock = vi.fn().mockResolvedValue(undefined)
const fetchStatusMock = vi.fn().mockResolvedValue(undefined)
const startMock = vi.fn().mockResolvedValue(undefined)
const stopMock = vi.fn()

vi.mock('@/features/pipeline/composables/usePlanReview', () => ({
  usePlanReview: () => ({
    gateState: ref('awaiting_user'),
    approvedPlan: ref({ steps: ['step one', 'step two'] }),
    loading: ref(false),
    error: ref(null),
    fetchStatus: fetchStatusMock,
    start: startMock,
    stop: stopMock,
    approve: approveMock,
    reject: rejectMock,
  }),
}))

vi.mock('@/utils/markdown', () => ({
  renderMarkdown: (text: string) => `<p>${text}</p>`,
}))

// Loaded lazily — import after mocks are in place.
let PlanReviewPanel: any

beforeEach(async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 200, json: async () => ({}) })))
  const mod = await import('@/features/pipeline/components/PlanReviewPanel.vue')
  PlanReviewPanel = mod.default
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  vi.resetModules()
})

function mountPanel(overrides: Record<string, unknown> = {}) {
  return mount(PlanReviewPanel, {
    props: {
      open: true,
      task: { id: 't1', slug: 's1', title: 'My Task', currentStage: 'plan_review', createdAt: '', updatedAt: '' },
      ...overrides,
    },
    attachTo: document.body,
  })
}

describe('planReviewPanel', () => {
  it('renders nothing when open=false', async () => {
    const wrapper = mountPanel({ open: false })
    await flushPromises()
    expect(wrapper.find('[data-testid="plan-review-panel"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the plan content when open', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.find('[data-testid="plan-review-panel"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('calls start when opened with a task', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    expect(startMock).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('emits approved with updated task when Approve is clicked', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    const approveBtn = wrapper.find('[data-testid="approve-plan-btn"]')
    expect(approveBtn.exists()).toBe(true)
    await approveBtn.trigger('click')
    await flushPromises()

    expect(approveMock).toHaveBeenCalled()
    expect(wrapper.emitted('approved')).toBeTruthy()
    expect(wrapper.emitted('approved')![0][0]).toMatchObject({ id: 't1', currentStage: 'implementation' })
    wrapper.unmount()
  })

  it('shows reject feedback textarea when Reject is clicked', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    const rejectBtn = wrapper.find('[data-testid="reject-plan-btn"]')
    expect(rejectBtn.exists()).toBe(true)
    await rejectBtn.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="reject-feedback-textarea"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('calls reject with feedback and emits rejected', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    await wrapper.find('[data-testid="reject-plan-btn"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-testid="reject-feedback-textarea"]').setValue('needs more detail')
    await wrapper.find('[data-testid="submit-reject-btn"]').trigger('click')
    await flushPromises()

    expect(rejectMock).toHaveBeenCalledWith('needs more detail')
    expect(wrapper.emitted('rejected')).toBeTruthy()
    wrapper.unmount()
  })

  it('emits close when the close button is clicked', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    await wrapper.find('[data-testid="plan-review-close-btn"]').trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
    wrapper.unmount()
  })
})

describe('gate-state guards', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.resetModules()
  })

  async function mountWithMock(overrides: Record<string, unknown>) {
    const defaults = {
      gateState: ref('awaiting_user'),
      approvedPlan: ref({ steps: [] }),
      loading: ref(false),
      error: ref(null),
      fetchStatus: vi.fn(),
      start: vi.fn(),
      stop: vi.fn(),
      approve: vi.fn().mockResolvedValue(null),
      reject: vi.fn().mockResolvedValue(undefined),
    }
    // The outer beforeEach already cached the component against the hoisted mock.
    vi.resetModules()
    vi.doMock('@/features/pipeline/composables/usePlanReview', () => ({
      usePlanReview: () => ({ ...defaults, ...overrides }),
    }))
    vi.doMock('@/utils/markdown', () => ({
      renderMarkdown: (text: string) => `<p>${text}</p>`,
    }))
    const mod = await import('@/features/pipeline/components/PlanReviewPanel.vue')
    return mount(mod.default as any, {
      props: {
        open: true,
        task: { id: 't1', slug: 's1', title: 'T', currentStage: 'plan_review', createdAt: '', updatedAt: '' },
      },
      attachTo: document.body,
    })
  }

  it('approve and request-changes disabled while running', async () => {
    const wrapper = await mountWithMock({ gateState: ref('running'), approvedPlan: ref(null) })
    await flushPromises()
    expect(wrapper.find('[data-testid="approve-plan-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="reject-plan-btn"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('approve and request-changes disabled when awaiting_user but no plan', async () => {
    const wrapper = await mountWithMock({ gateState: ref('awaiting_user'), approvedPlan: ref(null) })
    await flushPromises()
    expect(wrapper.find('[data-testid="approve-plan-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="reject-plan-btn"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('approve and request-changes enabled when awaiting_user with plan', async () => {
    const wrapper = await mountWithMock({ gateState: ref('awaiting_user'), approvedPlan: ref({ steps: [] }) })
    await flushPromises()
    expect(wrapper.find('[data-testid="approve-plan-btn"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-testid="reject-plan-btn"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('pending hint visible while running', async () => {
    const wrapper = await mountWithMock({ gateState: ref('running'), approvedPlan: ref(null) })
    await flushPromises()
    expect(wrapper.find('[data-testid="plan-review-pending-hint"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('pending hint absent when awaiting_user', async () => {
    const wrapper = await mountWithMock({ gateState: ref('awaiting_user'), approvedPlan: ref({ steps: [] }) })
    await flushPromises()
    expect(wrapper.find('[data-testid="plan-review-pending-hint"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('409 from approve surfaces as toast error', async () => {
    const errorRef = ref<string | null>(null)
    const approveFn = vi.fn(async () => {
      errorRef.value = 'plan_review is running, not awaiting_user'
      return null
    })
    const wrapper = await mountWithMock({
      gateState: ref('awaiting_user'),
      approvedPlan: ref({ steps: [] }),
      error: errorRef,
      approve: approveFn,
    })
    // Import after mountWithMock so the spy targets the same useToast instance the component got.
    const { toast } = await import('@/composables/useToast')
    vi.spyOn(toast, 'error')
    await flushPromises()
    await wrapper.find('[data-testid="approve-plan-btn"]').trigger('click')
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith('plan_review is running, not awaiting_user')
    wrapper.unmount()
  })
})
