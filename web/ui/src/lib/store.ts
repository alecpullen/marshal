import { writable, type Readable } from 'svelte/store'
import * as api from './api.js'
import { connectSSE, type SSEEvent } from './sse.js'

export type Mode = 'read' | 'default' | 'plan' | 'edit' | 'copilot' | 'auto'

export const MODES: Mode[] = ['read', 'default', 'plan', 'edit', 'copilot', 'auto']

export interface Message {
  id: string
  role: 'user' | 'assistant' | 'system'
  text: string
  reasoning?: string
}

export interface ToolCall {
  id: string
  name: string
  args: unknown
  status: 'pending' | 'running' | 'success' | 'error'
  output?: string
  error?: string
}

export interface PendingPermission {
  toolCallId: string
  toolName: string
  command?: string
  diff?: string
}

export interface QuestionOption {
  label: string
  value: string
}

export interface Question {
  question: string
  options?: QuestionOption[]
  multi?: boolean
  allowOther?: boolean
}

export interface PendingQuestion {
  questionId: string
  sessionId: string
  questions: Question[]
}

export interface SessionState {
  id: string
  cwd: string
  busy: boolean
  mode: Mode
  messages: Message[]
  toolCalls: ToolCall[]
  pendingPermission: PendingPermission | null
  pendingQuestion: PendingQuestion | null
  error: string | null
  connected: boolean
}

function uid(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
}

function createSessionState(id: string, cwd: string): SessionState {
  return {
    id,
    cwd,
    busy: false,
    mode: 'default',
    messages: [],
    toolCalls: [],
    pendingPermission: null,
    pendingQuestion: null,
    error: null,
    connected: false,
  }
}

function normalizeMode(mode: string): Mode {
  const m = mode.toLowerCase()
  if (MODES.includes(m as Mode)) return m as Mode
  return 'default'
}

export function createSessionStore(id: string, cwd: string) {
  const state = writable<SessionState>(createSessionState(id, cwd))
  let unsubscribeSSE: (() => void) | null = null

  const applyEvent = (event: unknown) => {
    const ev = event as Record<string, unknown>
    const type = ev.type as string | undefined

    if (type === 'permission_request') {
      const params = (ev.params as Record<string, unknown>) ?? {}
      state.update((s) => ({
        ...s,
        pendingPermission: {
          toolCallId: String(ev.toolCallId ?? params.toolCallId ?? ''),
          toolName: String(params.toolName ?? ''),
          command: params.command ? String(params.command) : undefined,
          diff: params.diff ? String(params.diff) : undefined,
        },
      }))
      return
    }

    if (type === 'question_request') {
      const params = (ev.params as Record<string, unknown>) ?? {}
      const rawQuestions = Array.isArray(params.questions) ? params.questions : []
      const questions: Question[] = rawQuestions.map((q: unknown) => {
        const rq = q as Record<string, unknown>
        return {
          question: String(rq.question ?? ''),
          options: Array.isArray(rq.options)
            ? rq.options.map((o: unknown) => {
                const ro = o as Record<string, unknown>
                return { label: String(ro.label ?? ro.value ?? ''), value: String(ro.value ?? ro.label ?? '') }
              })
            : undefined,
          multi: Boolean(rq.multi),
          allowOther: Boolean(rq.allowOther),
        }
      })
      state.update((s) => ({
        ...s,
        pendingQuestion: {
          questionId: String(ev.questionId ?? params.questionId ?? ''),
          sessionId: String(ev.sessionId ?? params.sessionId ?? id),
          questions,
        },
      }))
      return
    }

    if (type === 'turn_end') {
      state.update((s) => ({ ...s, busy: false, error: ev.error ? String(ev.error) : null }))
      return
    }

    if (type === 'bridge_restarted') {
      state.update((s) => ({ ...s, busy: false, connected: true, error: null }))
      return
    }

    if (type === 'mode_changed') {
      const params = (ev.params as Record<string, unknown>) ?? ev
      const mode = normalizeMode(String(params.mode ?? 'default'))
      state.update((s) => ({ ...s, mode }))
      return
    }

    // ACP envelope: { method, params }
    const method = ev.method as string | undefined
    if (method) {
      const params = (ev.params as Record<string, unknown>) ?? {}
      applyACP(state, method, params)
    }
  }

  const handleSSE = (e: SSEEvent) => {
    if (e.type === 'connected') {
      state.update((s) => ({ ...s, connected: true }))
    } else if (e.type === 'disconnected') {
      state.update((s) => ({ ...s, connected: false }))
    } else if (e.type === 'error') {
      // Surface connection errors only when idle so transient reconnects
      // do not spam the chat.
      state.update((s) => (s.busy ? s : { ...s, error: e.error.message }))
    } else if (e.type === 'message') {
      try {
        const payload = JSON.parse(e.message.data)
        applyEvent(payload)
      } catch {
        // ignore malformed event
      }
    }
  }

  const connect = () => {
    if (unsubscribeSSE) return
    unsubscribeSSE = connectSSE({ sessionId: id, onEvent: handleSSE })
  }

  const disconnect = () => {
    unsubscribeSSE?.()
    unsubscribeSSE = null
  }

  const actions = {
    connect,
    disconnect,
    async load() {
      const res = await api.loadSession(id, cwd)
      for (const ev of res.events) {
        applyEvent(ev.data)
      }
    },
    async prompt(text: string) {
      state.update((s) => ({
        ...s,
        busy: true,
        messages: [...s.messages, { id: uid(), role: 'user', text }],
      }))
      await api.promptSession(id, text)
    },
    async steer(text: string) {
      await api.steerSession(id, text)
    },
    async cancel() {
      await api.cancelSession(id)
    },
    async setMode(mode: Mode) {
      await api.setMode(id, mode)
    },
    async resolvePermission(decision: api.Decision) {
      const toolCallId = get(state).pendingPermission?.toolCallId
      if (!toolCallId) return
      state.update((s) => ({ ...s, pendingPermission: null }))
      await api.resolvePermission(toolCallId, decision)
    },
    async resolveQuestion(answers: api.Answers) {
      const questionId = get(state).pendingQuestion?.questionId
      if (!questionId) return
      state.update((s) => ({ ...s, pendingQuestion: null }))
      await api.resolveQuestion(questionId, answers)
    },
    dismissError() {
      state.update((s) => ({ ...s, error: null }))
    },
  }

  return { state, actions }
}

