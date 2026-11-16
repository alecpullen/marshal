import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, fireEvent, waitFor } from '@testing-library/svelte'
import AgentConversation from './AgentConversation.svelte'
import * as api from '../api.js'

/*
  The embeddable conversation is the whole of Chat with two behaviours added:
  it owns (and disconnects) the store for the session it was given, and inside
  a factory item it must not own the URL, the dock, or the session's ending.
  The api mock keeps the session off the network while the real APIError comes
  along, exactly as Chat.test does for the full page.
*/
vi.mock('../api.js', async (importActual) => {
  const actual = await importActual<typeof import('../api.js')>()
  return {
    ...actual,
    listAgents: vi.fn(),
    loadSession: vi.fn(),
    getStack: vi.fn(),
    cancelSession: vi.fn(),
    resolvePermission: vi.fn(),
    resolveQuestion: vi.fn(),
    getNode: vi.fn(),
    getLastRequest: vi.fn(),
    getDiff: vi.fn(),
    getGate: vi.fn(),
    getStepDiffs: vi.fn(),
    listFiles: vi.fn(),
    readFile: vi.fn(),
    getAccess: vi.fn(),
    patchAccess: vi.fn(),
  }
})

/*
  The store only needs connect/disconnect to exist; the returned unsubscribe
  is what proves the conversation lets go of the session it owned.
*/
const { unsub } = vi.hoisted(() => ({ unsub: vi.fn() }))
vi.mock('../sse.js', () => ({
  connectSSE: vi.fn(() => unsub),
}))
import { connectSSE } from '../sse.js'

vi.mock('../markdown.js', () => ({
  renderMarkdown: vi.fn((s: string) => s),
  initHighlighter: vi.fn(() => Promise.resolve()),
}))

afterEach(cleanup)

/*
  One turn with a step in it, a task band so the progress row is a handle, and
  a file.read call so a row names a file. The band comes first among the turn's
  children so the browse cursor lands on the tool row at the end.
*/
function stack(nodeExtra: object[] = [], stepChildren: string[] = []) {
  return {
    rev: 1,
    roots: ['turn:1'],
    nodes: [
      { id: 'turn:1', kind: 'turn', children: ['band', 'msg:1', 'step:1'] },
      {
        id: 'band',
        kind: 'band',
        parent: 'turn:1',
        band: {
          todos: true,
          done: 1,
          total: 2,
          active: 'Second',
          tasks: [
            { id: 'a', content: 'First', status: 'completed' },
            { id: 'b', content: 'Second', status: 'in_progress' },
          ],
        },
      },
      { id: 'msg:1', kind: 'message', parent: 'turn:1', message: { role: 'user', content: 'go' } },
      { id: 'step:1', kind: 'step', parent: 'turn:1', live: true, children: stepChildren, step: { headline: 'Working on it', startedAt: 1 } },
      ...nodeExtra,
    ],
  }
}

const withFile = () =>
  stack([{ id: 'tool:1', kind: 'tool', parent: 'step:1', tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'a.ts' }] } }], ['tool:1'])

