import type { VueWrapper } from '@vue/test-utils'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { axe } from '../../utils/testA11y'
import AppSelect from './AppSelect.vue'

// AppSelect teleports its panel to <body> (real DOM is required there — the
// panel anchors to the trigger's getBoundingClientRect() and the codebase
// pattern for teleported content, documented in SpawnDialog.test.ts, is to
// query via document.querySelector rather than wrapper.find).

const options = [
  { value: 'a', label: 'Option A' },
  { value: 'b', label: 'Option B' },
  { value: 'c', label: 'Option C' },
]

const optionsWithDisabled = [
  { value: 'a', label: 'Option A' },
  { value: 'b', label: 'Option B', disabled: true },
  { value: 'c', label: 'Option C' },
]

let wrapper: VueWrapper | null = null

function mountSelect(props: Record<string, unknown>) {
  wrapper = mount(AppSelect, { props: props as any, attachTo: document.body })
  return wrapper
}

function panel(): HTMLElement | null {
  return document.querySelector('[role="listbox"]')
}

function optionEls(): HTMLElement[] {
  return Array.from(document.querySelectorAll('[role="option"]'))
}

function input(): HTMLInputElement {
  const el = document.querySelector<HTMLInputElement>('input[role="combobox"]')
  if (!el)
    throw new Error('filter input is not rendered')
  return el
}

async function press(key: string) {
  input().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  await flushPromises()
}

async function type(text: string) {
  input().value = text
  input().dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}

async function openByKey(w: VueWrapper) {
  await w.get('button').trigger('keydown', { key: 'ArrowDown' })
  await flushPromises()
}

const fruitOptions = [
  { value: 'apple', label: 'Apple' },
  { value: 'banana', label: 'Banana' },
  { value: 'cherry', label: 'Cherry' },
  { value: 'pineapple', label: 'Tropical' },
]

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  document.body.innerHTML = ''
})

