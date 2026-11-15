import { describe, expect, it } from 'vitest'
import type { FactoryCheck, FactoryConfig, FactoryDecision, FactoryEvent, FactoryItem, FactoryItemView, FactoryRound, FactoryWorker } from '../factory'
import {
  defaultFactoryItemView,
  factoryItemRoute,
  factoryWorkerIds,
  filterFactoryItems,
  isFactoryActive,
  isIndependentAgent,
  partitionFactoryChecks,
  sortedFactoryHistory,
  summarizeFactoryProject,
} from './model'

/**
 * Complete fixture items: the projections read `projectRoot`, `state`,
 * `updatedAt`, `decision`, `workers` and `history`, but a factory item is
 * always whole on the wire, so the fixtures are whole too and read as data.
 */
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

const worker = (agentId: string, role = 'implementer'): FactoryWorker => ({
  agentId,
  sessionId: agentId ? `session-${agentId}` : '',
  role,
  spawnEffectId: '',
  parked: false,
})

const decision = (id: string, kind = 'approval'): FactoryDecision => ({
  id,
  kind,
  question: 'Approve the plan?',
  reason: '',
  effectId: '',
  createdAt: '2026-11-01T00:00:00Z',
})

const check = (name: string, conclusion: string, status = 'completed'): FactoryCheck => ({
  key: { kind: 'check_run', name, appId: 1 },
  id: name,
  sha: 'head1',
  status,
  conclusion,
  url: '',
  logTail: '',
  observedAt: '2026-11-02T00:00:00Z',
})

function round(checks: FactoryCheck[] | null, number = 1): FactoryRound {
  return {
    number,
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
  }
}

const event = (sequence: number, message: string): FactoryEvent => ({
  sequence,
  at: '2026-11-02T00:00:00Z',
  kind: 'state',
  message,
  effectId: '',
})

describe('filterFactoryItems', () => {
  const items = [
    item({ id: 'draft', state: 'draft', updatedAt: '2026-11-05T00:00:00Z' }),
    item({ id: 'decided', state: 'waiting', decision: decision('d1'), updatedAt: '2026-11-04T00:00:00Z' }),
    item({ id: 'ready', state: 'ready', updatedAt: '2026-11-09T00:00:00Z' }),
    item({ id: 'mystery', state: 'quantum', updatedAt: 'not-a-date' }),
    item({ id: 'closed', state: 'closed', updatedAt: '2026-11-07T00:00:00Z' }),
    item({ id: 'stopped', state: 'stopped', updatedAt: '2026-11-08T00:00:00Z' }),
    item({ id: 'working', state: 'implementing', updatedAt: '2026-11-06T00:00:00Z' }),
    item({ id: 'also-working', state: 'implementing', updatedAt: '2026-11-06T00:00:00Z' }),
  ]
  const ids = (list: FactoryItem[]) => list.map((i) => i.id)
  const order = items.map((i) => i.id)

  it('filtersAndSortsFactoryItems', () => {
    // Needs-you first, ready second, then newest, and an unparsable timestamp sorts as zero.
    expect(ids(filterFactoryItems(items))).toEqual(['draft', 'decided', 'ready', 'also-working', 'working', 'mystery'])
    expect(ids(filterFactoryItems(items, 'current'))).toEqual(['draft', 'decided', 'ready', 'also-working', 'working', 'mystery'])
    expect(ids(filterFactoryItems(items, 'needs-you'))).toEqual(['draft', 'decided'])
    expect(ids(filterFactoryItems(items, 'ready'))).toEqual(['ready'])
    expect(ids(filterFactoryItems(items, 'history'))).toEqual(['stopped', 'closed'])
    // An unknown filter is the current list, not an empty one.
    expect(ids(filterFactoryItems(items, 'bogus' as never))).toEqual(['draft', 'decided', 'ready', 'also-working', 'working', 'mystery'])
  })

  it('never reorders the caller array', () => {
    const result = filterFactoryItems(items)
    expect(result).not.toBe(items)
    expect(items.map((i) => i.id)).toEqual(order)
    expect(result[0]).toBe(items[0])
  })

  it('keeps polling only while the state is live', () => {
    expect(items.filter(isFactoryActive).map((i) => i.id)).toEqual(['decided', 'mystery', 'working', 'also-working'])
    expect(isFactoryActive(item({ id: 'a', state: 'needs_input' }))).toBe(false)
    expect(isFactoryActive(item({ id: 'b', state: 'unknown-state' }))).toBe(true)
  })
})