describe('AgentConversation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ;(api.listAgents as Mock).mockResolvedValue([])
    ;(api.loadSession as Mock).mockResolvedValue({ events: [] })
    // Unless a test says otherwise the stack is unavailable, so the legacy
    // list renders and the header has nothing to lay out around it.
    ;(api.getStack as Mock).mockRejectedValue(new Error('no stack'))
    ;(api.getNode as Mock).mockResolvedValue({ node: {}, detail: {} })
    ;(api.getLastRequest as Mock).mockResolvedValue('unsupported')
    ;(api.getDiff as Mock).mockResolvedValue({ files: [] })
    ;(api.getGate as Mock).mockResolvedValue(null)
    ;(api.getStepDiffs as Mock).mockResolvedValue([])
    ;(api.listFiles as Mock).mockResolvedValue({ entries: [] })
    ;(api.getAccess as Mock).mockResolvedValue({
      mode: 'act',
      auto: false,
      system_access: false,
      access_revision: 1,
    })
    try {
      localStorage.clear()
      sessionStorage.clear()
    } catch {
      // not available
    }
    location.hash = ''
  })

  it('ownsAndDisconnectsSelectedSession', async () => {
    ;(api.getStack as Mock).mockResolvedValue(stack())

    const first = render(AgentConversation, { sessionId: 's1', embedded: true, onBack: () => {}, onOpenFull: () => {} })
    await screen.findByTestId('now-bar')

    // The store is the one for the session the conversation was given: its
    // stream, its snapshot, its replay.
    expect((connectSSE as Mock).mock.calls[0][0]).toMatchObject({ sessionId: 's1' })
    expect(api.loadSession).toHaveBeenCalledWith('s1', '/')
    expect(api.getStack).toHaveBeenCalledWith('s1', undefined)

    first.unmount()
    // Nothing of the previous session survives the conversation that owned it.
    expect(unsub).toHaveBeenCalledTimes(1)

    /*
      A parent that swaps agents keys on the id, so the next agent is a fresh
      store connected on its own id — the reason the key exists at all.
    */
    const second = render(AgentConversation, { sessionId: 's2', embedded: true, onBack: () => {}, onOpenFull: () => {} })
    await waitFor(() => expect(connectSSE).toHaveBeenCalledTimes(2))
    expect((connectSSE as Mock).mock.calls[1][0]).toMatchObject({ sessionId: 's2' })
    expect(api.loadSession).toHaveBeenCalledWith('s2', '/')

    second.unmount()
    expect(unsub).toHaveBeenCalledTimes(2)
  })

  it('doesNotWriteChatHash', async () => {
    location.hash = '#factory/brief-1'

    // Embedded, the hash belongs to the factory item: the dock state it was
    // handed changes the layout, never the address bar.
    render(AgentConversation, {
      sessionId: 's1',
      embedded: true,
      onBack: () => {},
      onOpenFull: () => {},
      route: { id: 's1', view: 'session', dock: 'expanded', tab: 'files', from: '#factory/brief-1' },
    })
    expect(await screen.findByText('No messages yet.')).toBeTruthy()
    await waitFor(() => expect(api.listAgents).toHaveBeenCalled())
    expect(location.hash).toBe('#factory/brief-1')

    // The full session still tracks its dock — and carries the factory return
    // it was opened from, so a dock change cannot erase the way back.
    cleanup()
    location.hash = ''
    render(AgentConversation, {
      sessionId: 's1',
      onBack: () => {},
      route: { id: 's1', view: 'session', from: '#factory/brief-1' },
    })
    await waitFor(() => expect(location.hash).toContain('from=%23factory%2Fbrief-1'))
    expect(location.hash.startsWith('#chat/s1')).toBe(true)
  })

  it('hidesDockAndExitPanel', async () => {
    // An isolated agent is the one whose exit panel has something to offer, so
    // its absence in an embedded copy is a real absence rather than an empty
    // panel that renders nothing anyway.
    ;(api.listAgents as Mock).mockResolvedValue([
      { id: 's1', project: '/p', status: 'idle', updatedAt: '', isolated: true, sourceKind: 'git', branch: 'marshal/one', readOnly: false },
    ])
    ;(api.getStack as Mock).mockResolvedValue(stack())

    render(AgentConversation, { sessionId: 's1', embedded: true, onBack: () => {}, onOpenFull: () => {} })
    await screen.findByTestId('now-bar')

    expect(screen.queryByTestId('dock')).toBeNull()
    expect(screen.queryByTestId('dock-strip')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Share status' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull()
    // The conversation itself is all there: transcript, composer, modes.
    expect(screen.getByTestId('transcript')).toBeTruthy()
    expect(screen.getByPlaceholderText('Ask Marshal…')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Act' })).toBeTruthy()

    // The same session on its own page keeps the dock and the exit panel.
    cleanup()
    render(AgentConversation, { sessionId: 's1', onBack: () => {} })
    expect(await screen.findByTestId('dock')).toBeTruthy()
    expect(await screen.findByRole('button', { name: 'Push' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Share status' })).toBeTruthy()
  })

  it('backReturnsToActivity', async () => {
    const onBack = vi.fn()
    const onOpenFull = vi.fn()
    render(AgentConversation, { sessionId: 's1', embedded: true, onBack, onOpenFull })
    expect(await screen.findByText('No messages yet.')).toBeTruthy()

    // The factory item is where this conversation lives; its own page is a
    // separate destination, not a fallback for the back button.
    expect(screen.queryByRole('button', { name: '← Fleet' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Review' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Network' })).toBeNull()

    await fireEvent.click(screen.getByRole('button', { name: '← Activity' }))
    expect(onBack).toHaveBeenCalledTimes(1)

    await fireEvent.click(screen.getByRole('button', { name: 'Open full session' }))
    expect(onOpenFull).toHaveBeenCalledTimes(1)

    // Without onOpenFull the copy still works; it just does not offer a door
    // it cannot open.
    cleanup()
    render(AgentConversation, { sessionId: 's1', embedded: true, onBack: () => {} })
    expect(await screen.findByText('No messages yet.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Open full session' })).toBeNull()
    expect(screen.getByRole('button', { name: '← Activity' })).toBeTruthy()
  })

  it('taskAffordanceOpensFullSessionTasks', async () => {
    const onOpenFull = vi.fn()
    ;(api.getStack as Mock).mockResolvedValue(withFile())
    render(AgentConversation, { sessionId: 's1', embedded: true, onBack: () => {}, onOpenFull })

    // The progress row is the task list's handle; there is no dock here to
    // open it into, so it hands the reader to the full session's Tasks tab.
    await fireEvent.click(await screen.findByTestId('band-progress'))
    expect(onOpenFull).toHaveBeenCalledWith('tasks')
    expect(screen.queryByRole('tab', { name: 'Tasks' })).toBeNull()

    // Browse's other dock-bound rows take the same route rather than
    // dispatching selections into an invisible dock.
    await fireEvent.keyDown(screen.getByPlaceholderText('Ask Marshal…'), { key: 'Escape' })
    await screen.findByText(/browse · j\/k move/)
    await fireEvent.keyDown(document.body, { key: 'i' })
    expect(onOpenFull).toHaveBeenLastCalledWith('inspect')
    await fireEvent.keyDown(document.body, { key: 'o' })
    expect(onOpenFull).toHaveBeenLastCalledWith('files')
  })

  it('pendingPermissionAndQuestionStillResolve', async () => {
    ;(api.getStack as Mock).mockResolvedValue(stack())
    render(AgentConversation, { sessionId: 's1', embedded: true, onBack: () => {}, onOpenFull: () => {} })
    await screen.findByTestId('now-bar')

    const call = (connectSSE as Mock).mock.calls.at(-1)![0] as { onEvent: (e: unknown) => void }
    const emit = (payload: unknown) => call.onEvent({ type: 'message', message: { id: 1, data: JSON.stringify(payload) } })

    emit({
      type: 'permission_request',
      toolCallId: 'tc-1',
      params: { toolCallId: 'tc-1', toolName: 'shell.run', command: 'rm -rf build' },
    })
    expect(await screen.findByText('Permission request')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(api.resolvePermission).toHaveBeenCalledWith('tc-1', { approved: true, edited: undefined }))

    emit({
      type: 'question_request',
      questionId: 'q-1',
      sessionId: 's1',
      params: { questionId: 'q-1', questions: [{ question: 'Proceed?', options: [{ label: 'Yes', value: 'yes' }] }] },
    })
    expect(await screen.findByText('Proceed?')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    await waitFor(() =>
      expect(api.resolveQuestion).toHaveBeenCalledWith('q-1', { answers: [{ question: 'Proceed?', answer: 'yes' }] }),
    )
  })
})
