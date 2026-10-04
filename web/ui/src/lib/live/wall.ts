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
