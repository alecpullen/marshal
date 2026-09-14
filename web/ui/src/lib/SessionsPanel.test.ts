import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import SessionsPanel from './SessionsPanel.svelte'
import * as api from './api.js'

/*
  The api mock keeps the panel off fetch under jsdom while the real
  errMessage (which the catch paths route through) comes along from the
  actual module.
*/
vi.mock('./api.js', async (importActual) => {
  const actual = await importActual<typeof import('./api.js')>()
  return {
    ...actual,
    listProjects: vi.fn(),
    listSessions: vi.fn(),
    deleteSession: vi.fn(),
  }
})

const projects = [
  { root: '/home/u/alpha', available: true, trust: 'trusted' },
  { root: '/home/u/beta', available: false, error: 'down', trust: 'untrusted' },
]

const sessions = [
  { sessionId: 's-1', title: 'Fix the build', updated: '2024-05-04T12:00:00Z', messageCount: 12 },
  { sessionId: 's-2', updated: 'yesterday, sort of', messageCount: 1 },
  { sessionId: 's-3' },
]

afterEach(cleanup)

describe('SessionsPanel', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    ;(api.listProjects as Mock).mockResolvedValue(projects)
    ;(api.listSessions as Mock).mockResolvedValue(sessions)
    ;(api.deleteSession as Mock).mockResolvedValue(undefined)
  })

  it('renders the project picker and lists a picked project’s sessions', async () => {
    render(SessionsPanel)
    expect(await screen.findByText('alpha')).toBeTruthy()
    expect(api.listSessions).not.toHaveBeenCalled()
    await userEvent.click(screen.getByTitle('/home/u/alpha'))
    expect(api.listSessions).toHaveBeenCalledWith('/home/u/alpha')
    expect(await screen.findByText('Fix the build')).toBeTruthy()
    expect(screen.getByText('Sessions — alpha')).toBeTruthy()
  })

  it('lists sessions directly when the project prop is set', async () => {
    render(SessionsPanel, { project: '/home/u/alpha' })
    await screen.findByText('Fix the build')
    expect(api.listSessions).toHaveBeenCalledWith('/home/u/alpha')
    expect(api.listProjects).not.toHaveBeenCalled()
  })

  it('renders title, updated, and messageCount defensively', async () => {
    render(SessionsPanel, { project: '/home/u/alpha' })
    expect(await screen.findByText('Fix the build')).toBeTruthy()
    // s-2 has no title, so the id shows instead.
    expect(screen.getByText('s-2')).toBeTruthy()
    // An unparseable `updated` renders as the raw string, not an Invalid Date.
    expect(screen.getByText('yesterday, sort of')).toBeTruthy()
    // s-3 has no updated at all: nothing renders for it.
    expect(screen.getByText('1 message')).toBeTruthy()
    expect(screen.getByText('12 messages')).toBeTruthy()
  })

  it('resumes by navigating to #chat/<id>', async () => {
    render(SessionsPanel, { project: '/home/u/alpha' })
    await screen.findByText('Fix the build')
    window.location.hash = ''
    await userEvent.click(screen.getAllByRole('button', { name: 'Resume' })[0])
    expect(window.location.hash).toBe('#chat/s-1')
  })

  it('requires a confirm before deleting, then refreshes', async () => {
    render(SessionsPanel, { project: '/home/u/alpha' })
    await screen.findByText('Fix the build')
    await userEvent.click(screen.getAllByRole('button', { name: 'Delete' })[0])
    expect(api.deleteSession).not.toHaveBeenCalled()
    await screen.findByRole('button', { name: 'Confirm delete' })
    // Cancel disarms without a round-trip.
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(api.deleteSession).not.toHaveBeenCalled()
    await userEvent.click(screen.getAllByRole('button', { name: 'Delete' })[0])
    await userEvent.click(await screen.findByRole('button', { name: 'Confirm delete' }))
    expect(api.deleteSession).toHaveBeenCalledWith('s-1')
    // The delete path refreshes from the mocked listSessions.
    expect(api.listSessions).toHaveBeenCalledWith('/home/u/alpha')
  })
})
