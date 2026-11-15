import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import Factory from './Factory.svelte'
import * as api from '../lib/api'
import type { FactoryItem } from '../lib/factory'
import type { FactoryState } from '../lib/factoryStore'
vi.mock('../lib/api', async (actual) => ({ ...await actual<typeof import('../lib/api')>(), listFactoryItems: vi.fn(), getFactoryItem: vi.fn() }))
afterEach(cleanup)

/** One item with the wire shape's required fields; each test overrides what it asserts. */
const item = (id: string, over: Partial<FactoryItem> = {}): FactoryItem => ({
  id,
  title: id,
  state: 'running',
  stage: '',
  repoId: 'repo',
  projectRoot: '/repo',
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

it('shows an empty factory distinctly from unavailable data', async () => {
  const empty = view(factoryState({ items: [], loading: false }))
  expect(await screen.findByText('No factory work items yet.')).toBeTruthy()
  empty.unmount()
  // A failed read keeps its own card and a way back to the store; it is never
  // rendered as "nothing exists".
  view(factoryState({ items: [], error: 'offline' }))
  expect(screen.getByText(/Factory items unavailable: offline/)).toBeTruthy()
  expect(screen.queryByText('No factory work items yet.')).toBeNull()
  expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
})

it('aFirstLoadIsNotAnEmptyList', async () => {
  view(factoryState({ items: [], loading: true }))
  expect(await screen.findByRole('status')).toBeTruthy()
  expect(screen.queryByText('No factory work items yet.')).toBeNull()
})

it('scopedRouteFiltersWithoutDuplicatingPoller', async () => {
  const items = [
    item('a', { projectRoot: '/repo/a', state: 'running' }),
    // A sibling that merely shares a prefix is a different project.
    item('b', { projectRoot: '/repo/ab', state: 'running' }),
    item('c', { projectRoot: '/repo/a', state: 'closed' }),
  ]
  const scoped = view(factoryState({ items }), { projectRoot: '/repo/a' })
  expect(await screen.findByRole('heading', { name: 'a' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'b' })).toBeNull()
  // Closed work has left the current list for history.
  expect(screen.queryByRole('heading', { name: 'c' })).toBeNull()
  scoped.unmount()

  view(factoryState({ items }), { projectRoot: '/repo/a', filter: 'history' })
  expect(await screen.findByRole('heading', { name: 'c' })).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'a' })).toBeNull()

  // Every scope and filter is a projection of the one snapshot: rendering a
  // list reads nothing, because the shell's store owns the only poller.
  expect(api.listFactoryItems).not.toHaveBeenCalled()
  expect(api.getFactoryItem).not.toHaveBeenCalled()
})
