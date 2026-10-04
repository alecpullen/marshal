import { writable, get, type Readable } from 'svelte/store'
import { getStack } from './api'

// Wire shapes, mirroring internal/viewmodel/wire.go. Times are Unix
// milliseconds; absent means unset.

export interface WireThought { text: string; durationMs?: number }
export interface WireStep {
  headline: string
  rest?: string
  inferred?: boolean
  owner?: string
  roleWord?: string
  role?: string
  model?: string
  provider?: string
  todoId?: string
  heuristic?: boolean
  startedAt?: number
  endedAt?: number
  thoughts?: WireThought[]
  liveThinking?: string
}
export interface WireCall {
  callId?: string
  target?: string
  args?: string
  summary?: string
  output?: string
  error?: string
  exitCode?: number
  failed?: boolean
  approval?: string
  risk?: string
  files?: string[]
  role?: string
  notice?: string
  durationMs?: number
  at?: number
  truncated?: boolean
}
export interface WireRunning { target?: string; output?: string; startedAt?: number; truncated?: boolean }
export interface WireTool { name: string; display: string; calls?: WireCall[]; running?: WireRunning }
export interface WireMessage {
  role: string
  contentType?: string
  content: string
  final?: boolean
  usage?: string
  salvaged?: boolean
  salvageReason?: string
  thinkMs?: number
  at?: number
}
export interface WireThinking { text: string; durationMs?: number }
export interface WireSubagent {
  label: string
  status: string
  role?: string
  model?: string
  provider?: string
  toolCalls?: number
  currentTool?: string
  tokens?: number
  summary?: string
  error?: string
  salvaged?: string
  startedAt?: number
  endedAt?: number
  truncated?: boolean
}
export interface WireRunEvent {
  kind: string
  taskN?: number
  title?: string
  detail?: string
  body?: string
  severity?: string
  at?: number
  truncated?: boolean
}
export interface WireJobExit {
  jobId: string
  command: string
  exitCode: number
  durationMs?: number
  output?: string
  at?: number
  truncated?: boolean
}
export interface WireTask {
  todoId: string
  content: string
  status: string
  index?: number
  total?: number
  dropped?: boolean
  steps: number
  workMs?: number
  tools: number
  edits: number
  unresolvedFailure?: boolean
  firstNarration?: string
  startedAt?: number
  completedAt?: number
}
export interface WireReceipt {
  durationMs: number
  tasks: number
  steps: number
  tools: number
  files: number
  usage?: string
  salvaged?: boolean
}

export interface WireNode {
  id: string
  kind: string
  parent?: string
  children?: string[]
  live?: boolean
  step?: WireStep
  tool?: WireTool
  message?: WireMessage
  thinking?: WireThinking
  subagent?: WireSubagent
  runEvent?: WireRunEvent
  jobExit?: WireJobExit
  task?: WireTask
  receipt?: WireReceipt
}

export interface StackSnapshot {
  sessionId?: string
  rev: number
  roots: string[]
  nodes: WireNode[]
}

export interface StackPatch {
  kind: 'stack_patch'
  rev: number
  baseRev: number
  roots: string[]
  upsert?: WireNode[]
  remove?: string[]
}

export interface StackState {
  status: 'loading' | 'ready' | 'unsupported' | 'error'
  rev: number
  roots: string[]
  nodes: Map<string, WireNode>
}

export interface StackStore extends Readable<StackState> {
  load(): Promise<void>
  /** Stops any pending retry; call when the view goes away. */
  destroy(): void
  onEvent(envelope: unknown): void
}

const empty = (status: StackState['status']): StackState => ({ status, rev: 0, roots: [], nodes: new Map() })

/** Drops every node the roots do not reach (spec §5.4). */
function collect(nodes: Map<string, WireNode>, roots: string[]): void {
  const seen = new Set<string>()
  const stack = [...roots]
  while (stack.length) {
    const id = stack.pop()!
    if (seen.has(id)) continue
    seen.add(id)
    for (const c of nodes.get(id)?.children ?? []) stack.push(c)
  }
  for (const id of nodes.keys()) if (!seen.has(id)) nodes.delete(id)
}

