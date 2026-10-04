import { groupAgents, type AgentRow } from './fleet'
import type { PendingSubmission } from './api'

export type NeedsYouItem =
  | { kind: 'agent'; agent: AgentRow; at: string }
  | { kind: 'intake'; submission: PendingSubmission; at: string }

export interface Inbox {
  needsYou: NeedsYouItem[]
  ready: AgentRow[]
  running: AgentRow[]
}

const isMine = (ownerId?: string) => !ownerId || ownerId === 'local'

/**
 * The Home inbox: agents grouped by what they need, plus intake requests
 * merged into Needs you, oldest first. With `mine`, anything owned by
 * someone else is dropped; an unset owner counts as local.
 */
export function buildInbox(agents: AgentRow[], pending: PendingSubmission[], mine: boolean): Inbox {
  const scoped = mine ? agents.filter((a) => isMine(a.ownerId)) : agents
  const g = groupAgents(scoped)
  const intake = (mine ? pending.filter((p) => isMine((p as { ownerId?: string }).ownerId)) : pending).map<NeedsYouItem>(
    (p) => ({ kind: 'intake', submission: p, at: p.createdAt }),
  )
  const agentItems = g.needsYou.map<NeedsYouItem>((a) => ({ kind: 'agent', agent: a, at: a.updatedAt }))
  const needsYou = [...agentItems, ...intake].sort((a, b) => a.at.localeCompare(b.at))
  return { needsYou, ready: g.ready, running: g.running }
}
