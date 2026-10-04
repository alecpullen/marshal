import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import DecisionOutcome from './DecisionOutcome.svelte'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('DecisionOutcome', () => {
  it('toasts "Added to <workspace> draft" with a link to the designer', async () => {
    const onClose = vi.fn()
    render(DecisionOutcome, { outcome: { kind: 'draft', workspace: 'go dev', host: 'h.example' }, onClose })
    expect(screen.getByTestId('decision-toast').textContent).toContain('Added to go dev draft')
    const link = screen.getByRole('link', { name: 'Open designer' })
    expect(link.getAttribute('href')).toBe('#workspaces/go%20dev/edit')
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onClose).toHaveBeenCalled()
  })

  it('opens a modal with the repo patch and copies it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const onClose = vi.fn()
    render(DecisionOutcome, { outcome: { kind: 'patch', workspace: 'go', host: 'h.example', patch: '--- a\n+++ b' }, onClose })
    expect(await screen.findByTestId('patch-text')).toBeTruthy()
    expect(screen.getByTestId('patch-text').textContent).toBe('--- a\n+++ b')
    await userEvent.click(screen.getByRole('button', { name: 'Copy' }))
    expect(writeText).toHaveBeenCalledWith('--- a\n+++ b')
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy()
  })

  it('renders nothing without an outcome', () => {
    render(DecisionOutcome, { outcome: null, onClose: vi.fn() })
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
