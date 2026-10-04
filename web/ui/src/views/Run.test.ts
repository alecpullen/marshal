import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Run from './Run.svelte'
import * as api from '../lib/api.js'
import type { RunDetail, RunTask } from '../lib/api.js'
import type { StackSnapshot, WireNode } from '../lib/stack'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    getRun: vi.fn(),
    getRoster: vi.fn(),
    answerRun: vi.fn(),
    getStack: vi.fn(),
    getNode: vi.fn(),
    getLastRequest: vi.fn(),
    getDiff: vi.fn(),
    getGate: vi.fn(),
    getStepDiffs: vi.fn(),
    listFiles: vi.fn(),
    readFile: vi.fn(),
  }
})
vi.mock('../lib/sse.js', () => ({ connectSSE: vi.fn(() => () => {}) }))
vi.mock('../lib/markdown.js', () => ({ renderMarkdown: vi.fn((s: string) => s), initHighlighter: vi.fn(() => Promise.resolve()) }))

afterEach(cleanup)

const stages = (...states: string[]) => ['Implement', 'Verify', 'Review', 'Commit'].map((name, i) => ({ name, state: (states[i] ?? 'pending') as never }))
const tasks: RunTask[] = [
  { n: 1, title: 'Set up schema', dependsOn: [], status: 'done', startedAt: 1000, endedAt: 4000, fixRounds: 1, commit: { base: 'a', head: 'abc1234def' }, stages: stages('done', 'done', 'done', 'done') },
  { n: 2, title: 'Add handler', dependsOn: [1], status: 'active', startedAt: 5000, endedAt: 9000, fixRounds: 0, stages: stages('done', 'active') },
  { n: 3, title: 'Write docs', dependsOn: [1], status: 'done', startedAt: 4000, endedAt: 4500, fixRounds: 0, stages: stages('done', 'done', 'done', 'done') },
]
const run: RunDetail = {
  kind: 'sdd',
  sdd: { active: true, planName: 'Auth plan', branch: 'feat/auth', totalTasks: 3, doneTasks: 2, currentTask: 2, phase: 'verifying', fixRound: 0, maxFixRounds: 3, tokensUsed: 1200, tokensMax: 5000, finished: false, succeeded: false, startedAt: 1000, tasks },
}

const nodes: WireNode[] = [
  { id: 'turn:1', kind: 'turn', children: ['step:1', 'step:2', 'step:3', 'step:4'] },
  { id: 'step:1', kind: 'step', parent: 'turn:1', step: { headline: 'Write the schema', role: 'sdd_implementer', startedAt: 1500, endedAt: 2000 } },
  { id: 'step:2', kind: 'step', parent: 'turn:1', step: { headline: 'Write the handler', role: 'sdd_implementer', startedAt: 5500, endedAt: 6000 } },
  { id: 'step:3', kind: 'step', parent: 'turn:1', step: { headline: 'Review the handler', role: 'sdd_reviewer', startedAt: 7000, endedAt: 7500 } },
  { id: 'step:4', kind: 'step', parent: 'turn:1', children: ['tool:1'], step: { headline: 'Run the tests', startedAt: 8000, endedAt: 8500 } },
  { id: 'tool:1', kind: 'tool', parent: 'step:4', tool: { name: 'shell.run', display: 'Run command', calls: [{ target: 'go test ./...' }] } },
]
const snapshot: StackSnapshot = { rev: 1, roots: ['turn:1'], nodes }

const props = (over: Record<string, unknown> = {}) => ({
  agentId: 'a1',
  route: { id: 'a1', view: 'lanes' as const },
  onNavigate: vi.fn(),
  ...over,
})

