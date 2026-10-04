import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import * as api from '../api.js'
import InspectTab from './InspectTab.svelte'
import ChangesTab from './ChangesTab.svelte'
import FilesTab from './FilesTab.svelte'
import GateStrip from './GateStrip.svelte'
import { createNodeCache } from './nodeCache'
import { initialDock } from './dock'
import type { StackState, WireNode } from '../stack'

vi.mock('../api.js', async (importActual) => {
  const actual = await importActual<typeof import('../api.js')>()
  return {
    ...actual,
    getNode: vi.fn(),
    getLastRequest: vi.fn(),
    getDiff: vi.fn(),
    getGate: vi.fn(),
    runGate: vi.fn(),
    getStepDiffs: vi.fn(),
    listFiles: vi.fn(),
    readFile: vi.fn(),
  }
})

afterEach(cleanup)

const stackOf = (nodes: WireNode[], roots = ['turn:1']): StackState => ({
  status: 'ready',
  rev: 1,
  roots,
  nodes: new Map(nodes.map((n) => [n.id, n])),
})

const nodes: WireNode[] = [
  { id: 'turn:1', kind: 'turn', children: ['step:1'] },
  { id: 'step:1', kind: 'step', parent: 'turn:1', children: ['tool:1', 'tool:2'], step: { headline: 'Fix the parser', owner: 'implementer', role: 'implementer' } },
  { id: 'tool:1', kind: 'tool', parent: 'step:1', tool: { name: 'shell.run', display: 'shell', calls: [{ target: 'go test', callId: 'c1', exitCode: 1, failed: true }] } },
  { id: 'tool:2', kind: 'tool', parent: 'step:1', tool: { name: 'file.write_patch', display: 'edit', calls: [{ target: 'p.go', files: ['p.go'] }] } },
]

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getGate as Mock).mockResolvedValue(null)
  ;(api.getLastRequest as Mock).mockResolvedValue('unsupported')
})

