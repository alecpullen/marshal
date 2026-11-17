import type { FactoryFilter, FactoryItemView, FactoryRoute } from './factory'

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

/**
 * The old Dashboard and Home now live at #fleet: the empty hash is the fleet
 * screen itself, and `#inbox` is its attention list. Neither is a separate
 * page, so no `home` member survives.
 */
export type Page = 'fleet' | 'inbox' | 'new' | 'chat' | 'sessions' | 'runs' | 'run' | 'live' | 'library' | 'settings' | 'usage' | 'watches' | 'workspaces' | 'network' | 'project' | 'factory' | 'planner' | 'roadmap' | 'other'

export function pageFromHash(hash: string): Page {
  if (hash === '' || hash === '#' || hash === '#fleet') return 'fleet'
  if (hash === '#inbox') return 'inbox'
  if (parseNewAgentRoute(hash)) return 'new'
  if (parsePlannerRoute(hash)) return 'planner'
  if (parseFactoryRoute(hash)) return 'factory'
  if (parseChatRoute(hash)) return 'chat'
  if (hash === '#sessions' || isScopedSessions(hash)) return 'sessions'
  if (parseRunRoute(hash)) return 'run'
  if (hash === '#runs') return 'runs'
  if (parseLiveRoute(hash)) return 'live'
  if (parseLibraryRoute(hash)) return 'library'
  if (parseSettingsRoute(hash)) return 'settings'
  if (parseUsageRoute(hash)) return 'usage'
  if (parseWatchesRoute(hash)) return 'watches'
  if (parseNetworkRoute(hash)) return 'network'
  if (parseProjectRoute(hash)) return 'project'
  if (parseWorkspacesRoute(hash)) return 'workspaces'
  if (parseRoadmapRoute(hash)) return 'roadmap'
  return 'other'
}

export type DockSizeParam = 'collapsed' | 'docked' | 'expanded'
export type DockTabParam = 'inspect' | 'changes' | 'files' | 'tasks' | 'terminal' | 'preview'

/** A session page URL: `#chat/<id>[/review][?node=…&dock=…&tab=…&from=…]`. */
export interface ChatRoute {
  id: string
  view: 'session' | 'review'
  node?: string
  dock?: DockSizeParam
  tab?: DockTabParam
  /** The factory item this session was opened from, as a `#factory/…` hash. */
  from?: string
}

const DOCK_SIZES: readonly string[] = ['collapsed', 'docked', 'expanded']
const DOCK_TABS: readonly string[] = ['inspect', 'changes', 'files', 'tasks', 'terminal', 'preview']

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
  const route: ChatRoute = { id: decodeSafe(m[1]), view: m[2] ? 'review' : 'session' }
  const q = new URLSearchParams(m[3] ?? '')
  const node = q.get('node')
  if (node) route.node = decodeSafe(node)
  const dock = q.get('dock')
  if (dock && DOCK_SIZES.includes(dock)) route.dock = dock as DockSizeParam
  const tab = q.get('tab')
  if (tab && DOCK_TABS.includes(tab)) route.tab = tab as DockTabParam
  /*
    `from` is a hash of its own, so it is decoded exactly once: formatChatRoute
    hands it to URLSearchParams, which escapes it, and `get` undoes that escape
    and nothing more — a further decode would eat an item ID's own `%2F`. It
    survives only when it names one factory item to return to.
  */
  const from = q.get('from')
  if (from && parseFactoryRoute(from)?.itemId) route.from = from
  return route
}

