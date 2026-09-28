import type { PermissionItem } from '@/composables/usePendingPermissions'
import type { PendingCapabilityDecision, PendingPermission, PendingToolUse } from '@/sdk.generated'
import type { Agent } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@/composables/useToast'
import AgentTriageBand from './AgentTriageBand.vue'

vi.mock('@/composables/useToast', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

afterEach(() => {
  vi.clearAllMocks()
})

function makeAgent(overrides: Partial<Agent>): Agent {
  return {
    pid: 4242,
    sessionId: 'sess-1',
    provider: 'claude',
    projectName: 'demo',
    projectPath: '/repo/demo',
    cwd: '/repo/demo',
    status: 'active',
    working: false,
    entrypoint: 'cli',
    lastActivity: new Date().toISOString(),
    uptime: 60,
    liveInjectable: true,
    channelAvailable: true,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    costEstimate: 0,
    cacheCreationCostEstimate: 0,
    cacheReadCostEstimate: 0,
    healthScore: 100,
    conversationTurns: 0,
    toolCounts: {},
    meta: null,
    costUnknown: false,
    currentAction: null,
    lastTools: [],
    tasks: [],
    subagents: [],
    convergenceAlert: false,
    ...overrides,
  } as unknown as Agent
}

// Typed so a fixture cannot silently drop a field the wire type requires.
// patternDisplay defaults to pattern: these fixtures carry no deceptive runes,
// and the two differing is what the sanitize tests are for.
function toolUse(o: Partial<PendingToolUse> & Pick<PendingToolUse, 'tool'>): PendingToolUse {
  return { id: 'tu_1', pattern: '', patternDisplay: o.pattern ?? '', ...o }
}

function mountBand(agent: Agent) {
  return mount(AgentTriageBand, { props: { agents: [agent], permissionItems: [] } })
}

describe('agentTriageBand pending tool use', () => {
  // An unresolved AskUserQuestion reaches the client through PendingToolUse (the
  // parser reports it so a session whose screen cannot be probed shows
  // something), but it waits for an ANSWER, not a grant — offering to allow it
  // would write a standing rule for a tool nobody has to approve.
  it('offers no permission grant for AskUserQuestion', () => {
    const wrapper = mountBand(makeAgent({
      pendingToolUse: toolUse({ id: 'tu_q', tool: 'AskUserQuestion', pattern: '' }),
    }))
    expect(wrapper.text()).not.toContain('Allow AskUserQuestion')
    expect(wrapper.text()).toContain('Waiting for your answer')
  })

  // A bare pendingToolUse on an otherwise finished agent is a "your turn" card,
  // not a blocked one: nothing is waiting for a grant. The exclusion form this
  // replaced only ruled out 'stalled', so it offered the button here too.
  it('offers no grant for a leftover tool use on a finished turn', () => {
    const wrapper = mountBand(makeAgent({
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
    }))
    expect(wrapper.text()).not.toContain('Allow Bash')
    // Positive anchor: without it the assertion above also passes when the card
    // stops rendering altogether.
    expect(wrapper.text()).toContain('Bash')
  })

  // A question is answered, not granted: the prompt on screen is about something
  // other than the pending tool call, so it is not evidence for a standing rule.
  it('offers no grant while a question is the only prompt on screen', () => {
    const wrapper = mountBand(makeAgent({
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
      pendingQuestion: {
        header: 'branch',
        question: 'Which branch?',
        multiSelect: false,
        options: [{ index: 1, label: 'main' }, { index: 2, label: 'dev' }],
        typeSomethingIndex: 3,
        chatAboutIndex: 4,
      },
    }))
    expect(wrapper.text()).not.toContain('Allow Bash')
    expect(wrapper.text()).toContain('Which branch?')
  })

  // The server sets PendingPermissions only together with PipelineTaskID
  // (agentbroadcast/enricher.go), and an orchestrated agent is approved through
  // the pipeline control, not through a standing project-wide rule. A fixture
  // with permissions and no task id describes a payload that cannot be emitted.
  it('routes an orchestrated permission to Approve, not to a standing grant', () => {
    const wrapper = mountBand(makeAgent({
      pipelineTaskId: 'task-1',
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
      pendingPermissions: [{ id: 'p1', tool: 'Bash', pattern: 'npm publish', requestedAt: new Date().toISOString() }],
    }))
    expect(wrapper.text()).not.toContain('Allow Bash')
    expect(wrapper.text()).toContain('Approve')
  })

  // With the question itself detected, the card renders the answer UI — and the
  // grant must stay away there too.
  it('offers no grant when the question is detected alongside the tool use', () => {
    const wrapper = mountBand(makeAgent({
      pendingToolUse: toolUse({ id: 'tu_q', tool: 'AskUserQuestion', pattern: '' }),
      pendingQuestion: {
        header: 'chrome',
        question: 'Which colour?',
        multiSelect: false,
        options: [{ index: 1, label: 'Red' }, { index: 2, label: 'Green' }],
        typeSomethingIndex: 3,
        chatAboutIndex: 4,
      },
    }))
    expect(wrapper.text()).not.toContain('Allow AskUserQuestion')
    expect(wrapper.text()).toContain('Which colour?')
  })
})

describe('agentTriageBand stalled card', () => {
  // A tool_use left unresolved for a long time is reclassified 'stalled' (nobody
  // asked for approval, the run is just quiet) — the card must not imply an
  // approval is pending or print the full command that was never submitted for review.
  it('renders no approve control for a stalled agent', () => {
    const wrapper = mountBand(makeAgent({
      pendingToolUse: toolUse({ id: 'tu_s', tool: 'Bash', pattern: 'rm -rf /tmp/build-cache && npm publish --access public' }),
      lastActivity: new Date(Date.now() - 300_000).toISOString(),
    }))
    expect(wrapper.text()).not.toContain('Allow Bash')
    expect(wrapper.text()).not.toContain('rm -rf /tmp/build-cache')
    expect(wrapper.text()).toContain('Bash — running but silent, last output')
  })

  // errorState and pendingToolUse arrive together whenever an API error
  // interrupts a tool call. The card must report the failure, not the command
  // that was interrupted — the badge already says the run failed.
  it('reports the error, not the interrupted command, on a failed run', () => {
    const wrapper = mountBand(makeAgent({
      pendingToolUse: toolUse({ id: 'tu_e', tool: 'Bash', pattern: 'npm publish --access public' }),
      errorState: 'rate_limited',
    }))
    expect(wrapper.text()).not.toContain('npm publish --access public')
    expect(wrapper.text()).toContain('Rate limited')
  })

  it('still offers the approve control for a real pending permission', () => {
    const wrapper = mountBand(makeAgent({
      pipelineTaskId: 'task-1',
      pendingPermissions: [{ id: 'perm_1', tool: 'Bash', pattern: 'npm publish', requestedAt: new Date().toISOString() }],
    }))
    expect(wrapper.text()).toContain('Approve')
  })
})

describe('agentTriageBand all-clear state', () => {
  // "Nothing needs you" is the normal case, so it must not read as loudly as a
  // band full of blocked agents.
  it('renders the all-clear line without a filled banner', () => {
    const w = mount(AgentTriageBand, { props: { agents: [], permissionItems: [] } })
    const line = w.get('[data-testid="triage-all-clear"]')
    expect(line.text()).toContain('All clear')
    expect(line.classes().join(' ')).not.toMatch(/bg-success-soft|border-success-line/)
  })

  it('drops the all-clear line as soon as an agent needs attention', () => {
    const w = mountBand(makeAgent({ pendingToolUse: toolUse({ tool: 'Bash', pattern: 'ls', id: 'tu_1' }) }))
    expect(w.find('[data-testid="triage-all-clear"]').exists()).toBe(false)
  })
})

describe('agentTriageBand permission bridge', () => {
  function perm(o: Partial<PendingPermission> = {}): PendingPermission {
    return { id: 'req-1', tool: 'Bash', pattern: 'npm publish', requestedAt: new Date().toISOString(), ...o }
  }

  // The bridge's own controls. Matched by test id rather than by label text:
  // the pipeline bar and the standing-rule button both spell "Allow" too.
  function bridgeButtons(wrapper: ReturnType<typeof mountBand>) {
    return [
      ...wrapper.findAll('[data-testid="permission-decide-allow"]'),
      ...wrapper.findAll('[data-testid="permission-decide-deny"]'),
    ]
  }

  // A held PreToolUse hook call is the live decision: answering it releases the
  // run, so the card offers both directions rather than a rule for next time.
  it('offers Allow and Deny for an agent whose prompt the bridge holds', () => {
    const wrapper = mountBand(makeAgent({ heldPermissions: [perm()] }))
    expect(bridgeButtons(wrapper).map(b => b.text())).toEqual(['Allow', 'Deny'])
  })

  // A held hook call is answerable however the session was started. Orchestration
  // decides how a PIPELINE request is resolved, not whether a blocked session can
  // be released.
  it('offers the decision for an orchestrated agent too', () => {
    const wrapper = mountBand(makeAgent({ pipelineTaskId: 'task-1', heldPermissions: [perm()] }))
    expect(bridgeButtons(wrapper)).toHaveLength(2)
  })

  // Pipeline requests live in a different field and resolve through a different
  // endpoint; the bridge must not offer to answer them.
  it('offers no bridge decision for a pipeline request', () => {
    const wrapper = mountBand(makeAgent({ pipelineTaskId: 'task-1', pendingPermissions: [perm()] }))
    expect(wrapper.text()).toContain('Approve')
    expect(bridgeButtons(wrapper)).toHaveLength(0)
  })

  // The bridge can hold several calls at once when the agent batches tool calls.
  // Answering only the first left the rest to lapse with nothing on screen.
  it('renders one decision row per held request', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ id: 'a' }), perm({ id: 'b', pattern: 'rm -rf /tmp/x' })],
    }))
    expect(wrapper.findAll('[data-testid="permission-decide-allow"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('rm -rf /tmp/x')
  })

  // The body must describe what the buttons below it answer. pendingToolUse is
  // reconstructed from the transcript and can name a different call entirely.
  it('describes the held call, not the transcript\'s pending tool use', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ pattern: 'npm publish' })],
      pendingToolUse: toolUse({ id: 'tu_other', tool: 'Read', pattern: '/etc/passwd' }),
    }))
    expect(wrapper.text()).toContain('npm publish')
    expect(wrapper.text()).not.toContain('/etc/passwd')
  })

  // The terminal is already asking, so there is nothing here to answer — only a
  // rule for future runs, which is what the older control always did.
  it('offers to intercept the next prompt once this one reached the terminal', () => {
    const wrapper = mountBand(makeAgent({
      awaitingTerminalPermission: true,
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
    }))
    expect(wrapper.find('[data-testid="permission-arm"]').exists()).toBe(true)
  })

  it('offers no interception control for a session already armed', () => {
    const wrapper = mountBand(makeAgent({
      awaitingTerminalPermission: true,
      permissionBridgeArmed: true,
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
    }))
    expect(wrapper.find('[data-testid="permission-arm"]').exists()).toBe(false)
  })

  it('offers the standing rule, not a live decision, once the prompt reached the terminal', () => {
    const wrapper = mountBand(makeAgent({
      awaitingTerminalPermission: true,
      terminalPermissionToolUseId: 'tu_b',
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
    }))
    expect(wrapper.text()).toContain('Allow Bash')
    expect(bridgeButtons(wrapper)).toHaveLength(0)
  })

  // The notice fires once when the prompt opens and never when it is answered,
  // so it outlives its prompt; pendingToolUse is reconstructed separately and
  // drifts on its own. Without the bridge naming the call, a rule written from
  // the pair names whatever the trail happens to show.
  it('offers no standing rule when the bridge cannot name the call on screen', () => {
    const wrapper = mountBand(makeAgent({
      awaitingTerminalPermission: true,
      pendingToolUse: toolUse({ id: 'tu_b', tool: 'Bash', pattern: 'npm publish' }),
    }))
    expect(wrapper.text()).not.toContain('Allow Bash')
  })

  // A hook "allow" short-circuits Claude Code's own permission evaluation, deny
  // rules included, so a click here would release a restriction the user wrote
  // by hand and reasonably believes is absolute.
  it('offers no Allow for a call the user\'s own rules deny', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ deniedBy: 'Bash(rm:*)', pattern: 'rm -rf /tmp/x' })],
    }))
    expect(wrapper.findAll('[data-testid="permission-decide-allow"]')).toHaveLength(0)
    expect(wrapper.findAll('[data-testid="permission-decide-deny"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="permission-denied-by-rule"]').text()).toContain('Bash(rm:*)')
  })

  // PatternElided mirrors ValueElided/ContextElided on the capability card: a
  // truncated pattern must read as truncated, not as a complete command.
  it('renders a visible truncation marker for an elided pattern', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ pattern: 'npm publish --access publ', patternElided: 42 })],
    }))
    const marker = wrapper.get('[data-testid="pattern-elided"]')
    expect(marker.text()).toBe('…')
    expect(marker.attributes('title')).toContain('42')
  })

  it('renders no truncation marker when the pattern was not cut', () => {
    const wrapper = mountBand(makeAgent({ heldPermissions: [perm()] }))
    expect(wrapper.find('[data-testid="pattern-elided"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('npm publish')
  })

  // DeniedByElided: the deny-rule text is the reason a human reads to
  // understand a refusal, so a cut there is as deceptive as a cut pattern.
  it('renders a visible truncation marker for an elided deny rule', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ deniedBy: 'Bash(rm:*', deniedByElided: 7 })],
    }))
    const marker = wrapper.get('[data-testid="denied-by-elided"]')
    expect(marker.text()).toBe('…')
    expect(marker.attributes('title')).toContain('7')
  })

  it('renders no truncation marker when the deny rule was not cut', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ deniedBy: 'Bash(rm:*)' })],
    }))
    expect(wrapper.find('[data-testid="denied-by-elided"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="permission-denied-by-rule"]').text()).toContain('Bash(rm:*)')
  })

  // Splitting the label around the pattern (for the marker) must not leave
  // stray whitespace behind when the field is absent — the untouched case
  // renders character for character what a single interpolation produced.
  it('renders the pattern label byte-identically when nothing was cut', () => {
    const wrapper = mountBand(makeAgent({ heldPermissions: [perm()] }))
    expect(wrapper.html()).toContain('>Bash(npm publish)<')
  })

  // Only the covered request loses its Allow: a batch holds several calls and
  // the others are still ordinary decisions.
  it('keeps Allow for the sibling requests no rule covers', () => {
    const wrapper = mountBand(makeAgent({
      heldPermissions: [perm({ id: 'a', deniedBy: 'Bash(rm:*)' }), perm({ id: 'b' })],
    }))
    expect(wrapper.findAll('[data-testid="permission-decide-allow"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-testid="permission-decide-deny"]')).toHaveLength(2)
  })

  it('posts the decision for the held request', async () => {
    const calls: { url: string, body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init.body)) })
      return { ok: true, status: 200 } as Response
    }))

    const wrapper = mountBand(makeAgent({ heldPermissions: [perm({ id: 'req-42' })] }))
    const allow = bridgeButtons(wrapper)[0]
    expect(allow).toBeTruthy()
    await allow.trigger('click')

    expect(calls).toEqual([{ url: '/api/hooks/permission/respond', body: { id: 'req-42', decision: 'allow' } }])
    vi.unstubAllGlobals()
  })

  // F-16 (WCAG 2.4.3): the Allow/Deny row and the terminal-fallback control are
  // separate v-if branches on the same card. A hold lapsing between SSE ticks
  // must not dump a focused user on <body> mid-decision.
  it('keeps focus inside the card when a held request lapses to terminal fallback', async () => {
    const wrapper = mount(AgentTriageBand, {
      props: { agents: [makeAgent({ heldPermissions: [perm()] })], permissionItems: [] },
      attachTo: document.body,
    })
    const allow = wrapper.get('[data-testid="permission-decide-allow"]')
    ;(allow.element as HTMLElement).focus()
    expect(document.activeElement).toBe(allow.element)

    await wrapper.setProps({
      agents: [makeAgent({ heldPermissions: [], awaitingTerminalPermission: true })],
    })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()

    expect(document.activeElement).not.toBe(document.body)
    wrapper.unmount()
  })

  // F-18 (WCAG 4.1.3): a live region announces net-new held requests so a
  // screen-reader user away from the card still gets notice inside the 25s
  // window — but it must not re-fire on a later tick that resends the same
  // request as a fresh object.
  it('announces a new held request once and never repeats it for the same request id', async () => {
    const wrapper = mountBand(makeAgent({ heldPermissions: [] }))
    const live = wrapper.get('[data-testid="triage-live-announcement"]')
    expect(live.text()).toBe('')

    await wrapper.setProps({
      agents: [makeAgent({ heldPermissions: [perm({ id: 'req-99', pattern: 'npm publish' })] })],
    })
    expect(live.text()).toContain('npm publish')
    const firstAnnouncement = live.text()

    // Same request id, brand-new object identity — as a real SSE tick would
    // send — but carrying different text, so a re-announcement would be visible
    // rather than hidden behind an identical string.
    await wrapper.setProps({
      agents: [makeAgent({ heldPermissions: [perm({ id: 'req-99', pattern: 'rm -rf /tmp/x' })] })],
    })
    expect(live.text()).toBe(firstAnnouncement)
    expect(live.text()).not.toContain('rm -rf')
  })

  // The bridge holds a whole batch at once. Overwriting the string per item
  // announced only the last of them, so the rest passed in silence.
  it('announces every request of a batch, not just the last', async () => {
    const wrapper = mountBand(makeAgent({ heldPermissions: [] }))
    const live = wrapper.get('[data-testid="triage-live-announcement"]')

    await wrapper.setProps({
      agents: [makeAgent({
        heldPermissions: [
          perm({ id: 'a', pattern: 'npm publish' }),
          perm({ id: 'b', pattern: 'git push --force' }),
        ],
      })],
    })

    expect(live.text()).toContain('npm publish')
    expect(live.text()).toContain('git push --force')
  })
})

