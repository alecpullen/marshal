import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import TerminalTab from './TerminalTab.svelte'
import { initialDock } from './dock'
import { createNodeCache } from './nodeCache'
import * as api from '../api'
import type { StackState } from '../stack'

const h = vi.hoisted(() => {
  const state = { term: null as null | { write: ReturnType<typeof vi.fn>; data: (s: string) => void; dispose: ReturnType<typeof vi.fn> }, sse: null as null | { url: string; onEvent: (e: unknown) => void; stop: ReturnType<typeof vi.fn> } }
  return state
})

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 24
    write = vi.fn()
    dispose = vi.fn()
    data: (s: string) => void = () => {}
    constructor() {
      h.term = this
    }
    loadAddon() {}
    open() {}
    onData(cb: (s: string) => void) {
      this.data = cb
    }
    onResize() {}
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('../sse', () => ({
  connectSSE: (o: { url: string; onEvent: (e: unknown) => void }) => {
    const stop = vi.fn()
    h.sse = { url: o.url, onEvent: o.onEvent, stop }
    return stop
  },
}))
vi.mock('../api', async (importActual) => {
  const actual = await importActual<typeof import('../api')>()
  return { ...actual, openTerminal: vi.fn(), terminalInput: vi.fn(), terminalResize: vi.fn(), terminalRelease: vi.fn(), closeTerminal: vi.fn() }
})

const stack = (nodes: StackState['nodes'] = new Map()): StackState => ({ rev: 1, roots: [], nodes }) as unknown as StackState
const msg = (data: unknown) => ({ type: 'message', message: { id: 1, data: JSON.stringify(data) } })

beforeEach(() => {
  h.term = null
  h.sse = null
  vi.mocked(api.openTerminal).mockResolvedValue({ terminalId: 't1' })
  vi.mocked(api.terminalInput).mockResolvedValue()
  vi.mocked(api.terminalRelease).mockResolvedValue()
  vi.mocked(api.closeTerminal).mockResolvedValue()
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const renderTab = (over: Record<string, unknown> = {}) =>
  render(TerminalTab, { agentId: 'a1', sessionId: 'a1', stack: stack(), dock: initialDock(), cache: createNodeCache(async () => 'unsupported'), ...over })

async function openShell() {
  await fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
  await waitFor(() => expect(h.sse).not.toBeNull())
}

describe('TerminalTab', () => {
  it('opens a shell on the agent and streams its events from the start', async () => {
    renderTab()
    await openShell()
    expect(api.openTerminal).toHaveBeenCalledWith('a1', { cols: 80, rows: 24 })
    expect(h.sse?.url).toBe('/api/agents/a1/terminal/t1/events')
  })

  it('posts typed input and writes decoded output', async () => {
    renderTab()
    await openShell()
    h.term!.data('ls\r')
    await waitFor(() => expect(api.terminalInput).toHaveBeenCalledWith({ agentId: 'a1' }, 't1', 'ls\r'))
    h.sse!.onEvent(msg({ data: btoa('hello') }))
    expect(new TextDecoder().decode(h.term!.write.mock.calls[0][0])).toBe('hello')
  })

  it('reports an exit and keeps the shell closable', async () => {
    renderTab()
    await openShell()
    h.sse!.onEvent(msg({ exit: 3 }))
    await waitFor(() => expect(screen.getByText('process exited (3)')).toBeTruthy())
    expect(h.sse!.stop).toHaveBeenCalled()
  })

  it('shows the hold strip and Hand back releases the agent', async () => {
    const view = renderTab({ held: false })
    expect(screen.queryByTestId('hold-strip')).toBeNull()
    await openShell()
    await view.rerender({ held: true })
    expect(screen.getByText('agent paused')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Hand back' }))
    expect(api.terminalRelease).toHaveBeenCalledWith('a1', 't1')
  })

  it('replays the selected shell call from the node cache', async () => {
    const node = { id: 'tool:2', kind: 'tool', parent: 'step:1', tool: { name: 'shell.run', display: 'Run command', calls: [{ target: 'go test ./...' }] } }
    const cache = createNodeCache(async () => ({ node, detail: { calls: [{ toolName: 'shell.run', output: 'ok pkg 0.1s' }] } }) as never)
    renderTab({
      stack: stack(new Map([['tool:2', node]]) as never),
      dock: initialDock({ mode: 'select', selected: 'tool:2' }),
      cache,
    })
    expect(screen.getByText('go test ./...')).toBeTruthy()
    await waitFor(() => expect(screen.getByText('ok pkg 0.1s')).toBeTruthy())
  })

  it('shows no replay for a non-shell node', () => {
    const node = { id: 'tool:2', kind: 'tool', tool: { name: 'file.read', display: 'Read', calls: [] } }
    renderTab({ stack: stack(new Map([['tool:2', node]]) as never), dock: initialDock({ mode: 'select', selected: 'tool:2' }) })
    expect(screen.queryByTestId('terminal-replay')).toBeNull()
  })
})
