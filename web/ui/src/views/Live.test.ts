import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { writable } from 'svelte/store'
import Live from './Live.svelte'
import * as api from '../lib/api.js'
import * as stack from '../lib/stack.js'
import type { AgentRow } from '../lib/fleet'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return { ...actual, resolvePermission: vi.fn().mockResolvedValue(undefined) }
})
vi.mock('../lib/sse.js', () => ({ connectSSE: vi.fn(() => vi.fn()) }))
vi.mock('../lib/stack.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/stack.js')>()
  return { ...actual, createStackStore: vi.fn() }
})

afterEach(cleanup)

// A controllable IntersectionObserver: tests flip tiles in and out of view.
type IOCallback = (entries: { target: Element; isIntersecting: boolean }[]) => void
let observers: { cb: IOCallback; targets: Element[] }[] = []
function setVisible(el: Element, isIntersecting: boolean) {
  for (const o of observers) if (o.targets.includes(el)) o.cb([{ target: el, isIntersecting }])
}

const agent = (x: Partial<AgentRow> & { id: string }): AgentRow =>
  ({ project: '/work/alpha', status: 'idle', updatedAt: '2026-01-01T00:00:00Z', name: x.id, mode: '', activity: '', contextPct: 0, changedFiles: 0, interrupted: false, ...x }) as AgentRow

const route = (over = {}) => ({ runsOnly: false, page: 1, ...over })
const mount = (agents: AgentRow[], over: Record<string, unknown> = {}) =>
  render(Live, { agents, projects: [{ root: '/work/alpha' } as never], route: route(), onRefreshPending: vi.fn(), onNavigate: vi.fn(), ...over })

describe('Live wall', () => {
  let destroy: Mock
  beforeEach(() => {
    vi.clearAllMocks()
    observers = []
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        cb: IOCallback
        targets: Element[] = []
        constructor(cb: IOCallback) {
          this.cb = cb
          observers.push(this)
        }
        observe(el: Element) {
          this.targets.push(el)
        }
        disconnect() {}
        unobserve() {}
      },
    )
    destroy = vi.fn()
    ;(stack.createStackStore as Mock).mockImplementation(() => ({
      ...writable({ status: 'ready', rev: 1, roots: [], nodes: new Map() }),
      load: vi.fn().mockResolvedValue(undefined),
      destroy,
      onEvent: vi.fn(),
    }))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows 12 tiles a page and navigates between pages', async () => {
    const many = Array.from({ length: 14 }, (_, i) => agent({ id: `a${String(i).padStart(2, '0')}` }))
    const onNavigate = vi.fn()
    mount(many, { onNavigate })
    expect(screen.getAllByTestId('tile')).toHaveLength(12)
    await fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    expect(onNavigate).toHaveBeenCalledWith('#live?page=2')
  })

  it('shows the remainder on the last page', () => {
    const many = Array.from({ length: 14 }, (_, i) => agent({ id: `a${String(i).padStart(2, '0')}` }))
    mount(many, { route: route({ page: 2 }) })
    expect(screen.getAllByTestId('tile')).toHaveLength(2)
    expect((screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('creates a tile store on intersect and disposes it on leave', async () => {
    mount([agent({ id: 'a1' })])
    const tile = screen.getByTestId('tile')
    expect(stack.createStackStore).not.toHaveBeenCalled()
    setVisible(tile, true)
    await waitFor(() => expect(stack.createStackStore).toHaveBeenCalledWith('a1'))
    expect(destroy).not.toHaveBeenCalled()
    setVisible(tile, false)
    await waitFor(() => expect(destroy).toHaveBeenCalledTimes(1))
    // Re-entering builds a fresh store.
    setVisible(tile, true)
    await waitFor(() => expect(stack.createStackStore).toHaveBeenCalledTimes(2))
  })

  it('approves inline through the permission API and tints the tile', async () => {
    const needs = agent({ id: 'ask', pending: { kind: 'approval', id: 'tc1', params: { command: 'rm -rf build' } }, status: 'awaiting-approval' })
    const onRefreshPending = vi.fn()
    mount([needs], { onRefreshPending })
    expect(screen.getByTestId('tile').className).toContain('bg-warn/10')
    expect(screen.getByText('rm -rf build')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(api.resolvePermission).toHaveBeenCalledWith('tc1', { approved: true }))
    expect(onRefreshPending).toHaveBeenCalled()
  })

  it('opens the chat with the dock collapsed on a tile click, but not on a control click', async () => {
    const onNavigate = vi.fn()
    mount([agent({ id: 'ask', pending: { kind: 'approval', id: 'tc1', params: { command: 'ls' } } })], { onNavigate })
    await fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    expect(onNavigate).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByTestId('tile'))
    expect(onNavigate).toHaveBeenCalledWith('#chat/ask?dock=collapsed')
  })

  it('lists needs-you first and honours the runs-only filter', () => {
    const agents = [
      agent({ id: 'plain', updatedAt: '2026-01-05T00:00:00Z' }),
      agent({ id: 'runner', run: { kind: 'swarm', swarm: { active: true, roles: [], tokensUsed: 0, tokensMax: 0 } } }),
      agent({ id: 'ask', pending: { kind: 'approval', id: 'x' } }),
    ]
    const { unmount } = mount(agents)
    expect(screen.getAllByTestId('tile').map((t) => t.getAttribute('data-agent'))).toEqual(['ask', 'plain', 'runner'])
    unmount()
    mount(agents, { route: route({ runsOnly: true }) })
    expect(screen.getAllByTestId('tile').map((t) => t.getAttribute('data-agent'))).toEqual(['runner'])
  })

  it('filters by project through the route', async () => {
    const onNavigate = vi.fn()
    mount([agent({ id: 'a' })], { onNavigate })
    await fireEvent.change(screen.getByLabelText('Project'), { target: { value: '/work/alpha' } })
    expect(onNavigate).toHaveBeenCalledWith('#live?project=%2Fwork%2Falpha')
  })

  it('attaches at most four tiles at once and hands a freed slot to a waiting tile', async () => {
    mount(Array.from({ length: 6 }, (_, i) => agent({ id: `a${i}` })))
    const tiles = screen.getAllByTestId('tile')
    for (const t of tiles) setVisible(t, true)
    await waitFor(() => expect(stack.createStackStore).toHaveBeenCalledTimes(4))
    expect((stack.createStackStore as Mock).mock.calls.map((c) => c[0])).toEqual(['a0', 'a1', 'a2', 'a3'])
    // A tile leaving view frees its slot for the first waiting one.
    setVisible(tiles[1], false)
    await waitFor(() => expect(stack.createStackStore).toHaveBeenCalledTimes(5))
    expect((stack.createStackStore as Mock).mock.calls[4][0]).toBe('a4')
    // A waiting tile that left view before a slot freed does not take one.
    setVisible(tiles[5], false)
    setVisible(tiles[0], false)
    await waitFor(() => expect(destroy).toHaveBeenCalledTimes(2))
    expect(stack.createStackStore).toHaveBeenCalledTimes(5)
  })

  it('shows the workspace tag on a tile only for agents that run in one', () => {
    mount([agent({ id: 'w1', workspace: { name: 'go-service', version: 3, source: 'studio' } }), agent({ id: 'w2' })])
    const tags = screen.getAllByTestId('tile-workspace')
    expect(tags).toHaveLength(1)
    expect(tags[0].textContent).toContain('▦ go-service@v3')
  })
})
