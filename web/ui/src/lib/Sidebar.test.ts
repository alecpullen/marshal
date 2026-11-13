import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import Sidebar from './Sidebar.svelte'
import type { AgentRow } from './fleet'
import type { Inbox, PendingSubmission, ProjectStatus } from './api'

afterEach(cleanup)

const projects: ProjectStatus[] = [
  { root: '/home/u/alpha', available: true, trust: 'trusted', isolation: 'available' },
  { root: '/home/u/beta', available: false, error: 'down', trust: 'untrusted', isolation: 'available' },
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

const inbox = (o: Partial<Inbox['groups']> = {}, queue: Inbox['queue'] = []): Inbox => ({
  groups: { needsYou: [], running: [], ready: [], earlier: [], ...o },
  queue,
})

const submission = (id: string): PendingSubmission => ({
  id,
  auto: null,
  clientId: 'c1',
  mode: 'act',
  origin: 'mcp',
  plan: '',
  prompt: '',
  ref: '',
  title: id,
  repoId: 'r',
  createdAt: '2024-05-04T09:00:00Z',
  expiresAt: '2024-06-01T00:00:00Z',
})

function mount(route = '#') {
  const onNavigate = vi.fn()
  render(Sidebar, {
    open: true,
    onToggle: () => {},
    agents: [],
    inbox: inbox(),
    projects,
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
    const ib = inbox({ needsYou: ['ask'], running: ['busy'], ready: ['done'], earlier: ['old'] })
    render(Sidebar, { open: true, onToggle: () => {}, agents: rows, inbox: ib, projects, clientCount: 0, route: '#', activeAgentId: null, onNavigate: () => {} })
    for (const label of ['Needs you', 'Running', 'Ready to ship', 'Earlier']) expect(screen.getByText(label)).toBeTruthy()
    expect(screen.getByText('ask')).toBeTruthy()
    expect(screen.getByText('busy')).toBeTruthy()
    expect(screen.getByText('done')).toBeTruthy()
    expect(screen.queryByText('old')).toBeNull()
    await userEvent.click(screen.getByText('Earlier'))
    expect(screen.getByText('old')).toBeTruthy()
  })

  it('lists each group in the order the bridge sent', () => {
    const rows = [agent('first', '/home/u/alpha'), agent('second', '/home/u/alpha')]
    render(Sidebar, { open: true, onToggle: () => {}, agents: rows, inbox: inbox({ needsYou: ['second', 'first'] }), projects, clientCount: 0, route: '#', activeAgentId: null, onNavigate: () => {} })
    expect(screen.getAllByText(/^(second|first)$/).map((el) => el.textContent)).toEqual(['second', 'first'])
  })

  it('skips an ID it has no row for', () => {
    const rows = [agent('busy', '/home/u/alpha')]
    render(Sidebar, { open: true, onToggle: () => {}, agents: rows, inbox: inbox({ running: ['busy', 'ghost'] }), projects, clientCount: 0, route: '#', activeAgentId: null, onNavigate: () => {} })
    expect(screen.getByText('busy')).toBeTruthy()
    expect(screen.queryByText('ghost')).toBeNull()
  })

  it('counts the queue’s intake items as Pending', () => {
    const queue: Inbox['queue'] = [
      { kind: 'intake', submission: submission('p1'), at: '2024-05-04T09:00:00Z' },
      { kind: 'agent', agentId: 'busy', at: '2024-05-04T10:00:00Z' },
    ]
    render(Sidebar, { open: true, onToggle: () => {}, agents: [agent('busy', '/home/u/alpha')], inbox: inbox({ needsYou: ['busy'] }, queue), projects, clientCount: 0, route: '#', activeAgentId: null, onNavigate: () => {} })
    const pending = screen.getByRole('button', { name: /^Pending/ })
    expect(pending.textContent).toContain('1')
  })

  it('navigates to the agent chat', async () => {
    const onNavigate = vi.fn()
    render(Sidebar, { open: true, onToggle: () => {}, agents: [{ ...agent('busy', '/home/u/alpha'), status: 'running' }], inbox: inbox({ running: ['busy'] }), projects, clientCount: 0, route: '#', activeAgentId: null, onNavigate })
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
