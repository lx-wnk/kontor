import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HubLegend from '../components/HubLegend.vue'

describe('hubLegend', () => {
  it('shows only the toggle button when collapsed', () => {
    const w = mount(HubLegend, { props: { open: false, level: 0 } })
    expect(w.find('button[aria-label="Show map legend"]').exists()).toBe(true)
    expect(w.find('[data-testid="hub-legend"]').exists()).toBe(false)
  })

  it('emits toggle when the button is clicked', async () => {
    const w = mount(HubLegend, { props: { open: false, level: 0 } })
    await w.get('button[aria-label="Show map legend"]').trigger('click')
    expect(w.emitted('toggle')).toHaveLength(1)
  })

  it('shows the region with the rows when open', () => {
    const w = mount(HubLegend, { props: { open: true, level: 0 } })
    const region = w.get('[data-testid="hub-legend"]')
    expect(region.attributes('role')).toBe('region')
    expect(region.attributes('aria-label')).toBe('Map legend')
    expect(region.text()).toContain('Note (know-how)')
    expect(region.text()).toContain('Session log')
  })

  it('shows age as fading and containment as circles, not the old distance-from-centre row', () => {
    const w = mount(HubLegend, { props: { open: true, level: 0 } })
    const text = w.get('[data-testid="hub-legend"]').text()
    expect(text).toContain('Fainter = older (untouched for longer)')
    expect(text).toContain('Circles = category › project; colour = category, shade = project')
    expect(text).not.toContain('Distance from centre = age')
    expect(text).not.toContain('Colour = category, shade = project')
  })

  it('hides the know-how link row at level 0 and shows it at level 1', () => {
    const collapsed = mount(HubLegend, { props: { open: true, level: 0 } })
    expect(collapsed.get('[data-testid="hub-legend"]').text()).not.toContain('Know-how links inside a project')

    const expanded = mount(HubLegend, { props: { open: true, level: 1 } })
    expect(expanded.get('[data-testid="hub-legend"]').text()).toContain('Know-how links inside a project')
  })
})