function applyACP(state: ReturnType<typeof writable<SessionState>>, method: string, params: Record<string, unknown>) {
  switch (method) {
    case 'agent_message_chunk': {
      const chunk = params.chunk as Record<string, unknown> | undefined
      const text = chunk?.text as string | undefined
      if (!text) return
      state.update((s) => {
        const last = s.messages[s.messages.length - 1]
        if (last && last.role === 'assistant') {
          last.text += text
          return { ...s, messages: [...s.messages] }
        }
        return { ...s, messages: [...s.messages, { id: uid(), role: 'assistant', text }] }
      })
      break
    }
    case 'agent_thought_chunk': {
      const chunk = params.chunk as Record<string, unknown> | undefined
      const text = chunk?.text as string | undefined
      if (!text) return
      state.update((s) => {
        const last = s.messages[s.messages.length - 1]
        if (last && last.role === 'assistant') {
          last.reasoning = (last.reasoning ?? '') + text
          return { ...s, messages: [...s.messages] }
        }
        return { ...s, messages: [...s.messages, { id: uid(), role: 'assistant', text: '', reasoning: text }] }
      })
      break
    }
    case 'tool_call': {
      const toolCallId = String(params.toolCallId ?? '')
      if (!toolCallId) return
      state.update((s) => ({
        ...s,
        toolCalls: [
          ...s.toolCalls,
          {
            id: toolCallId,
            name: String(params.name ?? ''),
            args: params.arguments ?? params.args,
            status: 'pending',
          },
        ],
      }))
      break
    }
    case 'tool_call_update': {
      const toolCallId = String(params.toolCallId ?? '')
      const status = String(params.status ?? '')
      state.update((s) => {
        const tc = s.toolCalls.find((t) => t.id === toolCallId)
        if (!tc) return s
        tc.status = status as ToolCall['status']
        tc.output = params.output ? String(params.output) : tc.output
        tc.error = params.error ? String(params.error) : tc.error
        return { ...s, toolCalls: [...s.toolCalls] }
      })
      break
    }
    case 'turn_end': {
      state.update((s) => ({
        ...s,
        busy: false,
        error: params.error ? String(params.error) : null,
      }))
      break
    }
    case 'mode_changed': {
      const mode = normalizeMode(String(params.mode ?? 'default'))
      state.update((s) => ({ ...s, mode }))
      break
    }
  }
}

function get<T>(store: Readable<T>): T {
  let value: T
  const unsub = store.subscribe((v) => (value = v))
  unsub()
  return value!
}