describe('agentTriageBand capability decisions', () => {
  function capabilityDecision(o: Partial<PendingCapabilityDecision> = {}): PendingCapabilityDecision {
    return {
      id: 'cap-1',
      capability: 'network.egress',
      value: 'api.stripe.com',
      context: 'project:demo',
      reason: 'Outbound call to a new host',
      requestedAt: new Date().toISOString(),
      ...o,
    }
  }

  function mountWithDecisions(decisions: PendingCapabilityDecision[]) {
    return mount(AgentTriageBand, {
      props: { agents: [], permissionItems: [], capabilityDecisions: decisions },
    })
  }

  it('renders the capability, value and context of a decision', () => {
    const wrapper = mountWithDecisions([capabilityDecision()])
    expect(wrapper.text()).toContain('network.egress')
    expect(wrapper.text()).toContain('api.stripe.com')
    expect(wrapper.text()).toContain('project:demo')
  })

  it('renders "Everything" instead of a blank when value is empty', () => {
    const wrapper = mountWithDecisions([capabilityDecision({ value: '' })])
    expect(wrapper.text()).toContain('Everything')
  })

  it('posts allow for the decision id when Allow is clicked', async () => {
    const calls: { url: string, body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init.body)) })
      return { ok: true, status: 200 } as Response
    }))

    const wrapper = mountWithDecisions([capabilityDecision({ id: 'cap-42' })])
    await wrapper.get('[data-testid="capability-decision-allow"]').trigger('click')

    expect(calls).toEqual([{ url: '/api/capabilities/decisions/respond', body: { id: 'cap-42', decision: 'allow' } }])
    vi.unstubAllGlobals()
  })

  it('posts deny for the decision id when Deny is clicked', async () => {
    const calls: { url: string, body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init.body)) })
      return { ok: true, status: 200 } as Response
    }))

    const wrapper = mountWithDecisions([capabilityDecision({ id: 'cap-43' })])
    await wrapper.get('[data-testid="capability-decision-deny"]').trigger('click')

    expect(calls).toEqual([{ url: '/api/capabilities/decisions/respond', body: { id: 'cap-43', decision: 'deny' } }])
    vi.unstubAllGlobals()
  })

  it('disables both buttons for a decision while its resolve is in flight', async () => {
    let resolveFetch: (() => void) | null = null
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => {
      resolveFetch = () => resolve({ ok: true, status: 200 } as Response)
    })))

    const wrapper = mountWithDecisions([capabilityDecision({ id: 'cap-44' })])
    const allow = wrapper.get('[data-testid="capability-decision-allow"]')
    const deny = wrapper.get('[data-testid="capability-decision-deny"]')
    await allow.trigger('click')

    expect(allow.attributes('disabled')).toBeDefined()
    expect(deny.attributes('disabled')).toBeDefined()

    resolveFetch!()
    await flushPromises()
    vi.unstubAllGlobals()
  })

  it('renders no capability cards for an empty list and leaves permission cards intact', () => {
    const item: PermissionItem = {
      taskId: 'task-1',
      projectName: 'demo',
      cwd: '/work/checkout',
      title: 'Ship it',
      routineId: null,
      requests: [{
        id: 'req-1',
        stageRunId: 'run-1',
        tool: 'Bash',
        pattern: 'npm publish',
        reason: null,
        requestedAt: new Date().toISOString(),
        resolvedAt: null,
        outcome: null,
      }],
    }
    const wrapper = mount(AgentTriageBand, {
      props: { agents: [], permissionItems: [item], capabilityDecisions: [] },
    })
    expect(wrapper.find('[data-testid="capability-decision-card"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Ship it')
    expect(wrapper.text(), 'the remember label names the folder the preset is saved for').toContain('Don\'t ask again in checkout')
  })

  // ValueElided/ContextElided carry the cut count as their own field so a
  // truncated value cannot pass as complete.
  it('renders a visible truncation marker for an elided value', () => {
    const wrapper = mountWithDecisions([capabilityDecision({ value: 'api.stripe.co', valueElided: 42 })])
    const marker = wrapper.get('[data-testid="capability-value-elided"]')
    expect(marker.text()).toBe('…')
    expect(marker.attributes('title')).toContain('42')
  })

  it('renders a visible truncation marker for an elided context', () => {
    const wrapper = mountWithDecisions([capabilityDecision({ context: 'project:dem', contextElided: 3 })])
    expect(wrapper.get('[data-testid="capability-context-elided"]').text()).toBe('…')
  })

  it('renders no truncation marker when nothing was cut', () => {
    const wrapper = mountWithDecisions([capabilityDecision()])
    expect(wrapper.find('[data-testid="capability-value-elided"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="capability-context-elided"]').exists()).toBe(false)
  })
})