/** The inverse of parseChatRoute. */
export function formatChatRoute(r: ChatRoute): string {
  const q = new URLSearchParams()
  if (r.node) q.set('node', r.node)
  if (r.dock) q.set('dock', r.dock)
  if (r.tab) q.set('tab', r.tab)
  if (r.from) q.set('from', r.from)
  const qs = q.toString()
  return `#chat/${encodeURIComponent(r.id)}${r.view === 'review' ? '/review' : ''}${qs ? `?${qs}` : ''}`
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

export type LibraryTab = 'skills' | 'plugins' | 'mcp' | 'memory' | 'recipes'
const LIBRARY_TABS: readonly string[] = ['skills', 'plugins', 'mcp', 'memory', 'recipes']

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

export type SettingsTab = 'models' | 'providers' | 'secrets' | 'credentials' | 'repos' | 'tokens' | 'notifications' | 'status-links'
const SETTINGS_TABS: readonly string[] = ['models', 'providers', 'secrets', 'credentials', 'repos', 'tokens', 'notifications', 'status-links']

/** `#settings[/<tab>]`; a bare `#settings` is Models. */
export function parseSettingsRoute(hash: string): { tab: SettingsTab } | null {
  const m = /^#settings(?:\/([^/?]+))?$/.exec(hash)
  if (!m) return null
  const tab = m[1] ?? 'models'
  return SETTINGS_TABS.includes(tab) ? { tab: tab as SettingsTab } : null
}

export type WatchesTab = 'watches' | 'schedules'

/** `#watches` or `#watches/schedules`. */
export function parseWatchesRoute(hash: string): { tab: WatchesTab } | null {
  if (hash === '#watches') return { tab: 'watches' }
  if (hash === '#watches/schedules') return { tab: 'schedules' }
  return null
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

/** The network inspector: `#network?agent=<id>` for one agent, `#workspaces/<name>/network` for one workspace. */
export interface NetworkRoute { workspace?: string; agent?: string }

export function parseNetworkRoute(hash: string): NetworkRoute | null {
  const ws = /^#workspaces\/([^/?]+)\/network$/.exec(hash)
  if (ws) return { workspace: decodeSafe(ws[1]) }
  const m = /^#network(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const q = new URLSearchParams(m[1] ?? '')
  return { agent: q.get('agent') || undefined, workspace: q.get('workspace') || undefined }
}

export function formatNetworkRoute(r: NetworkRoute): string {
  if (r.workspace && !r.agent) return `#workspaces/${encodeURIComponent(r.workspace)}/network`
  return `#network${r.agent ? `?agent=${encodeURIComponent(r.agent)}` : ''}`
}

export type AutomationPage = 'review-bot' | 'ci-fixer'
/**
 * The project overview `#projects/<encoded root>`, or one of its automations
 * pages `#projects/<root>/automations/review-bot|ci-fixer[?draft=<id>]`;
 * null for the bare project list. An unknown page is not a route.
 */
export function parseProjectRoute(hash: string): { root: string; automations?: AutomationPage; draft?: string } | null {
  const m = /^#projects\/([^/?]+)(?:\/automations\/(review-bot|ci-fixer)(?:\?(.*))?)?$/.exec(hash)
  if (!m) return null
  if (!m[2]) return { root: decodeSafe(m[1]) }
  const draft = new URLSearchParams(m[3] ?? '').get('draft')
  return { root: decodeSafe(m[1]), automations: m[2] as AutomationPage, ...(draft ? { draft } : {}) }
}

export const formatProjectRoute = (root: string) => `#projects/${encodeURIComponent(root)}`
export const formatAutomationRoute = (root: string, page: AutomationPage, draft?: string) =>
  `${formatProjectRoute(root)}/automations/${page}${draft ? `?draft=${encodeURIComponent(draft)}` : ''}`

/** The new-agent page: `#new[?project=<encoded root>]`. */
export interface NewAgentRoute { projectRoot?: string }

export function parseNewAgentRoute(hash: string): NewAgentRoute | null {
  const m = /^#new(?:\?(.*))?$/.exec(hash)
  if (!m) return null
  const projectRoot = new URLSearchParams(m[1] ?? '').get('project') || undefined
  return projectRoot ? { projectRoot } : {}
}

export const ROADMAP_AREAS = ['planning', 'voice', 'workflow', 'hosts', 'environments', 'connections'] as const
export type RoadmapArea = (typeof ROADMAP_AREAS)[number]

/**
 * The alignment roadmap: `#roadmap/<area>`. A bare `#roadmap` names no area,
 * so it is not a route and an unknown area is not either.
 */
export interface RoadmapRoute { area: RoadmapArea }

export function parseRoadmapRoute(hash: string): RoadmapRoute | null {
  const m = /^#roadmap\/([^/?]+)$/.exec(hash)
  if (!m || !(ROADMAP_AREAS as readonly string[]).includes(m[1])) return null
  return { area: m[1] as RoadmapArea }
}

/** Where an old standalone page moved to, or null when the hash is current. */
export function redirectLegacy(hash: string): string | null {
  switch (hash) {
    case '#fleet':
      return '#'
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

const FACTORY_FILTERS: readonly string[] = ['current', 'needs-you', 'ready', 'history']
const FACTORY_VIEWS: readonly string[] = ['approval', 'activity', 'review', 'prs', 'documents']

/** A list filter, with `current` — the default view — and unknown values dropped. */
function factoryFilter(query: string | undefined): FactoryRoute {
  const value = new URLSearchParams(query ?? '').get('filter')
  return value && value !== 'current' && FACTORY_FILTERS.includes(value) ? { filter: value as FactoryFilter } : {}
}

/**
 * The factory hashes: the global list `#factory`, the project-scoped list
 * `#projects/<root>/factory`, creation `#factory/new[?project=<root>]`,
 * working drafts `#factory/drafts[/new|/<id>]`, and one encoded item
 * `#factory/<id>[?view=…&agent=…]`. Both lists take `?filter=…`, and every
 * creation and draft form takes `?project=<root>`.
 *
 * The working-drafts forms are matched before the generic item segment, which
 * would otherwise read `#factory/drafts` as an item called "drafts". A draft
 * route never carries a project path; its project comes from the `?project=`
 * query, exactly as `#factory/new` does. Parsing `#projects/<root>/factory`
 * here keeps one owner for every factory route.
 */
export function parseFactoryRoute(hash: string): FactoryRoute | null {
  const global = /^#factory(?:\?(.*))?$/.exec(hash)
  if (global) return factoryFilter(global[1])
  const scoped = /^#projects\/([^/?]+)\/factory(?:\?(.*))?$/.exec(hash)
  if (scoped) return { projectRoot: decodeSafe(scoped[1]), ...factoryFilter(scoped[2]) }
  if (hash === '#factory/drafts' || /^#factory\/drafts\?[^#]*$/.test(hash)) return { drafts: true }
  if (/^#factory\/drafts\/new(?:\?[^#]*)?$/.test(hash)) return { drafts: true, create: true }
  const working = /^#factory\/drafts\/([^/?#]+)(?:\?[^#]*)?$/.exec(hash)
  if (working) {
    try { return { drafts: true, draftId: decodeURIComponent(working[1]) } } catch { return null }
  }
  const create = /^#factory\/new(?:\?(.*))?$/.exec(hash)
  if (create) {
    const projectRoot = new URLSearchParams(create[1] ?? '').get('project') || undefined
    return projectRoot ? { create: true, projectRoot } : { create: true }
  }
  const item = /^#factory\/([^/?#]+)(?:\?(.*))?$/.exec(hash)
  if (!item) return null
  const route: FactoryRoute = { itemId: decodeSafe(item[1]) }
  const q = new URLSearchParams(item[2] ?? '')
  const view = q.get('view')
  if (view && FACTORY_VIEWS.includes(view)) route.view = view as FactoryItemView
  const agent = q.get('agent')
  if (agent) {
    /*
      `agent` is decoded exactly once: formatFactoryRoute hands it to
      URLSearchParams, which escapes it, and `get` undoes that escape and
      nothing more — a further decode would eat an agent id's own `%2F`.
    */
    route.agent = agent
    // A worker drill-in without an explicit tab shows that worker's activity.
    if (!route.view) route.view = 'activity'
  }
  return route
}

export function formatFactoryRoute(route: FactoryRoute): string {
  if (route.drafts) {
    if (route.create) return '#factory/drafts/new'
    if (route.draftId) return `#factory/drafts/${encodeURIComponent(route.draftId)}`
    return '#factory/drafts'
  }
  if (route.create) return `#factory/new${route.projectRoot ? `?project=${encodeURIComponent(route.projectRoot)}` : ''}`
  if (route.itemId) {
    const q = new URLSearchParams()
    if (route.view) q.set('view', route.view)
    else if (route.agent) q.set('view', 'activity')
    if (route.agent) q.set('agent', route.agent)
    const qs = q.toString()
    return `#factory/${encodeURIComponent(route.itemId)}${qs ? `?${qs}` : ''}`
  }
  const q = new URLSearchParams()
  if (route.filter && route.filter !== 'current') q.set('filter', route.filter)
  const qs = q.toString()
  const base = route.projectRoot ? `#projects/${encodeURIComponent(route.projectRoot)}/factory` : '#factory'
  return `${base}${qs ? `?${qs}` : ''}`
}

/**
 * A project-scoped working-drafts hash: the list, a new draft, or one draft.
 * The project stays in the query so the draft id is never a path and no route
 * ever carries a plan, a spec or a credential.
 */
export function formatWorkingDraftsRoute(route: { create?: boolean; draftId?: string | null }, projectRoot?: string): string {
  const segment = route.create ? '/new' : route.draftId ? `/${encodeURIComponent(route.draftId)}` : ''
  return `#factory/drafts${segment}${projectRoot ? `?project=${encodeURIComponent(projectRoot)}` : ''}`
}

/**
 * The Studio planner: `#planner/new[?project=<root>]` creates one, and
 * `#planner/<id>` is one session. A planner id is the 32-hex identity the
 * bridge minted, so a hand-edited hash naming anything else is not a route and
 * the shell shows its own Not Found rather than calling the API with it.
 */
export interface PlannerRoute {
  kind: 'new' | 'session'
  /** The session's planner id; present exactly when `kind` is `session`. */
  id?: string
  /** The project creation is scoped to, when the entry point knew one. */
  project?: string
}

const PLANNER_ID = /^[0-9a-f]{32}$/

export function parsePlannerRoute(hash: string): PlannerRoute | null {
  if (hash === '#planner/new') return { kind: 'new' }
  const create = /^#planner\/new\?(.*)$/.exec(hash)
  if (create) {
    const project = new URLSearchParams(create[1]).get('project')
    return project ? { kind: 'new', project } : { kind: 'new' }
  }
  const m = /^#planner\/([^/?#]+)$/.exec(hash)
  if (!m || !PLANNER_ID.test(m[1])) return null
  return { kind: 'session', id: m[1] }
}

/** The inverse of parsePlannerRoute; every navigation to the planner uses it. */
export function plannerRoute(route: PlannerRoute): string {
  if (route.kind === 'new') return `#planner/new${route.project ? `?project=${encodeURIComponent(route.project)}` : ''}`
  return `#planner/${encodeURIComponent(route.id ?? '')}`
}
