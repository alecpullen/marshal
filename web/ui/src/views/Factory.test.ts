import { afterEach, beforeEach, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Factory from './Factory.svelte'
import * as api from '../lib/api'
import { toRow, type AgentRow } from '../lib/fleet'
import type { FactoryItem, FactoryWorker } from '../lib/factory'
import type { FactoryState } from '../lib/factoryStore'

vi.mock('../lib/api', async (actual) => ({
  ...await actual<typeof import('../lib/api')>(),
  listFactoryItems: vi.fn(),
  getFactoryItem: vi.fn(),
  getProjectSettings: vi.fn(),
}))

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {} })
})

/** One item with the wire shape's required fields; each test overrides what it asserts. */
const item = (id: string, over: Partial<FactoryItem> = {}): FactoryItem => ({
  id,
  title: id,
  state: 'running',
  stage: '',
  repoId: 'repo',
  projectRoot: '/repo/a',
  hostId: 'host',
  branch: `factory/${id}`,
  baseSha: '',
  version: 1,
  draft: { requestId: id, title: id, plan: '', spec: '', config: {} as FactoryItem['draft']['config'] },
  approvals: null,
  grants: null,
  rounds: null,
  workers: null,
  prs: null,
  effects: null,
  decision: null,
  history: null,
  createdAt: '2026-01-01T00:00:00Z',
  approvedAt: '',
  updatedAt: '2026-01-01T00:00:00Z',
  ...over,
})

const worker = (agentId: string, role = 'implementer', parked = false): FactoryWorker => ({
  agentId,
  sessionId: `session-${agentId}`,
  role,
  spawnEffectId: '',
  parked,
})

const decision = (id: string) => ({ id, kind: 'approval', question: 'Approve the plan?', reason: '', effectId: '', createdAt: '2026-01-01T00:00:00Z' })

/** The shell store's projection, as App hands it to a view. */
const factoryState = (over: Partial<FactoryState> = {}): FactoryState => ({
  items: [],
  loading: false,
  refreshing: false,
  error: null,
  lastUpdated: null,
  ...over,
})

/** The shell's callback: the store owns the polling, so this is the only seam. */
const onRefresh = vi.fn()

const view = (state: FactoryState, extra: Record<string, unknown> = {}) =>
  render(Factory, { factory: state, onRefresh, onNavigate: vi.fn(), ...extra })