describe('InspectTab', () => {
  const props = (selected: string, over = {}) => ({
    sessionId: 's1',
    stack: stackOf(nodes),
    dock: initialDock({ mode: 'select', selected }),
    cache: createNodeCache(),
    onSelect: vi.fn(),
    ...over,
  })

  it('shows output longer than 4 KB in full', async () => {
    const big = 'x'.repeat(5000)
    ;(api.getNode as Mock).mockResolvedValue({ node: nodes[2], detail: { calls: [{ toolName: 'shell.run', output: big, exitCode: 1 }] } })
    render(InspectTab, props('tool:1'))
    await waitFor(() => expect(screen.getByTestId('inspect-body').textContent).toContain(big))
    expect(screen.getByText('failed')).toBeTruthy()
  })

  it('links relations to their nodes', async () => {
    ;(api.getNode as Mock).mockResolvedValue({ node: nodes[1], detail: { relations: { fixedBy: ['tool:2'] } } })
    const p = props('step:1')
    render(InspectTab, p)
    await fireEvent.click(await screen.findByRole('button', { name: 'edit p.go' }))
    expect(p.onSelect).toHaveBeenCalledWith('tool:2')
  })

  it('shows a diff for an edit call', async () => {
    ;(api.getNode as Mock).mockResolvedValue({ node: nodes[3], detail: { calls: [{ toolName: 'file.write_patch', diff: '+added\n-gone' }] } })
    render(InspectTab, props('tool:2'))
    expect(await screen.findByTestId('diff-lines')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Diff' })).toBeTruthy()
  })

  it('falls back to wire fields on an older agent', async () => {
    ;(api.getNode as Mock).mockResolvedValue('unsupported')
    render(InspectTab, props('tool:1'))
    expect(await screen.findByText('Full detail needs a newer agent.')).toBeTruthy()
  })

  it('keeps the previous detail up while a live node refetches', async () => {
    const detail = { calls: [{ toolName: 'file.write_patch', diff: '+added' }] }
    let release: (v: unknown) => void = () => {}
    ;(api.getNode as Mock).mockResolvedValueOnce({ node: nodes[3], detail }).mockReturnValueOnce(new Promise((r) => (release = r)))
    const p = props('tool:2')
    const { rerender } = render(InspectTab, p)
    await screen.findByTestId('diff-lines')
    // A patch gives the same node a new wire object, so the cache misses.
    const next = new Map(p.stack.nodes)
    next.set('tool:2', { ...nodes[3], live: true })
    await rerender({ ...p, stack: { ...p.stack, nodes: next } })
    await waitFor(() => expect(api.getNode).toHaveBeenCalledTimes(2))
    expect(screen.getByRole('button', { name: 'Diff' })).toBeTruthy()
    expect(screen.getByTestId('diff-lines')).toBeTruthy()
    release({ node: nodes[3], detail })
  })

  it('offers the last model request on the newest step only', async () => {
    ;(api.getNode as Mock).mockResolvedValue({ node: nodes[1], detail: {} })
    ;(api.getLastRequest as Mock).mockResolvedValue({
      attemptId: 1,
      provider: 'ollama',
      model: 'qwen',
      messages: [{ role: 'user', content: 'hi' }],
      tools: [],
      options: { streaming: true },
      outcome: { status: 'ok' },
      packKnown: true,
      packTokens: 10,
      packWindow: 100,
    })
    render(InspectTab, props('step:1'))
    const summary = await screen.findByText('Last model request')
    await fireEvent.click(summary)
    expect(await screen.findByText('ollama / qwen')).toBeTruthy()
    cleanup()
    render(InspectTab, props('tool:1'))
    await screen.findByTestId('inspect')
    expect(screen.queryByText('Last model request')).toBeNull()
  })
})

describe('GateStrip', () => {
  it('shows a failing command and runs the gate', async () => {
    const failed = { result: { ok: false, skipped: false, failedCommand: 'go test ./...' }, at: new Date().toISOString() }
    ;(api.runGate as Mock).mockResolvedValue({ result: { ok: true, skipped: false }, at: new Date(Date.now() + 1000).toISOString() })
    render(GateStrip, { agentId: 'a1', gate: failed })
    expect(screen.getByText(/gate failed: go test/)).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Run gate' }))
    expect(await screen.findByText('✓ gate passed')).toBeTruthy()
  })

  it('says a skipped gate proves nothing, and loads the stored one on mount', async () => {
    ;(api.getGate as Mock).mockResolvedValue({ result: { ok: true, skipped: true }, at: new Date().toISOString() })
    render(GateStrip, { agentId: 'a1' })
    expect(await screen.findByText('skipped — proves nothing')).toBeTruthy()
  })
})

describe('ChangesTab', () => {
  const base = { agentId: 'a1', sessionId: 's1', stack: stackOf(nodes) }

  it('lists the diff and opens the latest edited file in follow mode', async () => {
    ;(api.getDiff as Mock).mockImplementation(async (_id: string, path?: string) =>
      path ? { files: [], diff: '+new line' } : { files: [{ path: 'p.go', added: 1, removed: 0 }] },
    )
    render(ChangesTab, { ...base, dock: initialDock({ tab: 'changes' }) })
    expect(await screen.findByText('p.go')).toBeTruthy()
    expect(await screen.findByTestId('diff-lines')).toBeTruthy()
  })

  it('explains a non-isolated agent, with the telemetry count', async () => {
    ;(api.getDiff as Mock).mockRejectedValue(new api.APIError(502, { error: 'agent is not isolated' }))
    render(ChangesTab, { ...base, dock: initialDock({ tab: 'changes' }), changedFiles: 3 })
    expect(await screen.findByText(/Changes are tracked for isolated agents/)).toBeTruthy()
    expect(screen.getByText(/3 changed files/)).toBeTruthy()
  })

  it('says step changes are not tracked per subagent while drilled in', async () => {
    render(ChangesTab, { ...base, drilled: true, dock: initialDock({ tab: 'changes', mode: 'select', selected: 'step:9' }) })
    expect(await screen.findByText(/aren't tracked per subagent/)).toBeTruthy()
    expect(api.getStepDiffs).not.toHaveBeenCalled()
  })

  it('filters to the selected step in select mode', async () => {
    ;(api.getStepDiffs as Mock).mockResolvedValue([
      { stepNode: 'step:1', turnNode: 'turn:1', headline: 'Fix the parser', files: ['p.go'], diff: '+a' },
      { stepNode: 'step:2', turnNode: 'turn:1', headline: 'Other step', files: ['q.go'], diff: '+b' },
    ])
    render(ChangesTab, { ...base, dock: initialDock({ tab: 'changes', mode: 'select', selected: 'tool:2' }) })
    expect(await screen.findByRole('heading', { name: 'Fix the parser' })).toBeTruthy()
    expect(screen.queryByText('Other step')).toBeNull()
  })
})

describe('FilesTab', () => {
  it('opens a file at a line and highlights it', async () => {
    ;(api.listFiles as Mock).mockImplementation(async (_a: string, p: string) =>
      p === '' ? { entries: [{ name: 'src', dir: true, size: 0 }] } : { entries: [{ name: 'a.go', dir: false, size: 3 }] },
    )
    ;(api.readFile as Mock).mockResolvedValue({ path: 'src/a.go', size: 9, binary: false, truncated: false, content: 'one\ntwo\nthree' })
    render(FilesTab, { agentId: 'a1', stack: stackOf(nodes), dock: initialDock({ tab: 'files' }), request: { path: 'src/a.go', line: 2, seq: 1 } })
    await waitFor(() => expect(document.querySelector('[data-line="2"]')?.className).toContain('bg-accent'))
    expect(document.querySelector('[data-line="1"]')?.className).not.toContain('bg-accent')
    expect(await screen.findByRole('button', { name: /a\.go/ })).toBeTruthy()
  })

  it('ignores a read that resolves after a newer one', async () => {
    ;(api.listFiles as Mock).mockResolvedValue({ entries: [] })
    let slow: (v: unknown) => void = () => {}
    ;(api.readFile as Mock).mockImplementation((_a: string, path: string) =>
      path === 'a.go'
        ? new Promise((r) => (slow = r))
        : Promise.resolve({ path, size: 1, binary: false, truncated: false, content: 'file B' }),
    )
    const props = { agentId: 'a1', stack: stackOf(nodes), dock: initialDock({ tab: 'files' }) }
    const { rerender } = render(FilesTab, { ...props, request: { path: 'a.go', seq: 1 } })
    await rerender({ ...props, request: { path: 'b.go', seq: 2 } })
    await screen.findByText('file B')
    slow({ path: 'a.go', size: 1, binary: false, truncated: false, content: 'file A' })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('file A')).toBeNull()
    expect(screen.getByText('file B')).toBeTruthy()
  })

  it('notes a binary file', async () => {
    ;(api.listFiles as Mock).mockResolvedValue({ entries: [] })
    ;(api.readFile as Mock).mockResolvedValue({ path: 'x.bin', size: 4, binary: true, truncated: false, content: '' })
    render(FilesTab, { agentId: 'a1', stack: stackOf(nodes), dock: initialDock({ tab: 'files' }), request: { path: 'x.bin', seq: 1 } })
    expect(await screen.findByText(/binary file/)).toBeTruthy()
  })
})
