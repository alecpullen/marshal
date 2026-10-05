import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import StatusShare from './StatusShare.svelte'
import * as api from './api'

vi.mock('./api', async (importActual) => {
  const actual = await importActual<typeof import('./api')>()
  return { ...actual, createStatusLink: vi.fn() }
})

beforeEach(() => (api.createStatusLink as Mock).mockResolvedValue({ id: 'l1', url: '/s/tok123', expiresAt: '2026-10-12T00:00:00Z' }))
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('StatusShare', () => {
  it('create posts the chosen TTL and shows the absolute URL once', async () => {
    render(StatusShare, { agentId: 'a1' })
    await fireEvent.click(screen.getByRole('button', { name: 'Share status' }))
    await fireEvent.change(screen.getByLabelText('Expires after'), { target: { value: '720' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(api.createStatusLink).toHaveBeenCalledWith('a1', 720))
    expect((await screen.findByLabelText('Status link') as HTMLInputElement).value).toBe(`${location.origin}/s/tok123`)
    expect(screen.getByText(/Shown only once/)).toBeTruthy()

    // Closing and reopening forgets the token.
    await fireEvent.click(screen.getByRole('button', { name: 'Share status' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Share status' }))
    expect(screen.queryByLabelText('Status link')).toBeNull()
  })

  it('defaults to seven days', async () => {
    render(StatusShare, { agentId: 'a1' })
    await fireEvent.click(screen.getByRole('button', { name: 'Share status' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(api.createStatusLink).toHaveBeenCalledWith('a1', 168))
  })
})
