import { describe, it, expect, vi } from 'vitest'
import { createSessionStore, transcriptEntries, type SessionState } from './store.js'

const emitters: Array<(e: { type: 'message'; message: { id: number; data: string } }) => void> = []

vi.mock('./sse.js', () => ({
  connectSSE: vi.fn(({ onEvent }: { onEvent: (e: unknown) => void }) => {
    const emit = (e: { type: 'message'; message: { id: number; data: string } }) => onEvent(e)
    emitters.push(emit)
    onEvent({ type: 'connected' })
    return () => {}
  }),
}))
vi.mock('./api.js', () => ({
  getToken: vi.fn(() => 'tok'),
  ensureToken: vi.fn(() => 'tok'),
  AuthError: class extends Error {},
  APIError: class extends Error {},
  loadSession: vi.fn(),
  promptSession: vi.fn(),
  cancelSession: vi.fn(),
  setSessionMode: vi.fn(),
  steerSession: vi.fn(),
  resolvePermission: vi.fn(),
  resolveQuestion: vi.fn(),
}))

function emit(payload: unknown) {
  const data = JSON.stringify({ jsonrpc: '2.0', ...(payload as object) })
  for (const e of [...emitters]) e({ type: 'message', message: { id: 1, data } })
}

function session(): { state: () => SessionState; connect: () => void } {
  const { state, actions } = createSessionStore('s1', '/tmp')
  let current!: SessionState
  state.subscribe((s) => (current = s))
  return { state: () => current, connect: () => actions.connect() }
}

const chunk = (text: string) => ({
  method: 'agent_message_chunk',
  params: { sessionId: 's1', chunk: { type: 'text', text } },
})
const toolCall = (id: string, name: string) => ({
  method: 'tool_call',
  params: { sessionId: 's1', toolCallId: id, name },
})

describe('transcript ordering', () => {
  it('interleaves a tool call between two assistant messages', () => {
    const s = session()
    s.connect()
    emit(chunk('Let me look.'))
    emit(toolCall('tc-1', 'shell.run'))
    emit(chunk('Found it.'))

    const entries = transcriptEntries(s.state())
    expect(entries.map((e) => e.kind)).toEqual(['message', 'toolCall', 'message'])
    expect((entries[0] as { value: { text: string } }).value.text).toBe('Let me look.')
    expect((entries[2] as { value: { text: string } }).value.text).toBe('Found it.')
  })

  /*
    The subtle half of the bug. Streaming chunks merge into the trailing
    assistant message, so without a check a chunk arriving after a tool
    call is appended to the message that preceded it — putting text the
    agent wrote after the call above it, forever.
  */
  it('starts a new message when a tool call has intervened', () => {
    const s = session()
    s.connect()
    emit(chunk('Before.'))
    emit(toolCall('tc-1', 'shell.run'))
    emit(chunk('After.'))

    expect(s.state().messages).toHaveLength(2)
    expect(s.state().messages[0].text).toBe('Before.')
    expect(s.state().messages[1].text).toBe('After.')
  })

  it('still merges consecutive chunks with nothing in between', () => {
    const s = session()
    s.connect()
    emit(chunk('Hello '))
    emit(chunk('world'))

    expect(s.state().messages).toHaveLength(1)
    expect(s.state().messages[0].text).toBe('Hello world')
  })

  it('orders several tool calls among messages by arrival', () => {
    const s = session()
    s.connect()
    emit(toolCall('tc-1', 'read'))
    emit(chunk('One.'))
    emit(toolCall('tc-2', 'grep'))
    emit(toolCall('tc-3', 'edit'))
    emit(chunk('Two.'))

    const entries = transcriptEntries(s.state())
    expect(entries.map((e) => e.kind)).toEqual(['toolCall', 'message', 'toolCall', 'toolCall', 'message'])
  })

  it('is empty for a fresh session', () => {
    const s = session()
    expect(transcriptEntries(s.state())).toEqual([])
  })

  it('gives every entry a stable key', () => {
    const s = session()
    s.connect()
    emit(chunk('One.'))
    emit(toolCall('tc-1', 'read'))
    const keys = transcriptEntries(s.state()).map((e) => e.key)
    expect(new Set(keys).size).toBe(2)
    expect(keys.every((k) => typeof k === 'string' && k.length > 0)).toBe(true)
  })
})
