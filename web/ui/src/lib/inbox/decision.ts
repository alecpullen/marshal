import type { NetDecisionKind, NetDecisionResult } from '../api'
import type { NetworkDecisionItem } from '../fleet'

/** What to show after a decision settled: a toast for a changed Studio draft, a modal for a repo patch. */
export type DecisionOutcome =
  | { kind: 'draft'; workspace: string; host: string }
  | { kind: 'patch'; workspace?: string; host: string; patch: string }

/**
 * Only add-to-workspace has anything to show. A returned `patch` means a repo
 * template (the bridge never writes to repos); without one the Studio
 * template's draft changed.
 */
export function outcomeFor(item: NetworkDecisionItem, decision: NetDecisionKind, res: NetDecisionResult): DecisionOutcome | null {
  if (decision !== 'add-to-workspace') return null
  const workspace = res.workspace ?? item.workspace
  if (typeof res.patch === 'string' && res.patch !== '') return { kind: 'patch', workspace, host: item.host, patch: res.patch }
  return { kind: 'draft', workspace: workspace ?? 'the workspace', host: item.host }
}

export type DecideFn = (item: NetworkDecisionItem, decision: NetDecisionKind) => Promise<void>
