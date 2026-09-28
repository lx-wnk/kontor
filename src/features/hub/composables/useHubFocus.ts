import type { Ref } from 'vue'
import { ref } from 'vue'
import { pageView, useViewState } from '@/composables/useViewState'
import { HUB_WIDGET, pageWithWidget, useWorkspace } from '@/features/workspace'

export type HubTarget = { kind: 'note', path: string } | { kind: 'agent', pid: number }

export const NO_HUB_PAGE_MESSAGE = 'No page shows the Zentrale hub; add the hub tile to a page.'

export const hubFocusRequest: Ref<HubTarget | null> = ref(null)

export function focusInHub(target: HubTarget): boolean {
  const page = pageWithWidget(useWorkspace().layout.value, HUB_WIDGET)
  if (!page)
    return false
  useViewState().activeView.value = pageView(page.id)
  hubFocusRequest.value = target
  return true
}