describe('appSelect', () => {
  it('renders the selected option label on the trigger and no panel until opened', () => {
    const w = mountSelect({ modelValue: 'b', options })
    expect(w.get('button').text()).toContain('Option B')
    expect(panel()).toBeNull()
  })

  it('renders an empty trigger label when modelValue matches nothing', () => {
    const w = mountSelect({ modelValue: 'zzz', options })
    expect(w.get('button').text()).not.toMatch(/Option/)
  })

  it('forwards id, aria-label and disabled to the trigger button', () => {
    const w = mountSelect({ modelValue: 'a', options, id: 'my-select', ariaLabel: 'Choose option', disabled: true })
    const button = w.get('button')
    expect(button.attributes('id')).toBe('my-select')
    expect(button.attributes('aria-label')).toBe('Choose option')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('is not disabled by default', () => {
    const w = mountSelect({ modelValue: 'a', options })
    expect((w.get('button').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('merges fallthrough class with the trigger classes instead of clobbering them', () => {
    const w = mountSelect({ 'modelValue': 'a', options, 'class': 'w-full', 'data-testid': 'my-select' })
    const button = w.get('button')
    expect(button.classes()).toContain('w-full')
    expect(button.classes()).toContain('bg-card')
    expect(button.attributes('data-testid')).toBe('my-select')
  })

  it('clicking an option emits update:modelValue with the string value', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('click')
    await optionEls()[2].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await w.vm.$nextTick()
    expect(w.emitted('update:modelValue')?.[0]).toEqual(['c'])
  })

  it('emits a numeric option value unchanged, without string round-tripping', async () => {
    const numOptions = [{ value: 1, label: 'One' }, { value: 2, label: 'Two' }]
    const w = mountSelect({ modelValue: 1, options: numOptions })
    await w.get('button').trigger('click')
    await optionEls()[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await w.vm.$nextTick()
    const emitted = w.emitted('update:modelValue')?.[0]
    expect(emitted?.[0]).toBe(2)
    expect(typeof emitted?.[0]).toBe('number')
  })

  it('clicking a disabled option emits nothing', async () => {
    const w = mountSelect({ modelValue: 'a', options: optionsWithDisabled })
    await w.get('button').trigger('click')
    await optionEls()[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await w.vm.$nextTick()
    expect(w.emitted('update:modelValue')).toBeUndefined()
  })

  it('keyboard navigation skips disabled options', async () => {
    const w = mountSelect({ modelValue: 'a', options: optionsWithDisabled })
    await openByKey(w) // active = selected ('a', index 0)
    await press('ArrowDown') // skips disabled 'b' (index 1) -> 'c' (index 2)
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[2].id)
  })

  it('arrowDown opens the panel', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    expect(panel()).toBeNull()
    await w.get('button').trigger('keydown', { key: 'ArrowDown' })
    expect(panel()).not.toBeNull()
  })

  it('arrowDown/ArrowUp move the active option', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await openByKey(w) // active = 'a' (index 0)
    await press('ArrowDown') // -> 'b' (index 1)
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[1].id)
    await press('ArrowUp') // -> 'a' (index 0)
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[0].id)
  })

  it('enter selects the active option, closes the panel and refocuses the trigger', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await openByKey(w)
    await press('ArrowDown') // active = 'b'
    await press('Enter')
    expect(w.emitted('update:modelValue')?.[0]).toEqual(['b'])
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(w.get('button').element)
  })

  it('escape closes without emitting and returns focus to the trigger', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await openByKey(w)
    await press('ArrowDown')
    await press('Escape')
    expect(panel()).toBeNull()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    expect(document.activeElement).toBe(w.get('button').element)
  })

  it('moving focus away (Tab) closes the panel without emitting', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await openByKey(w)
    await press('ArrowDown')
    const next = document.createElement('button')
    document.body.appendChild(next)
    next.focus()
    await flushPromises()
    expect(panel()).toBeNull()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    expect(document.activeElement).toBe(next)
  })

  it('a mousedown on an option keeps focus in the filter input so the click can land', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await openByKey(w)
    const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    optionEls()[1].dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    expect(panel()).not.toBeNull()
  })

  it('mousedown outside the trigger and panel closes it', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('keydown', { key: 'ArrowDown' })
    expect(panel()).not.toBeNull()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await w.vm.$nextTick()
    expect(panel()).toBeNull()
  })

  it('a right-click outside closes the panel without arming the suppressor, so a subsequent bare click (no preceding mousedown, as keyboard activation dispatches) still reaches its handler', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('keydown', { key: 'ArrowDown' })
    expect(panel()).not.toBeNull()

    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 2 }))
    await w.vm.$nextTick()
    expect(panel()).toBeNull()

    const target = document.createElement('button')
    document.body.appendChild(target)
    const handler = vi.fn()
    target.addEventListener('click', handler)
    try {
      // No mousedown precedes this click, matching Enter/Space activation
      // or element.click() — exactly what a stray suppressor would eat.
      target.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(handler).toHaveBeenCalledTimes(1)
    }
    finally {
      target.remove()
    }
  })

  it('a primary-button outside mousedown suppresses exactly the click that follows it, and only that one', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('keydown', { key: 'ArrowDown' })
    expect(panel()).not.toBeNull()

    const target = document.createElement('button')
    document.body.appendChild(target)
    const handler = vi.fn()
    target.addEventListener('click', handler)
    try {
      target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }))
      await w.vm.$nextTick()
      expect(panel()).toBeNull()

      target.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      expect(handler).not.toHaveBeenCalled()

      target.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(handler).toHaveBeenCalledTimes(1)
    }
    finally {
      target.remove()
    }
  })

  it('the compact size prop applies the compact trigger classes', () => {
    const w = mountSelect({ modelValue: 'a', options, size: 'compact' })
    const button = w.get('button')
    expect(button.classes()).toContain('px-2')
    expect(button.classes()).toContain('py-1')
    expect(button.classes()).toContain('text-xs')
  })

  it('the default size prop applies the default trigger classes', () => {
    const w = mountSelect({ modelValue: 'a', options })
    const button = w.get('button')
    expect(button.classes()).toContain('px-3')
    expect(button.classes()).toContain('py-2')
    expect(button.classes()).toContain('text-sm')
  })

  it('opening shows every option with an empty filter, the selection as placeholder and as the active row', async () => {
    const w = mountSelect({ modelValue: 'b', options })
    await w.get('button').trigger('click')
    await flushPromises()
    expect(optionEls()).toHaveLength(3)
    expect(input().value).toBe('')
    expect(input().placeholder).toBe('Option B')
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[1].id)
  })

  it('typing filters the options by case-insensitive substring of label or string value', async () => {
    const w = mountSelect({ modelValue: 'apple', options: fruitOptions })
    await openByKey(w)
    await type('APPLE')
    expect(optionEls().map(o => o.textContent?.trim())).toEqual(['Apple✓', 'Tropical'])
    await type('an')
    expect(optionEls().map(o => o.textContent?.trim())).toEqual(['Banana'])
    expect(w.emitted('update:modelValue')).toBeUndefined()
  })

  it('arrow keys and Enter pick from the filtered list, skipping disabled rows', async () => {
    const w = mountSelect({ modelValue: 'a', options: [...optionsWithDisabled, { value: 'd', label: 'Other D' }] })
    await openByKey(w)
    await type('option')
    expect(optionEls()).toHaveLength(3)
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[0].id)
    await press('ArrowDown') // skips disabled 'Option B'
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[2].id)
    await press('Enter')
    expect(w.emitted('update:modelValue')?.[0]).toEqual(['c'])
  })

  it('escape after typing closes, restores the selected label and emits nothing', async () => {
    const w = mountSelect({ modelValue: 'banana', options: fruitOptions })
    await openByKey(w)
    await type('che')
    await press('Escape')
    expect(panel()).toBeNull()
    expect(w.get('button').text()).toContain('Banana')
    expect(w.emitted('update:modelValue')).toBeUndefined()
  })

  it('shows a non-selectable "No matches" row when the filter matches nothing', async () => {
    const w = mountSelect({ modelValue: 'apple', options: fruitOptions })
    await openByKey(w)
    await type('zzz')
    const rows = optionEls()
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent?.trim()).toBe('No matches')
    expect(rows[0].getAttribute('aria-disabled')).toBe('true')
    expect(input().getAttribute('aria-activedescendant')).toBeNull()
    rows[0].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()
  })

  it('free text never becomes the model value', async () => {
    const w = mountSelect({ modelValue: 'apple', options: fruitOptions })
    await openByKey(w)
    await type('zzz')
    await press('Enter')
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await flushPromises()
    expect(panel()).toBeNull()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    expect(w.get('button').text()).toContain('Apple')
  })

  it('typing a character while closed opens the panel filtered by that character', async () => {
    const w = mountSelect({ modelValue: 'apple', options: fruitOptions })
    await w.get('button').trigger('keydown', { key: 'c' })
    await flushPromises()
    expect(input().value).toBe('c')
    expect(document.activeElement).toBe(input())
    expect(optionEls().map(o => o.textContent?.trim())).toEqual(['Cherry', 'Tropical'])
    expect(input().getAttribute('aria-activedescendant')).toBe(optionEls()[0].id)
  })

  it('has combobox/listbox ARIA wiring: role, aria-expanded, aria-selected, aria-activedescendant', async () => {
    const w = mountSelect({ modelValue: 'b', options, ariaLabel: 'Choose option' })
    const button = w.get('button')
    expect(button.attributes('role')).toBe('combobox')
    expect(button.attributes('aria-haspopup')).toBe('listbox')
    expect(button.attributes('aria-expanded')).toBe('false')

    await button.trigger('click')
    await flushPromises()
    expect(input().getAttribute('aria-expanded')).toBe('true')
    expect(input().getAttribute('aria-autocomplete')).toBe('list')
    expect(input().getAttribute('aria-label')).toBe('Choose option')
    expect(panel()?.getAttribute('role')).toBe('listbox')

    const opts = optionEls()
    expect(opts[1].getAttribute('aria-selected')).toBe('true')
    expect(opts[0].getAttribute('aria-selected')).toBe('false')
    expect(input().getAttribute('aria-activedescendant')).toBe(opts[1].id)
    expect(input().getAttribute('aria-controls')).toBe(panel()?.id)
    expect(button.attributes('aria-controls')).toBe(panel()?.id)
  })

  it('disabled options carry aria-disabled', async () => {
    const w = mountSelect({ modelValue: 'a', options: optionsWithDisabled })
    await w.get('button').trigger('click')
    expect(optionEls()[1].getAttribute('aria-disabled')).toBe('true')
  })

  it('has no axe violations on the closed trigger', async () => {
    const w = mountSelect({ modelValue: 'a', options, ariaLabel: 'Choose option' })
    expect(await axe(w.get('button').element as HTMLElement)).toHaveNoViolations()
  })

  it('has no axe violations on the open panel', async () => {
    const w = mountSelect({ modelValue: 'a', options, ariaLabel: 'Choose option' })
    await w.get('button').trigger('click')
    expect(await axe(panel() as HTMLElement)).toHaveNoViolations()
  })

  it('escape with the panel open does not let the event reach a parent handler', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await openByKey(w)
    const parentHandler = vi.fn()
    document.addEventListener('keydown', parentHandler)
    try {
      await press('Escape')
      expect(parentHandler).not.toHaveBeenCalled()
    }
    finally {
      document.removeEventListener('keydown', parentHandler)
    }
  })

  it('escape with the panel closed lets the event reach a parent handler', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    const button = w.get('button')
    const parentHandler = vi.fn()
    document.addEventListener('keydown', parentHandler)
    try {
      await button.trigger('keydown', { key: 'Escape' }) // panel is already closed
      expect(parentHandler).toHaveBeenCalledTimes(1)
    }
    finally {
      document.removeEventListener('keydown', parentHandler)
    }
  })

  it('selecting the already-selected option emits nothing', async () => {
    const w = mountSelect({ modelValue: 'b', options })
    await w.get('button').trigger('click')
    await optionEls()[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await w.vm.$nextTick()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    expect(panel()).toBeNull()
  })

  it('opening focuses the filter input', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(input())
  })

  it('an options array where every option is disabled does not hang or throw', async () => {
    const allDisabled = [
      { value: 'a', label: 'Option A', disabled: true },
      { value: 'b', label: 'Option B', disabled: true },
    ]
    const w = mountSelect({ modelValue: 'zzz', options: allDisabled })
    await openByKey(w)
    await expect(press('ArrowDown')).resolves.not.toThrow()
    expect(panel()).not.toBeNull()
  })

  it('an empty options array opens without throwing', async () => {
    const w = mountSelect({ modelValue: 'a', options: [] })
    const button = w.get('button')
    await expect(button.trigger('keydown', { key: 'ArrowDown' })).resolves.not.toThrow()
    expect(panel()).not.toBeNull()
  })

  it('clicking the chevron toggle button while open closes the panel and refocuses the trigger', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('click')
    await flushPromises()
    expect(panel()).not.toBeNull()
    const chevron = input().nextElementSibling as HTMLButtonElement
    // A real click is preceded by a mousedown, which the outside-mousedown listener sees first.
    chevron.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
    chevron.click()
    await flushPromises()
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(w.get('button').element)
  })

  it('a caller class sits on the button when closed and on the input wrapper when open', async () => {
    wrapper = mount(AppSelect, { props: { modelValue: 'a', options }, attrs: { class: 'flex-1 shrink-0' }, attachTo: document.body })
    const button = wrapper.get('button').element
    expect(button.classList.contains('flex-1')).toBe(true)
    await wrapper.get('button').trigger('click')
    await flushPromises()
    const openWrapper = input().parentElement!
    expect(openWrapper.classList.contains('flex-1')).toBe(true)
    expect(openWrapper.classList.contains('shrink-0')).toBe(true)
    expect(input().classList.contains('flex-1')).toBe(false)
  })

  it('only one role=combobox element is visible (not display:none) while open', async () => {
    const w = mountSelect({ modelValue: 'a', options })
    await w.get('button').trigger('click')
    await flushPromises()
    const comboboxes = document.querySelectorAll<HTMLElement>('[role="combobox"]')
    const visible = Array.from(comboboxes).filter(el => getComputedStyle(el).display !== 'none')
    expect(visible).toHaveLength(1)
    expect(visible[0].tagName).toBe('INPUT')
  })

  it('placeholder prop shows on the closed trigger when modelValue matches no option', () => {
    const w = mountSelect({ modelValue: 'zzz', options, placeholder: 'Pick one…' })
    expect(w.get('button').text()).toContain('Pick one…')
  })

  it('selected label takes precedence over placeholder prop', () => {
    const w = mountSelect({ modelValue: 'b', options, placeholder: 'Pick one…' })
    expect(w.get('button').text()).toContain('Option B')
    expect(w.get('button').text()).not.toContain('Pick one…')
  })

  it('open input shows placeholder prop as hint when modelValue matches nothing', async () => {
    const w = mountSelect({ modelValue: 'zzz', options, placeholder: 'Pick one…' })
    await w.get('button').trigger('click')
    await flushPromises()
    expect(input().placeholder).toBe('Pick one…')
  })
})
