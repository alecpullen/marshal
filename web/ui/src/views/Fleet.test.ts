import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import Fleet from './Fleet.svelte'
import * as api from '../lib/api.js'
import { toRow, type AgentRow, type NetworkDecisionItem } from '../lib/fleet'
import type { FactoryState } from '../lib/factoryStore'
import type { FactoryConfig, FactoryDecision, FactoryItem, FactoryWorker } from '../lib/factory'
import { factoryItemRoute } from '../lib/studio/model'
import { formatAutomationRoute } from '../lib/routes'
import type { AgentStatus, Inbox, NetDecisionKind, PendingSubmission, ProjectStatus } from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    resolvePermission: vi.fn().mockResolvedValue(undefined),
    approvePending: vi.fn().mockResolvedValue({ agentId: 'x', status: 'ok' }),
    denyPending: vi.fn().mockResolvedValue(undefined),
    undoReroute: vi.fn().mockResolvedValue(undefined),
    postNetworkDecision: vi.fn().mockResolvedValue({ ok: true }),
    listReviewDrafts: vi.fn().mockResolvedValue([]),
    listCIHistory: vi.fn().mockResolvedValue([]),
  }
})

beforeEach(() => {
  vi.mocked(api.listReviewDrafts).mockResolvedValue([] as never)
  vi.mocked(api.listCIHistory).mockResolvedValue([] as never)
})
afterEach(cleanup)

/* ---------------------------------------------------------------- fixtures */

const config: FactoryConfig = {
  repoId: 'acme/widgets',
  projectRoot: '/home/u/alpha',
  hostId: 'local',
  targetBranch: 'main',
  workspace: 'default',
  native: false,
  implementerRouting: {},
  reviewerRouting: {},
  reviewerInstructions: '',
  implementerAuto: false,
  expectedChecks: [],
  noRequiredCiAcknowledged: false,
  maxRounds: 3,
  ciPollSeconds: 30,
  ciWaitSeconds: 600,
}

/** A whole factory item: the projections read its root, state, decision and workers. */
function item(x: Partial<FactoryItem> & { id: string }): FactoryItem {
  const title = `Work on ${x.id}`
  return {
    title,
    state: 'implementing',
    stage: 'implement',
    repoId: 'acme/widgets',
    projectRoot: '/home/u/alpha',
    hostId: 'local',
    branch: `factory/${x.id}`,
    baseSha: 'base0',
    version: 1,
    draft: { requestId: `req-${x.id}`, title, plan: '', spec: '', config },
    approvals: null,
    grants: null,
    rounds: null,
    workers: null,
    prs: null,
    effects: null,
    decision: null,
    history: null,
    createdAt: '2026-11-01T00:00:00Z',
    approvedAt: '',
    updatedAt: '2026-11-01T00:00:00Z',
    ...x,
  }
}

const decision = (id: string): FactoryDecision => ({
  id,
  kind: 'approval',
  question: 'Approve the plan?',
  reason: '',
  effectId: '',
  createdAt: '2026-11-01T00:00:00Z',
})

const worker = (agentId: string): FactoryWorker => ({
  agentId,
  sessionId: `session-${agentId}`,
  role: 'implementer',
  spawnEffectId: '',
  parked: false,
})

const mk = (o: Partial<AgentStatus> & { id: string }) =>
  toRow({ project: '/home/u/alpha', status: 'idle', updatedAt: '2026-11-01T00:00:00Z', ...o })

const emptyInbox: Inbox = { groups: { needsYou: [], running: [], ready: [], earlier: [] }, queue: [] }

const factoryState = (over: Partial<FactoryState> = {}): FactoryState => ({
  items: [],
  loading: false,
  refreshing: false,
  error: null,
  lastUpdated: null,
  ...over,
})

const project = (root: string, over: Partial<ProjectStatus> = {}): ProjectStatus => ({
  root,
  available: true,
  trust: 'trusted',
  isolation: 'available',
  ...over,
})