describe('agentTriageBand capability decision resolve outcomes', () => {
  function capabilityDecision(o: Partial<PendingCapabilityDecision> = {}): PendingCapabilityDecision {
    return {
      id: 'cap-1',
      capability: 'network.egress',
      value: 'api.stripe.com',
      context: 'project:demo',
      reason: 'Outbound call to a new host',
      requestedAt: new Date().toISOString(),
      ...o,
    }
  }

  function mountWithDecisions(decisions: PendingCapabilityDecision[]) {
    return mount(AgentTriageBand, {
      props: { agents: [], permissionItems: [], capabilityDecisions: decisions },
    })
  }

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // F-1: the template used to fire-and-forget resolveCapability with no await
  // and no catch, so a click that actually failed looked pixel-identical to
  // one that worked. Each outcome must now produce its own, distinct toast.
  it('toasts success when the click applies before the ask self-denies', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 }) as Response))
    const wrapper = mountWithDecisions([capabilityDecision({ id: 'cap-a' })])

    await wrapper.get('[data-testid="capability-decision-allow"]').trigger('click')
    await flushPromises()

    expect(toast.success).toHaveBeenCalledTimes(1)
    expect(toast.info).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('toasts a distinct "too late" notice when the ask already lapsed (404)', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 404 }) as Response))
    const wrapper = mountWithDecisions([capabilityDecision({ id: 'cap-b' })])

    await wrapper.get('[data-testid="capability-decision-allow"]').trigger('click')
    await flushPromises()

    expect(toast.info).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('toasts an error, distinct from success and "too late", on a real failure', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: false,
      status: 500,
      json: async () => ({ error: 'boom' }),
    }) as unknown as Response))
    const wrapper = mountWithDecisions([capabilityDecision({ id: 'cap-c' })])

    await wrapper.get('[data-testid="capability-decision-deny"]').trigger('click')
    await flushPromises()

    expect(toast.error).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.info).not.toHaveBeenCalled()
  })
})

