import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import Home from './Home.svelte'
import * as api from '../lib/api.js'
import { toRow, createFleetStore } from '../lib/fleet'
import { get } from 'svelte/store'
import { outcomeFor } from '../lib/inbox/decision'
import type { AgentStatus, PendingSubmission } from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    resolvePermission: vi.fn().mockResolvedValue(undefined),
    approvePending: vi.fn().mockResolvedValue({ agentId: 'x', status: 'ok' }),
    denyPending: vi.fn().mockResolvedValue(undefined),
    undoReroute: vi.fn().mockResolvedValue(undefined),
    postNetworkDecision: vi.fn().mockResolvedValue({ ok: true }),
    getDiskUsage: vi.fn().mockRejectedValue(new Error('no disk')),
    listAudit: vi.fn().mockResolvedValue([]),
  }
})

beforeEach(() => {
  try {
    localStorage.clear()
  } catch {
    // jsdom without storage: nothing to reset.
  }
})
afterEach(cleanup)

const mk = (o: Partial<AgentStatus> & { id: string }) =>
  toRow({ project: '/home/u/alpha', status: 'idle', updatedAt: new Date().toISOString(), ...o })

const agents = [
  mk({ id: 'ask', name: 'ask-agent', origin: 'cli', pending: { kind: 'approval', id: 'tc1', params: { command: 'rm -rf build' } } }),
  mk({ id: 'run', name: 'run-agent', status: 'running', activity: 'editing main.go' }),
  mk({ id: 'done', name: 'done-agent', changedFiles: 3, branch: 'feat/x' }),
]
const intake: PendingSubmission = { id: 'p1', origin: 'mcp', title: 'Plan X', repoId: 'repo', createdAt: '2024-05-04T09:00:00Z', expiresAt: '2024-06-01T00:00:00Z' }

function mount(onRefreshPending = vi.fn()) {
  render(Home, { agents, pending: [intake], onRefreshPending, onOpenAgent: () => {}, onNavigate: () => {} })
  return { onRefreshPending }
}

describe('Home', () => {
  it('renders each inbox section from the fixture', () => {
    mount()
    expect(screen.getByText(/Needs you · 2/)).toBeTruthy()
    expect(screen.getByText('rm -rf build')).toBeTruthy()
    expect(screen.getByText('Plan X')).toBeTruthy()
    expect(screen.getByText(/Ready to ship · 1/)).toBeTruthy()
    expect(screen.getByText(/feat\/x · 3 files/)).toBeTruthy()
    expect(screen.getByText(/Running · 1/)).toBeTruthy()
    expect(screen.getByText('editing main.go')).toBeTruthy()
  })

  it('approves a permission through the permissions API', async () => {
    const { onRefreshPending } = mount()
    await userEvent.click(screen.getAllByRole('button', { name: 'Approve' })[1])
    expect(api.resolvePermission).toHaveBeenCalledWith('tc1', { approved: true })
    expect(onRefreshPending).toHaveBeenCalled()
  })

  it('denies an intake request through the pending API', async () => {
    mount()
    await userEvent.click(screen.getAllByRole('button', { name: 'Deny' })[0])
    expect(api.denyPending).toHaveBeenCalledWith('p1')
  })

  it('persists the Mine/Everyone choice', async () => {
    mount()
    await userEvent.click(screen.getByRole('button', { name: 'Everyone' }))
    expect(localStorage.getItem('marshal.ui.inbox.scope')).toBe('everyone')
  })

  it('offers Undo on a reroute notice and dismisses it', async () => {
    const onDismissNotice = vi.fn()
    const notices = [{ id: 'r1', watch: 'w', role: 'reviewer', from: 'big', to: 'small' }]
    render(Home, { agents, pending: [], notices, onDismissNotice, onRefreshPending: () => {}, onOpenAgent: () => {}, onNavigate: () => {} })
    await userEvent.click(screen.getByRole('button', { name: 'Undo' }))
    expect(api.undoReroute).toHaveBeenCalledWith('r1')
    expect(onDismissNotice).toHaveBeenCalledWith('r1')
  })

  it('dismisses a reroute notice whose undo the bridge refuses with 409', async () => {
    ;(api.undoReroute as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new api.APIError(409, { error: 'the reviewer binding changed since' }))
    const onDismissNotice = vi.fn()
    const notices = [{ id: 'r1', watch: 'w', role: 'reviewer', from: 'big', to: 'small' }]
    render(Home, { agents, pending: [], notices, onDismissNotice, onRefreshPending: () => {}, onOpenAgent: () => {}, onNavigate: () => {} })
    await userEvent.click(screen.getByRole('button', { name: 'Undo' }))
    expect(await screen.findByText('the reviewer binding changed since')).toBeTruthy()
    expect(onDismissNotice).toHaveBeenCalledWith('r1')
  })
})

