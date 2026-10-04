import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { writable } from 'svelte/store'
import Transcript from './Transcript.svelte'
import type { StackState, WireNode } from '../stack'

afterEach(cleanup)

const nodes: WireNode[] = [
  { id: 'turn:1', kind: 'turn', children: ['msg:1', 'task:1', 'task:2', 'final:1', 'receipt:1'] },
  { id: 'msg:1', kind: 'message', parent: 'turn:1', message: { role: 'user', content: 'fix the bug' } },
  { id: 'task:1', kind: 'task', parent: 'turn:1', children: ['step:1'], task: { todoId: 't1', content: 'Find the bug', status: 'completed', index: 1, total: 2, steps: 1, tools: 3, edits: 0, workMs: 12000 } },
  { id: 'step:1', kind: 'step', parent: 'task:1', children: ['tool:1', 'tool:2'], step: { headline: 'Reading the handler', rest: 'It looks at main.go first.\nThen the tests.', role: 'implementer', owner: 'implementer', startedAt: 1000, endedAt: 4000 } },
  {
    id: 'tool:1',
    kind: 'tool',
    parent: 'step:1',
    tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'a.go', summary: '40 lines' }, { target: 'b.go', summary: '12 lines' }, { target: 'c.go', failed: true, error: 'not found' }] },
  },
  { id: 'tool:2', kind: 'tool', parent: 'step:1', tool: { name: 'shell.run', display: 'Run command', calls: [{ target: 'go test ./...', summary: 'ok', exitCode: 0 }] } },
  { id: 'task:2', kind: 'task', parent: 'turn:1', children: ['step:2'], task: { todoId: 't2', content: 'Fix it', status: 'in_progress', index: 2, total: 2, steps: 1, tools: 1, edits: 1 } },
  { id: 'step:2', kind: 'step', parent: 'task:2', children: [], step: { headline: 'Patching the handler' } },
  { id: 'final:1', kind: 'final', parent: 'turn:1', message: { role: 'assistant', content: 'All **done**.', final: true } },
  { id: 'receipt:1', kind: 'receipt', parent: 'turn:1', receipt: { durationMs: 400000, tasks: 2, steps: 2, tools: 4, files: 1, usage: '212k tok' } },
]

function stackStore() {
  return writable<StackState>({ status: 'ready', rev: 1, roots: ['turn:1'], nodes: new Map(nodes.map((n) => [n.id, n])) })
}

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

  it('folds a finished task but not the live one', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: true })
    expect(screen.getByText('Find the bug')).toBeTruthy()
    expect(screen.queryByText('Reading the handler')).toBeNull()
    expect(screen.getByText('1 step · 3 tools · 12s')).toBeTruthy()
    expect(screen.getByText('Patching the handler')).toBeTruthy()
  })

  it('opens a folded task through the toggle callback', async () => {
    const toggled: string[] = []
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: true, onToggleFold: (id: string) => toggled.push(id) })
    await userEvent.click(screen.getByText('Find the bug'))
    expect(toggled).toEqual(['task:1'])
  })

  it('applies a per-node override down the subtree', () => {
    render(Transcript, { store: stackStore(), density: 'outline', foldTasks: false, overrides: new Map([['step:1', 'steps' as const]]) })
    expect(screen.getByText('Read file')).toBeTruthy()
  })

  it('marks the cursor node', () => {
    const { container } = render(Transcript, { store: stackStore(), density: 'steps', foldTasks: false, cursor: 'step:1' })
    expect(container.querySelector('[data-node-id="step:1"]')?.className).toContain('border-violet')
  })

  it('limits the tree to onlyNodes with their ancestors and descendants', () => {
    render(Transcript, { store: stackStore(), density: 'steps', foldTasks: false, onlyNodes: new Set(['step:1']) })
    expect(screen.getByText('Reading the handler')).toBeTruthy()
    expect(screen.getByText('Read file')).toBeTruthy()
    expect(screen.getByText('Find the bug')).toBeTruthy()
    expect(screen.queryByText('Patching the handler')).toBeNull()
    expect(screen.queryByText('fix the bug')).toBeNull()
  })
})