describe('factory workers', () => {
  const items = [
    item({ id: 'live', state: 'implementing', workers: [worker('w1'), worker('w1', 'reviewer'), worker('')] }),
    item({ id: 'other-live', state: 'checking', workers: [worker('w2')] }),
    item({ id: 'retired', state: 'closed', workers: [worker('w3')] }),
  ]

  it('excludesHistoricalFactoryWorkersFromIndependentAgents', () => {
    // Every item by default: a worker on a closed item is still a factory worker,
    // so the fleet does not show its agent twice.
    expect(factoryWorkerIds(items)).toEqual(new Set(['w1', 'w2', 'w3']))
    // Current only: the closed item's worker is history.
    expect(factoryWorkerIds(items, true)).toEqual(new Set(['w1', 'w2']))
    expect(isIndependentAgent('w1', items)).toBe(false)
    expect(isIndependentAgent('w3', items)).toBe(false)
    expect(isIndependentAgent('solo', items)).toBe(true)
    // An empty ID is no agent at all, so it is never an independent one.
    expect(isIndependentAgent('', items)).toBe(false)
    expect(factoryWorkerIds([])).toEqual(new Set())
    expect([...factoryWorkerIds(items)].length).toBe(3)
  })
})

describe('summarizeFactoryProject', () => {
  const items = [
    item({ id: 'draft', state: 'draft', updatedAt: '2026-11-02T00:00:00Z', workers: [worker('w1'), worker('')] }),
    item({ id: 'decided', state: 'waiting', updatedAt: '2026-11-03T00:00:00Z', decision: decision('d2'), workers: [worker('w2')] }),
    item({ id: 'ready', state: 'ready', updatedAt: '2026-11-04T00:00:00Z' }),
    item({ id: 'closed', state: 'closed', updatedAt: '2026-11-05T00:00:00Z', workers: [worker('w3')] }),
    // A sibling directory that merely shares the prefix is a different project.
    item({ id: 'prefixed', state: 'closed', projectRoot: '/home/u/alpha-two', workers: [worker('w4')] }),
    item({ id: 'nested', state: 'implementing', projectRoot: '/home/u/alpha/', workers: [worker('w5')] }),
  ]

  it('summarizesCurrentProjectWork', () => {
    const summary = summarizeFactoryProject('/home/u/alpha', items)
    expect(summary.root).toBe('/home/u/alpha')
    // Every item of the project, in the shared sort order, closed ones included.
    expect(summary.items.map((i) => i.id)).toEqual(['decided', 'draft', 'ready', 'closed'])
    expect(summary.current).toBe(3)
    expect(summary.needsYou).toBe(2)
    expect(summary.ready).toBe(1)
    // Linked agents come from the current items only, unique and without an empty ID.
    expect(summary.linkedAgentIds).toEqual(['w1', 'w2'])
  })

  it('keepsProjectsWithNoFactoryItemsEmpty', () => {
    expect(summarizeFactoryProject('/home/u/empty', items)).toEqual({
      root: '/home/u/empty',
      items: [],
      current: 0,
      needsYou: 0,
      ready: 0,
      linkedAgentIds: [],
    })
  })
})

