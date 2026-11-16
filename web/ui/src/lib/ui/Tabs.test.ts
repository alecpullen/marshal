import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, createEvent, fireEvent, render, screen } from '@testing-library/svelte'
import Tabs from './Tabs.svelte'

afterEach(cleanup)

/*
  The tablist declares the ARIA tabs pattern, so these tests pin the pattern's
  keyboard model: the arrows move the selection (wrapping at the ends), Home
  and End jump, focus follows the newly selected tab, and the handled keys do
  not scroll the page. Every tab keeps its place in the normal tab order — the
  primitive adds no roving tabindex, because views outside this file rely on
  the tab order.
*/
const tabs = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta' },
  { value: 'c', label: 'Gamma' },
]

describe('Tabs', () => {
  it('exposes the tablist roles and the expected aria-selected values', async () => {
    const onchange = vi.fn()
    render(Tabs, { tabs, value: 'b', onchange, label: 'Sections' })

    expect(screen.getByRole('tablist', { name: 'Sections' })).toBeTruthy()
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('aria-selected')).toBe('false')
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByRole('tab', { name: 'Gamma' }).getAttribute('aria-selected')).toBe('false')

    // Clicking still selects, and no roving tabindex has crept in.
    for (const tab of screen.getAllByRole('tab')) expect(tab.getAttribute('tabindex')).toBeNull()
    await fireEvent.click(screen.getByRole('tab', { name: 'Gamma' }))
    expect(onchange).toHaveBeenCalledWith('c')
  })

  it('ArrowRight selects and focuses the next tab, wrapping at the end', async () => {
    const onchange = vi.fn()
    const { rerender } = render(Tabs, { tabs, value: 'a', onchange })

    // The handled key is cancelled: it must not scroll the page.
    const right = createEvent.keyDown(screen.getByRole('tablist'), { key: 'ArrowRight', cancelable: true })
    await fireEvent(screen.getByRole('tablist'), right)
    expect(right.defaultPrevented).toBe(true)
    expect(onchange).toHaveBeenCalledWith('b')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Beta' }))

    await rerender({ tabs, value: 'c', onchange })
    onchange.mockClear()
    await fireEvent.keyDown(screen.getByRole('tablist'), { key: 'ArrowRight' })
    expect(onchange).toHaveBeenCalledWith('a')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Alpha' }))
  })

  it('ArrowLeft wraps from the first tab to the last', async () => {
    const onchange = vi.fn()
    render(Tabs, { tabs, value: 'a', onchange })

    await fireEvent.keyDown(screen.getByRole('tablist'), { key: 'ArrowLeft' })
    expect(onchange).toHaveBeenCalledWith('c')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Gamma' }))
  })

  it('Home and End jump to the ends', async () => {
    const onchange = vi.fn()
    const { rerender } = render(Tabs, { tabs, value: 'b', onchange })

    await fireEvent.keyDown(screen.getByRole('tablist'), { key: 'End' })
    expect(onchange).toHaveBeenLastCalledWith('c')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Gamma' }))

    await rerender({ tabs, value: 'c', onchange })
    onchange.mockClear()
    await fireEvent.keyDown(screen.getByRole('tablist'), { key: 'Home' })
    expect(onchange).toHaveBeenLastCalledWith('a')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Alpha' }))
  })

  it('leaves other keys to the page', async () => {
    const onchange = vi.fn()
    render(Tabs, { tabs, value: 'a', onchange })

    // An unhandled key is neither cancelled nor a selection move.
    const down = createEvent.keyDown(screen.getByRole('tablist'), { key: 'ArrowDown', cancelable: true })
    await fireEvent(screen.getByRole('tablist'), down)
    expect(down.defaultPrevented).toBe(false)
    expect(onchange).not.toHaveBeenCalled()
  })
})
