import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import DiskPanel from './DiskPanel.svelte'
import * as api from './api.js'

/*
  The panel calls the api module on mount and on prune; the mock keeps
  those off fetch (and off ensureToken's prompt) under jsdom. Everything
  else — APIError and errMessage especially — comes from the real module,
  so the prune-failure case below exercises the actual body unwrapping
  the component ships with, not a copy of it.
*/
vi.mock('./api.js', async (importActual) => {
  const actual = await importActual<typeof import('./api.js')>()
  return {
    ...actual,
    getDiskUsage: vi.fn(),
    pruneDisk: vi.fn(),
  }
})

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

  it('unwraps the bridge reason in the error banner when a prune fails', async () => {
    ;(api.pruneDisk as Mock).mockRejectedValue(new api.APIError(502, { error: 'prune failed' }))
    render(DiskPanel)
    await screen.findByText('2.0 GB')

    await userEvent.click(screen.getByRole('button', { name: 'Prune' }))
    await userEvent.click(screen.getByRole('button', { name: 'Confirm prune' }))

    // errMessage unwraps the body's error; the generic status text never shows.
    expect(await screen.findByText('prune failed')).toBeTruthy()
    expect(screen.queryByText('API error 502')).toBeNull()
  })

  it('shows the raw error banner, not the fleet-mode note, when a prune hits a 503', async () => {
    ;(api.pruneDisk as Mock).mockRejectedValue(new api.APIError(503, undefined))
    render(DiskPanel)
    await screen.findByText('2.0 GB')

    await userEvent.click(screen.getByRole('button', { name: 'Prune' }))
    await userEvent.click(screen.getByRole('button', { name: 'Confirm prune' }))

    /*
      Pinning current behavior: only refresh() special-cases a 503, so a
      fleet-missing prune lands in the raw banner. A muted note here would
      be a reasonable follow-up, but is beyond this change.
    */
    expect(await screen.findByText('API error 503')).toBeTruthy()
    expect(screen.queryByText('Disk tracking requires fleet mode.')).toBeNull()
  })
})