describe('agentTriageBand capability cards vs. the bulk collapse', () => {
  function capabilityDecision(o: Partial<PendingCapabilityDecision> = {}): PendingCapabilityDecision {
    return {
      id: 'cap-1',
      capability: 'network.egress',
      value: 'api.stripe.com',
      context: 'project:demo',
      reason: 'Outbound call to a new host',
      requestedAt: new Date().toISOString(),
      ...o,
    }
  }

  // F-4: hasBulk (>=2 task permission requests) used to default the whole
  // card list — capability cards included — behind "Review individually".
  // A capability ask self-denies in 25s; it must stay reachable regardless.
  it('keeps a capability card reachable while two task permission requests trigger the bulk collapse', () => {
    const item: PermissionItem = {
      taskId: 'task-1',
      projectName: 'demo',
      cwd: '/repo/demo',
      title: 'Ship it',
      routineId: null,
      requests: [
        { id: 'req-1', stageRunId: 'run-1', tool: 'Bash', pattern: 'npm publish', reason: null, requestedAt: new Date().toISOString(), resolvedAt: null, outcome: null },
        { id: 'req-2', stageRunId: 'run-1', tool: 'Read', pattern: '/etc/passwd', reason: null, requestedAt: new Date().toISOString(), resolvedAt: null, outcome: null },
      ],
    }
    const wrapper = mount(AgentTriageBand, {
      props: { agents: [], permissionItems: [item], capabilityDecisions: [capabilityDecision({ id: 'cap-reachable' })] },
    })

    // The task-permission card is the one the collapse hides...
    expect(wrapper.text()).not.toContain('Ship it')
    // ...but the capability ask's own Allow/Deny stay on screen and enabled.
    expect(wrapper.get('[data-testid="capability-decision-allow"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="capability-decision-deny"]').attributes('disabled')).toBeUndefined()
  })
})

