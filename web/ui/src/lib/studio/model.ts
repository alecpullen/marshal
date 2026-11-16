import type { FactoryCheck, FactoryEvent, FactoryFilter, FactoryItem, FactoryItemView, FactoryRound } from '../factory'
import { formatFactoryRoute } from '../routes'

/**
 * The cold-path projections Fleet, the navigation counts and the factory
 * views share. Everything here is pure: no API call, no Svelte store and no
 * DOM, so the same answer comes back from a render, a test or a route
 * decision. The wire shapes come from `lib/factory`; nothing is re-derived
 * that the bridge already reports.
 */

/** One project's factory work: its items, the counts over its current work, and the agents it holds. */
export interface FactoryProjectSummary {
  root: string
  items: FactoryItem[]
  current: number
  needsYou: number
  ready: number
  linkedAgentIds: string[]
}
export interface FactoryCheckGroups { failed: FactoryCheck[]; passed: FactoryCheck[]; other: FactoryCheck[] }

/**
 * States that need a person rather than polling: a draft is not approved
 * yet, ready and needs_input wait on a person, and stopped/closed are
 * terminal. Everything else — including a state this UI does not know yet —
 * is treated as live work, because a poller that stops on an unknown state
 * goes stale silently.
 */
const INACTIVE_STATES: readonly string[] = ['draft', 'ready', 'stopped', 'closed', 'needs_input']

/** Terminal states: they leave the current list for history. */
const HISTORICAL_STATES: readonly string[] = ['closed', 'stopped']

const ITEM_VIEWS: readonly string[] = ['approval', 'activity', 'review', 'prs', 'documents']

/** Conclusions that are not a failure. A conclusion outside this set has failed. */
const NON_FAILING_CONCLUSIONS: readonly string[] = ['success', 'neutral', 'skipped']

export function isFactoryActive(item: FactoryItem): boolean {
  return !INACTIVE_STATES.includes(item.state)
}

/** A decision or a draft is waiting on a person; ready waits on a review instead. */
function isNeedsYou(item: FactoryItem): boolean {
  return Boolean(item.decision) || item.state === 'draft'
}

function isHistorical(item: FactoryItem): boolean {
  return HISTORICAL_STATES.includes(item.state)
}

function matchesFilter(item: FactoryItem, filter: FactoryFilter): boolean {
  switch (filter) {
    case 'needs-you':
      return isNeedsYou(item)
    case 'ready':
      return item.state === 'ready'
    case 'history':
      return isHistorical(item)
    case 'current':
    default:
      // An unknown filter falls back to the current list rather than emptying it.
      return !isHistorical(item)
  }
}

/** Milliseconds for sorting; a missing or unparsable timestamp sorts as zero. */
function timestamp(value: string | undefined | null): number {
  if (!value) return 0
  const at = Date.parse(value)
  return Number.isFinite(at) ? at : 0
}

/** Needs-you first, ready second, then newest, with the ID breaking a tie. */
function rank(item: FactoryItem): number {
  if (isNeedsYou(item)) return 0
  return item.state === 'ready' ? 1 : 2
}

/** A new sorted array; the caller's array is never reordered. */
function sortItems(items: FactoryItem[]): FactoryItem[] {
  return [...items].sort(
    (a, b) => rank(a) - rank(b) || timestamp(b.updatedAt) - timestamp(a.updatedAt) || a.id.localeCompare(b.id),
  )
}

export function filterFactoryItems(items: FactoryItem[], filter: FactoryFilter = 'current'): FactoryItem[] {
  return sortItems(items.filter((item) => matchesFilter(item, filter)))
}

/**
 * The agent IDs the given items hold. Empty IDs are not agents, and an ID
 * held twice is one worker. `currentOnly` ignores the historical items, which
 * is what the fleet's counts use; the default (every item) is what decides
 * whether an agent is a factory worker at all.
 */
export function factoryWorkerIds(items: FactoryItem[], currentOnly = false): Set<string> {
  const ids = new Set<string>()
  const scope = currentOnly ? items.filter((item) => matchesFilter(item, 'current')) : items
  for (const item of scope) for (const worker of item.workers ?? []) if (worker.agentId) ids.add(worker.agentId)
  return ids
}

/**
 * One project's factory summary. The root matches exactly: a sibling
 * directory that merely shares a prefix is a different project. Every one of
 * the project's items is listed in sorted order; the counts and the linked
 * agents come from its current work only.
 */
export function summarizeFactoryProject(root: string, items: FactoryItem[]): FactoryProjectSummary {
  const scoped = items.filter((item) => item.projectRoot === root)
  const current = scoped.filter((item) => !isHistorical(item))
  return {
    root,
    items: sortItems(scoped),
    current: current.length,
    needsYou: current.filter((item) => isNeedsYou(item)).length,
    ready: current.filter((item) => item.state === 'ready').length,
    linkedAgentIds: [...factoryWorkerIds(current)].sort(),
  }
}

/**
 * The tab an item opens on. A requested view is honoured, except approval on
 * something a person already approved: that tab would show an empty form.
 * Without a request: a draft asks for approval, ready asks for review, and
 * everything else is the activity feed.
 */
export function defaultFactoryItemView(item: FactoryItem, requested?: FactoryItemView): FactoryItemView {
  if (requested && ITEM_VIEWS.includes(requested)) {
    return requested === 'approval' && item.state !== 'draft' ? 'activity' : requested
  }
  if (item.state === 'draft') return 'approval'
  if (item.state === 'ready') return 'review'
  return 'activity'
}

/**
 * The item's direct route, as `lib/routes` formats it. The order matters: a
 * draft's action is approving it, an open decision outranks a ready review
 * because it is blocking, and everything else lands on the activity feed.
 */
export function factoryItemRoute(item: FactoryItem): string {
  const view: FactoryItemView =
    item.state === 'draft' ? 'approval' : item.decision ? 'activity' : item.state === 'ready' ? 'review' : 'activity'
  return formatFactoryRoute({ itemId: item.id, view })
}

/**
 * The latest round's checks in the three groups the evidence card renders,
 * each keeping the server's order. A check whose conclusion is empty has not
 * finished yet, so it is `other` rather than a failure — the same split the
 * bridge's PR body uses.
 */
export function partitionFactoryChecks(round?: FactoryRound): FactoryCheckGroups {
  const groups: FactoryCheckGroups = { failed: [], passed: [], other: [] }
  for (const check of round?.ci?.checks ?? []) {
    if (check.conclusion === 'success') groups.passed.push(check)
    else if (check.conclusion && !NON_FAILING_CONCLUSIONS.includes(check.conclusion)) groups.failed.push(check)
    else groups.other.push(check)
  }
  return groups
}

/**
 * An item's history oldest first. Sorting is stable, so events sharing a
 * sequence keep the order the server sent them in.
 */
export function sortedFactoryHistory(item: FactoryItem): FactoryEvent[] {
  return [...(item.history ?? [])].sort((a, b) => a.sequence - b.sequence)
}
