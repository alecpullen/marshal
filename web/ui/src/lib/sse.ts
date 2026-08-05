import { ensureToken, getToken } from './api.js'

export interface SSEMessage {
  id: number
  data: string
}

export type SSEEvent =
  | { type: 'message'; message: SSEMessage }
  | { type: 'error'; error: Error }
  | { type: 'connected' }
  | { type: 'disconnected' }

const LAST_EVENT_ID_KEY = 'marshal:lastEventId:'

function lastEventIdKey(sessionId: string): string {
  return LAST_EVENT_ID_KEY + sessionId
}

function getLastEventId(sessionId: string): number {
  try {
    const raw = sessionStorage.getItem(lastEventIdKey(sessionId))
    if (raw) {
      const n = parseInt(raw, 10)
      if (!isNaN(n)) return n
    }
  } catch {
    // ignore
  }
  return 0
}

function setLastEventId(sessionId: string, id: number): void {
  try {
    sessionStorage.setItem(lastEventIdKey(sessionId), String(id))
  } catch {
    // ignore
  }
}

function parseSSEEvents(chunk: string): SSEMessage[] {
  const messages: SSEMessage[] = []
  const lines = chunk.split('\n')
  let id = 0
  let dataLines: string[] = []

  const flush = () => {
    if (dataLines.length > 0) {
      messages.push({ id, data: dataLines.join('\n') })
      dataLines = []
    }
  }

  for (const line of lines) {
    if (line.startsWith('id:')) {
      const raw = line.slice(3).trim()
      const n = parseInt(raw, 10)
      if (!isNaN(n)) id = n
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    } else if (line.trim() === '') {
      flush()
      id = 0
    }
  }
  flush()
  return messages
}

export interface SSEOptions {
  sessionId: string
  onEvent: (event: SSEEvent) => void
  signal?: AbortSignal
}

export function connectSSE({ sessionId, onEvent, signal }: SSEOptions): () => void {
  let abortController = new AbortController()
  let cancelled = false
  let reconnectDelay = 1000
  const maxDelay = 30000

  if (signal) {
    signal.addEventListener('abort', () => {
      cancelled = true
      abortController.abort()
    })
  }

  const run = async () => {
    while (!cancelled) {
      const token = getToken() ?? ensureToken()
      const lastId = getLastEventId(sessionId)
      try {
        const res = await fetch(`/api/events?sessionId=${encodeURIComponent(sessionId)}&lastEventId=${lastId}`, {
          headers: {
            Accept: 'text/event-stream',
            Authorization: `Bearer ${token}`,
          },
          signal: abortController.signal,
        })
        if (!res.ok) {
          throw new Error(`SSE connect failed: ${res.status}`)
        }
        if (!res.body) {
          throw new Error('SSE response has no body')
        }
        reconnectDelay = 1000
        onEvent({ type: 'connected' })

        const reader = res.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        while (!cancelled) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          const parts = buffer.split('\n\n')
          buffer = parts.pop() ?? ''
          for (const part of parts) {
            const messages = parseSSEEvents(part)
            for (const msg of messages) {
              if (msg.id > 0) {
                setLastEventId(sessionId, msg.id)
              }
              onEvent({ type: 'message', message: msg })
            }
          }
        }
        onEvent({ type: 'disconnected' })
      } catch (err) {
        if (cancelled || abortController.signal.aborted) return
        onEvent({ type: 'error', error: err instanceof Error ? err : new Error(String(err)) })
      }

      if (cancelled) return
      await sleep(reconnectDelay)
      reconnectDelay = Math.min(reconnectDelay * 2, maxDelay)
      abortController = new AbortController()
    }
  }

  run()

  return () => {
    cancelled = true
    abortController.abort()
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}