type FleetProps = {
  agents: AgentRow[]
  inbox: Inbox
  decisions?: NetworkDecisionItem[]
  projects?: ProjectStatus[]
  automationTick?: number
  factory: FactoryState
  onRefreshPending: () => void
  onOpenAgent: (id: string) => void
  onNavigate: (hash: string) => void
  onRefreshFactory: () => void
  onRefreshFleet: () => void
  onDecide?: (item: NetworkDecisionItem, decision: NetDecisionKind) => Promise<void>
}

function mount(over: Partial<FleetProps> = {}) {
  const props: FleetProps = {
    agents: [],
    inbox: emptyInbox,
    factory: factoryState(),
    onRefreshPending: () => {},
    onOpenAgent: () => {},
    onNavigate: () => {},
    onRefreshFactory: () => {},
    onRefreshFleet: () => {},
    ...over,
  }
  return render(Fleet, props)
}

/* ------------------------------------------------------------------- tests */

describe('Fleet attention', () => {
  it('combinesFactoryAgentIntakeAndNetworkAttention', () => {
    const intake: PendingSubmission = {
      id: 'p1',
      auto: null,
      clientId: 'c1',
      mode: 'act',
      origin: 'mcp',
      plan: '',
      prompt: '',
      ref: '',
      title: 'Plan X',
      repoId: 'acme/widgets',
      createdAt: '2026-11-01T00:00:00Z',
      expiresAt: '2026-12-01T00:00:00Z',
    }
    const agents = [mk({ id: 'ask', name: 'ask-agent', pending: { kind: 'approval', id: 'tc1', params: { command: 'rm -rf build' } } })]
    const inbox: Inbox = {
      groups: { needsYou: ['ask'], running: [], ready: [], earlier: [] },
      queue: [
        { kind: 'intake', submission: intake, at: '2026-11-01T09:00:00Z' },
        { kind: 'agent', agentId: 'ask', at: '2026-11-01T10:00:00Z' },
      ],
    }

    mount({
      agents,
      inbox,
      decisions: [{ agentId: 'ask', host: 'api.example', workspace: 'go-dev', at: 1 }],
      onDecide: async () => {},
      factory: factoryState({
        items: [
          item({ id: 'f1', decision: decision('d1') }),
          item({ id: 'f2', state: 'draft', updatedAt: '2026-11-02T00:00:00Z' }),
        ],
      }),
    })

    // Two factory entries, the agent request, the intake submission and the blocked request.
    expect(screen.getByText(/Needs you · 5/)).toBeTruthy()
    const cards = screen.getAllByTestId('attention-card')
    expect(cards).toHaveLength(5)
    const sources = (label: string) => cards.filter((c) => c.textContent?.includes(label)).length
    expect(sources('Factory')).toBe(2)
    expect(sources('Agent')).toBe(1)
    expect(sources('Intake')).toBe(1)
    expect(sources('Network')).toBe(1)
    expect(screen.getByText('Approve the plan?')).toBeTruthy()
    expect(screen.getByTestId('network-decision')).toBeTruthy()
  })

  it('keepsAgentPendingRequestsDistinctFromFactoryDecisions', () => {
    const agents = [mk({ id: 'ask', name: 'ask-agent', pending: { kind: 'approval', id: 'tc1', params: { command: 'go test ./...' } } })]
    const inbox: Inbox = {
      groups: { needsYou: ['ask'], running: [], ready: [], earlier: [] },
      queue: [{ kind: 'agent', agentId: 'ask', at: '2026-11-01T10:00:00Z' }],
    }

    mount({
      agents,
      inbox,
      factory: factoryState({ items: [item({ id: 'f1', decision: decision('d1') })] }),
    })

    // The agent's request and the factory's decision are separate rows, both kept.
    expect(screen.getByText(/Needs you · 2/)).toBeTruthy()
    const cards = screen.getAllByTestId('attention-card')
    expect(cards).toHaveLength(2)
    expect(cards.some((c) => c.textContent?.includes('Agent') && c.textContent?.includes('go test ./...'))).toBe(true)
    expect(cards.some((c) => c.textContent?.includes('Factory') && c.textContent?.includes('Approve the plan?'))).toBe(true)
  })

  it('linksDraftDecisionAndReadyItemsToTheirViews', async () => {
    const draft = item({ id: 'f1', state: 'draft' })
    const waiting = item({ id: 'f2', decision: decision('d1'), updatedAt: '2026-11-03T00:00:00Z' })
    const ready = item({ id: 'f3', state: 'ready', updatedAt: '2026-11-02T00:00:00Z' })
    const reviewDraft = {
      id: 'd1',
      repoId: 'r1',
      number: 12,
      headSha: 'abc',
      agentId: 'rv',
      summary: '',
      status: 'draft',
      createdAt: '2026-11-05T00:00:00Z',
      findings: [],
    }
    vi.mocked(api.listReviewDrafts).mockResolvedValue([reviewDraft] as never)

    const onNavigate = vi.fn()
    mount({
      onNavigate,
      projects: [project('/home/u/alpha')],
      factory: factoryState({ items: [draft, waiting, ready], lastUpdated: 1 }),
    })

    // A draft opens its approval tab, an open decision its activity feed.
    await userEvent.click(screen.getByRole('button', { name: 'Review draft' }))
    expect(onNavigate).toHaveBeenLastCalledWith(factoryItemRoute(draft))
    await userEvent.click(screen.getByRole('button', { name: 'Decide' }))
    expect(onNavigate).toHaveBeenLastCalledWith(factoryItemRoute(waiting))
    // Ready work opens its review tab.
    await userEvent.click(screen.getByRole('button', { name: 'Review work' }))
    expect(onNavigate).toHaveBeenLastCalledWith(factoryItemRoute(ready))
    // A review draft opens its automations page.
    await userEvent.click(await screen.findByRole('button', { name: 'Open draft' }))
    expect(onNavigate).toHaveBeenLastCalledWith(formatAutomationRoute('/home/u/alpha', 'review-bot', 'd1'))
  })
})

