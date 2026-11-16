import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import WorkItemCard from './WorkItemCard.svelte'
import type { FactoryCheck, FactoryConfig, FactoryDecision, FactoryItem, FactoryPR, FactoryRound, FactoryWorker } from '../factory'

afterEach(cleanup)

/**
 * The card reads a factory item's state, stage, PRs, latest round and
 * workers, so the fixture is a whole item: the wire always sends one.
 */
const config: FactoryConfig = {
  repoId: 'acme/widgets',
  projectRoot: '/w/alpha',
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

function item(x: Partial<FactoryItem> & { id: string }): FactoryItem {
  const title = `Work on ${x.id}`
  return {
    title,
    state: 'implementing',
    stage: 'implement',
    repoId: 'acme/widgets',
    projectRoot: '/w/alpha',
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

const check = (name: string, conclusion: string): FactoryCheck => ({
  key: { kind: 'check_run', name, appId: 1 },
  id: name,
  sha: 'head1',
  status: 'completed',
  conclusion,
  url: '',
  logTail: '',
  observedAt: '2026-11-02T00:00:00Z',
})

const round = (checks: FactoryCheck[]): FactoryRound => ({
  number: 1,
  approvalRevision: 1,
  implementEffectId: 'effect-1',
  baseSha: 'base0',
  headSha: 'head1',
  treeSha: 'tree1',
  ci: { headSha: 'head1', checks, complete: true, observedAt: '2026-11-02T00:00:00Z' },
  implementation: null,
  review: null,
  startedAt: '2026-11-01T00:00:00Z',
  finishedAt: '2026-11-02T00:00:00Z',
})

const pr = (number: number): FactoryPR => ({
  number,
  url: `https://forge.example/pr/${number}`,
  nodeId: `node-${number}`,
  headRef: 'factory/x',
  baseRef: 'main',
  headSha: 'head1',
  baseSha: 'base0',
  state: 'open',
  draft: false,
})

const worker = (agentId: string, role = 'implementer', parked = false): FactoryWorker => ({
  agentId,
  sessionId: agentId ? `session-${agentId}` : '',
  role,
  spawnEffectId: '',
  parked,
})

const decision = (id: string): FactoryDecision => ({
  id,
  kind: 'approval',
  question: 'Approve the plan?',
  reason: '',
  effectId: '',
  createdAt: '2026-11-01T00:00:00Z',
})

describe('WorkItemCard', () => {
  it('showsStateStagePRChecksAndWorkers', () => {
    render(WorkItemCard, {
      item: item({
        id: 'live',
        state: 'implementing',
        stage: 'review',
        prs: [pr(1), pr(2)],
        rounds: [round([check('build', 'success'), check('lint', 'failure'), check('e2e', 'failure'), check('queued', '')])],
        // The same worker twice is one agent; an empty ID is no agent at all.
        workers: [worker('w1', 'implementer'), worker('w1', 'reviewer'), worker('')],
      }),
      onNavigate: () => {},
    })

    // The backend string is shown as it reports it, not retitled by this card.
    expect(screen.getByTestId('work-item-state').textContent).toBe('implementing')
    expect(screen.getByTestId('work-item-stage').textContent).toBe('review')
    expect(screen.getByTestId('work-item-prs').textContent).toBe('2 PRs')
    // The latest round's checks, in C2's three groups.
    expect(screen.getByTestId('work-item-checks').textContent).toBe('2 failed · 1 passed · 1 other')
    expect(screen.getByTestId('work-item-workers').textContent).toBe('1 agent')
    // A live item is not estimated: no percentage and no workflow strip.
    expect(screen.queryByText(/%/)).toBeNull()
  })

  it('usesDirectDraftDecisionReadyActions', async () => {
    const onNavigate = vi.fn()

    const draft = render(WorkItemCard, { item: item({ id: 'draft', state: 'draft', stage: '' }), onNavigate })
    await fireEvent.click(screen.getByRole('button', { name: 'Review draft' }))
    expect(onNavigate).toHaveBeenLastCalledWith('#factory/draft?view=approval')
    draft.unmount()

    // A decision outranks everything else: it is what blocks the item.
    const decided = render(WorkItemCard, {
      item: item({ id: 'decided', state: 'waiting', decision: decision('d1') }),
      onNavigate,
    })
    expect(screen.getByText('Approve the plan?')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Resolve decision' }))
    expect(onNavigate).toHaveBeenLastCalledWith('#factory/decided?view=activity')
    decided.unmount()

    const ready = render(WorkItemCard, { item: item({ id: 'ready', state: 'ready' }), onNavigate })
    await fireEvent.click(screen.getByRole('button', { name: 'Review work' }))
    expect(onNavigate).toHaveBeenLastCalledWith('#factory/ready?view=review')
    ready.unmount()

    // Everything else just opens the item's activity.
    render(WorkItemCard, { item: item({ id: 'live', state: 'checking' }), onNavigate })
    await fireEvent.click(screen.getByRole('button', { name: 'Open item' }))
    expect(onNavigate).toHaveBeenLastCalledWith('#factory/live?view=activity')
  })

  it('handlesUnknownStateAndLongContent', () => {
    const title = `${'A factory work item title that keeps going '.repeat(6)}and ends here`
    render(WorkItemCard, {
      item: item({ id: 'odd', title, state: 'quantum', stage: '' }),
      onNavigate: () => {},
    })

    // A state this build does not know is shown verbatim, and it is still openable.
    expect(screen.getByTestId('work-item-state').textContent).toBe('quantum')
    expect(screen.getByTestId('work-item-stage').textContent).toBe('none')
    expect(screen.getByTestId('work-item-checks').textContent).toBe('No checks yet')
    expect(screen.getByTestId('work-item-prs').textContent).toBe('No PRs')
    expect(screen.getByTestId('work-item-workers').textContent).toBe('No agents')
    expect(screen.getByRole('button', { name: 'Open item' })).toBeTruthy()

    // The whole title is rendered, and it wraps instead of being clipped.
    const rendered = screen.getByText(title)
    expect(rendered.textContent).toBe(title)
    expect(rendered.className).toContain('break-words')
    expect(rendered.className).not.toContain('truncate')
  })
})
