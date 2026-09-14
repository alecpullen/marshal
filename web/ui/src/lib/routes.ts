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