describe('Fleet projects and agents', () => {
  it('summarizesEveryProjectAndFactoryOnlyRoot', () => {
    mount({
      projects: [project('/w/alpha')],
      factory: factoryState({
        items: [
          item({ id: 'a1', projectRoot: '/w/alpha', state: 'draft' }),
          item({ id: 'b1', projectRoot: '/w/beta', state: 'ready', updatedAt: '2026-11-02T00:00:00Z' }),
        ],
      }),
    })

    const cards = screen.getAllByTestId('project-factory-card')
    expect(cards).toHaveLength(2)
    // Registered project first, by short name; the factory-only root follows and keeps a card.
    expect(within(cards[0]).getByText('alpha')).toBeTruthy()
    expect(within(cards[0]).queryByText('Project not registered')).toBeNull()
    expect(within(cards[0]).getByTestId('count-current').textContent).toBe('1')
    expect(within(cards[1]).getByText('beta')).toBeTruthy()
    expect(within(cards[1]).getByText('Project not registered')).toBeTruthy()
    expect(within(cards[1]).getByTestId('count-ready').textContent).toBe('1')
  })

  it('excludesFactoryWorkersFromIndependentAgents', () => {
    const agents = [
      mk({ id: 'w1', name: 'w1-agent', status: 'running', isolated: true }),
      mk({ id: 'w2', name: 'w2-agent' }),
      mk({ id: 'free', name: 'free-agent' }),
    ]
    mount({
      agents,
      factory: factoryState({
        items: [
          item({ id: 'f1', workers: [worker('w1')] }),
          // History counts too: an old factory session is still a factory worker.
          item({ id: 'f2', state: 'closed', workers: [worker('w2')], updatedAt: '2026-11-02T00:00:00Z' }),
        ],
      }),
    })

    const list = screen.getByTestId('independent-agents')
    expect(within(list).getByText('free-agent')).toBeTruthy()
    expect(within(list).queryByText('w1-agent')).toBeNull()
    expect(within(list).queryByText('w2-agent')).toBeNull()
    expect(screen.getByText(/Independent agents · 1/)).toBeTruthy()
  })
})

