import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { writable } from 'svelte/store'
import Transcript from './Transcript.svelte'
import { BEAT_ROW_CAP, type StackState, type WireNode } from '../stack'

afterEach(cleanup)

// A turn of two beats. The first is finished (the wire marks it folded) and
// carries the todo label for the work it moves on to; the second is still
// live, so it never folds.
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
      folded: true,
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
  { id: 'step:2', kind: 'step', parent: 'turn:1', live: true, children: ['tool:3'], step: { headline: 'Patching the handler', todoId: 'p', folded: true } },
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

// A job card and a completion marker: the card is the shell.run that started
// the job, and the marker records the moment it finished, at the end of the
// beat that was running when it did. The two sit in different places.
const jobNodes: WireNode[] = [
  { id: 'turn:4', kind: 'turn', children: ['step:11', 'marker:tool:11:j'] },
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
      job: { id: 'job-4', command: 'pytest -q --junitxml=/tmp/a.xml', running: true, exitCode: 0 },
    },
  },
  {
    id: 'marker:tool:11:j',
    kind: 'marker',
    parent: 'turn:4',
    marker: { refs: ['tool:11'], labels: ['#3 Task 1'], durationMs: 81000 },
  },
]

const jobStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:4'], nodes: new Map(jobNodes.map((n) => [n.id, n])) })

// The same job, settled: the card carries the outcome and the marker says it
// failed.
const settledJobNodes: WireNode[] = jobNodes.map((n) =>
  n.id === 'tool:11'
    ? { ...n, tool: { ...n.tool!, job: { id: 'job-4', command: 'pytest -q', running: false, exitCode: 2, durationMs: 122000, output: 'undefined reference to foo' } } }
    : n.id === 'marker:tool:11:j'
      ? { ...n, marker: { refs: ['tool:11'], labels: ['job-4'], failed: true } }
      : n,
)

const settledJobStore = () =>
  writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:4'], nodes: new Map(settledJobNodes.map((n) => [n.id, n])) })

describe('Transcript', () => {
  it('shows the prompt, steps, tool rows and receipt at the steps density', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: false })
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
    render(Transcript, { store: stackStore(), density: 'outline', foldTasks: false })
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.queryByText('Read file')).toBeNull()
    expect(screen.queryByText('It looks at main.go first.')).toBeNull()
  })

  it('expands a merged run to one line per call and renders rest as markdown at full', () => {
    render(Transcript, { store: stackStore(), density: 'full', foldTasks: false })
    expect(screen.getByText('a.go')).toBeTruthy()
    expect(screen.getByText('c.go')).toBeTruthy()
    expect(screen.getByText('not found')).toBeTruthy()
    expect(screen.getByText('Then the tests.', { exact: false })).toBeTruthy()
  })

  it('marks the beat that moves work to another todo with a divider', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: false })
    // Only the transition beat carries a label, so exactly one divider shows.
    expect(screen.getAllByText('3/9 AccessibleModal')).toHaveLength(1)
  })

  it('folds a finished beat to its header but not the live one', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: true })
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.queryByText('Read file')).toBeNull()
    expect(screen.queryByText('It looks at main.go first.')).toBeNull()
    // A live beat never folds, even when the wire marked it folded.
    expect(screen.getByText('Write file')).toBeTruthy()
  })

  it('unfolds a folded beat when the reader opens its id', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: true, unfolded: new Set(['step:1']) })
    expect(screen.getByText('Read file')).toBeTruthy()
    expect(screen.getByText('It looks at main.go first.')).toBeTruthy()
  })

  it('applies a per-node override down the subtree', () => {
    render(Transcript, { store: stackStore(), density: 'outline', foldTasks: false, overrides: new Map([['step:1', 'steps' as const]]) })
    expect(screen.getByText('Read file')).toBeTruthy()
  })

  it('marks the cursor node', () => {
    const { container } = render(Transcript, { store: stackStore(), density: 'steps', foldTasks: false, cursor: 'step:1' })
    expect(container.querySelector('[data-node-id="step:1"]')?.className).toContain('border-violet')
  })

  it('renders a narrationless beat as rows under the turn, with no inferred tag and no header', () => {
    const { container } = render(Transcript, { store: beatStore(), density: 'steps', foldTasks: false })
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
    render(Transcript, { store: longBeatStore(), density: 'steps', foldTasks: false })
    expect(screen.getByText('+2 earlier calls')).toBeTruthy()
    // The oldest rows are the ones that collapse.
    expect(screen.queryByText('call-0.go')).toBeNull()
    expect(screen.queryByText('call-1.go')).toBeNull()
    expect(screen.getByText(`call-${BEAT_ROW_CAP + 1}.go`)).toBeTruthy()
  })

  it('renders a job card on one line with its outcome', () => {
    const { container } = render(Transcript, { store: jobStore(), density: 'steps', foldTasks: false })
    const card = container.querySelector('[data-node-id="tool:11"]')
    expect(card).toBeTruthy()
    expect(card?.textContent).toContain('job-4')
    expect(card?.textContent).toContain('pytest -q')
    expect(card?.textContent).toContain('running')
  })

  it('settles a job card in place and shows its output only when open', () => {
    render(Transcript, { store: settledJobStore(), density: 'steps', foldTasks: false })
    expect(screen.getByText(/exit 2/)).toBeTruthy()
    expect(screen.queryByText(/undefined reference to foo/)).toBeNull()
    cleanup()
    render(Transcript, { store: settledJobStore(), density: 'full', foldTasks: false })
    expect(screen.getByText(/undefined reference to foo/)).toBeTruthy()
  })

  it('renders a completion marker as one line, and jumps to its card when clicked', async () => {
    const onSelect = vi.fn()
    render(Transcript, { store: jobStore(), density: 'steps', foldTasks: false, onSelect })
    expect(screen.getByText(/#3 Task 1 done/)).toBeTruthy()
    await userEvent.click(screen.getByText(/#3 Task 1 done/))
    expect(onSelect).toHaveBeenCalledWith('tool:11')
  })

  it('limits the tree to onlyNodes with their ancestors and descendants', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: false, onlyNodes: new Set(['step:1']) })
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.getByText('Read file')).toBeTruthy()
    expect(screen.getByText('3/9 AccessibleModal')).toBeTruthy()
    expect(screen.queryByText('Patching the handler')).toBeNull()
    expect(screen.queryByText('fix the bug')).toBeNull()
  })
})
