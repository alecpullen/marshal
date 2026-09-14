import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import ClientsPanel from './ClientsPanel.svelte'
import * as api from './api.js'

/*
  Quota badges are the only visible signal that a client is restricted,
  and the bridge defaults an absent field to unlimited — so the form must
  drop 0/empty fields from the payload rather than send them as zeros.
*/
vi.mock('./api.js', () => ({
  listClients: vi.fn(),
  createClient: vi.fn(),
  deleteClient: vi.fn(),
}))

function client(over: Partial<api.MCPClient> = {}): api.MCPClient {
  return {
    id: 'c1',
    name: 'claude-code',
    autonomous: false,
    maxConcurrent: 0,
    maxPerDay: 0,
    allowedRepos: [],
    ownerId: 'o1',
    createdAt: '2025-01-01T00:00:00Z',
    ...over,
  }
}

function seedCreate() {
  ;(api.createClient as Mock).mockResolvedValue({
    id: 'c1',
    name: 'x',
    token: 'tok-1',
    autonomous: false,
  })
}

afterEach(cleanup)

beforeEach(() => {
  vi.clearAllMocks()
})

describe('ClientsPanel', () => {
  it('renders quota badges for a restricted client', async () => {
    ;(api.listClients as Mock).mockResolvedValue([
      client({ maxConcurrent: 3, maxPerDay: 10, allowedRepos: ['a/b', 'c/d'] }),
    ])

    render(ClientsPanel)

    expect(await screen.findByText('max 3 concurrent')).toBeTruthy()
    expect(screen.getByText('max 10/day')).toBeTruthy()
    expect(screen.getByText('2 repos')).toBeTruthy()
  })

  it('sends the entered quotas to createClient', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.type(screen.getByPlaceholderText('Max concurrent'), '3')
    await user.type(screen.getByPlaceholderText('Max per day'), '10')
    await user.type(
      screen.getByPlaceholderText('Allowed repos (owner/repo, comma-separated)'),
      'a/b, c/d',
    )
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledWith({
      name: 'x',
      autonomous: false,
      maxConcurrent: 3,
      maxPerDay: 10,
      allowedRepos: ['a/b', 'c/d'],
    })
  })

  it('omits quota fields when only a name is given', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledTimes(1)
    const payload = (api.createClient as Mock).mock.calls[0][0] as Record<string, unknown>
    expect(payload).toEqual({ name: 'x', autonomous: false })
    expect(payload).not.toHaveProperty('maxConcurrent')
    expect(payload).not.toHaveProperty('maxPerDay')
    expect(payload).not.toHaveProperty('allowedRepos')
  })

  it('trims and drops empty segments from the repos input', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.type(
      screen.getByPlaceholderText('Allowed repos (owner/repo, comma-separated)'),
      'a/b, , c/d',
    )
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledWith({
      name: 'x',
      autonomous: false,
      allowedRepos: ['a/b', 'c/d'],
    })
  })
})