describe('defaultFactoryItemView', () => {
  it('resolvesInitialAndRequestedItemViews', () => {
    expect(defaultFactoryItemView(item({ id: 'a', state: 'draft' }))).toBe('approval')
    expect(defaultFactoryItemView(item({ id: 'b', state: 'ready' }))).toBe('review')
    expect(defaultFactoryItemView(item({ id: 'c', state: 'implementing' }))).toBe('activity')
    expect(defaultFactoryItemView(item({ id: 'd', state: 'needs_input', decision: decision('d3') }))).toBe('activity')
    expect(defaultFactoryItemView(item({ id: 'e', state: 'quantum' }))).toBe('activity')
    // A requested view is honoured on any state…
    expect(defaultFactoryItemView(item({ id: 'f', state: 'implementing' }), 'review')).toBe('review')
    expect(defaultFactoryItemView(item({ id: 'g', state: 'closed' }), 'prs')).toBe('prs')
    expect(defaultFactoryItemView(item({ id: 'h', state: 'draft' }), 'approval')).toBe('approval')
    // …except approval on something already approved, which shows the activity instead.
    expect(defaultFactoryItemView(item({ id: 'i', state: 'ready' }), 'approval')).toBe('activity')
    expect(defaultFactoryItemView(item({ id: 'j', state: 'waiting', decision: decision('d4') }), 'approval')).toBe('activity')
    // A request this build does not know falls back to the derived view.
    expect(defaultFactoryItemView(item({ id: 'k', state: 'ready' }), 'history' as unknown as FactoryItemView)).toBe('review')
  })
})

describe('factoryItemRoute', () => {
  it('buildsDirectItemRoutes', () => {
    expect(factoryItemRoute(item({ id: 'draft', state: 'draft' }))).toBe('#factory/draft?view=approval')
    // Approving comes first: a draft with an open decision still lands on approval.
    expect(factoryItemRoute(item({ id: 'draft-decided', state: 'draft', decision: decision('d5') }))).toBe('#factory/draft-decided?view=approval')
    // A decision outranks a ready review: it is the thing blocking the item.
    expect(factoryItemRoute(item({ id: 'ready-decided', state: 'ready', decision: decision('d6') }))).toBe('#factory/ready-decided?view=activity')
    expect(factoryItemRoute(item({ id: 'ready', state: 'ready' }))).toBe('#factory/ready?view=review')
    expect(factoryItemRoute(item({ id: 'closed', state: 'closed' }))).toBe('#factory/closed?view=activity')
    // IDs are encoded by the route formatter, not by hand.
    expect(factoryItemRoute(item({ id: 'a/b', state: 'implementing' }))).toBe('#factory/a%2Fb?view=activity')
  })
})

describe('partitionFactoryChecks', () => {
  it('partitionsChecksWithoutLosingPendingChecks', () => {
    const checks = [
      check('build', 'success'),
      check('lint', 'failure'),
      check('scan', 'neutral'),
      check('docs', 'skipped'),
      check('queued', '', 'in_progress'),
      check('deploy', 'cancelled'),
      check('e2e', 'timed_out'),
    ]
    const groups = partitionFactoryChecks(round(checks))
    // Server order within each group.
    expect(groups.failed.map((c) => c.id)).toEqual(['lint', 'deploy', 'e2e'])
    expect(groups.passed.map((c) => c.id)).toEqual(['build'])
    // A check with no conclusion yet is unfinished, not failed, so it stays here.
    expect(groups.other.map((c) => c.id)).toEqual(['scan', 'docs', 'queued'])
    // Nothing is dropped or duplicated.
    expect([...groups.failed, ...groups.passed, ...groups.other].map((c) => c.id).sort()).toEqual(checks.map((c) => c.id).sort())
  })

  it('returns empty groups for a missing, incomplete or null check list', () => {
    expect(partitionFactoryChecks()).toEqual({ failed: [], passed: [], other: [] })
    expect(partitionFactoryChecks(round(null))).toEqual({ failed: [], passed: [], other: [] })
    const noCI: FactoryRound = { ...round([]), ci: null }
    expect(partitionFactoryChecks(noCI)).toEqual({ failed: [], passed: [], other: [] })
  })
})

describe('sortedFactoryHistory', () => {
  const history = [event(3, 'reviewed'), event(1, 'created'), event(2, 'implemented'), event(1, 'approved')]

  it('sortsHistoryStablyBySequence', () => {
    expect(sortedFactoryHistory(item({ id: 'a', history })).map((e) => e.message)).toEqual(['created', 'approved', 'implemented', 'reviewed'])
    // The server's order breaks a tie, and the item's own array is untouched.
    expect(history.map((e) => e.message)).toEqual(['reviewed', 'created', 'implemented', 'approved'])
    expect(sortedFactoryHistory(item({ id: 'b' }))).toEqual([])
    expect(sortedFactoryHistory(item({ id: 'c', history: [] }))).toEqual([])
  })
})
