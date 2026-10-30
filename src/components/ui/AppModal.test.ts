import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import AppModal from './AppModal.vue'

describe('appModal drawer', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('renders drawer panel right-aligned', () => {
    const wrapper = mount(AppModal, {
      props: { open: true, size: 'drawer', width: '640px' },
      slots: { default: '<p>x</p>' },
      attachTo: document.body,
    })
    const panel = document.body.querySelector('.base-modal-box') as HTMLElement
    expect(panel.classList.contains('modal-drawer')).toBe(true)
    expect(panel.classList.contains('h-full')).toBe(true)
    expect(panel.style.getPropertyValue('--drawer-w')).toBe('640px')
    expect((document.body.querySelector('[role="dialog"]') as HTMLElement).classList.contains('justify-end')).toBe(true)
    wrapper.unmount()
  })

  it('drawer closes on Escape and restores focus', async () => {
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()
    const wrapper = mount(AppModal, {
      props: { open: false, size: 'drawer' },
      slots: { default: '<button>in</button>' },
      attachTo: document.body,
    })
    await wrapper.setProps({ open: true })
    await nextTick()
    const dialog = document.body.querySelector('[role="dialog"]') as HTMLElement
    expect(document.activeElement).toBe(document.body.querySelector('.base-modal-box'))
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ open: false })
    expect(document.activeElement).toBe(trigger)
    wrapper.unmount()
  })
})
