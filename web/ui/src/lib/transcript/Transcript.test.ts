import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { writable } from 'svelte/store'
import Transcript from './Transcript.svelte'
import { BEAT_ROW_CAP, type StackState, type WireNode } from '../stack'

afterEach(cleanup)

// A turn of two beats. The first carries the todo label for the work it moves
// on to; the second is still live.
const nodes: WireNode[] = [
  { id: 'turn:1', kind: 'turn', children: ['msg:1', 'step:1', 'step:2', 'final:1', 'receipt:1'] },
  { id: 'msg:1', kind: 'message', parent: 'turn:1', message: { role: 'user', content: 'fix the bug' } },
  {
    id: 'step:1',
    kind: 'step',
    parent: 'turn:1',
    children: ['tool:1', 'tool:2'],
    step: {
      headline: 'Reading the handler',
      rest: 'It looks at main.go first.\nThen the tests.',
      role: 'implementer',
      owner: 'implementer',
      todoId: 'p',
      todo: { id: 'p', index: 3, total: 9, content: 'AccessibleModal' },
      startedAt: 1000,
      endedAt: 4000,
    },
  },
  {
    id: 'tool:1',
    kind: 'tool',
    parent: 'step:1',
    tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'a.go', summary: '40 lines' }, { target: 'b.go', summary: '12 lines' }, { target: 'c.go', failed: true, error: 'not found' }] },
  },
  { id: 'tool:2', kind: 'tool', parent: 'step:1', tool: { name: 'shell.run', display: 'Run command', calls: [{ target: 'go test ./...', summary: 'ok', exitCode: 0 }] } },
  { id: 'step:2', kind: 'step', parent: 'turn:1', live: true, children: ['tool:3'], step: { headline: 'Patching the handler', todoId: 'p' } },
  { id: 'tool:3', kind: 'tool', parent: 'step:2', tool: { name: 'file.write', display: 'Write file', calls: [{ target: 'main.go', summary: 'ok' }] } },
  { id: 'final:1', kind: 'final', parent: 'turn:1', message: { role: 'assistant', content: 'All **done**.', final: true } },
  { id: 'receipt:1', kind: 'receipt', parent: 'turn:1', receipt: { durationMs: 400000, tasks: 2, steps: 2, tools: 4, files: 1, usage: '212k tok' } },
]

function stackStore() {
  return writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:1'], nodes: new Map(nodes.map((n) => [n.id, n])) })
}

// A narrationless beat: the model narrated nothing, so the beat has no
// headline and its rows stand alone under the turn.
const beatNodes: WireNode[] = [
  { id: 'turn:2', kind: 'turn', children: ['step:9'] },
  {
    id: 'step:9',
    kind: 'step',
    parent: 'turn:2',
    children: ['tool:9', 'tool:10', 'tool:11'],
    step: { headline: '', stepIds: [9] },
  },
  { id: 'tool:9', kind: 'tool', parent: 'step:9', tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'one.go' }] } },
  { id: 'tool:10', kind: 'tool', parent: 'step:9', tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'two.go' }] } },
  { id: 'tool:11', kind: 'tool', parent: 'step:9', tool: { name: 'shell.run', display: 'Run command', calls: [{ target: 'go vet ./...' }] } },
]

const beatStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:2'], nodes: new Map(beatNodes.map((n) => [n.id, n])) })

// A beat with more rows than fit: the model ran calls without narrating in
// between, so the older ones must collapse behind a count.
const call = (i: number) => ({
  kind: 'tool',
  parent: 'step:10',
  tool: { name: 'file.read', display: 'Read file', calls: [{ target: `call-${i}.go` }] },
})
const longBeatNodes: WireNode[] = [
  { id: 'turn:3', kind: 'turn', children: ['step:10'] },
  {
    id: 'step:10',
    kind: 'step',
    parent: 'turn:3',
    children: Array.from({ length: BEAT_ROW_CAP + 2 }, (_, i) => `tool:2${i}`),
    step: { headline: '', stepIds: [10] },
  },
  ...Array.from({ length: BEAT_ROW_CAP + 2 }, (_, i) => ({ id: `tool:2${i}`, ...call(i) })) as WireNode[],
]

const longBeatStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:3'], nodes: new Map(longBeatNodes.map((n) => [n.id, n])) })

// A job's completion event: the dispatch row stays where the work started, and
// the event records the moment it ended, at the end of the beat that was
// running when it did.
const completionNodes: WireNode[] = [
  { id: 'turn:4', kind: 'turn', children: ['step:11', 'completion:job:4'] },
  {
    id: 'step:11',
    kind: 'step',
    parent: 'turn:4',
    children: ['tool:11'],
    step: { headline: 'Running the backend gate.', stepIds: [11] },
  },
  {
    id: 'tool:11',
    kind: 'tool',
    parent: 'step:11',
    tool: {
      name: 'shell.run',
      display: 'Run command',
      calls: [{ target: 'pytest -q', summary: 'started background job job-4' }],
    },
  },
  {
    id: 'completion:job:4',
    kind: 'completion',
    parent: 'turn:4',
    completion: {
      source: 'job',
      id: 'job-4',
      label: 'pytest -q',
      outcome: 'exit 2',
      failed: true,
      exitCode: 2,
      durationMs: 122000,
      measure: '3/4 tests',
      summary: 'the backend gate failed',
      command: 'pytest -q --junitxml=/tmp/a.xml',
      cwd: '/repo',
      output: 'undefined reference to foo',
    },
  },
]

const completionStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:4'], nodes: new Map(completionNodes.map((n) => [n.id, n])) })

// A beat's plan line: the todo list as the beat's last todo.write left it.
const planNodes: WireNode[] = [
  { id: 'turn:5', kind: 'turn', children: ['step:12', 'plan:12'] },
  { id: 'step:12', kind: 'step', parent: 'turn:5', children: [], step: { headline: 'Writing the plan' } },
  {
    id: 'plan:12',
    kind: 'planUpdate',
    parent: 'turn:5',
    plan: { status: ['completed', 'in_progress', 'pending'], done: 1, total: 3, active: 2, activeText: 'Wire the row' },
  },
]

const planStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:5'], nodes: new Map(planNodes.map((n) => [n.id, n])) })

// Streaming reasoning is the turn's last child, below every beat and row.
const thinkNodes: WireNode[] = [
  { id: 'turn:6', kind: 'turn', children: ['step:13', 'think:live'] },
  { id: 'step:13', kind: 'step', parent: 'turn:6', children: ['tool:13'], step: { headline: 'Reading the handler' } },
  { id: 'tool:13', kind: 'tool', parent: 'step:13', tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'a.go' }] } },
  { id: 'think:live', kind: 'thinking', parent: 'turn:6', live: true, thinking: { text: 'considering the fix' } },
]

const thinkStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:6'], nodes: new Map(thinkNodes.map((n) => [n.id, n])) })

