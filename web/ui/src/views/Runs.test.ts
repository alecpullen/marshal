import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Runs from './Runs.svelte'
import * as api from '../lib/api.js'
import type { AgentRow } from '../lib/fleet'
import type { RunRow } from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return { ...actual, listRuns: vi.fn(), startRun: vi.fn() }
})

afterEach(cleanup)

const agent = (x: Partial<AgentRow>): AgentRow => ({
  id: 'a1',
  project: '/work/alpha',
  status: 'idle',
  updatedAt: '',
  name: 'Alpha',
  mode: 'edit',
  activity: '',
  contextPct: 0,
  changedFiles: 0,
  interrupted: false,
  ...x,
})

const sdd = (over: Record<string, unknown> = {}) => ({
  kind: 'sdd' as const,
  sdd: { active: true, planName: 'Auth plan', totalTasks: 4, doneTasks: 1, currentTask: 2, phase: 'implementing', fixRound: 0, maxFixRounds: 3, tokensUsed: 10, tokensMax: 100, finished: false, succeeded: false, startedAt: Date.now() - 60_000, tasks: [], ...over },
})

const listed: RunRow[] = [
  { agentId: 'a1', name: 'Alpha', project: '/work/alpha', run: sdd(), at: 3 },
  { agentId: 'a2', name: 'Beta', project: '/work/beta', run: sdd({ active: false, finished: true, succeeded: true, planName: 'Docs plan', doneTasks: 4 }), at: 2 },
  { agentId: 'a3', name: 'Gamma', project: '/work/gamma', run: sdd({ planName: 'Gate plan', gate: { taskN: 2, question: 'Proceed?' } }), at: 1 },
]

describe('Runs', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ;(api.listRuns as Mock).mockResolvedValue(listed)
  })

  const mount = (agents: AgentRow[] = [], onNavigate = vi.fn()) =>
    render(Runs, { agents, projects: [{ root: '/work/alpha' } as never], onNavigate })

  it('lists runs newest first with progress and phase', async () => {
    mount()
    const rows = await screen.findAllByTestId('run-row')
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('Alpha'),
      expect.stringContaining('Beta'),
      expect.stringContaining('Gamma'),
    ])
    expect(rows[0].textContent).toContain('Auth plan')
    expect(rows[0].textContent).toContain('1/4')
    expect(rows[0].textContent).toContain('implementing')
    expect(rows[2].textContent).toContain('needs you')
  })

  it('filters by running, finished and needs you', async () => {
    mount()
    await screen.findAllByTestId('run-row')
    await fireEvent.click(screen.getByRole('button', { name: 'Finished' }))
    expect(screen.getAllByTestId('run-row').map((r) => r.textContent)).toEqual([expect.stringContaining('Beta')])
    await fireEvent.click(screen.getByRole('button', { name: 'Needs you' }))
    expect(screen.getAllByTestId('run-row').map((r) => r.textContent)).toEqual([expect.stringContaining('Gamma')])
    await fireEvent.click(screen.getByRole('button', { name: 'Running' }))
    expect(screen.getAllByTestId('run-row')).toHaveLength(2)
  })

  it('takes a fresher digest from the fleet row', async () => {
    mount([agent({ run: sdd({ planName: 'Auth plan', doneTasks: 3 }), runAt: 10 })])
    const rows = await screen.findAllByTestId('run-row')
    expect(rows[0].textContent).toContain('3/4')
  })

  it('shows a run error the bridge recorded', async () => {
    ;(api.listRuns as Mock).mockResolvedValue([{ ...listed[1], error: 'pipeline exited: build failed' }])
    mount()
    expect(await screen.findByText('pipeline exited: build failed')).toBeTruthy()
  })

  it('opens a run on click', async () => {
    const onNavigate = vi.fn()
    mount([], onNavigate)
    await fireEvent.click((await screen.findAllByTestId('run-row'))[0])
    expect(onNavigate).toHaveBeenCalledWith('#runs/a1')
  })

  it('starts a swarm run on an existing agent and navigates to it', async () => {
    ;(api.startRun as Mock).mockResolvedValue({ agentId: 'a1' })
    const onNavigate = vi.fn()
    mount([agent({})], onNavigate)
    await fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Swarm' }))
    await fireEvent.input(screen.getByLabelText('Goal'), { target: { value: 'Ship it' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Start run' }))
    await waitFor(() => expect(api.startRun).toHaveBeenCalledWith({ kind: 'swarm', agentId: 'a1', goal: 'Ship it' }))
    await waitFor(() => expect(onNavigate).toHaveBeenCalledWith('#runs/a1'))
  })

  it('starts a plan run in a new agent with a pasted plan', async () => {
    ;(api.startRun as Mock).mockResolvedValue({ agentId: 'new1' })
    mount([])
    await fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    await fireEvent.input(await screen.findByLabelText('Plan'), { target: { value: '## Task 1' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Start run' }))
    await waitFor(() => expect(api.startRun).toHaveBeenCalledWith({ kind: 'sdd', project: '/work/alpha', plan: '## Task 1' }))
  })

  it('refreshes the fleet before opening a run on a new agent', async () => {
    ;(api.startRun as Mock).mockResolvedValue({ agentId: 'new1' })
    const order: string[] = []
    const onRefresh = vi.fn(async () => void order.push('refresh'))
    const onNavigate = vi.fn(() => void order.push('navigate'))
    render(Runs, { agents: [], projects: [{ root: '/work/alpha' } as never], onNavigate, onRefresh })
    await fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    await fireEvent.input(await screen.findByLabelText('Plan'), { target: { value: '## Task 1' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Start run' }))
    await waitFor(() => expect(onNavigate).toHaveBeenCalledWith('#runs/new1'))
    expect(order).toEqual(['refresh', 'navigate'])
  })

  it('does not navigate when the response names no agent', async () => {
    ;(api.startRun as Mock).mockResolvedValue({})
    const onNavigate = vi.fn()
    mount([], onNavigate)
    await fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    await fireEvent.input(await screen.findByLabelText('Plan'), { target: { value: 'x' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Start run' }))
    expect(await screen.findByText(/did not say which agent/)).toBeTruthy()
    expect(onNavigate).not.toHaveBeenCalled()
  })

  it('shows a budget stop inline and stays open', async () => {
    ;(api.startRun as Mock).mockRejectedValue(new api.BudgetError({ scope: 'daily' }))
    mount([])
    await fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    await fireEvent.input(await screen.findByLabelText('Plan'), { target: { value: 'x' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Start run' }))
    expect(await screen.findByText('Budget reached (daily)')).toBeTruthy()
  })
})
