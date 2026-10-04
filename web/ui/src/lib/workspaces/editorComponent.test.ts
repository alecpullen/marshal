import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import SourceEditor from './SourceEditor.svelte'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

const src = '[workspace]\nbase = "x"\n\n[packages]\napt = []\n'

describe('SourceEditor', () => {
  it('debounces onChange by 400ms', async () => {
    vi.useFakeTimers()
    const onChange = vi.fn()
    render(SourceEditor, { value: src, onChange })
    const ta = screen.getByLabelText('Workspace source') as HTMLTextAreaElement
    await fireEvent.input(ta, { target: { value: src + 'x' } })
    await fireEvent.input(ta, { target: { value: src + 'xy' } })
    vi.advanceTimersByTime(399)
    expect(onChange).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith(src + 'xy')
  })

  it('reports the 1-based cursor line', async () => {
    const onCursorLine = vi.fn()
    render(SourceEditor, { value: src, onChange: vi.fn(), onCursorLine })
    const ta = screen.getByLabelText('Workspace source') as HTMLTextAreaElement
    ta.focus()
    ta.setSelectionRange(src.indexOf('[packages]'), src.indexOf('[packages]'))
    await fireEvent.click(ta)
    expect(onCursorLine).toHaveBeenCalledWith(4)
  })

  it('marks diagnostics in the gutter and shows the cursor line message below', async () => {
    render(SourceEditor, { value: src, diagnostics: [{ line: 2, message: 'unknown key', severity: 'error' }], onChange: vi.fn(), onCursorLine: vi.fn() })
    expect(screen.getAllByTestId('gutter-diag')).toHaveLength(1)
    const ta = screen.getByLabelText('Workspace source') as HTMLTextAreaElement
    ta.focus()
    ta.setSelectionRange(14, 14)
    await fireEvent.click(ta)
    expect(screen.getByTestId('cursor-diag').textContent).toContain('unknown key')
  })

  it('tints the highlighted range and flashes changed lines', () => {
    const { container } = render(SourceEditor, { value: src, highlight: { start: 4, end: 5 }, flash: [5], onChange: vi.fn() })
    expect(container.querySelectorAll('[data-highlight]')).toHaveLength(2)
    expect(container.querySelectorAll('[data-flash]')).toHaveLength(1)
  })

  it('replaces the text when the value prop changes', async () => {
    const { rerender } = render(SourceEditor, { value: src, onChange: vi.fn() })
    await rerender({ value: 'new', onChange: vi.fn() })
    expect((screen.getByLabelText('Workspace source') as HTMLTextAreaElement).value).toBe('new')
  })
})
