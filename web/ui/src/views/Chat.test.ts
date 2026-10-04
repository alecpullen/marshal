import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, fireEvent, waitFor, within } from '@testing-library/svelte'
import Chat from './Chat.svelte'
import * as api from '../lib/api.js'

/*
  Chat is the whole room: header, transcript, composer, exit panel, and a
  session store whose load() is the bridge's verdict on whether the
  routed session still maps to a live agent. The api mock keeps all of
  that off fetch while the real APIError comes along, so the 404 the
  component matches with instanceof is the same class the mock throws.
  listAgents resolving [] also keeps ExitPanel from finding an isolated
  agent, so it renders nothing rather than a diff view.
*/
vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    listAgents: vi.fn(),
    loadSession: vi.fn(),
    getStack: vi.fn(),
    cancelSession: vi.fn(),
    getNode: vi.fn(),
    getLastRequest: vi.fn(),
    getDiff: vi.fn(),
    getGate: vi.fn(),
    getStepDiffs: vi.fn(),
    listFiles: vi.fn(),
    readFile: vi.fn(),
  }
})

/*
  The real connectSSE opens a fetch-and-reconnect loop; the store only
  needs the connect/disconnect pair to exist. The chat SSE stream is not
  what this test is about.
*/
vi.mock('../lib/sse.js', () => ({
  connectSSE: vi.fn(() => () => {}),
}))
import { connectSSE } from '../lib/sse.js'

/*
  Shiki's grammars are a heavyweight async load with nothing to do with
  the 404 path, and the transcript it highlights is empty here anyway.
*/
vi.mock('../lib/markdown.js', () => ({
  renderMarkdown: vi.fn((s: string) => s),
  initHighlighter: vi.fn(() => Promise.resolve()),
}))

afterEach(cleanup)

