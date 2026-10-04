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
export type Page = 'home' | 'fleet' | 'new' | 'chat' | 'sessions' | 'runs' | 'run' | 'live' | 'library' | 'settings' | 'usage' | 'watches' | 'workspaces' | 'other'

export function pageFromHash(hash: string): Page {
  if (hash === '' || hash === '#') return 'home'
  if (hash === '#fleet') return 'fleet'
  if (hash === '#new') return 'new'
  if (parseChatRoute(hash)) return 'chat'
  if (hash === '#sessions' || isScopedSessions(hash)) return 'sessions'
  if (parseRunRoute(hash)) return 'run'
  if (hash === '#runs') return 'runs'
  if (parseLiveRoute(hash)) return 'live'
  if (parseLibraryRoute(hash)) return 'library'
  if (parseSettingsRoute(hash)) return 'settings'
  if (parseUsageRoute(hash)) return 'usage'
  if (hash === '#watches') return 'watches'
  if (parseWorkspacesRoute(hash)) return 'workspaces'
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

export type RunView = 'lanes' | 'graph' | 'timeline'
const RUN_VIEWS: readonly string[] = ['lanes', 'graph', 'timeline']

/** A run page URL: `#runs/<agentId>[?view=…&node=<task>:<stage>&dock=…]`. */
export interface RunRoute {
  id: string
  view: RunView
  /** The selected cell, as `<task number>:<stage>`. */
  node?: string
  dock?: DockSizeParam
}

export function parseRunRoute(hash: string): RunRoute | null {
  const m = /^#runs\/([^/?]+)(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const q = new URLSearchParams(m[2] ?? '')
  const view = q.get('view')
  const route: RunRoute = { id: decodeSafe(m[1]), view: view && RUN_VIEWS.includes(view) ? (view as RunView) : 'lanes' }
  const node = q.get('node')
  if (node) route.node = node
  const dock = q.get('dock')
  if (dock && DOCK_SIZES.includes(dock)) route.dock = dock as DockSizeParam
  return route
}

export function formatRunRoute(r: RunRoute): string {
  const q = new URLSearchParams()
  if (r.view !== 'lanes') q.set('view', r.view)
  if (r.node) q.set('node', r.node)
  if (r.dock) q.set('dock', r.dock)
  const qs = q.toString()
  return `#runs/${encodeURIComponent(r.id)}${qs ? `?${qs}` : ''}`
}

/** The Live wall URL: `#live[?project=<root>&runs=1&page=N]`; page is 1-based. */
export interface LiveRoute {
  project?: string
  runsOnly: boolean
  page: number
}

export function parseLiveRoute(hash: string): LiveRoute | null {
  const m = /^#live(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const q = new URLSearchParams(m[1] ?? '')
  const page = Number.parseInt(q.get('page') ?? '', 10)
  return { project: q.get('project') || undefined, runsOnly: q.get('runs') === '1', page: Number.isFinite(page) && page > 0 ? page : 1 }
}

export function formatLiveRoute(r: LiveRoute): string {
  const q = new URLSearchParams()
  if (r.project) q.set('project', r.project)
  if (r.runsOnly) q.set('runs', '1')
  if (r.page > 1) q.set('page', String(r.page))
  const qs = q.toString()
  return `#live${qs ? `?${qs}` : ''}`
}

export type LibraryTab = 'skills' | 'plugins' | 'mcp' | 'memory'
const LIBRARY_TABS: readonly string[] = ['skills', 'plugins', 'mcp', 'memory']

/** The Library URL: `#library[/<tab>][?project=<root>]`; a bare `#library` is Skills. */
export interface LibraryRoute { tab: LibraryTab; project?: string }

export function parseLibraryRoute(hash: string): LibraryRoute | null {
  const m = /^#library(?:\/([^/?]+))?(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const tab = m[1] ?? 'skills'
  if (!LIBRARY_TABS.includes(tab)) return null
  const project = new URLSearchParams(m[2] ?? '').get('project') || undefined
  return { tab: tab as LibraryTab, project }
}

export function formatLibraryRoute(r: LibraryRoute): string {
  return `#library/${r.tab}${r.project ? `?project=${encodeURIComponent(r.project)}` : ''}`
}

export type SettingsTab = 'models' | 'providers' | 'tokens'
const SETTINGS_TABS: readonly string[] = ['models', 'providers', 'tokens']

/** `#settings[/<tab>]`; a bare `#settings` is Models. */
export function parseSettingsRoute(hash: string): { tab: SettingsTab } | null {
  const m = /^#settings(?:\/([^/?]+))?$/.exec(hash)
  if (!m) return null
  const tab = m[1] ?? 'models'
  return SETTINGS_TABS.includes(tab) ? { tab: tab as SettingsTab } : null
}

export type UsageTab = 'cost' | 'audit' | 'disk'
const USAGE_TABS: readonly string[] = ['cost', 'audit', 'disk']

/** `#usage[?tab=cost|audit|disk]`; an unknown tab falls back to cost. */
export function parseUsageRoute(hash: string): { tab: UsageTab } | null {
  const m = /^#usage(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const tab = new URLSearchParams(m[1] ?? '').get('tab') ?? 'cost'
  return { tab: USAGE_TABS.includes(tab) ? (tab as UsageTab) : 'cost' }
}

export type WorkspacesView = 'gallery' | 'edit' | 'builds' | 'network'

/** `#workspaces` (the gallery) or `#workspaces/<name>/edit|builds|network`; an unknown view is not a route. */
export interface WorkspacesRoute { view: WorkspacesView; name?: string }

export function parseWorkspacesRoute(hash: string): WorkspacesRoute | null {
  if (hash === '#workspaces') return { view: 'gallery' }
  const m = /^#workspaces\/([^/?]+)\/(edit|builds|network)$/.exec(hash)
  return m ? { view: m[2] as WorkspacesView, name: decodeSafe(m[1]) } : null
}

export function formatWorkspacesRoute(r: WorkspacesRoute): string {
  return r.name ? `#workspaces/${encodeURIComponent(r.name)}/${r.view === 'gallery' ? 'edit' : r.view}` : '#workspaces'
}

/** Where an old standalone page moved to, or null when the hash is current. */
export function redirectLegacy(hash: string): string | null {
  switch (hash) {
    case '#clients':
      return '#library/mcp'
    case '#disk':
      return '#usage?tab=disk'
    case '#activity':
      return '#usage?tab=audit'
    default:
      return null
  }
}