type Fetcher = (sessionId: string) => Promise<StackSnapshot | 'unsupported'>

/**
 * A client-side copy of a session's transcript tree. It is seeded from the
 * snapshot route and kept current by `stack_patch` events; any sign that it
 * might have missed one (a revision gap, an SSE overflow, a turn ending)
 * refetches the snapshot instead of trying to patch the hole.
 */
export function createStackStore(sessionId: string, fetcher: Fetcher = getStack): StackStore {
  const store = writable<StackState>(empty('loading'))
  let inflight: Promise<void> | null = null
  let again = false
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let failures = 0
  let destroyed = false

  async function fetchOnce() {
    try {
      const snap = await fetcher(sessionId)
      if (snap === 'unsupported') {
        store.set(empty('unsupported'))
        return
      }
      const nodes = new Map<string, WireNode>()
      for (const n of snap.nodes ?? []) nodes.set(n.id, n)
      store.set({ status: 'ready', rev: snap.rev, roots: snap.roots ?? [], nodes })
      failures = 0
    } catch {
      scheduleRetry()
      // Keep a snapshot we already hold; with none, report the failure so the
      // page can fall back. The next event or reload tries again.
      store.update((s) => (s.status === 'ready' ? s : { ...s, status: 'error' }))
    }
  }

  // A failed fetch leaves the view stale, and an idle agent sends nothing to
  // trigger another, so retry on a growing delay until one succeeds.
  function scheduleRetry() {
    if (destroyed || retryTimer) return
    const delay = Math.min(1000 * 2 ** failures, 15000)
    failures++
    retryTimer = setTimeout(() => {
      retryTimer = undefined
      void load()
    }, delay)
  }

  /**
   * One fetch at a time. A request made while one is in flight is folded
   * into a single follow-up, so a burst of triggers costs one refetch.
   */
  function load(): Promise<void> {
    clearTimeout(retryTimer)
    retryTimer = undefined
    if (inflight) {
      again = true
      return inflight
    }
    inflight = (async () => {
      do {
        again = false
        await fetchOnce()
      } while (again)
    })().finally(() => {
      inflight = null
    })
    return inflight
  }

  function applyPatch(p: StackPatch) {
    const s = get(store)
    if (s.status === 'unsupported') return
    // A retry is already scheduled while in error, so patches do not each fetch.
    if (s.status === 'error') return
    if (inflight || s.status === 'loading') {
      // The snapshot in flight may or may not include this patch. Whatever
      // it returns is the authority; ask for one more look afterwards.
      void load()
      return
    }
    if (p.rev <= s.rev) return
    if (p.baseRev !== s.rev) {
      void load()
      return
    }
    const nodes = new Map(s.nodes)
    for (const n of p.upsert ?? []) nodes.set(n.id, n)
    for (const id of p.remove ?? []) nodes.delete(id)
    collect(nodes, p.roots ?? [])
    store.set({ status: 'ready', rev: p.rev, roots: p.roots ?? [], nodes })
  }

  function onEvent(envelope: unknown) {
    const ev = envelope as { type?: string; method?: string; params?: { update?: { kind?: string } } } | null
    if (!ev) return
    // A reconnect may have skipped patches; the snapshot is the authority.
    if (ev.type === 'replay_overflow' || ev.type === 'connected') {
      void load()
      return
    }
    if (ev.method !== 'session/update') return
    const update = ev.params?.update
    if (update?.kind === 'stack_patch') applyPatch(update as unknown as StackPatch)
    else if (update?.kind === 'session_telemetry') void load()
  }

  function destroy() {
    destroyed = true
    clearTimeout(retryTimer)
    retryTimer = undefined
  }

  return { subscribe: store.subscribe, load, destroy, onEvent }
}
