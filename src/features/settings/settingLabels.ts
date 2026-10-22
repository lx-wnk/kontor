export interface SettingLabel {
  label: string
  help: string
}

export const SETTING_LABELS: Readonly<Record<string, SettingLabel>> = {
  'features.preview': {
    label: 'Preview features',
    help: 'Turns on features that may change or go away.',
  },
}
