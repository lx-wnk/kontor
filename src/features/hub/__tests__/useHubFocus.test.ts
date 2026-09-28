import { beforeEach, describe, expect, it } from 'vitest'
import { useViewState } from '@/composables/useViewState'
import { DEFAULT_LAYOUT, useWorkspace } from '@/features/workspace'
import { focusInHub, hubFocusRequest } from '../composables/useHubFocus'

const HUB_PAGE = { id: 'p-hub', title: 'Hub page', tiles: [{ widget: 'hub', col: 1, row: 1, colSpan: 6, rowSpan: 6 }] }

beforeEach(() => {
  hubFocusRequest.value = null
  useWorkspace().layout.value = DEFAULT_LAYOUT
  useViewState().activeView.value = 'zentrale'
})

describe('useHubFocus', () => {
  it('navigates to the page holding the hub and sets the request', () => {
    useWorkspace().layout.value = { version: 1, pages: [{ id: 'zentrale', title: 'Zentrale', tiles: [] }, HUB_PAGE] }
    useViewState().activeView.value = 'dashboard'
    const ok = focusInHub({ kind: 'note', path: 'a.md' })
    expect(ok).toBe(true)
    expect(useViewState().activeView.value).toBe('page:p-hub')
    expect(hubFocusRequest.value).toEqual({ kind: 'note', path: 'a.md' })
  })

  it('returns false, stays put and leaves the request untouched without a hub page', () => {
    useWorkspace().layout.value = { version: 1, pages: [{ id: 'zentrale', title: 'Zentrale', tiles: [] }] }
    useViewState().activeView.value = 'dashboard'
    const ok = focusInHub({ kind: 'agent', pid: 42 })
    expect(ok).toBe(false)
    expect(useViewState().activeView.value).toBe('dashboard')
    expect(hubFocusRequest.value).toBeNull()
  })
})
