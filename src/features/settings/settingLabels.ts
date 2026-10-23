export interface SettingLabel {
  label: string
  help: string
}

export const PREVIEW_FEATURES_KEY = 'features.preview'

export const SETTING_LABELS: Readonly<Record<string, SettingLabel>> = {
  [PREVIEW_FEATURES_KEY]: {
    label: 'Preview features',
    help: 'Turns on features that may change or go away.',
  },
}