describe('Home network decisions', () => {
  const block = (agentId: string, host: string, at: number, workspace = 'go-dev') => ({ kind: 'network_block' as const, sessionId: agentId, agentId, host, workspace, at })

  function mountWithStore() {
    const { state, actions } = createFleetStore()
    actions.applyDelta(block('run', 'later.example', 20))
    actions.applyDelta(block('run', 'first.example', 10))
    const onDecide = vi.fn(async (item: { agentId: string; host: string }, d: Parameters<typeof actions.decideNetwork>[2]) => {
      await actions.decideNetwork(item.agentId, item.host, d)
    })
    render(Home, { agents, pending: [], decisions: get(state).decisions, onDecide, onRefreshPending: () => {}, onOpenAgent: () => {}, onNavigate: () => {} })
    return { state, onDecide }
  }

  it('lists decisions in Needs you, oldest first, counted with the rest', () => {
    mountWithStore()
    expect(screen.getByText(/Needs you · 3$/)).toBeTruthy()
    const rows = screen.getAllByTestId('network-decision')
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('first.example'),
      expect.stringContaining('later.example'),
    ])
    expect(rows[0].textContent).toContain('run-agent tried to reach first.example')
    expect(rows[0].textContent).toContain('workspace go-dev')
  })

  it.each([
    ['Block', 'block'],
    ['Allow for this agent', 'allow-agent'],
    ['Add to workspace', 'add-to-workspace'],
  ])('%s posts the %s decision for that agent and host', async (label, decision) => {
    mountWithStore()
    await userEvent.click(screen.getAllByRole('button', { name: label })[0])
    expect(api.postNetworkDecision).toHaveBeenCalledWith('run', 'first.example', decision)
  })

  it('keeps the request and shows the reason when the post fails', async () => {
    ;(api.postNetworkDecision as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new api.APIError(409, { error: 'agent is not behind the egress proxy' }))
    mountWithStore()
    await userEvent.click(screen.getAllByRole('button', { name: 'Allow for this agent' })[0])
    expect((await screen.findByRole('alert')).textContent).toBe('agent is not behind the egress proxy')
    expect(screen.getAllByTestId('network-decision')).toHaveLength(2)
  })

  it('maps add-to-workspace results to a draft toast or a patch modal', () => {
    const item = { agentId: 'run', host: 'h.example', workspace: 'go-dev', at: 1 }
    expect(outcomeFor(item, 'block', {})).toBeNull()
    expect(outcomeFor(item, 'allow-agent', { ok: true })).toBeNull()
    expect(outcomeFor(item, 'add-to-workspace', { ok: true })).toEqual({ kind: 'draft', workspace: 'go-dev', host: 'h.example' })
    expect(outcomeFor(item, 'add-to-workspace', { patch: '+egress = ["h.example"]' })).toEqual({ kind: 'patch', workspace: 'go-dev', host: 'h.example', patch: '+egress = ["h.example"]' })
  })
})