describe('agentTriageBand capability card focus and announcement', () => {
  function capabilityDecision(o: Partial<PendingCapabilityDecision> = {}): PendingCapabilityDecision {
    return {
      id: 'cap-1',
      capability: 'network.egress',
      value: 'api.stripe.com',
      context: 'project:demo',
      reason: 'Outbound call to a new host',
      requestedAt: new Date().toISOString(),
      ...o,
    }
  }

  // F-2 (WCAG 2.4.3): a capability card has no held-permission list to shrink
  // — the whole card unmounts the moment the ask is gone, so there is no
  // "same card" left to look inside. Focus must land somewhere sane, never
  // on <body>.
  it('moves focus off <body> when a focused capability card disappears', async () => {
    const wrapper = mount(AgentTriageBand, {
      props: {
        agents: [],
        permissionItems: [],
        capabilityDecisions: [
          capabilityDecision({ id: 'cap-gone' }),
          capabilityDecision({ id: 'cap-stays', capability: 'fs.write' }),
        ],
      },
      attachTo: document.body,
    })
    const allowButtons = wrapper.findAll('[data-testid="capability-decision-allow"]')
    ;(allowButtons[0].element as HTMLElement).focus()
    expect(document.activeElement).toBe(allowButtons[0].element)

    await wrapper.setProps({
      capabilityDecisions: [capabilityDecision({ id: 'cap-stays', capability: 'fs.write' })],
    })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()

    expect(document.activeElement).not.toBe(document.body)
    wrapper.unmount()
  })

  // F-3: a new capability ask gets 25 seconds and, without this, no notice
  // for a screen-reader user away from the card.
  it('announces a newly appearing capability ask', async () => {
    const wrapper = mount(AgentTriageBand, {
      props: { agents: [], permissionItems: [], capabilityDecisions: [] },
    })
    const live = wrapper.get('[data-testid="triage-live-announcement"]')
    expect(live.text()).toBe('')

    await wrapper.setProps({
      capabilityDecisions: [capabilityDecision({ id: 'cap-new', capability: 'network.egress', value: 'api.stripe.com' })],
    })

    expect(live.text()).toContain('network.egress')
    expect(live.text()).toContain('api.stripe.com')
  })
})

