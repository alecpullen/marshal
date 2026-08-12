import { ensureToken, getToken } from './api.js'
import type { FleetDelta } from './fleet'

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
  sessionId?: string
  query?: string
  onEvent: (event: SSEEvent) => void
  signal?: AbortSignal
}

export function connectSSE({ sessionId, query, onEvent, signal }: SSEOptions): () => void {
	const streamKey = sessionId ?? query ?? 'fleet'
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
      const lastId = getLastEventId(streamKey)
      try {
        const qs = query ? `${query}&lastEventId=${lastId}` : `sessionId=${encodeURIComponent(sessionId!)}&lastEventId=${lastId}`
        const res = await fetch(`/api/events?${qs}`, {
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
                setLastEventId(streamKey, msg.id)
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

export function parseFleetEvent(data: string): FleetDelta | 'overflow' | null {
  try {
    const value = JSON.parse(data) as Record<string, unknown>
    if (value.type === 'replay_overflow') return 'overflow'
    if (typeof value.kind !== 'string' || typeof value.sessionId !== 'string') return null
    return value as unknown as FleetDelta
  } catch { return null }
}

export function connectFleetSSE(opts: { onDelta: (d: FleetDelta) => void; onOverflow: () => void; signal?: AbortSignal }): () => void {
  return connectSSE({ query: 'stream=fleet', onEvent: e => { if (e.type !== 'message') return; const parsed = parseFleetEvent(e.message.data); if (parsed === 'overflow') opts.onOverflow(); else if (parsed) opts.onDelta(parsed) }, signal: opts.signal })
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}