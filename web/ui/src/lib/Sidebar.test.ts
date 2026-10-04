import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import Sidebar from './Sidebar.svelte'
import type { AgentRow } from './fleet'
import type { ProjectStatus } from './api'

afterEach(cleanup)

const projects: ProjectStatus[] = [
  { root: '/home/u/alpha', available: true, trust: 'trusted' },
  { root: '/home/u/beta', available: false, error: 'down', trust: 'untrusted' },
]

function agent(id: string, project: string): AgentRow {
  return {
    id,
    project,
    name: id,
    mode: '',
    status: 'idle',
    activity: '',
    contextPct: 0,
    changedFiles: 0,
    interrupted: false,
    updatedAt: '2024-05-04T12:00:00Z',
  }
}

function mount(route = '#') {
  const onNavigate = vi.fn()
  render(Sidebar, {
    open: true,
    onToggle: () => {},
    agents: [],
    projects,
    pendingCount: 0,
    clientCount: 0,
    route,
    activeAgentId: null,
    onNavigate,
  })
  return { onNavigate }
}

describe('Sidebar groups', () => {
  it('groups agents by what they need, with Earlier collapsed by default', async () => {
    const rows = [
      { ...agent('ask', '/home/u/alpha'), pending: { kind: 'approval' as const, id: 't' } },
      { ...agent('busy', '/home/u/alpha'), status: 'running' as const },
      { ...agent('done', '/home/u/beta'), changedFiles: 2 },
      agent('old', '/home/u/beta'),
    ]
    render(Sidebar, { open: true, onToggle: () => {}, agents: rows, projects, pendingCount: 0, clientCount: 0, route: '#', activeAgentId: null, onNavigate: () => {} })
    for (const label of ['Needs you', 'Running', 'Ready to ship', 'Earlier']) expect(screen.getByText(label)).toBeTruthy()
    expect(screen.getByText('ask')).toBeTruthy()
    expect(screen.getByText('busy')).toBeTruthy()
    expect(screen.getByText('done')).toBeTruthy()
    expect(screen.queryByText('old')).toBeNull()
    await userEvent.click(screen.getByText('Earlier'))
    expect(screen.getByText('old')).toBeTruthy()
  })

  it('navigates to the agent chat', async () => {
    const onNavigate = vi.fn()
    render(Sidebar, { open: true, onToggle: () => {}, agents: [{ ...agent('busy', '/home/u/alpha'), status: 'running' }], projects, pendingCount: 0, clientCount: 0, route: '#', activeAgentId: null, onNavigate })
    await userEvent.click(screen.getByText('busy'))
    expect(onNavigate).toHaveBeenCalledWith('#chat/busy')
  })
})

describe('Sidebar sessions nav', () => {
  it('highlights the Sessions nav item on the scoped route', () => {
    mount('#sessions/%2Fhome%2Fu%2Falpha')
    const sessions = screen.getByRole('button', { name: /^Sessions$/ })
    expect(sessions.className).toContain('font-medium')
  })

  it('highlights the Sessions nav item on the unscoped route too', () => {
    mount('#sessions')
    const sessions = screen.getByRole('button', { name: /^Sessions$/ })
    expect(sessions.className).toContain('font-medium')
  })

  it('does not highlight the Sessions nav item on the picker when unscoped elsewhere', () => {
    mount('#projects')
    const sessions = screen.getByRole('button', { name: /^Sessions$/ })
    expect(sessions.className).not.toContain('font-medium')
  })
})