describe('Chat', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ;(api.listAgents as Mock).mockResolvedValue([])
    ;(api.loadSession as Mock).mockResolvedValue({ events: [] })
    // Unless a test says otherwise the stack is unavailable, so the legacy
    // list renders, which is what the tests above exercise.
    ;(api.getStack as Mock).mockRejectedValue(new Error('no stack'))
    ;(api.getNode as Mock).mockResolvedValue({ node: {}, detail: {} })
    ;(api.getLastRequest as Mock).mockResolvedValue('unsupported')
    ;(api.getDiff as Mock).mockResolvedValue({ files: [] })
    ;(api.getGate as Mock).mockResolvedValue(null)
    ;(api.getStepDiffs as Mock).mockResolvedValue([])
    ;(api.listFiles as Mock).mockResolvedValue({ entries: [] })
    try {
      localStorage.clear()
    } catch {
      // not available
    }
  })

  it('replaces the transcript with a could-not-be-resumed note when the load 404s', async () => {
    ;(api.loadSession as Mock).mockRejectedValue(
      new api.APIError(404, { error: 'no such session' }),
    )

    render(Chat, { sessionId: 'gone', onBack: () => {} })

    expect(
      await screen.findByText(
        'This session could not be resumed — its agent is no longer tracked by the bridge.',
      ),
    ).toBeTruthy()
    // The header stays reachable so the operator is not stranded in the note.
    expect(screen.getByRole('button', { name: '← Fleet' })).toBeTruthy()
    // The transcript itself is replaced, not supplemented.
    expect(screen.queryByText('No messages yet.')).toBeNull()
  })

  it('keeps the ordinary empty transcript when the load fails for a non-404 reason', async () => {
    ;(api.loadSession as Mock).mockRejectedValue(new api.APIError(500, { error: 'boom' }))

    render(Chat, { sessionId: 's1', onBack: () => {} })

    expect(await screen.findByText('No messages yet.')).toBeTruthy()
    expect(
      screen.queryByText(
        'This session could not be resumed — its agent is no longer tracked by the bridge.',
      ),
    ).toBeNull()
  })

  describe('stack transcript', () => {
    const turn = (live: boolean, extra: object[] = []) => ({
      rev: 1,
      roots: ['turn:1'],
      nodes: [
        { id: 'turn:1', kind: 'turn', children: ['msg:1', 'step:1', ...extra.map((n) => (n as { id: string }).id)] },
        { id: 'msg:1', kind: 'message', parent: 'turn:1', message: { role: 'user', content: 'do the thing' } },
        { id: 'step:1', kind: 'step', parent: 'turn:1', live, step: { headline: 'Reading the code', owner: 'implementer', role: 'implementer', startedAt: 1 } },
        ...extra,
      ],
    })

    function sse() {
      const call = (connectSSE as Mock).mock.calls.at(-1)![0] as { onEvent: (e: unknown) => void }
      return (payload: unknown) => call.onEvent({ type: 'message', message: { data: JSON.stringify(payload) } })
    }

    it('renders the stack transcript when the snapshot loads', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(false))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      const transcript = await screen.findByTestId('transcript')
      expect(await within(transcript).findByText('Reading the code')).toBeTruthy()
      expect(within(transcript).getByText('do the thing')).toBeTruthy()
      expect(screen.getByRole('group', { name: 'Detail' })).toBeTruthy()
    })

    it('falls back to the legacy list when the agent has no stack', async () => {
      ;(api.getStack as Mock).mockResolvedValue('unsupported')
      render(Chat, { sessionId: 's1', onBack: () => {} })
      expect(await screen.findByText('No messages yet.')).toBeTruthy()
      expect(screen.queryByTestId('transcript')).toBeNull()
    })

    it('updates the DOM from a stack_patch event', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(false))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await within(await screen.findByTestId('transcript')).findByText('Reading the code')
      sse()({
        method: 'session/update',
        params: {
          sessionId: 's1',
          update: {
            kind: 'stack_patch',
            rev: 2,
            baseRev: 1,
            roots: ['turn:1'],
            upsert: [
              { id: 'turn:1', kind: 'turn', children: ['msg:1', 'step:1', 'step:2'] },
              { id: 'step:2', kind: 'step', parent: 'turn:1', step: { headline: 'Running the tests' } },
            ],
          },
        },
      })
      expect(await within(screen.getByTestId('transcript')).findByText('Running the tests')).toBeTruthy()
    })

    it('stops the turn on a second Ctrl+C within a second, and only then', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(true))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('now-bar')
      await fireEvent.keyDown(document, { key: 'c', ctrlKey: true })
      expect(api.cancelSession).not.toHaveBeenCalled()
      expect(await screen.findByText('Press Ctrl+C again to stop')).toBeTruthy()
      await fireEvent.keyDown(document, { key: 'c', ctrlKey: true })
      await waitFor(() => expect(api.cancelSession).toHaveBeenCalledTimes(1))
    })

    it('leaves Ctrl+C alone when text is selected in the composer', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(true))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('now-bar')
      const box = screen.getByPlaceholderText('Ask Marshal…') as HTMLTextAreaElement
      box.value = 'some text'
      box.setSelectionRange(0, 4)
      const notPrevented = await fireEvent.keyDown(box, { key: 'c', ctrlKey: true })
      expect(notPrevented).toBe(true)
      expect(screen.queryByText('Press Ctrl+C again to stop')).toBeNull()
    })

    it('keeps j at the last row in browse mode', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(true))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('now-bar')
      await fireEvent.keyDown(screen.getByPlaceholderText('Ask Marshal…'), { key: 'Escape' })
      await screen.findByText(/browse · j\/k move/)
      await fireEvent.keyDown(document.body, { key: 'j' })
      expect(screen.getByText(/browse · j\/k move/)).toBeTruthy()
    })

    it('enters browse mode on Esc in the composer and never cancels', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(true))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('now-bar')
      await fireEvent.keyDown(screen.getByPlaceholderText('Ask Marshal…'), { key: 'Escape' })
      expect(await screen.findByText(/browse · j\/k move/)).toBeTruthy()
      expect(api.cancelSession).not.toHaveBeenCalled()
    })
  })

  describe('session dock', () => {
    const tree = (extra: object[] = [], stepChildren: string[] = []) => ({
      rev: 1,
      roots: ['turn:1'],
      nodes: [
        { id: 'turn:1', kind: 'turn', children: ['msg:1', 'step:1'] },
        { id: 'msg:1', kind: 'message', parent: 'turn:1', message: { role: 'user', content: 'go' } },
        { id: 'step:1', kind: 'step', parent: 'turn:1', live: true, children: stepChildren, step: { headline: 'Working on it', startedAt: 1 } },
        ...extra,
      ],
    })
    const withSub = () =>
      tree([{ id: 'sub:3', kind: 'subagent', parent: 'step:1', subagent: { label: 'reviewer', status: 'running' } }], ['sub:3'])
    const childTree = {
      rev: 1,
      roots: ['turn:9'],
      nodes: [
        { id: 'turn:9', kind: 'turn', children: ['step:9'] },
        { id: 'step:9', kind: 'step', parent: 'turn:9', step: { headline: 'Inside the subagent' } },
      ],
    }
    const browse = async () => {
      await screen.findByTestId('now-bar')
      await fireEvent.keyDown(screen.getByPlaceholderText('Ask Marshal…'), { key: 'Escape' })
      await screen.findByText(/browse · j\/k move/)
    }

    it('renders the dock docked by default, with Terminal disabled', async () => {
      ;(api.getStack as Mock).mockResolvedValue(tree())
      render(Chat, { sessionId: 's1', onBack: () => {} })
      expect((await screen.findByTestId('dock')).getAttribute('data-size')).toBe('docked')
      expect((screen.getByRole('tab', { name: 'Terminal' }) as HTMLButtonElement).disabled).toBe(true)
      expect((screen.getByRole('tab', { name: 'Preview' }) as HTMLButtonElement).disabled).toBe(true)
    })

    it('renders the strip when the stored size is collapsed', async () => {
      localStorage.setItem('marshal.ui.dock', JSON.stringify({ size: 'collapsed', width: 440 }))
      ;(api.getStack as Mock).mockResolvedValue(tree())
      render(Chat, { sessionId: 's1', onBack: () => {} })
      expect(await screen.findByTestId('dock-strip')).toBeTruthy()
      expect(screen.queryByTestId('dock')).toBeNull()
    })

    it('i selects the cursor row and opens Inspect; Back to live clears it', async () => {
      ;(api.getStack as Mock).mockResolvedValue(tree())
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await browse()
      await fireEvent.keyDown(document.body, { key: 'i' })
      expect(await screen.findByRole('button', { name: 'Back to live' })).toBeTruthy()
      expect(screen.getByRole('tab', { name: 'Inspect' }).getAttribute('aria-selected')).toBe('true')
      expect(screen.getByTestId('mirror-row').textContent).toContain('Working on it')
      await fireEvent.click(screen.getByRole('button', { name: 'Back to live' }))
      await waitFor(() => expect(screen.queryByRole('button', { name: 'Back to live' })).toBeNull())
      expect(screen.getByText('following agent')).toBeTruthy()
    })

    it('⌘J cycles the dock size', async () => {
      ;(api.getStack as Mock).mockResolvedValue(tree())
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('dock')
      await fireEvent.keyDown(document.body, { key: 'j', metaKey: true })
      await waitFor(() => expect(screen.getByTestId('dock').getAttribute('data-size')).toBe('expanded'))
      await fireEvent.keyDown(document.body, { key: 'j', ctrlKey: true })
      expect(await screen.findByTestId('dock-strip')).toBeTruthy()
    })

    it('⌘P pins without opening the print dialog', async () => {
      ;(api.getStack as Mock).mockResolvedValue(tree())
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('dock')
      const notPrevented = await fireEvent.keyDown(document.body, { key: 'p', metaKey: true })
      expect(notPrevented).toBe(false)
      expect(screen.getByRole('button', { name: 'Pinned' })).toBeTruthy()
    })

    it('f drills into a subagent, shows the breadcrumb, and Backspace returns', async () => {
      ;(api.getStack as Mock).mockImplementation(async (_id: string, sub?: number) => (sub === 3 ? childTree : withSub()))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await browse()
      await fireEvent.keyDown(document.body, { key: 'f' })
      const crumbs = await screen.findByRole('navigation', { name: 'Transcript path' })
      expect(within(crumbs).getByText('reviewer')).toBeTruthy()
      expect(api.getStack).toHaveBeenCalledWith('s1', 3)
      expect(await within(screen.getByTestId('transcript')).findByText('Inside the subagent')).toBeTruthy()
      await fireEvent.keyDown(document.body, { key: 'Backspace' })
      await waitFor(() => expect(screen.queryByRole('navigation', { name: 'Transcript path' })).toBeNull())
      expect(within(screen.getByTestId('transcript')).getByText('Working on it')).toBeTruthy()
    })

    it('stays on the card when a subagent has no transcript of its own', async () => {
      ;(api.getStack as Mock).mockImplementation(async (_id: string, sub?: number) => {
        if (sub === 3) throw new api.APIError(400, { error: 'no separate transcript' })
        return withSub()
      })
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await browse()
      await fireEvent.keyDown(document.body, { key: 'f' })
      expect(await screen.findByText('This subagent has no separate transcript')).toBeTruthy()
      expect(screen.queryByRole('navigation', { name: 'Transcript path' })).toBeNull()
    })

    it('restores the dock state from the route', async () => {
      ;(api.getStack as Mock).mockResolvedValue(tree())
      render(Chat, { sessionId: 's1', onBack: () => {}, route: { id: 's1', view: 'session', node: 'step:1', dock: 'expanded', tab: 'files' } })
      expect((await screen.findByTestId('dock')).getAttribute('data-size')).toBe('expanded')
      expect(screen.getByRole('tab', { name: 'Files' }).getAttribute('aria-selected')).toBe('true')
      expect(screen.getByRole('button', { name: 'Back to live' })).toBeTruthy()
    })
  })

  describe('workspace tag', () => {
    it('links a Studio workspace to its designer in the header', async () => {
      const agent = { id: 's1', project: '/p', status: 'idle', updatedAt: '', workspace: { name: 'go-service', version: 2, source: 'studio' } } as never
      render(Chat, { sessionId: 's1', onBack: () => {}, agent })
      const tag = await screen.findByText('▦ go-service@v2')
      expect(tag.getAttribute('href')).toBe('#workspaces/go-service/edit')
    })

    it('shows a repo workspace as plain text, since only Studio templates have a designer', async () => {
      const agent = { id: 's1', project: '/p', status: 'idle', updatedAt: '', workspace: { name: 'ci', version: 1, source: 'repo' } } as never
      render(Chat, { sessionId: 's1', onBack: () => {}, agent })
      const tag = await screen.findByText('▦ ci@v1')
      expect(tag.getAttribute('href')).toBeNull()
    })

    it('shows nothing for an agent without a workspace', async () => {
      render(Chat, { sessionId: 's1', onBack: () => {}, agent: { id: 's1', project: '/p', status: 'idle', updatedAt: '' } as never })
      await screen.findByText('No messages yet.')
      expect(screen.queryByText(/▦/)).toBeNull()
    })
  })
})