describe('Fleet loading and failures', () => {
  it('showsEmptyOnlyAfterAuthoritativeLoads', async () => {
    const { rerender } = mount({ factory: factoryState({ loading: true }) })

    // A first factory read says nothing yet, so no empty claim is made.
    expect(screen.queryByText('Nothing needs you')).toBeNull()
    expect(screen.getByTestId('factory-loading')).toBeTruthy()

    await rerender({ factory: factoryState({ loading: false }) })
    expect(await screen.findByText('Nothing needs you')).toBeTruthy()
    expect(screen.getByRole('navigation', { name: 'Fleet resources' })).toBeTruthy()
  })

  it('retainsFleetContentWhenFactoryRefreshFails', () => {
    mount({
      factory: factoryState({
        items: [item({ id: 'f1', decision: decision('d1') })],
        error: 'factory is unreachable',
        lastUpdated: Date.parse('2026-11-05T10:00:00Z'),
      }),
    })

    // The failed read is reported, and what the last good snapshot held still stands.
    expect(screen.getByTestId('factory-error').textContent).toContain('factory is unreachable')
    expect(screen.getByText(/Showing the last update from/)).toBeTruthy()
    expect(screen.getByText('Approve the plan?')).toBeTruthy()
    expect(screen.queryByText('Nothing needs you')).toBeNull()
  })

  it('showsPartialAutomationErrors', async () => {
    vi.mocked(api.listReviewDrafts).mockRejectedValueOnce(new api.APIError(500, { error: 'automations are down' }))
    const agents = [mk({ id: 'ask', name: 'ask-agent', pending: { kind: 'approval', id: 'tc1', params: { command: 'go test ./...' } } })]
    const inbox: Inbox = {
      groups: { needsYou: ['ask'], running: [], ready: [], earlier: [] },
      queue: [{ kind: 'agent', agentId: 'ask', at: '2026-11-01T10:00:00Z' }],
    }

    mount({ agents, inbox, projects: [project('/home/u/alpha')] })

    // One project's read failed; the rest of Fleet is untouched.
    const card = await screen.findByTestId('automation-error')
    expect(card.textContent).toContain('alpha · automations are down')
    expect(screen.getByText(/Needs you · 1/)).toBeTruthy()
    expect(screen.getAllByTestId('attention-card')[0].textContent).toContain('go test ./...')
  })

  it('preservesProjectWarnings', () => {
    mount({
      projects: [
        project('/w/gone', { available: false, error: 'dial unix /run/marshal.sock: no such file', trust: 'na' }),
        project('/w/new', { trust: 'untrusted' }),
        project('/w/orphan', { orphanWorktrees: ['/w/orphan/.marshal/worktrees/old'] }),
      ],
      agents: [
        mk({ id: 'a1', project: '/w/shared', status: 'running' }),
        mk({ id: 'a2', project: '/w/shared', status: 'running' }),
      ],
    })

    expect(screen.getByText('/w/gone is unavailable')).toBeTruthy()
    expect(screen.getByText('dial unix /run/marshal.sock: no such file')).toBeTruthy()
    expect(screen.getByText('/w/new is not trusted')).toBeTruthy()
    expect(screen.getByText('1 unclaimed worktree in /w/orphan')).toBeTruthy()
    expect(screen.getByText('2 non-isolated agents are active in /w/shared')).toBeTruthy()
  })
})