describe('agentTriageBand routine decisions', () => {
  function applicationRequest(o: Partial<PermissionItem['requests'][number]> = {}) {
    return {
      id: 'req-1',
      stageRunId: 'run-1',
      tool: 'mcp__mail__read',
      pattern: null,
      reason: null,
      requestedAt: new Date().toISOString(),
      resolvedAt: null,
      outcome: null,
      ...o,
    }
  }

  function routineItem(o: Partial<PermissionItem> = {}): PermissionItem {
    return {
      taskId: 'task-1',
      projectName: 'demo',
      cwd: '/repo/demo',
      title: 'Ship it',
      routineId: 'routine-1',
      requests: [applicationRequest()],
      ...o,
    }
  }

  it('offers the four routine decisions for a routine task whose requests are all application tools', async () => {
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [routineItem()] } })

    await wrapper.get('[data-testid="permission-decide-allow-once"]').trigger('click')
    await wrapper.get('[data-testid="permission-decide-allow-routine"]').trigger('click')
    await wrapper.get('[data-testid="permission-decide-deny-routine"]').trigger('click')
    await wrapper.get('[data-testid="permission-decide-deny-once"]').trigger('click')

    expect(wrapper.emitted('decide')).toEqual([
      ['task-1', ['req-1'], 'allow_once'],
      ['task-1', ['req-1'], 'allow_routine'],
      ['task-1', ['req-1'], 'deny_routine'],
      ['task-1', ['req-1'], 'deny_once'],
    ])
    wrapper.unmount()
  })

  it('falls back to Approve/Deny when a routine task has a non-application-tool request', async () => {
    const item = routineItem({ requests: [applicationRequest({ tool: 'Bash' })] })
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [item] } })

    expect(wrapper.find('[data-testid="permission-decide-allow-routine"]').exists()).toBe(false)
    await wrapper.get('[data-testid="permission-item-approve"]').trigger('click')
    expect(wrapper.emitted('approve')?.[0]).toEqual(['task-1', ['req-1'], false])

    await wrapper.get('[data-testid="permission-item-deny"]').trigger('click')
    expect(wrapper.emitted('deny')?.[0]).toEqual(['task-1', ['req-1']])
    wrapper.unmount()
  })

  it('falls back to Approve/Deny when the task has no routineId', async () => {
    const item = routineItem({ routineId: null })
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [item] } })

    expect(wrapper.find('[data-testid="permission-decide-allow-routine"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="permission-item-approve"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('agentTriageBand denied-by-default requests', () => {
  function plainRequest(o: Partial<PermissionItem['requests'][number]> = {}): PermissionItem['requests'][number] {
    return {
      id: 'req-1',
      stageRunId: 'run-1',
      tool: 'Bash',
      pattern: 'npm publish',
      reason: null,
      requestedAt: new Date().toISOString(),
      resolvedAt: null,
      outcome: null,
      ...o,
    }
  }

  function plainItem(o: Partial<PermissionItem> = {}): PermissionItem {
    return {
      taskId: 'task-1',
      projectName: 'demo',
      cwd: '/repo/demo',
      title: 'Ship it',
      routineId: null,
      requests: [plainRequest()],
      ...o,
    }
  }

  // Every request already denied: an answer here could not reach the agent —
  // the note points at the grant instead of offering a decision.
  it('renders the note instead of a decision when every request is already denied', () => {
    const item = plainItem({ requests: [plainRequest({ deniedByDefault: true })] })
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [item] } })

    const note = wrapper.get('[data-testid="permission-item-denied-by-default"]')
    expect(note.text()).toBe('Denied by default — change it under Settings → Grants')
    expect(note.attributes('role')).toBe('note')
    expect(wrapper.find('[data-testid="permission-decide-allow-once"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="permission-item-approve"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // Only some requests are denied: the operator still has to answer the
  // others, so the ordinary decision row stays exactly as it was.
  it('keeps the decision row when only some requests are denied', async () => {
    const item = plainItem({
      requests: [plainRequest({ id: 'req-1', deniedByDefault: true }), plainRequest({ id: 'req-2' })],
    })
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [item] } })
    await wrapper.findAll('button').find(b => b.text().includes('Review individually'))!.trigger('click')

    expect(wrapper.find('[data-testid="permission-item-denied-by-default"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="permission-item-approve"]').exists()).toBe(true)
    wrapper.unmount()
  })

  // No request is denied: nothing changes.
  it('renders nothing extra when no request is denied', () => {
    const item = plainItem({ requests: [plainRequest({ deniedByDefault: false })] })
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [item] } })

    expect(wrapper.find('[data-testid="permission-item-denied-by-default"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="permission-item-approve"]').exists()).toBe(true)
    wrapper.unmount()
  })

  // The field is optional: a request that never carries it behaves exactly
  // like one explicitly not denied.
  it('behaves like none-denied when the field is absent from every request', () => {
    const item = plainItem({ requests: [plainRequest()] })
    const wrapper = mount(AgentTriageBand, { props: { agents: [], permissionItems: [item] } })

    expect(wrapper.find('[data-testid="permission-item-denied-by-default"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="permission-item-approve"]').exists()).toBe(true)
    wrapper.unmount()
  })
})
