import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import StatusLinks from './StatusLinks.svelte'
import * as api from '../../lib/api'

vi.mock('../../lib/api', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api')>()
  return { ...actual, listStatusLinks: vi.fn(), revokeStatusLink: vi.fn() }
})

const future = new Date(Date.now() + 86_400_000).toISOString()
const past = new Date(Date.now() - 86_400_000).toISOString()

beforeEach(() => {
  ;(api.listStatusLinks as Mock).mockResolvedValue([
    { id: 'l1', agentId: 'a1', createdAt: past, expiresAt: future },
    { id: 'l2', agentId: 'a2', createdAt: past, expiresAt: past },
    { id: 'l3', agentId: 'a3', createdAt: past, expiresAt: future, revokedAt: past },
  ])
  ;(api.revokeStatusLink as Mock).mockResolvedValue(undefined)
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('StatusLinks settings', () => {
  it('lists links with their state; only active ones can be revoked', async () => {
    render(StatusLinks, { onToast: vi.fn() })
    const rows = await screen.findAllByTestId('status-link')
    expect(rows.map((r) => /active|expired|revoked/.exec(r.textContent!)![0])).toEqual(['active', 'expired', 'revoked'])
    expect(screen.getAllByRole('button', { name: 'Revoke' })).toHaveLength(1)
  })

  it('revoke posts and marks the link revoked', async () => {
    render(StatusLinks, { onToast: vi.fn() })
    await fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(api.revokeStatusLink).toHaveBeenCalledWith('l1'))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Revoke' })).toBeNull())
  })
})
