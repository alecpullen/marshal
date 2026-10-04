import type { AgentRow } from '../fleet'

export const PAGE_SIZE = 12

const rank = (a: AgentRow) => (a.pending ? 0 : a.status === 'running' ? 1 : 2)

/**
 * The agents on the wall: filtered by project and by "runs only" (agents
 * that have a run digest), needs-you first, then running, then most
 * recently updated.
 */
export function wallAgents(agents: AgentRow[], opts: { project?: string; runsOnly: boolean }): AgentRow[] {
  return agents
    .filter((a) => (!opts.project || a.project === opts.project) && (!opts.runsOnly || (a.run && a.run.kind !== 'none')))
    .sort((a, b) => rank(a) - rank(b) || b.updatedAt.localeCompare(a.updatedAt) || a.id.localeCompare(b.id))
}

/** One 1-based page of `items`; an out-of-range page is clamped to the last. */
export function paginate<T>(items: T[], page: number, size = PAGE_SIZE): { items: T[]; page: number; pages: number } {
  const pages = Math.max(1, Math.ceil(items.length / size))
  const p = Math.min(Math.max(1, page), pages)
  return { items: items.slice((p - 1) * size, p * size), page: p, pages }
}

/**
 * Each attached tile holds a session stream, and browsers allow only about 6
 * connections per origin on HTTP/1.1. The fleet stream, an open chat and the
 * ordinary fetches (the wall's own Approve and Deny) need room too, so only
 * a few tiles may be attached at once; the rest show fleet-derived content
 * until a slot frees.
 */
export const MAX_ATTACHED = 4

const attached = new Set<string>()
const waiters = new Set<() => void>()

/** Claims a slot for a tile; false when all are taken. Claiming again is a no-op that succeeds. */
export function acquireSlot(id: string): boolean {
  if (attached.has(id)) return true
  if (attached.size >= MAX_ATTACHED) return false
  attached.add(id)
  return true
}

/** Frees a tile's slot and offers it to tiles that were waiting. */
export function releaseSlot(id: string): void {
  if (!attached.delete(id)) return
  for (const wake of [...waiters]) wake()
}

/** Registers a callback for when a slot frees; returns the unregister function. */
export function onSlotFree(wake: () => void): () => void {
  waiters.add(wake)
  return () => waiters.delete(wake)
}
