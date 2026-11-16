import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import StateMessage from './StateMessage.svelte'

afterEach(cleanup)

describe('StateMessage', () => {
  it('announces a failure as an alert and everything else as a status', () => {
    /*
      The role is what decides whether a screen reader interrupts: an error
      must be heard immediately, while an empty list or a success note must
      wait its turn. Both roles also make the region findable by role, which
      is how every view's tests assert on these messages.
    */
    for (const tone of ['neutral', 'info', 'success', 'warn'] as const) {
      const { unmount } = render(StateMessage, { tone, title: 'Nothing here' })
      expect(screen.getByRole('status').textContent?.trim()).toBe('Nothing here')
      unmount()
    }

    render(StateMessage, { tone: 'error', title: 'Bridge unreachable' })
    expect(screen.getByRole('alert').textContent?.trim()).toBe('Bridge unreachable')
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('renders the body under the title', () => {
    render(StateMessage, {
      tone: 'info',
      title: 'No runs yet',
      body: 'Start one from the fleet screen.',
    })
    const region = screen.getByRole('status')
    expect(region.textContent).toContain('No runs yet')
    expect(region.textContent).toContain('Start one from the fleet screen.')
  })

  it('runs the action when its button is pressed', async () => {
    const onAction = vi.fn()
    render(StateMessage, {
      tone: 'warn',
      title: 'Daemon not running',
      body: 'The fleet screen needs it.',
      actionLabel: 'Start the daemon',
      onAction,
    })

    await fireEvent.click(screen.getByRole('button', { name: 'Start the daemon' }))

    expect(onAction).toHaveBeenCalledOnce()
  })

  it('offers no action without both a label and a handler', () => {
    // A button that renders but does nothing is worse than none: it invites a
    // click and answers with silence, so the pair is required.
    const { unmount } = render(StateMessage, { title: 'No runs yet', actionLabel: 'Retry' })
    expect(screen.queryByRole('button')).toBeNull()
    unmount()

    render(StateMessage, { title: 'No runs yet', onAction: vi.fn() })
    expect(screen.queryByRole('button')).toBeNull()
  })
})
