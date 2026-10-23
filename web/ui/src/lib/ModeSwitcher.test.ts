import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import ModeSwitcher from './ModeSwitcher.svelte'
import type { AccessState } from './interaction'

afterEach(cleanup)

const access = (over: Partial<AccessState> = {}): AccessState => ({
  mode: 'act',
  auto: false,
  system: false,
  revision: 1,
  ...over,
})

describe('ModeSwitcher', () => {
  it('shows two modes and independent modifiers', async () => {
    const onChange = vi.fn()
    render(ModeSwitcher, { access: access(), onChange })

    expect(screen.getByRole('button', { name: 'Ask' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Act' })).toBeTruthy()
    // The retired five-mode list is gone from the picker.
    for (const retired of ['Plan', 'Default', 'Edit', 'Copilot']) {
      expect(screen.queryByRole('button', { name: retired })).toBeNull()
    }

    await fireEvent.click(screen.getByRole('button', { name: 'Auto' }))
    expect(onChange).toHaveBeenLastCalledWith({ auto: true })
    await fireEvent.click(screen.getByRole('button', { name: 'System' }))
    expect(onChange).toHaveBeenLastCalledWith({ system_access: true })
  })

  it('waits for committed access before updating', async () => {
    const onChange = vi.fn()
    const { rerender } = render(ModeSwitcher, { access: access({ mode: 'act' }), onChange })

    await fireEvent.click(screen.getByRole('button', { name: 'Ask' }))
    expect(onChange).toHaveBeenLastCalledWith({ mode: 'ask' })
    // The control still shows the committed Act until a new state arrives.
    expect(screen.getByRole('button', { name: 'Act' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'Ask' }).getAttribute('aria-pressed')).toBe('false')

    await rerender({ access: access({ mode: 'ask', revision: 2 }), onChange })
    expect(screen.getByRole('button', { name: 'Ask' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('presents an unavailable tuple as unavailable rather than a default', () => {
    render(ModeSwitcher, { access: null, onChange: vi.fn() })
    expect(screen.getByTestId('access-label').textContent).toBe('access unavailable')
    expect(screen.getByRole('button', { name: 'Ask' }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByRole('button', { name: 'Act' }).hasAttribute('disabled')).toBe(true)
  })

  it('disables Auto in Ask because it is an Act modifier', () => {
    render(ModeSwitcher, { access: access({ mode: 'ask' }), onChange: vi.fn() })
    expect(screen.getByRole('button', { name: 'Auto' }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByRole('button', { name: 'System' }).hasAttribute('disabled')).toBe(false)
  })
})
