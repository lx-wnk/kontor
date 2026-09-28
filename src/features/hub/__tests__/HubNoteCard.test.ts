import type { HubNote } from '../composables/useObsidianGraph'
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const openInObsidian = vi.fn(async (_path: string): Promise<string | null> => null)
vi.mock('../composables/useObsidianGraph', () => ({ useObsidianGraph: () => ({ openInObsidian }) }))

const { default: HubNoteCard } = await import('../components/HubNoteCard.vue')

const HOUR_MS = 3600_000
const notes: HubNote[] = [
  { index: 0, path: 'Privat/Reise.md', title: 'Reise', mtimeMs: Date.now() - 2 * HOUR_MS, links: [1, 2], backlinks: [2] },
  { index: 1, path: 'Privat/Packliste.md', title: 'Packliste', mtimeMs: 0, links: [], backlinks: [0] },
  { index: 2, path: 'Journal/Heute.md', title: 'Heute', mtimeMs: 0, links: [0], backlinks: [0] },
]

function mountCard(kontorBlocked?: string) {
  return mount(HubNoteCard, { props: { note: notes[0], notes, sectorLabel: 'Privat', kontorBlocked } })
}

describe('hubNoteCard', () => {
  it('is a dialog named after the note that shows its path, sector, age and link counts', () => {
    const w = mountCard()
    const card = w.get('[role="dialog"]')
    expect(card.attributes('data-hub-layer')).toBeDefined()
    expect(card.attributes('aria-label')).toBe('Reise')
    expect(w.get('[data-testid="hub-note-path"]').text()).toBe('Privat/Reise.md')
    expect(w.text()).toContain('Privat')
    expect(w.get('[data-testid="hub-note-meta"]').text()).toBe('changed 2h ago · 2 links · 1 backlink')
  })

  it('flies to a linked note and to a backlinking note from their chips', async () => {
    const w = mountCard()
    const links = w.findAll('[data-testid="hub-note-link"]')
    expect(links.map(c => c.text())).toEqual(['Packliste', 'Heute'])
    await links[0].trigger('click')
    await w.get('[data-testid="hub-note-backlink"]').trigger('click')
    expect(w.emitted('fly')).toEqual([[1], [2]])
  })

  it('asks Kontor about the note as a wiki link without the extension', async () => {
    const w = mountCard()
    await w.findAll('button').find(b => b.text() === 'Ask Kontor about this')!.trigger('click')
    expect(w.emitted('ask')).toEqual([['[[Privat/Reise]] ']])
  })

  it('disables Ask Kontor with the reason the hub gave', () => {
    const button = mountCard('Kontor could not load — reload the app').findAll('button').find(b => b.text() === 'Ask Kontor about this')!
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('title')).toBe('Kontor could not load — reload the app')
  })

  it('opens the note in Obsidian and shows a failure inline', async () => {
    openInObsidian.mockResolvedValueOnce('note not found')
    const w = mountCard()
    await w.findAll('button').find(b => b.text() === 'Open in Obsidian')!.trigger('click')
    await flushPromises()
    expect(openInObsidian).toHaveBeenCalledWith('Privat/Reise.md')
    expect(w.get('[role="alert"]').text()).toBe('note not found')
  })

  it('disables Open in Obsidian while the request is in flight, so a second click cannot double-fire it', async () => {
    let resolve!: (v: string | null) => void
    const pending = new Promise<string | null>((res) => {
      resolve = res
    })
    openInObsidian.mockClear().mockReturnValueOnce(pending)
    const w = mountCard()
    const open = () => w.findAll('button').find(b => b.text() === 'Open in Obsidian')!
    await open().trigger('click')
    expect(open().attributes('disabled')).toBeDefined()
    await open().trigger('click')
    expect(openInObsidian).toHaveBeenCalledOnce()
    resolve(null)
    await flushPromises()
    expect(open().attributes('disabled')).toBeUndefined()
  })

  it('closes from its button', async () => {
    const w = mountCard()
    await w.get('button[aria-label="Close card"]').trigger('click')
    expect(w.emitted('close')).toHaveLength(1)
  })
})
