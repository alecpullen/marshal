/**
 * Home's "Mine" scope, as a filter over the bridge's inbox.
 *
 * The bridge groups the fleet and orders the needs-you queue; the browser does
 * not group anything. "Mine" is still a browser-side view, so it is applied
 * here, to the lists the bridge sent, keeping their order.
 */
import type { Inbox } from './contract'

// "Mine" is the single local owner for now; once agents carry real owner ids
// this needs the viewer's id (design §5.4).
const isMine = (ownerId?: string) => !ownerId || ownerId === 'local'

/**
 * Home's "Mine": drops what another owner has, keeping the bridge's order.
 * The bridge scopes nothing, so the browser filters.
 */
export function mineOnly(inbox: Inbox, ownerOf: (agentId: string) => string | undefined): Inbox {
  const group = (ids: string[]) => ids.filter((id) => isMine(ownerOf(id)))
  return {
    groups: {
      needsYou: group(inbox.groups.needsYou),
      running: group(inbox.groups.running),
      ready: group(inbox.groups.ready),
      earlier: group(inbox.groups.earlier),
    },
    queue: inbox.queue.filter((item) =>
      item.kind === 'intake'
        ? isMine(item.submission?.ownerId)
        : isMine(item.agentId ? ownerOf(item.agentId) : undefined),
    ),
  }
}
