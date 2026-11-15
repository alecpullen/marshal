import type { ProjectStatus } from '../api'
import type { FactoryItem } from '../factory'
import { glyph } from '../glyphs'
import { formatFactoryRoute, formatProjectRoute } from '../routes'
import { factoryItemRoute, filterFactoryItems, summarizeFactoryProject } from '../studio/model'
import { shortName } from '../utils'

/**
 * One navigation model for every Studio shell — the labelled desktop sidebar,
 * its icon rail, and the phone/tablet drawer — so the three cannot disagree
 * about where a destination points or which one is current.
 *
 * Everything here is pure: the caller passes the routed hash, the bridge's
 * project list, the factory items and the needs-you count. Components never
 * fetch, never subscribe and never read `window.location`, so the same model
 * comes back from a render, a test or a route decision.
 */

export interface NavDestination {
  hash: string
  label: string
  glyph: string
  active: boolean
  planned?: boolean
  count?: number
}

export interface NavProjectDestination extends NavDestination {
  root: string
  workItems: NavDestination[]
}

export interface NavModel {
  primary: NavDestination[]
  projects: NavProjectDestination[]
  resources: NavDestination[]
  roadmap: NavDestination[]
  /** Every global and project destination once, in render order, for the all-pages menu. */
  all: NavDestination[]
}

export interface NavModelInput {
  route: string
  projects: ProjectStatus[]
  factoryItems: FactoryItem[]
  needsCount: number
}

export interface StudioNavProps {
  model: NavModel
  open: boolean
  onToggle: () => void
  onNavigate: (hash: string) => void
}

export interface MobileNavProps {
  model: NavModel
  onNavigate: (hash: string) => void
}

/*
  Marks. Each one is the shared glyph vocabulary's meaning, plus the three
  shapes the current Rail owns (⋔ Runs, ▦ Workspaces, ∿ Usage) and two the
  design does not spell out:

  - Inbox takes ◉, the mark the approved phone navigation gives it. Live
    therefore takes Running (▸) — "anything in flight", which is exactly what
    the live wall shows.
  - A roadmap destination reuses the collapsed-disclosure mark (▹): the row
    opens the plan for a capability that does not exist yet. Its Planned
    badge, not the glyph, carries the label.
*/
const INBOX_GLYPH = '◉'
const RUNS_GLYPH = '⋔'
const WORKSPACES_GLYPH = '▦'
const USAGE_GLYPH = '∿'
const PLANNED_GLYPH = glyph.DisclosureCollapsed

/** The roadmap areas this navigation links to, in render order. */
const ROADMAP_DESTINATIONS: { area: string; label: string }[] = [
  { area: 'hosts', label: 'Hosts' },
  { area: 'environments', label: 'Environments' },
  { area: 'connections', label: 'Connections' },
]

/**
 * True when `hash` or one of the sub-routes it owns is the current route, so
 * `#runs/<id>` and `#live?page=2` keep their entry lit. The fleet home is `#`
 * itself, which no prefix rule can own, so it is matched exactly — `#inbox`
 * must not light Fleet.
 */
function ownsRoute(hash: string, route: string): boolean {
  if (hash === '#') return route === '' || route === '#' || route === '#fleet'
  const base = hash.split(/[/?]/)[0]
  return route === base || route.startsWith(`${base}/`) || route.startsWith(`${base}?`)
}

/** True when the route is this destination, whatever query it carries. */
function isRoute(hash: string, route: string): boolean {
  return route === hash || route.startsWith(`${hash}?`)
}

/**
 * True when the route is `base` or sits under it. The boundary is explicit:
 * `#projects/%2Fw%2Falpha` must not match the sibling project `alpha-two`.
 */
function isUnder(base: string, route: string): boolean {
  return route === base || route.startsWith(`${base}/`) || route.startsWith(`${base}?`)
}

/** The first occurrence of each hash wins; later duplicates are dropped. */
function dedupe(destinations: NavDestination[]): NavDestination[] {
  const seen = new Set<string>()
  const out: NavDestination[] = []
  for (const destination of destinations) {
    if (seen.has(destination.hash)) continue
    seen.add(destination.hash)
    out.push(destination)
  }
  return out
}

export function buildNavModel(input: NavModelInput): NavModel {
  const { route, projects, factoryItems, needsCount } = input

  const primary: NavDestination[] = [
    { hash: '#', label: 'Fleet', glyph: glyph.Agent, active: ownsRoute('#', route) },
    {
      hash: '#inbox',
      label: 'Inbox',
      glyph: INBOX_GLYPH,
      active: ownsRoute('#inbox', route),
      ...(needsCount > 0 ? { count: needsCount } : {}),
    },
  ]

  /*
    A project destination points at its factory dashboard, and stays lit on the
    project overview and its automations pages too. Work items are contextual:
    only the project's current work belongs here — history lives in the item's
    own views and Fleet.
  */
  const projectDestinations: NavProjectDestination[] = projects
    .map((project) => {
      const summary = summarizeFactoryProject(project.root, factoryItems)
      const workItems: NavDestination[] = filterFactoryItems(summary.items, 'current').map((item) => ({
        hash: factoryItemRoute(item),
        label: item.title,
        glyph: glyph.Job,
        active: isRoute(formatFactoryRoute({ itemId: item.id }), route),
      }))
      return {
        root: project.root,
        hash: formatFactoryRoute({ projectRoot: project.root }),
        label: shortName(project.root),
        glyph: glyph.CustomAgent,
        active: isUnder(formatProjectRoute(project.root), route),
        workItems,
      }
    })
    .sort((a, b) => a.label.localeCompare(b.label) || a.root.localeCompare(b.root))

  const resources: NavDestination[] = [
    { hash: '#projects', label: 'Projects', glyph: glyph.File, active: ownsRoute('#projects', route) },
    { hash: '#live', label: 'Live', glyph: glyph.Running, active: ownsRoute('#live', route) },
    { hash: '#runs', label: 'Runs', glyph: RUNS_GLYPH, active: ownsRoute('#runs', route) },
    { hash: '#workspaces', label: 'Workspaces', glyph: WORKSPACES_GLYPH, active: ownsRoute('#workspaces', route) },
    { hash: '#watches', label: 'Watches', glyph: glyph.Watch, active: ownsRoute('#watches', route) },
    { hash: '#library/skills', label: 'Library', glyph: glyph.Search, active: ownsRoute('#library/skills', route) },
    { hash: '#usage', label: 'Usage', glyph: USAGE_GLYPH, active: ownsRoute('#usage', route) },
    { hash: '#settings/models', label: 'Settings', glyph: glyph.Thinking, active: ownsRoute('#settings/models', route) },
  ]

  /*
    A planned destination links to its roadmap page and nowhere else, so it is
    matched exactly: Hosts must not light on the Planning area's page.
  */
  const roadmap: NavDestination[] = ROADMAP_DESTINATIONS.map(({ area, label }) => {
    const hash = `#roadmap/${area}`
    return { hash, label, glyph: PLANNED_GLYPH, active: isRoute(hash, route), planned: true }
  })

  const all = dedupe([
    ...primary,
    ...projectDestinations.map(({ hash, label, glyph: mark, active, count, planned }) => ({
      hash,
      label,
      glyph: mark,
      active,
      ...(count !== undefined ? { count } : {}),
      ...(planned !== undefined ? { planned } : {}),
    })),
    ...resources,
    ...roadmap,
  ])

  return { primary, projects: projectDestinations, resources, roadmap, all }
}
