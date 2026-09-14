import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import ProjectsPanel from './ProjectsPanel.svelte'
import * as api from './api.js'

/*
  The panel calls the api module on mount and on every action; the mock
  keeps those off fetch (and off ensureToken's prompt) under jsdom.
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
  listProjects: vi.fn(),
  addProject: vi.fn(),
  removeProject: vi.fn(),
}))

const seeded = [
  { root: '/home/u/alpha', available: true, trust: 'trusted' },
  { root: '/home/u/beta', available: false, error: 'spawn failed', trust: 'untrusted' },
  { root: '/home/u/gamma', available: true, trust: 'na', orphanWorktrees: ['wt-1', 'wt-2'] },
]

afterEach(cleanup)

describe('ProjectsPanel', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    ;(api.listProjects as Mock).mockResolvedValue(seeded)
  })

  it('renders seeded projects with availability and trust indicators', async () => {
    render(ProjectsPanel)
    expect(await screen.findByText('alpha')).toBeTruthy()
    // The short label is backed by the full root in a title attribute.
    expect(screen.getByTitle('/home/u/alpha')).toBeTruthy()
    expect(screen.getByTitle('/home/u/gamma')).toBeTruthy()
    expect(screen.getByText('trusted')).toBeTruthy()
    expect(screen.getByText('untrusted')).toBeTruthy()
    expect(screen.getByText('na')).toBeTruthy()
    expect(screen.getByText('! spawn failed')).toBeTruthy()
    expect(screen.getByText('2 orphan worktrees')).toBeTruthy()
    // Nothing entered yet, so Add project is disabled.
    expect((screen.getByRole('button', { name: 'Add project' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('adds a project and replaces the list from the addProject return value', async () => {
    ;(api.addProject as Mock).mockResolvedValue([{ root: '/home/u/delta', available: true, trust: 'trusted' }])
    render(ProjectsPanel)
    await screen.findByText('alpha')
    await userEvent.type(screen.getByPlaceholderText('Repository root (e.g. /home/u/project)'), '/home/u/delta')
    await userEvent.click(screen.getByRole('button', { name: 'Add project' }))
    expect(api.addProject).toHaveBeenCalledWith('/home/u/delta')
    expect(await screen.findByText('delta')).toBeTruthy()
    expect(screen.queryByText('alpha')).toBeNull()
  })

  it('requires an inline confirm before removing a project', async () => {
    ;(api.removeProject as Mock).mockResolvedValue([])
    render(ProjectsPanel)
    await screen.findByText('alpha')
    await userEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0])
    // The first click only arms the confirmation.
    expect(api.removeProject).not.toHaveBeenCalled()
    await screen.findByRole('button', { name: 'Confirm remove' })
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(api.removeProject).not.toHaveBeenCalled()
    await userEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0])
    await userEvent.click(await screen.findByRole('button', { name: 'Confirm remove' }))
    expect(api.removeProject).toHaveBeenCalledWith('/home/u/alpha')
    // The list the bridge returns replaces the rows.
    expect(await screen.findByText('No projects registered.')).toBeTruthy()
  })

  it('shows the error banner when addProject rejects', async () => {
    ;(api.addProject as Mock).mockRejectedValue(new Error('root is not a git worktree'))
    render(ProjectsPanel)
    await screen.findByText('alpha')
    await userEvent.type(screen.getByPlaceholderText('Repository root (e.g. /home/u/project)'), '/nope')
    await userEvent.click(screen.getByRole('button', { name: 'Add project' }))
    expect(await screen.findByText('root is not a git worktree')).toBeTruthy()
  })
})