describe('Transcript', () => {
  it('shows the prompt, steps, tool rows and receipt at the steps density', () => {
    render(Transcript, { store: stackStore(), density: 'steps' })
    expect(screen.getByText('fix the bug')).toBeTruthy()
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.getByText('It looks at main.go first.')).toBeTruthy()
    expect(screen.getByText('Read file')).toBeTruthy()
    expect(screen.getByText('×3')).toBeTruthy()
    expect(screen.getByText('go test ./...')).toBeTruthy()
    expect(screen.getByText('done')).toBeTruthy()
    expect(screen.getByText(/done · 6m40s · 2 tasks · 2 steps · 4 tools · ±1 file · 212k tok/)).toBeTruthy()
  })

  it('hides tool rows and the rest line at outline', () => {
    render(Transcript, { store: stackStore(), density: 'outline' })
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.queryByText('Read file')).toBeNull()
    expect(screen.queryByText('It looks at main.go first.')).toBeNull()
  })

  it('expands a merged run to one line per call and renders rest as markdown at full', () => {
    render(Transcript, { store: stackStore(), density: 'full' })
    expect(screen.getByText('a.go')).toBeTruthy()
    expect(screen.getByText('c.go')).toBeTruthy()
    expect(screen.getByText('not found')).toBeTruthy()
    expect(screen.getByText('Then the tests.', { exact: false })).toBeTruthy()
  })

  it('marks the beat that moves work to another todo with a divider', () => {
    render(Transcript, { store: stackStore(), density: 'steps' })
    // Only the transition beat carries a label, so exactly one divider shows.
    expect(screen.getAllByText('3/9 AccessibleModal')).toHaveLength(1)
  })

  it('applies a per-node override down the subtree', () => {
    render(Transcript, { store: stackStore(), density: 'outline', overrides: new Map([['step:1', 'steps' as const]]) })
    expect(screen.getByText('Read file')).toBeTruthy()
  })

  it('marks the cursor node', () => {
    const { container } = render(Transcript, { store: stackStore(), density: 'steps', cursor: 'step:1' })
    expect(container.querySelector('[data-node-id="step:1"]')?.className).toContain('border-violet')
  })

  it('renders a narrationless beat as rows under the turn, with no inferred tag and no header', () => {
    const { container } = render(Transcript, { store: beatStore(), density: 'steps' })
    expect(screen.queryByText(/inferred/)).toBeNull()
    // StepHeader renders nothing at all for an empty headline with no owner
    // and no thoughts, so the beat's own node holds no header row.
    const step = container.querySelector('[data-node-id="step:9"]')
    expect(step).toBeTruthy()
    expect(step?.textContent?.trim()).toBe('')
    expect(container.querySelector('[data-node-id="step:9"] .items-baseline')).toBeNull()
    expect(screen.getByText('one.go')).toBeTruthy()
    expect(screen.getByText('two.go')).toBeTruthy()
    expect(screen.getByText('go vet ./...')).toBeTruthy()
  })

  it('caps a long beat at BEAT_ROW_CAP rows behind an earlier-calls count', () => {
    render(Transcript, { store: longBeatStore(), density: 'steps' })
    expect(screen.getByText('+2 earlier calls')).toBeTruthy()
    // The oldest rows are the ones that collapse.
    expect(screen.queryByText('call-0.go')).toBeNull()
    expect(screen.queryByText('call-1.go')).toBeNull()
    expect(screen.getByText(`call-${BEAT_ROW_CAP + 1}.go`)).toBeTruthy()
  })

  it('renders a completion event as one line and expands its report on click', async () => {
    render(Transcript, { store: completionStore(), density: 'steps' })
    const row = screen.getByTestId('completion-row')
    expect(row.textContent).toContain('job-4')
    expect(row.textContent).toContain('exit 2')
    expect(row.getAttribute('aria-expanded')).toBe('false')
    // The output is the issue report's, so it waits for the click.
    expect(screen.queryByText(/undefined reference to foo/)).toBeNull()
    await userEvent.click(row)
    expect(row.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText(/undefined reference to foo/)).toBeTruthy()
  })

  it('renders a plan line as a call row', () => {
    render(Transcript, { store: planStore(), density: 'steps' })
    const row = screen.getByTestId('plan-row')
    expect(row.textContent).toContain('plan')
    expect(row.textContent).toContain('2/3')
    expect(row.textContent).toContain('Wire the row')
  })

  it("draws streaming reasoning after the beat's rows", () => {
    const { container } = render(Transcript, { store: thinkStore(), density: 'steps' })
    const tool = container.querySelector('[data-node-id="tool:13"]')
    const think = container.querySelector('[data-node-id="think:live"]')
    expect(tool).toBeTruthy()
    expect(think).toBeTruthy()
    // The reasoning follows the beat's rows in document order.
    expect(tool!.compareDocumentPosition(think!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('limits the tree to onlyNodes with their ancestors and descendants', () => {
    render(Transcript, { store: stackStore(), density: 'steps', onlyNodes: new Set(['step:1']) })
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.getByText('Read file')).toBeTruthy()
    expect(screen.getByText('3/9 AccessibleModal')).toBeTruthy()
    expect(screen.queryByText('Patching the handler')).toBeNull()
    expect(screen.queryByText('fix the bug')).toBeNull()
  })
})
