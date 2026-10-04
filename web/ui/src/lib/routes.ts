/**
 * Route parsing shared by views. Kept separate from any component so the
 * rules can be tested without mounting the router itself.
 *
 * `#sessions/<encoded root>` scopes the sessions panel to one project; it
 * is the route the sidebar's per-project sessions affordance navigates
 * to. The root is decoded defensively: a malformed escape (hand-edited
 * URL) degrades to the raw string rather than throwing the app.
 */
export function sessionsProjectFromHash(hash: string): string | null {
  const m = /^#sessions\/(.+)$/.exec(hash)
  if (!m) return null
  try {
    return decodeURIComponent(m[1])
  } catch {
    return m[1]
  }
}

/**
 * True when the hash scopes the sessions panel to one project. Every
 * consumer that branches on the scoped form (App's badge-sync refresh,
 * the sidebar nav highlight) goes through this instead of re-encoding
 * the prefix, so a parse change cannot leave one of them behind.
 */
export function isScopedSessions(hash: string): boolean {
  return sessionsProjectFromHash(hash) !== null
}

/** The old Dashboard now lives at #fleet; the empty hash is Home. */
export type Page = 'home' | 'fleet' | 'new' | 'chat' | 'sessions' | 'other'

export function pageFromHash(hash: string): Page {
  if (hash === '' || hash === '#') return 'home'
  if (hash === '#fleet') return 'fleet'
  if (hash === '#new') return 'new'
  if (parseChatRoute(hash)) return 'chat'
  if (hash === '#sessions' || isScopedSessions(hash)) return 'sessions'
  return 'other'
}

export type DockSizeParam = 'collapsed' | 'docked' | 'expanded'
export type DockTabParam = 'inspect' | 'changes' | 'files'

/** A session page URL: `#chat/<id>[/review][?node=…&dock=…&tab=…]`. */
export interface ChatRoute {
  id: string
  view: 'session' | 'review'
  node?: string
  dock?: DockSizeParam
  tab?: DockTabParam
}

const DOCK_SIZES: readonly string[] = ['collapsed', 'docked', 'expanded']
const DOCK_TABS: readonly string[] = ['inspect', 'changes', 'files']

function decodeSafe(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

/** Parses a chat hash; unknown dock/tab values are dropped, not rejected. */
export function parseChatRoute(hash: string): ChatRoute | null {
  const m = /^#chat\/([^/?]+)(\/review)?(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const route: ChatRoute = { id: m[1], view: m[2] ? 'review' : 'session' }
  const q = new URLSearchParams(m[3] ?? '')
  const node = q.get('node')
  if (node) route.node = decodeSafe(node)
  const dock = q.get('dock')
  if (dock && DOCK_SIZES.includes(dock)) route.dock = dock as DockSizeParam
  const tab = q.get('tab')
  if (tab && DOCK_TABS.includes(tab)) route.tab = tab as DockTabParam
  return route
}

/** The inverse of parseChatRoute. */
export function formatChatRoute(r: ChatRoute): string {
  const q = new URLSearchParams()
  if (r.node) q.set('node', r.node)
  if (r.dock) q.set('dock', r.dock)
  if (r.tab) q.set('tab', r.tab)
  const qs = q.toString()
  return `#chat/${r.id}${r.view === 'review' ? '/review' : ''}${qs ? `?${qs}` : ''}`
}
