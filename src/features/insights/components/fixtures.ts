import type { InsightsSessionRow } from '../insightsApi'

export function sessionRow(over: Partial<InsightsSessionRow> = {}): InsightsSessionRow {
  return {
    sessionId: 's-1',
    title: 'Fix the login bug',
    projectPath: '/code/app',
    projectName: 'app',
    model: 'claude-opus-4',
    startedAt: '2026-10-01T10:00:00Z',
    recordedAt: '2026-10-01T11:00:00Z',
    costUsd: 1.5,
    costSource: 'cost_state',
    costUnknownModel: false,
    agentMs: 90_000,
    agentTimeSource: 'cost_state',
    inputTokens: 1000,
    outputTokens: 2000,
    cacheReadTokens: 3000,
    cacheCreationTokens: 4000,
    turns: 7,
    linesAdded: 12,
    linesRemoved: 3,
    modelCosts: {},
    ...over,
  }
}
