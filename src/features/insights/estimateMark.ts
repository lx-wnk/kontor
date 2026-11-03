export interface EstimateMark { prefix: string, title: string, srLabel: string }

export function estimateMark(source: string, unknownModel = false): EstimateMark {
  if (source === 'estimate')
    return { prefix: '≈', title: 'Source: estimate from the session log, no current cost-state value', srLabel: 'estimated' }
  if (source === 'legacy')
    return { prefix: '≈', title: 'Recorded before per-session tracking: no subagent spend, no agent time', srLabel: 'recorded before per-session tracking' }
  if (unknownModel)
    return { prefix: '≈', title: 'Source: estimate, the model has no known price', srLabel: 'estimated' }
  return { prefix: '', title: '', srLabel: '' }
}
