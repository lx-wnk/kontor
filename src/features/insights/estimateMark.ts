export interface EstimateMark { prefix: string, title: string }

export function estimateMark(source: string, unknownModel = false): EstimateMark {
  if (source === 'estimate')
    return { prefix: '≈', title: 'Source: estimate from the session log, no current cost-state value' }
  if (unknownModel)
    return { prefix: '≈', title: 'Source: estimate, the model has no known price' }
  return { prefix: '', title: '' }
}