describe('Run page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ;(api.getRun as Mock).mockResolvedValue(run)
    ;(api.getRoster as Mock).mockResolvedValue({ roles: [{ role: 'sdd_implementer', profile: 'p', model: 'qwen', localOnly: true }], swarmBudget: { maxFixRounds: 1, maxTotalTokens: 1 }, sddBudget: { maxFixRounds: 1, maxTotalTokens: 1 } })
    ;(api.getStack as Mock).mockResolvedValue(snapshot)
    ;(api.getNode as Mock).mockResolvedValue({ node: {}, detail: {} })
    ;(api.getLastRequest as Mock).mockResolvedValue('unsupported')
    ;(api.getDiff as Mock).mockResolvedValue({ files: [] })
    ;(api.getGate as Mock).mockResolvedValue(null)
    ;(api.getStepDiffs as Mock).mockResolvedValue([])
    ;(api.listFiles as Mock).mockResolvedValue({ entries: [] })
    try {
      localStorage.clear()
    } catch {
      // jsdom without storage
    }
  })

  it('renders the header and lanes from the run', async () => {
    render(Run, props())
    expect(await screen.findByText('Auth plan')).toBeTruthy()
    expect(screen.getByText('⎇ feat/auth')).toBeTruthy()
    expect(screen.getByText(/1,200 \/ 5,000 tokens/)).toBeTruthy()
    const l1 = await screen.findByTestId('lane-1')
    expect(l1.textContent).toContain('Set up schema')
    expect(l1.textContent).toContain('abc1234')
    expect(l1.textContent).toContain('↻1')
    expect(screen.getByTestId('lane-2').textContent).toContain('after 1')
    expect(await screen.findByText(/qwen/)).toBeTruthy()
  })

  it('navigates to the selected cell when clicked', async () => {
    const p = props()
    render(Run, p)
    await fireEvent.click(await screen.findByRole('button', { name: 'Task 2 Verify: active' }))
    expect(p.onNavigate).toHaveBeenCalledWith(expect.stringContaining('node=2%3Averify'))
  })

  it('filters the transcript to the stage steps of the selected task', async () => {
    render(Run, props({ route: { id: 'a1', view: 'lanes', node: '2:verify' } }))
    const panel = await screen.findByTestId('stage-steps')
    await waitFor(() => expect(panel.querySelectorAll('[data-node-id]').length).toBeGreaterThan(0))
    const ids = [...panel.querySelectorAll('[data-node-id]')].map((e) => e.getAttribute('data-node-id'))
    expect(ids).toContain('step:4')
    expect(ids).toContain('tool:1')
    expect(ids).not.toContain('step:1')
    expect(ids).not.toContain('step:2')
    expect(panel.textContent).toContain('Task 2 · Verify · 1 step')
  })

  it('selects implementer steps inside the task window for the implement stage', async () => {
    render(Run, props({ route: { id: 'a1', view: 'lanes', node: '2:implement' } }))
    const panel = await screen.findByTestId('stage-steps')
    await waitFor(() => expect(panel.textContent).toContain('Task 2 · Implement · 1 step'))
    const ids = [...panel.querySelectorAll('[data-node-id]')].map((e) => e.getAttribute('data-node-id'))
    expect(ids).toContain('step:2')
    expect(ids).not.toContain('step:1')
  })

  it('draws exactly one dashed critical edge in the graph', async () => {
    render(Run, props({ route: { id: 'a1', view: 'graph' } }))
    await screen.findByTestId('graph')
    const edges = screen.getAllByTestId('edge')
    expect(edges).toHaveLength(2)
    // 1 → 2 (4s) outlasts 1 → 3 (0.5s).
    const dashed = edges.filter((e) => e.getAttribute('stroke-dasharray'))
    expect(dashed).toHaveLength(1)
  })

  it('shows role bars on the timeline', async () => {
    render(Run, props({ route: { id: 'a1', view: 'timeline' } }))
    await screen.findByTestId('timeline')
    await waitFor(() => expect(screen.getAllByTestId('bar').length).toBe(3))
  })

  it('posts the gate answer', async () => {
    ;(api.getRun as Mock).mockResolvedValue({ kind: 'sdd', sdd: { ...run.sdd, gate: { taskN: 2, question: 'Use Postgres?' } } })
    ;(api.answerRun as Mock).mockResolvedValue(undefined)
    render(Run, props())
    expect(await screen.findByText('Use Postgres?')).toBeTruthy()
    await fireEvent.input(screen.getByLabelText('Answer'), { target: { value: 'yes' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    await waitFor(() => expect(api.answerRun).toHaveBeenCalledWith('a1', 'yes'))
  })

  it('explains an agent that predates run detail', async () => {
    ;(api.getRun as Mock).mockResolvedValue('unsupported')
    render(Run, props())
    expect(await screen.findByText(/Run detail needs a newer agent/)).toBeTruthy()
  })
})
