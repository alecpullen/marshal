import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest'
import { createSessionStore, type Mode, type SessionState } from './store.js'
import * as api from './api.js'

const emitters: Array<(e: { type: 'message'; message: { id: number; data: string } }) => void> = []

vi.mock('./sse.js', () => ({
  connectSSE: vi.fn(({ onEvent }: { onEvent: (e: unknown) => void }) => {
    const emit = (e: { type: 'message'; message: { id: number; data: string } }) => onEvent(e)
    emitters.push(emit)
    onEvent({ type: 'connected' })
    return () => {
      onEvent({ type: 'disconnected' })
      const idx = emitters.indexOf(emit)
      if (idx >= 0) emitters.splice(idx, 1)
    }
  }),
}))

vi.mock('./api.js', () => ({
  getToken: vi.fn(() => 'tok'),
  ensureToken: vi.fn(() => 'tok'),
  setToken: vi.fn(),
  AuthError: class extends Error {},
  APIError: class extends Error {
    status: number
    body: unknown
    constructor(status: number, body: unknown) {
      super('API error')
      this.status = status
      this.body = body
    }
  },
  listSessions: vi.fn(),
  newSession: vi.fn(),
  loadSession: vi.fn(),
  deleteSession: vi.fn(),
  promptSession: vi.fn(),
  steerSession: vi.fn(),
  cancelSession: vi.fn(),
  setMode: vi.fn(),
  resolvePermission: vi.fn(),
  resolveQuestion: vi.fn(),
}))

function collect<T>(store: { subscribe: (fn: (v: T) => void) => () => void }): T[] {
  const values: T[] = []
  store.subscribe((v) => values.push(v))
  return values
}

function emit(payload: unknown, id = 1) {
  for (const fn of emitters) {
    fn({ type: 'message', message: { id, data: JSON.stringify(payload) } })
  }
}

function latest<T>(values: T[]): T {
  return values[values.length - 1]
}

describe('createSessionStore', () => {
  beforeEach(() => {
    emitters.length = 0
    vi.clearAllMocks()
  })

  it('starts idle with default mode', () => {
    const { state } = createSessionStore('s1', '/tmp')
    const values = collect(state)
    const s = latest(values)
    expect(s.id).toBe('s1')
    expect(s.busy).toBe(false)
    expect(s.mode).toBe('default')
    expect(s.messages).toEqual([])
  })

  it('appends user message and marks busy on prompt', async () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    const values = collect(state)
    await actions.prompt('hello')
    const s = latest(values)
    expect(s.busy).toBe(true)
    expect(s.messages).toHaveLength(1)
    expect(s.messages[0].role).toBe('user')
    expect(s.messages[0].text).toBe('hello')
    expect(api.promptSession).toHaveBeenCalledWith('s1', 'hello')
  })

  it('appends assistant message chunks', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({ method: 'agent_message_chunk', params: { sessionId: 's1', chunk: { type: 'text', text: 'Hi' } } })
    emit({ method: 'agent_message_chunk', params: { sessionId: 's1', chunk: { type: 'text', text: ' there' } } })
    const s = latest(values)
    expect(s.messages).toHaveLength(1)
    expect(s.messages[0].role).toBe('assistant')
    expect(s.messages[0].text).toBe('Hi there')
  })

  it('tracks reasoning separately from message text', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({ method: 'agent_message_chunk', params: { sessionId: 's1', chunk: { type: 'text', text: 'Answer' } } })
    emit({ method: 'agent_thought_chunk', params: { sessionId: 's1', chunk: { type: 'text', text: 'thinking' } } })
    const s = latest(values)
    expect(s.messages[0].text).toBe('Answer')
    expect(s.messages[0].reasoning).toBe('thinking')
  })

  it('records tool calls and updates', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({ method: 'tool_call', params: { sessionId: 's1', toolCallId: 'tc-1', name: 'shell.run', arguments: { cmd: 'ls' } } })
    emit({ method: 'tool_call_update', params: { sessionId: 's1', toolCallId: 'tc-1', status: 'running' } })
    emit({ method: 'tool_call_update', params: { sessionId: 's1', toolCallId: 'tc-1', status: 'success', output: 'ok' } })
    const s = latest(values)
    expect(s.toolCalls).toHaveLength(1)
    expect(s.toolCalls[0].name).toBe('shell.run')
    expect(s.toolCalls[0].status).toBe('success')
    expect(s.toolCalls[0].output).toBe('ok')
  })

  it('clears busy on turn_end', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({ type: 'turn_end', stopReason: 'end_turn' })
    expect(latest(values).busy).toBe(false)
  })

  it('syncs mode via mode_changed', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({ method: 'mode_changed', params: { sessionId: 's1', mode: 'auto' } })
    expect(latest(values).mode).toBe('auto')
  })

  it('sets pending permission on permission_request', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({ type: 'permission_request', toolCallId: 'tc-1', params: { toolCallId: 'tc-1', toolName: 'shell.run', command: 'rm -rf /' } })
    const s = latest(values)
    expect(s.pendingPermission?.toolCallId).toBe('tc-1')
    expect(s.pendingPermission?.toolName).toBe('shell.run')
  })

  it('clears pending permission after resolve', async () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    emit({ type: 'permission_request', toolCallId: 'tc-1', params: { toolCallId: 'tc-1', toolName: 'shell.run' } })
    await actions.resolvePermission({ approved: true })
    let s: SessionState
    state.subscribe((v) => (s = v))()
    expect(s!.pendingPermission).toBeNull()
    expect(api.resolvePermission).toHaveBeenCalledWith('tc-1', { approved: true })
  })

  it('sets pending question on question_request', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    actions.connect()
    const values = collect(state)
    emit({
      type: 'question_request',
      questionId: 'q-1',
      sessionId: 's1',
      params: {
        sessionId: 's1',
        questionId: 'q-1',
        questions: [{ question: 'Proceed?', options: [{ label: 'Yes', value: 'yes' }] }],
      },
    })
    const s = latest(values)
    expect(s.pendingQuestion?.questionId).toBe('q-1')
    expect(s.pendingQuestion?.questions[0].question).toBe('Proceed?')
  })

  it('replays events from load response', async () => {
    ;(api.loadSession as Mock).mockResolvedValue({
      sessionId: 's1',
      events: [
        { id: 1, sessionId: 's1', data: { method: 'agent_message_chunk', params: { sessionId: 's1', chunk: { text: 'loaded' } } } },
      ],
    })
    const { state, actions } = createSessionStore('s1', '/tmp')
    const values = collect(state)
    await actions.load()
    const s = latest(values)
    expect(s.messages).toHaveLength(1)
    expect(s.messages[0].text).toBe('loaded')
  })

  it('marks connected/disconnected from SSE lifecycle', () => {
    const { state, actions } = createSessionStore('s1', '/tmp')
    const values = collect(state)
    actions.connect()
    expect(latest(values).connected).toBe(true)
  })
})