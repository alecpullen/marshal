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

const agents: AgentRow[] = [agent('a-1', '/home/u/alpha'), agent('b-1', '/home/u/beta')]

function mount(route = '#') {
  const onNavigate = vi.fn()
  render(Sidebar, {
    open: true,
    onToggle: () => {},
    agents,
    projects,
    pendingCount: 0,
    clientCount: 0,
    route,
    activeAgentId: null,
    onNavigate,
  })
  return { onNavigate }
}

describe('Sidebar sessions scoping', () => {
  it('renders a sessions affordance per project group that navigates to the scoped route', async () => {
    const { onNavigate } = mount()
    const buttons = screen.getAllByRole('button', { name: /^Sessions for / })
    expect(buttons).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: 'Sessions for /home/u/alpha' }))
    expect(onNavigate).toHaveBeenCalledWith('#sessions/' + encodeURIComponent('/home/u/alpha'))
  })

  it('collapsing still works alongside the sessions affordance', async () => {
    mount()
    expect(screen.getByText('a-1')).toBeTruthy()
    await userEvent.click(screen.getByTitle('/home/u/alpha'))
    expect(screen.queryByText('a-1')).toBeNull()
  })

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