it('scopesItemsToProject', async () => {
  const items = [
    item('a', { title: 'Alpha work', projectRoot: '/repo/a' }),
    // A sibling that merely shares a prefix is a different project.
    item('b', { title: 'Sibling work', projectRoot: '/repo/ab' }),
    item('c', { title: 'Other work', projectRoot: '/repo/other' }),
  ]

  const scoped = view(factoryState({ items }), { projectRoot: '/repo/a' })
  expect(await screen.findByRole('heading', { name: 'Alpha work' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Sibling work' })).toBeNull()
  expect(screen.queryByRole('heading', { name: 'Other work' })).toBeNull()
  // The scoped page reads its own project's defaults and nothing else.
  expect(api.getProjectSettings).toHaveBeenCalledWith('/repo/a')
  scoped.unmount()

  // The global list is the same component with no scope.
  view(factoryState({ items }))
  expect(await screen.findByRole('heading', { name: 'Alpha work' })).toBeTruthy()
  expect(screen.getByRole('heading', { name: 'Sibling work' })).toBeTruthy()
  expect(screen.getByRole('heading', { name: 'Other work' })).toBeTruthy()
  expect(api.getProjectSettings).not.toHaveBeenCalledTimes(2)

  // Every scope and filter is a projection of the one snapshot: rendering a
  // list reads nothing, because the shell's store owns the only poller.
  expect(api.listFactoryItems).not.toHaveBeenCalled()
  expect(api.getFactoryItem).not.toHaveBeenCalled()
})

it('filtersCurrentNeedsYouReadyAndHistoryThroughRoutes', async () => {
  const items = [
    item('decided', { title: 'Decided work', decision: decision('d1'), updatedAt: '2026-01-04T00:00:00Z' }),
    item('ready', { title: 'Ready work', state: 'ready', updatedAt: '2026-01-03T00:00:00Z' }),
    item('live', { title: 'Running work', state: 'implementing', updatedAt: '2026-01-02T00:00:00Z' }),
    item('done', { title: 'Closed work', state: 'closed', updatedAt: '2026-01-01T00:00:00Z' }),
  ]

  // Each filter is a projection of the route's value.
  const scoped = view(factoryState({ items }), { projectRoot: '/repo/a' })
  expect(await screen.findByRole('heading', { name: 'Running work' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Closed work' })).toBeNull()
  scoped.unmount()

  const needsYou = view(factoryState({ items }), { projectRoot: '/repo/a', filter: 'needs-you' })
  expect(await screen.findByRole('heading', { name: 'Decided work' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Running work' })).toBeNull()
  needsYou.unmount()

  const ready = view(factoryState({ items }), { projectRoot: '/repo/a', filter: 'ready' })
  expect(await screen.findByRole('heading', { name: 'Ready work' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Decided work' })).toBeNull()
  ready.unmount()

  const history = view(factoryState({ items }), { projectRoot: '/repo/a', filter: 'history' })
  expect(await screen.findByRole('heading', { name: 'Closed work' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Running work' })).toBeNull()
  history.unmount()

  // The controls navigate, preserving the project scope; they hold no local
  // filter state, so clicking one cannot change this render's list.
  const onNavigate = vi.fn()
  view(factoryState({ items }), { projectRoot: '/repo/a', onNavigate })
  expect(await screen.findByRole('heading', { name: 'Running work' })).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Needs you' }))
  expect(onNavigate).toHaveBeenLastCalledWith('#projects/%2Frepo%2Fa/factory?filter=needs-you')
  await fireEvent.click(screen.getByRole('button', { name: 'Ready' }))
  expect(onNavigate).toHaveBeenLastCalledWith('#projects/%2Frepo%2Fa/factory?filter=ready')
  await fireEvent.click(screen.getByRole('button', { name: 'History' }))
  expect(onNavigate).toHaveBeenLastCalledWith('#projects/%2Frepo%2Fa/factory?filter=history')
  // The route is authoritative: the mounted list still shows what the prop says.
  expect(screen.getByRole('heading', { name: 'Running work' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Closed work' })).toBeNull()
  // The already-current filter navigates nowhere.
  onNavigate.mockClear()
  await fireEvent.click(screen.getByRole('button', { name: 'Current' }))
  expect(onNavigate).not.toHaveBeenCalled()
})

it('showsNeedsDecisionBeforeWorkItems', async () => {
  const items = [
    item('live', { title: 'Running work', state: 'implementing' }),
    item('decided', { title: 'Decided work', decision: decision('d1') }),
  ]
  view(factoryState({ items }), { projectRoot: '/repo/a' })

  const decisions = await screen.findByTestId('factory-decisions')
  const work = screen.getByTestId('factory-work-items')
  expect(within(decisions).getByText('Decided work')).toBeTruthy()
  expect(within(work).getByText('Running work')).toBeTruthy()
  // The decision leads the page…
  expect(decisions.compareDocumentPosition(work) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  // …and the same item is not listed a second time below it.
  expect(screen.getAllByText('Decided work')).toHaveLength(1)
  expect(within(work).queryByText('Decided work')).toBeNull()
})

it('loadsDefaultsWithoutBlockingItems', async () => {
  let release!: (settings: api.ProjectSettings) => void
  ;(api.getProjectSettings as Mock).mockImplementation(
    () => new Promise((resolve) => { release = resolve }),
  )
  const items = [item('a', { title: 'Alpha work', projectRoot: '/repo/a' })]
  const pending = view(factoryState({ items }), { projectRoot: '/repo/a' })

  // The work items render from the shell's snapshot before settings answer.
  expect(await screen.findByRole('heading', { name: 'Alpha work' })).toBeTruthy()
  expect(screen.queryByTestId('factory-defaults')).toBeNull()

  release({ workspace: 'go-dev', routing: { profile: 'fast' }, intake: {}, factoryDefaults: { targetBranch: 'develop', maxRounds: 4 } })
  const defaults = await screen.findByTestId('factory-defaults')
  expect(within(defaults).getByTestId('defaults-workspace').textContent).toContain('Following the project default: go-dev')
  expect(within(defaults).getByTestId('defaults-implementer').textContent).toContain('Following the project default: fast')
  expect(within(defaults).getByTestId('defaults-target-branch').textContent).toBe('develop')
  expect(within(defaults).getByTestId('defaults-rounds').textContent).toBe('4')
  expect(screen.getByRole('heading', { name: 'Alpha work' })).toBeTruthy()
  pending.unmount()

  // A failed settings read is partial: it says so and leaves the items usable.
  ;(api.getProjectSettings as Mock).mockRejectedValue(new Error('settings unavailable'))
  view(factoryState({ items }), { projectRoot: '/repo/a' })
  expect(await screen.findByTestId('defaults-error')).toBeTruthy()
  expect(screen.getByTestId('defaults-error').textContent).toContain('settings unavailable')
  expect(screen.getByRole('heading', { name: 'Alpha work' })).toBeTruthy()
})

it('listsUniqueCurrentLinkedAgents', async () => {
  const items = [
    // The same worker on two current items is one linked agent.
    item('one', { title: 'First work', state: 'implementing', workers: [worker('w1', 'implementer', true)] }),
    item('two', { title: 'Second work', state: 'checking', workers: [worker('w1', 'reviewer'), worker('w2')] }),
    // A closed item's worker is not current work.
    item('done', { title: 'Closed work', state: 'closed', workers: [worker('w3')] }),
  ]
  // The fleet knows w1 only: w2 must still be listed, without status text.
  const agents: AgentRow[] = [toRow({ id: 'w1', project: '/repo/a', name: 'impl-one', status: 'running', updatedAt: '2026-01-01T00:00:00Z' })]
  const linkedView = view(factoryState({ items }), { projectRoot: '/repo/a', agents })

  const linked = await screen.findByTestId('linked-agents')
  const hrefs = [...linked.querySelectorAll('a')].map((a) => a.getAttribute('href'))
  expect(hrefs).toEqual(['#chat/w1', '#chat/w2'])
  expect(linked.textContent).toContain('Linked agents · 2')
  // The parked marker and the row's status come from the fleet row…
  expect(linked.textContent).toContain('parked')
  expect(linked.textContent).toContain('running')
  // …and a worker with no row is listed anyway, by its own ID.
  expect(within(linked).getByText('w2')).toBeTruthy()
  expect(within(linked).queryByText('w3')).toBeNull()

  // The link opens that agent's chat rather than a factory route.
  const onNavigate = vi.fn()
  linkedView.unmount()
  view(factoryState({ items }), { projectRoot: '/repo/a', agents, onNavigate })
  await fireEvent.click(await screen.findByText('impl-one'))
  expect(onNavigate).toHaveBeenLastCalledWith('#chat/w1')
})

it('showsGlobalAndScopedEmptyStates', async () => {
  // The two empty claims are different, and neither is made about a scope
  // that has work in another filter.
  const global = view(factoryState({ items: [] }))
  expect(await screen.findByText('No factory work items yet.')).toBeTruthy()
  expect(screen.queryByText('No factory work items in this project yet.')).toBeNull()
  global.unmount()

  const scoped = view(factoryState({ items: [] }), { projectRoot: '/repo/a' })
  expect(await screen.findByText('No factory work items in this project yet.')).toBeTruthy()
  expect(screen.queryByText('No factory work items yet.')).toBeNull()
  scoped.unmount()

  // History with nothing in it is a filter, not an empty project.
  const filtered = view(factoryState({ items: [item('live', { title: 'Running work', state: 'implementing' })] }), {
    projectRoot: '/repo/a',
    filter: 'history',
  })
  expect(await screen.findByText('Nothing in this filter')).toBeTruthy()
  expect(screen.queryByText('No factory work items in this project yet.')).toBeNull()
  filtered.unmount()

  // Neither empty claim is made while the first read is in flight…
  const loading = view(factoryState({ items: [], loading: true }))
  expect(await screen.findByRole('status')).toBeTruthy()
  expect(screen.queryByText('No factory work items yet.')).toBeNull()
  loading.unmount()

  // …or while the last read failed, which is reported instead.
  const failed = view(factoryState({ items: [], error: 'offline' }))
  expect(await screen.findByText(/Factory items unavailable: offline/)).toBeTruthy()
  expect(screen.queryByText('No factory work items yet.')).toBeNull()
  expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  failed.unmount()

  // A first load is not an empty list, and a refresh never blanks one.
  const refreshing = view(factoryState({ items: [item('a', { title: 'Alpha work' })], refreshing: true }))
  expect(await screen.findByRole('heading', { name: 'Alpha work' })).toBeTruthy()
  refreshing.unmount()
})

it('startsWorkAndAgentsWithProjectScopedRoutes', async () => {
  const onNavigate = vi.fn()
  view(factoryState({ items: [] }), { projectRoot: '/repo/a', onNavigate })

  // The empty state repeats the header action, so the first one is the header's.
  await fireEvent.click((await screen.findAllByRole('button', { name: 'New work item' }))[0])
  expect(onNavigate).toHaveBeenLastCalledWith('#factory/new?project=%2Frepo%2Fa')
  await fireEvent.click(screen.getByRole('button', { name: 'Start agent' }))
  expect(onNavigate).toHaveBeenLastCalledWith('#new?project=%2Frepo%2Fa')

  // The defaults card's links: the real settings editor, and the planned
  // workflow page rather than an unimplemented control.
  const defaults = await screen.findByTestId('factory-defaults')
  await fireEvent.click(within(defaults).getByText('Customize'))
  expect(onNavigate).toHaveBeenLastCalledWith('#projects/%2Frepo%2Fa')
  await fireEvent.click(within(defaults).getByText('Workflow editor'))
  expect(onNavigate).toHaveBeenLastCalledWith('#roadmap/workflow')

  await waitFor(() => expect(api.getProjectSettings).toHaveBeenCalledWith('/repo/a'))
})
