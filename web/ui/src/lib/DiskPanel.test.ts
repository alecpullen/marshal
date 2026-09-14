import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import DiskPanel from './DiskPanel.svelte'
import * as api from './api.js'

/*
  The panel calls the api module on mount and on prune; the mock keeps
  those off fetch (and off ensureToken's prompt) under jsdom. APIError is
  part of the surface under test — the 503 fleet-mode case throws it — so
  it is a real class with a status field, matching api.ts.
*/
vi.mock('./api.js', () => ({
  AuthError: class extends Error {},
  APIError: class extends Error {
    status: number
    body: unknown
    constructor(status: number, body: unknown) {
      super('API error')
      this.status = status
      this.body = body
    }
  },
  getDiskUsage: vi.fn(),
  pruneDisk: vi.fn(),
}))

afterEach(cleanup)

describe('DiskPanel', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    ;(api.getDiskUsage as Mock).mockResolvedValue({
      repos: 2 * 1024 * 1024 * 1024,
      work: 1024 * 1024 * 1024,
      total: 3 * 1024 * 1024 * 1024,
      measuredAt: '2024-01-02T03:04:05Z',
      budgetMB: 10240, // 10 GB → 30% of budget → running tone
    })
  })

  it('renders repos, work and total with a budget bar whose tone matches the budget share', async () => {
    render(DiskPanel)

    expect(await screen.findByText('2.0 GB')).toBeTruthy()
    expect(screen.getByText('1.0 GB')).toBeTruthy()
    expect(screen.getByText('3.0 GB')).toBeTruthy()

    const bar = screen.getByTestId('disk-bar')
    expect(bar.firstElementChild?.className).toContain('bg-running')

    // Over budget flips the tone to danger.
    ;(api.getDiskUsage as Mock).mockResolvedValue({
      repos: 8 * 1024 * 1024 * 1024,
      work: 4 * 1024 * 1024 * 1024,
      total: 12 * 1024 * 1024 * 1024,
      measuredAt: '2024-01-02T03:04:05Z',
      budgetMB: 10240,
    })
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() =>
      expect(screen.getByTestId('disk-bar').firstElementChild?.className).toContain('bg-danger'),
    )
  })

  it('renders "No budget set." with the figures when budgetMB is 0', async () => {
    ;(api.getDiskUsage as Mock).mockResolvedValue({
      repos: 2 * 1024 * 1024 * 1024,
      work: 1024 * 1024 * 1024,
      total: 3 * 1024 * 1024 * 1024,
      measuredAt: '2024-01-02T03:04:05Z',
      budgetMB: 0,
    })
    render(DiskPanel)

    expect(await screen.findByText('No budget set.')).toBeTruthy()
    expect(screen.getByText('2.0 GB')).toBeTruthy()
    expect(screen.getByText('1.0 GB')).toBeTruthy()
    expect(screen.queryByTestId('disk-bar')).toBeNull()
  })

  it('requires an inline confirm before pruning and refetches afterward', async () => {
    ;(api.pruneDisk as Mock).mockResolvedValue({ reclaimed: 1024 * 1024 * 1024, total: 2 * 1024 * 1024 * 1024 })
    render(DiskPanel)
    await screen.findByText('2.0 GB')

    await userEvent.click(screen.getByRole('button', { name: 'Prune' }))
    expect(api.pruneDisk).not.toHaveBeenCalled()

    await userEvent.click(await screen.findByRole('button', { name: 'Confirm prune' }))
    await waitFor(() => expect(api.pruneDisk).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('Reclaimed 1.0 GB')).toBeTruthy()

    // The result pane replaces the confirmation; Cancel is gone with it.
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()

    // Confirm triggers a refetch so the bar reflects the post-prune total.
    await waitFor(() => expect(api.getDiskUsage).toHaveBeenCalledTimes(2))
  })

  it('surfaces a prune warning in attention tone when present', async () => {
    ;(api.pruneDisk as Mock).mockResolvedValue({
      reclaimed: 512 * 1024 * 1024,
      total: 2 * 1024 * 1024 * 1024,
      warning: '1 worktree was in use and kept',
    })
    render(DiskPanel)
    await screen.findByText('2.0 GB')

    await userEvent.click(screen.getByRole('button', { name: 'Prune' }))
    await userEvent.click(screen.getByRole('button', { name: 'Confirm prune' }))

    const warning = await screen.findByText('1 worktree was in use and kept')
    expect(warning.className).toContain('text-attention')
  })

  it('renders the fleet-mode note instead of an error banner on a 503', async () => {
    ;(api.getDiskUsage as Mock).mockRejectedValue(new api.APIError(503, { error: 'disk endpoints require fleet mode' }))
    render(DiskPanel)

    expect(await screen.findByText('Disk tracking requires fleet mode.')).toBeTruthy()
    // The 503 is an expected absence, not a failure: no raw error banner.
    expect(document.querySelector('.text-danger')).toBeNull()
  })
})
