import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, fireEvent, waitFor } from '@testing-library/svelte'
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
      expect(await screen.findByText('Reading the code')).toBeTruthy()
      expect(screen.getByText('do the thing')).toBeTruthy()
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
      await screen.findByText('Reading the code')
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
      expect(await screen.findByText('Running the tests')).toBeTruthy()
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

    it('enters browse mode on Esc in the composer and never cancels', async () => {
      ;(api.getStack as Mock).mockResolvedValue(turn(true))
      render(Chat, { sessionId: 's1', onBack: () => {} })
      await screen.findByTestId('now-bar')
      await fireEvent.keyDown(screen.getByPlaceholderText('Ask Marshal…'), { key: 'Escape' })
      expect(await screen.findByText(/browse · j\/k move/)).toBeTruthy()
      expect(api.cancelSession).not.toHaveBeenCalled()
    })
  })
})
