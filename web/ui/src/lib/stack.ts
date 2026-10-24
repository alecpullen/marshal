import { writable, get, type Readable } from 'svelte/store'
import { APIError, getStack } from './api'

// Wire shapes, mirroring internal/viewmodel/wire.go. Times are Unix
// milliseconds; absent means unset.

// How many rows a beat shows before older ones collapse into a count, a
// mirror of viewmodel.BeatRowCap. Models that rarely narrate otherwise push
// the live tail off the transcript.
export const BEAT_ROW_CAP = 8

export interface WireThought { text: string; durationMs?: number }
export interface WireStep {
  headline: string
  rest?: string
  owner?: string
  roleWord?: string
  role?: string
  model?: string
  provider?: string
  todoId?: string
  /** Set on the one beat where work moves to a different todo. */
  todo?: WireTodoLabel
  /** The `session.Step` IDs this beat merges, oldest first. */
  stepIds?: number[]
  startedAt?: number
  endedAt?: number
  thoughts?: WireThought[]
}
// A todo transition: the beat where work moves to a different todo.
export interface WireTodoLabel {
  id: string
  index: number
  total: number
  content: string
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
export interface WireTool {
  name: string
  display: string
  workKey?: string
  calls?: WireCall[]
  running?: WireRunning
  /** The subagent this row started, 0 when it started none. */
  subagentId?: number
}
/** One finished piece of background work at the moment it ended: a job's exit or a subagent's end. */
export interface WireCompletion {
  source: string
  id: string
  subagentId?: number
  label: string
  outcome: string
  failed?: boolean
  exitCode?: number
  stopCause?: string
  startedAt?: number
  endedAt?: number
  durationMs?: number
  measure?: string
  summary?: string
  command?: string
  cwd?: string
  output?: string
  report?: string
  error?: string
  workKey?: string
  executionId?: string
  reportMessageId?: number
  truncated?: boolean
}
/** A beat's plan line: the todo list as the beat's last todo.write left it. */
export interface WirePlan {
  /** Each item's status in list order: "completed", "in_progress" or "pending". */
  status?: string[]
  done: number
  total: number
  active?: number
  activeText?: string
  cleared?: boolean
}
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

/** The pinned band: todo progress with its stale signal, and running cards. */
export interface WireBand {
  todos?: boolean
  done: number
  total: number
  active?: string
  stale?: boolean
  staleAgeMs?: number
  /** When the stale clock started (unix ms); ticks the age between patches. */
  staleSince?: number
  next?: string[]
  /** The whole list, for the Tasks tab: sent whenever there is one, idle or not. */
  tasks?: WireTask[]
  entities?: WireBandEntity[]
}
/** One todo in the Tasks tab, with its share of the transcript. Times are unix ms. */
export interface WireTask {
  id?: string
  content: string
  status: string
  startedAt?: number
  completedAt?: number
  steps?: number
  workMs?: number
  /** The first beat worked under it, where a click jumps. */
  ref?: string
}
/** One running card the band lists. */
export interface WireBandEntity {
  ref: string
  label: string
  detail?: string
  startedAt?: number
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
  runEvent?: WireRunEvent
  completion?: WireCompletion
  plan?: WirePlan
  receipt?: WireReceipt
  band?: WireBand
}

export interface StackSnapshot {
  sessionId?: string
  rev: number
  roots: string[]
  nodes: WireNode[]
}

export interface StackPatch {
  kind: 'stack_patch'
  /** Which transcript this patch belongs to; absent for the parent. */
  subagentId?: number
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

type Fetcher = (sessionId: string, subagentId?: number) => Promise<StackSnapshot | 'unsupported'>

export interface StackOptions {
  /** A child transcript to follow; the parent (0) when unset. */
  subagentId?: number
  fetcher?: Fetcher
}

/**
 * A client-side copy of a session's transcript tree. It is seeded from the
 * snapshot route and kept current by `stack_patch` events; any sign that it
 * might have missed one (a revision gap, an SSE overflow, a turn ending)
 * refetches the snapshot instead of trying to patch the hole.
 */
export function createStackStore(sessionId: string, opts: StackOptions = {}): StackStore {
  const fetcher = opts.fetcher ?? getStack
  const subagentId = opts.subagentId ?? 0
  const store = writable<StackState>(empty('loading'))
  let inflight: Promise<void> | null = null
  let again = false
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let failures = 0
  let destroyed = false

  async function fetchOnce() {
    try {
      const snap = await fetcher(sessionId, subagentId || undefined)
      if (snap === 'unsupported') {
        store.set(empty('unsupported'))
        return
      }
      const nodes = new Map<string, WireNode>()
      for (const n of snap.nodes ?? []) nodes.set(n.id, n)
      store.set({ status: 'ready', rev: snap.rev, roots: snap.roots ?? [], nodes })
      failures = 0
    } catch (e) {
      // A refusal (say, a subagent with no transcript of its own) will not
      // change by asking again; only a transient failure is worth a retry.
      const refused = e instanceof APIError && e.status >= 400 && e.status < 500 && e.status !== 408 && e.status !== 429
      if (!refused) scheduleRetry()
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
    if (update?.kind === 'stack_patch') {
      // One stream carries every transcript's patches; each store takes its own.
      const sub = (update as { subagentId?: number }).subagentId ?? 0
      if (sub === subagentId) applyPatch(update as unknown as StackPatch)
    }
    else if (update?.kind === 'session_telemetry') void load()
  }

  function destroy() {
    destroyed = true
    clearTimeout(retryTimer)
    retryTimer = undefined
  }

  return { subscribe: store.subscribe, load, destroy, onEvent }
}
