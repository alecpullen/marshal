import { ensureToken, getToken } from './api.js'
import type { FleetEvent, ProjectRemovedDelta } from './fleet'

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
  /** A complete stream URL (no query); `lastEventId` is appended. Overrides sessionId and query. */
  url?: string
  onEvent: (event: SSEEvent) => void
  signal?: AbortSignal
}

export function connectSSE({ sessionId, query, url, onEvent, signal }: SSEOptions): () => void {
	const streamKey = url ?? sessionId ?? query ?? 'fleet'
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
        const target = url
          ? `${url}?lastEventId=${lastId}`
          : `/api/events?${query ? `${query}&lastEventId=${lastId}` : `sessionId=${encodeURIComponent(sessionId!)}&lastEventId=${lastId}`}`
        const res = await fetch(target, {
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

export type BuildLogEvent = { line: string; at?: number } | { done: true; status: string }

/** One build-log SSE payload: `{line, at}` or the final `{done, status}`; anything else is null. */
export function parseBuildLogEvent(data: string): BuildLogEvent | null {
  try {
    const v = JSON.parse(data) as Record<string, unknown>
    if (v.done) return { done: true, status: typeof v.status === 'string' ? v.status : '' }
    if (typeof v.line === 'string') return { line: v.line, ...(typeof v.at === 'number' ? { at: v.at } : {}) }
  } catch {
    // A malformed event is skipped; the next line still renders.
  }
  return null
}

/**
 * Streams a workspace build's log. The bridge replays the whole log from its
 * ring, so a finished build reads the same way as a running one. The stream
 * is closed after `onDone` so connectSSE does not reconnect to a finished log.
 */
export function connectBuildLog(
  name: string,
  n: number,
  { onLine, onDone, signal }: { onLine: (line: string, at?: number) => void; onDone: (status: string) => void; signal?: AbortSignal },
): () => void {
  let stop = () => {}
  let finished = false
  stop = connectSSE({
    url: `/api/workspaces/${encodeURIComponent(name)}/builds/${n}/events`,
    signal,
    onEvent: (e) => {
      if (e.type !== 'message' || finished) return
      const ev = parseBuildLogEvent(e.message.data)
      if (!ev) return
      if ('done' in ev) {
        finished = true
        onDone(ev.status)
        stop()
      } else onLine(ev.line, ev.at)
    },
  })
  return () => {
    finished = true
    stop()
  }
}

/** A role binding as the engine reports it: a preset name, a custom-agent table, or null for none. */
function bindingLabel(v: unknown): string {
  if (v === null) return 'no binding'
  if (typeof v === 'string') return v === '' ? 'no binding' : v
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>
    for (const k of ['preset', 'Preset', 'customAgent', 'CustomAgent', 'custom_agent']) {
      if (typeof o[k] === 'string' && o[k] !== '') return o[k] as string
    }
    return JSON.stringify(v)
  }
  return ''
}

const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : null)

/**
 * A reroute delta carries its payload under `reroute` (`{id, watchId, watch,
 * role, profile, from, to, at}`; from and to are bindings, null for none), and
 * the flat spec shape is accepted too. Anything without a string id and role
 * and both bindings is dropped, so a drifted shape cannot put an "undefined"
 * notice (or an Undo that posts to /reroutes/undefined) on Home.
 */
function parseReroute(value: Record<string, unknown>): FleetEvent | null {
  const src = (value.reroute && typeof value.reroute === 'object' ? value.reroute : value) as Record<string, unknown>
  const id = str(src.id)
  const role = str(src.role)
  if (!id || !role || !('from' in src) || !('to' in src)) return null
  const from = bindingLabel(src.from)
  const to = bindingLabel(src.to)
  if (!from || !to) return null
  return { kind: 'reroute', id, watch: str(src.watch) ?? str(src.watchId) ?? '', role, from, to }
}

/** A budget delta is nested under `budget` on the wire; the flat spec shape is accepted too. */
function parseBudget(value: Record<string, unknown>): FleetEvent | null {
  const src = (value.budget && typeof value.budget === 'object' ? value.budget : value) as Record<string, unknown>
  if ((src.scope !== 'daily' && src.scope !== 'agent') || typeof src.spentUsd !== 'number' || typeof src.capUsd !== 'number') return null
  return {
    kind: 'budget',
    scope: src.scope,
    ...(str(src.agentId) ? { agentId: str(src.agentId)! } : {}),
    spentUsd: src.spentUsd,
    capUsd: src.capUsd,
    action: str(src.action) ?? '',
  }
}

export function parseFleetEvent(data: string): FleetEvent | 'overflow' | null {
  try {
    const value = JSON.parse(data) as Record<string, unknown>
    if (value.type === 'replay_overflow') return 'overflow'
    if (value.kind === 'project_removed' && typeof value.project === 'string') return value as unknown as ProjectRemovedDelta
    // Budget, reroute and watch are fleet-wide, so they carry no session.
    if (value.kind === 'budget') return parseBudget(value)
    if (value.kind === 'reroute') return parseReroute(value)
    if (value.kind === 'watch') return value as unknown as FleetEvent
    // The bridge may address a run delta as agentId; rows are keyed by sessionId.
    if (value.kind === 'run' && typeof value.sessionId !== 'string' && typeof value.agentId === 'string') {
      value.sessionId = value.agentId
    }
    if (typeof value.kind !== 'string' || typeof value.sessionId !== 'string') return null
    return value as unknown as FleetEvent
  } catch { return null }
}

export function connectFleetSSE(opts: { onDelta: (d: FleetEvent) => void; onOverflow: () => void; signal?: AbortSignal }): () => void {
  return connectSSE({ query: 'stream=fleet', onEvent: e => { if (e.type !== 'message') return; const parsed = parseFleetEvent(e.message.data); if (parsed === 'overflow') opts.onOverflow(); else if (parsed) opts.onDelta(parsed) }, signal: opts.signal })
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}