import { describe, expect, it } from 'vitest'
import { PREVIEW_FEATURES_KEY, SETTING_LABELS } from './settingLabels'

describe('settingLabels', () => {
  it('exports the preview switch key and labels it', () => {
    expect(PREVIEW_FEATURES_KEY).toBe('features.preview')
    expect(SETTING_LABELS[PREVIEW_FEATURES_KEY]?.label).toBe('Preview features')
  })
})
