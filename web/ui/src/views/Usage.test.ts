import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Usage from './Usage.svelte'
import * as api from '../lib/api.js'
import type { UsageBy } from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return { ...actual, getUsage: vi.fn(), getBudgets: vi.fn(), overrideBudget: vi.fn(), listAudit: vi.fn(), getDiskUsage: vi.fn() }
})

afterEach(cleanup)

const report = (by: UsageBy) => ({
  range: '7d',
  totals: { costUsd: 12.5, promptTokens: 1_500_000, completionTokens: 250_000, agentHours: 3.25, prsShipped: 4 },
  series:
    by === 'day'
      ? [
          { key: '2026-10-01', costUsd: 2, tokens: 1000 },
          { key: '2026-10-02', costUsd: 10.5, tokens: 5000 },
        ]
      : by === 'project'
        ? [{ key: '/work/alpha', costUsd: 12.5, tokens: 1_750_000 }]
        : [{ key: by === 'role' ? 'implementer' : 'big-1', costUsd: 12.5, tokens: 1_750_000 }],
})

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getUsage as Mock).mockImplementation(async (_r: string, by: UsageBy) => report(by))
  ;(api.getBudgets as Mock).mockResolvedValue({ budgets: { dailyUsd: 25, perAgentUsd: 5, onDailyCap: 'block', onAgentCap: 'pause' }, spentTodayUsd: 10.5, agents: { a2: 5.2 }, paused: ['a2'], blocked: false })
  ;(api.overrideBudget as Mock).mockResolvedValue(undefined)
  ;(api.listAudit as Mock).mockResolvedValue([
    { ts: '2026-10-04T01:00:00Z', event: 'spawn', agentId: 'a1', origin: 'ui' },
    { ts: '2026-10-04T02:00:00Z', event: 'push', agentId: 'a1', detail: 'origin/x' },
  ])
  ;(api.getDiskUsage as Mock).mockResolvedValue({ repos: 1, work: 2, total: 3, measuredAt: '', budgetMB: 100 })
})

describe('Usage cost tab', () => {
  it('renders the tiles and a bar per day', async () => {
    render(Usage, { tab: 'cost', onNavigate: vi.fn() })
    await waitFor(() => expect(screen.getByTestId('tile-spend').textContent).toBe('$12.50'))
    expect(screen.getByTestId('tile-tokens').textContent).toBe('1.8M')
    expect(screen.getByTestId('tile-hours').textContent).toBe('3.3')
    expect(screen.getByTestId('tile-prs').textContent).toBe('4')
    expect(await screen.findAllByTestId('bar')).toHaveLength(2)
  })

  it('makes a separate call per breakdown and fills the tables', async () => {
    render(Usage, { tab: 'cost', onNavigate: vi.fn() })
    const project = await screen.findByTestId('breakdown-project')
    await waitFor(() => expect(within(project).getByText('alpha')).toBeTruthy())
    expect(within(screen.getByTestId('breakdown-role')).getByText('implementer')).toBeTruthy()
    expect(within(screen.getByTestId('breakdown-model')).getByText('big-1')).toBeTruthy()
    expect((api.getUsage as Mock).mock.calls.map((c) => c[1]).sort()).toEqual(['day', 'model', 'project', 'role'])
  })

  it('re-queries with 30 days', async () => {
    render(Usage, { tab: 'cost', onNavigate: vi.fn() })
    await screen.findAllByTestId('bar')
    await fireEvent.click(screen.getByText('30 days'))
    await waitFor(() => expect((api.getUsage as Mock).mock.calls.some((c) => c[0] === '30d' && c[1] === 'day')).toBe(true))
  })

  it('draws the daily cap line when a cap is set, and not otherwise', async () => {
    const { unmount } = render(Usage, { tab: 'cost', onNavigate: vi.fn() })
    expect(await screen.findByTestId('cap-line')).toBeTruthy()
    unmount()
    ;(api.getBudgets as Mock).mockResolvedValue({ budgets: { dailyUsd: 0, perAgentUsd: 0, onDailyCap: 'warn', onAgentCap: 'warn' }, spentTodayUsd: 0, paused: [] })
    render(Usage, { tab: 'cost', onNavigate: vi.fn() })
    await screen.findAllByTestId('bar')
    await screen.findByTestId('budget-state')
    expect(screen.queryByTestId('cap-line')).toBeNull()
  })

  it('lists paused agents and posts the override', async () => {
    render(Usage, { tab: 'cost', agents: [{ id: 'a2', name: 'Beta' } as never], onNavigate: vi.fn() })
    const row = await screen.findByTestId('paused-agent')
    expect(within(row).getByText('Beta')).toBeTruthy()
    await fireEvent.click(within(row).getByText('Override'))
    await waitFor(() => expect(api.overrideBudget).toHaveBeenCalledWith('a2'))
  })

  it('shows sub-cent spend with its digits', async () => {
    ;(api.getUsage as Mock).mockImplementation(async (_r: string, by: UsageBy) => ({ ...report(by), totals: { ...report(by).totals, costUsd: 0.0036 } }))
    render(Usage, { tab: 'cost', onNavigate: vi.fn() })
    await waitFor(() => expect(screen.getByTestId('tile-spend').textContent).toBe('$0.0036'))
  })
})

describe('Usage other tabs', () => {
  it('filters the audit feed by event', async () => {
    render(Usage, { tab: 'audit', onNavigate: vi.fn() })
    await screen.findByText(/spawned a1/)
    expect(screen.getByText(/pushed a1/)).toBeTruthy()
    await fireEvent.change(screen.getByLabelText('Event type'), { target: { value: 'push' } })
    await waitFor(() => expect(screen.queryByText(/spawned a1/)).toBeNull())
    expect(screen.getByText(/pushed a1/)).toBeTruthy()
  })

  it('shows the disk panel', async () => {
    render(Usage, { tab: 'disk', onNavigate: vi.fn() })
    expect(await screen.findByRole('heading', { name: 'Disk' })).toBeTruthy()
    expect(api.getDiskUsage).toHaveBeenCalled()
  })

  it('navigates between tabs', async () => {
    const onNavigate = vi.fn()
    render(Usage, { tab: 'cost', onNavigate })
    await fireEvent.click(screen.getByRole('tab', { name: 'Audit' }))
    expect(onNavigate).toHaveBeenCalledWith('#usage?tab=audit')
  })
})
