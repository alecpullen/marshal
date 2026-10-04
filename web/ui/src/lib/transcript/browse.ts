import type { WireNode } from '../stack'
import type { TranscriptCtx } from './ctx'
import { effective, visible } from './density'

export interface BrowseState {
  cursor: string | null
  follow: boolean
}

export interface BrowseCtx {
  /** Every row the transcript shows, in order. */
  ids: string[]
  /** Indexes into ids where J/K stop: a task header, or a turn's first row. */
  stops: number[]
  /** Whether the last row is live, so G can resume following it. */
  lastLive: boolean
}

export type BrowseEffect =
  | { toggleDensity: string }
  | { toggleFold: string }
  | { copy: string }
  | { toast: string }
  | { exitBrowse: true }

const CONTAINERS = new Set(['turn', 'passthrough'])

/**
 * The rows a reader can put the cursor on, following the same visibility and
 * folding rules the renderer does.
 */
export function flattenVisible(ctx: TranscriptCtx, roots: string[]): BrowseCtx {
  const ids: string[] = []
  const stops: number[] = []
  const parentOf = (id: string) => ctx.nodes.get(id)?.parent
  const walk = (id: string, firstOfTurn: { v: boolean }) => {
    const n = ctx.nodes.get(id)
    if (!n) return
    const density = effective(id, ctx.overrides, parentOf, ctx.global)
    if (!visible(n.kind, density)) return
    if (!CONTAINERS.has(n.kind)) {
      if (n.kind === 'task' || firstOfTurn.v) stops.push(ids.length)
      firstOfTurn.v = false
      ids.push(id)
    }
    const folded =
      !!n.task && ctx.foldTasks && n.task.status === 'completed' && !n.task.unresolvedFailure && !n.live && !ctx.unfolded.has(id)
    if (folded) return
    for (const c of n.children ?? []) walk(c, firstOfTurn)
  }
  for (const r of roots) walk(r, { v: true })
  const last = ids.length ? ctx.nodes.get(ids[ids.length - 1]) : undefined
  return { ids, stops, lastLive: !!last?.live }
}

/** The text `y` copies for a node. */
export function nodeText(n: WireNode): string {
  if (n.step) return [n.step.headline, n.step.rest].filter(Boolean).join('\n')
  if (n.tool) {
    const calls = n.tool.calls ?? []
    if (calls.length) return calls.map((c) => [c.target, c.output].filter(Boolean).join('\n')).join('\n\n')
    return [n.tool.running?.target, n.tool.running?.output].filter(Boolean).join('\n')
  }
  if (n.message) return n.message.content
  if (n.task) return n.task.content
  if (n.thinking) return n.thinking.text
  if (n.subagent) return [n.subagent.label, n.subagent.summary].filter(Boolean).join('\n')
  if (n.jobExit) return [n.jobExit.command, n.jobExit.output].filter(Boolean).join('\n')
  if (n.runEvent) return [n.runEvent.title, n.runEvent.detail, n.runEvent.body].filter(Boolean).join('\n')
  return ''
}

/**
 * The browse-mode keys, as handleBrowseKey binds them in the TUI. Pure: it
 * returns the next cursor and, for keys that act on a node, an effect for the
 * caller to carry out.
 */
export function browseKey(state: BrowseState, key: string, ctx: BrowseCtx): { state: BrowseState; effect?: BrowseEffect } {
  const { ids, stops } = ctx
  if (key === 'Escape') return { state, effect: { exitBrowse: true } }
  if (ids.length === 0) return { state }

  const at = state.cursor === null ? -1 : ids.indexOf(state.cursor)
  // A cursor on a row that is gone (folded away) restarts from the end.
  const idx = at >= 0 ? at : ids.length - 1
  const move = (i: number, follow = false): { state: BrowseState } => ({
    state: { cursor: ids[Math.max(0, Math.min(ids.length - 1, i))], follow },
  })
  const cur = ids[idx]

  switch (key) {
    case 'j':
    case 'ArrowDown':
      return move(at < 0 ? idx : idx + 1)
    case 'k':
    case 'ArrowUp':
      return move(at < 0 ? idx : idx - 1)
    case 'J':
    case ']': {
      const next = stops.find((s) => s > idx)
      return move(next ?? ids.length - 1)
    }
    case 'K':
    case '[': {
      const prev = [...stops].reverse().find((s) => s < idx)
      return move(prev ?? 0)
    }
    case 'g':
    case 'Home':
      return move(0)
    case 'G':
    case 'End':
      return move(ids.length - 1, ctx.lastLive)
    case 'Enter':
      return { state: { ...state, cursor: cur }, effect: { toggleDensity: cur } }
    case 'z':
      return { state: { ...state, cursor: cur }, effect: { toggleFold: cur } }
    case 'y':
      return { state: { ...state, cursor: cur }, effect: { copy: cur } }
    case 'i':
    case 'o':
    case 'f':
      return { state, effect: { toast: 'Coming in W2' } }
  }
  return { state }
}
