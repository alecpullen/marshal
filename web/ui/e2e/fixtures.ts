import { expect, type Page, type Route } from '@playwright/test'
import type { AgentStatus, Inbox, ProjectStatus } from '../src/lib/contract'
import type { FactoryCommand, FactoryConfig, FactoryCreate, FactoryItem } from '../src/lib/factory'
import type { ProjectSettings } from '../src/lib/api'
import { env } from './env'

/*
  Shared Playwright helpers and fixtures.

  Two kinds of case live in this directory and they are not interchangeable:

  - real bridge: a throwaway `webbridge` owns a real git project, three agent
    profiles and no reachable provider (scripts/e2e/run_browser_checks.sh). The
    shell, its routes and its settings API are exercised for real, but no
    factory work is created, approved, retried or recovered: the bridge has no
    GitHub credential and `factoryAPI.preflight` requires one.
  - mocked presentation: the fleet/factory reads are answered with the builders
    below, so an expensive or partial state (mixed attention, a failed factory
    read, an unavailable project) can be presented deterministically. A mocked
    read is evidence about rendering only — never that the real contract
    succeeded.

  Both run against the built SPA the bridge actually serves.
*/

export const BASE = env('PW_BASE_URL', 'http://127.0.0.1:7799')
export const TOKEN = env('PW_TOKEN', 'e2e-token')
export const ROOT = env('PW_PROJECT_ROOT', '/tmp/pf/proj')

/** A deterministic timestamp, so a fixture never depends on the clock. */
export const AT = '2026-11-16T09:00:00Z'

export const auth = () => ({ Authorization: `Bearer ${TOKEN}` })

/**
 * The shell's own route, as `App.svelte` reads it: an empty hash and `#` are
 * the same route, and assigning `'#'` leaves `location.hash` empty in Chromium.
 */
export const currentHash = (page: Page): Promise<string> =>
  page.evaluate(() => window.location.hash || '#')

/** The fixture project's directory name, which is the shell's project label. */
export const projectName = (root: string = ROOT): string => root.split('/').filter(Boolean).pop() ?? root

/** Open a route with the bearer token already in sessionStorage, so no prompt appears. */
export async function open(page: Page, hash: string = '#') {
  await page.addInitScript(([key, token]) => {
    try { sessionStorage.setItem(key, token) } catch { /* storage may be unavailable */ }
  }, ['marshal:token', TOKEN])
  await page.goto(`${BASE}/${hash}`)
}

/**
 * Open a route in a fresh document.
 *
 * Navigating to a URL that differs only in its hash is a same-document
 * navigation, so `page.goto` alone leaves the previous route's components
 * mounted. This forces the reload a deep link actually gets.
 */
export async function reopen(page: Page, hash: string) {
  await page.goto(`${BASE}/${hash}`)
  await page.reload()
}

/** The narrow-width requirement: no sideways scrolling, every control inside the viewport. */
export async function expectNoHorizontalOverflow(page: Page, labels: string[] = []) {
  const width = await page.evaluate(() => document.documentElement.clientWidth)
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  expect(scrollWidth, `the document scrolls horizontally at ${width}px`).toBeLessThanOrEqual(width + 1)
  for (const label of labels) {
    const box = await page.getByLabel(label, { exact: true }).boundingBox()
    expect(box, `${label} is not visible`).not.toBeNull()
    expect(box!.x, `${label} starts off-screen`).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width, `${label} overflows the ${width}px viewport`).toBeLessThanOrEqual(width + 1)
  }
}

/** The focused element's accessible-ish name, for the keyboard walk below. */
const focusedName = (page: Page) =>
  page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    if (!el || el === document.body) return ''
    return el.getAttribute('aria-label') || (el.textContent ?? '').trim()
  })

/**
 * Tab until the focused control matches, then leave it focused.
 *
 * The phone shell (a bottom bar) and the desktop shell (a sidebar) order their
 * controls differently, so a fixed number of Tabs cannot reach one destination
 * at every width. The walk is bounded and reports what it did focus, so a
 * failure names the real focus order rather than a timeout.
 */
export async function tabTo(page: Page, match: RegExp, limit = 80): Promise<void> {
  const seen: string[] = []
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press('Tab')
    const name = await focusedName(page)
    if (match.test(name)) return
    seen.push(name)
  }
  throw new Error(`Tab never reached ${match}; focus order was: ${seen.join(' → ')}`)
}

/**
 * Wait for every running animation and CSS transition to finish.
 *
 * Switching `data-theme` starts the buttons' colour transitions, and a scan
 * taken mid-transition reads blended colours that belong to no palette —
 * reporting contrast failures that do not exist once the page settles.
 */
export async function settleAnimations(page: Page) {
  await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))))
}

/** A light/dark toggle on the document, which is how app.css scopes the theme. */
export async function setTheme(page: Page, theme: 'dark' | 'light') {
  await page.evaluate((t) => {
    if (t === 'light') document.documentElement.setAttribute('data-theme', 'light')
    else document.documentElement.removeAttribute('data-theme')
  }, theme)
  await settleAnimations(page)
}

// ---------------------------------------------------------------------------
// Fixture builders
//
// Every builder returns a complete wire record built from the generated
// contract types (`src/lib/contract.ts`, `src/lib/factory.ts`), so a field the
// bridge added or renamed breaks this file instead of silently disappearing
// from a mock.
// ---------------------------------------------------------------------------

export function factoryConfig(overrides: Partial<FactoryConfig> = {}): FactoryConfig {
  return {
    repoId: 'acme/rocket',
    projectRoot: ROOT,
    hostId: 'local',
    targetBranch: 'main',
    workspace: '',
    native: true,
    implementerRouting: {},
    reviewerRouting: {},
    reviewerInstructions: '',
    implementerAuto: false,
    expectedChecks: [],
    noRequiredCiAcknowledged: true,
    maxRounds: 3,
    ciPollSeconds: 30,
    ciWaitSeconds: 1800,
    ...overrides,
  }
}

export function factoryItem(overrides: Partial<FactoryItem> = {}): FactoryItem {
  const title = 'Add rate limiting to the intake route'
  return {
    id: 'item-draft',
    title,
    state: 'draft',
    stage: '',
    repoId: 'acme/rocket',
    projectRoot: ROOT,
    hostId: 'local',
    branch: 'factory/item-draft',
    baseSha: 'a'.repeat(40),
    version: 1,
    draft: { requestId: 'req-item-draft', title, plan: 'Plan body', spec: '', config: factoryConfig() },
    approvals: null,
    grants: null,
    rounds: null,
    workers: null,
    prs: null,
    effects: null,
    decision: null,
    history: null,
    createdAt: AT,
    approvedAt: '',
    updatedAt: AT,
    ...overrides,
  }
}

/**
 * The answer to a create the bridge accepted: the request saved as one draft.
 *
 * Only the fields the handoff sent are carried; everything else is resolved
 * the way the bridge would resolve it, from `factoryConfig`'s defaults. A case
 * that needs to prove the saved draft reads the *server's* resolution rather
 * than the handoff's own summary supplies its own `create`.
 */
export function savedDraft(request: FactoryCreate): FactoryItem {
  return factoryItem({
    id: 'item-saved',
    title: request.title,
    state: 'draft',
    version: 1,
    draft: {
      ...request,
      config: factoryConfig({ ...request.config, expectedChecks: request.config.expectedChecks ?? [] }),
    },
  })
}

export function agentStatus(overrides: Partial<AgentStatus> = {}): AgentStatus {
  return {
    id: 'agent-1',
    project: ROOT,
    name: 'agent one',
    origin: 'ui',
    ownerId: 'local',
    status: 'running',
    activity: '',
    contextPct: 0,
    changedFiles: 0,
    interrupted: false,
    isolated: false,
    updatedAt: AT,
    ...overrides,
  }
}

export function projectStatus(overrides: Partial<ProjectStatus> = {}): ProjectStatus {
  return { root: ROOT, available: true, trust: 'trusted', isolation: 'available', ...overrides }
}

export function inboxOf(groups: Partial<Inbox['groups']> = {}, queue: Inbox['queue'] = []): Inbox {
  return {
    groups: { needsYou: [], running: [], ready: [], earlier: [], ...groups },
    queue,
  }
}

/** A factory worker row, for an item that holds an agent. */
export const worker = (agentId: string) => ({
  agentId,
  sessionId: `session-${agentId}`,
  role: 'implementer',
  spawnEffectId: `effect-${agentId}`,
  parked: false,
})

// ---------------------------------------------------------------------------
// Mocked-presentation route helpers
// ---------------------------------------------------------------------------

export interface MockFleetOptions {
  agents?: AgentStatus[]
  projects?: ProjectStatus[]
  inbox?: Inbox
  factoryItems?: FactoryItem[]
  /** Status for `GET /api/factory/items`; a non-2xx makes the read fail. */
  factoryStatus?: number
  /**
   * Answer `GET /api/projects/settings` with this project's settings, so a
   * factory defaults card or a handoff inherits values the case chose. Omitted
   * means the real fixture bridge answers, as it does for every other read.
   */
  settings?: ProjectSettings
  /** Model profiles the profile pickers offer. Omitted leaves the bridge's list. */
  profiles?: string[]
  /**
   * Answer a create. The default saves the request as a draft (`savedDraft`);
   * a case proving server-side resolution answers with its own record.
   */
  create?: (request: FactoryCreate) => FactoryItem
  /**
   * Answer a command. The default echoes the item it was sent for. Either way
   * the fixture's list is updated, so a later poll agrees with the command.
   */
  command?: (id: string, command: FactoryCommand, item: FactoryItem | undefined) => FactoryItem | undefined
}

export interface MockedFleet {
  /** Answer the held fleet stream and stop intercepting, so teardown is clean. */
  release(): Promise<void>
  /** Fleet SSE connections opened by the document. */
  fleetStreams(): number
  /** Factory list reads, from the shell's poller or from a view. */
  factoryReads(): number
  /** Create requests the SPA sent, in order, so a case can assert its payload. */
  creates(): FactoryCreate[]
  /** Command requests the SPA sent, with the item each named. */
  commands(): { id: string; command: FactoryCommand }[]
}

/**
 * Answer the fleet, the inbox, the factory list, the project settings and the
 * model profiles with fixtures.
 *
 * The fleet stream is held open rather than completed: a completed stream is
 * one the SPA reconnects to on a timer, and that timer would make a request
 * count meaningless. Creates and commands are recorded and answered here too,
 * because a case that asserts a payload must not reach the bridge's real
 * factory store. Everything else still goes to the real fixture bridge.
 */
export async function mockFleet(page: Page, options: MockFleetOptions = {}): Promise<MockedFleet> {
  const agents = options.agents ?? []
  const projects = options.projects ?? []
  const inbox = options.inbox ?? inboxOf()
  // Copied, because a create or a command updates the list a later poll reads.
  const items = [...(options.factoryItems ?? [])]
  const creates: FactoryCreate[] = []
  const commands: { id: string; command: FactoryCommand }[] = []
  let streams = 0
  let reads = 0
  let releaseStream = () => {}
  const held = new Promise<void>((resolve) => (releaseStream = resolve))

  const handler = async (route: Route) => {
    const url = new URL(route.request().url())
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

    // A command, and the single-item read a drill-in makes when the list has
    // not landed yet. Answered before the switch: neither is a list read.
    const command = /^\/api\/factory\/items\/([^/]+)\/commands$/.exec(url.pathname)
    if (command && route.request().method() === 'POST') {
      const id = decodeURIComponent(command[1])
      const sent = route.request().postDataJSON() as FactoryCommand
      commands.push({ id, command: sent })
      const target = items.find((candidate) => candidate.id === id)
      const applied = options.command ? options.command(id, sent, target) : target
      if (!applied) return json({ error: 'no such work item' }, 404)
      const at = items.findIndex((candidate) => candidate.id === id)
      if (at >= 0) items[at] = applied
      return json({ item: applied })
    }
    const one = /^\/api\/factory\/items\/([^/]+)$/.exec(url.pathname)
    if (one && route.request().method() === 'GET') {
      const target = items.find((candidate) => candidate.id === decodeURIComponent(one[1]))
      return target ? json({ item: target }) : json({ error: 'no such work item' }, 404)
    }

    switch (url.pathname) {
      case '/api/agents':
        return json(agents)
      case '/api/projects':
        return json(projects)
      case '/api/inbox':
        return json(inbox)
      case '/api/pending':
        return json([])
      case '/api/automations/review/drafts':
        return json({ drafts: [] })
      case '/api/automations/ci/history':
        return json({ history: [] })
      case '/api/projects/settings':
        return options.settings ? json(options.settings) : route.continue()
      case '/api/models':
        // Only the profile names matter to a picker; the bindings are the
        // bridge's own business, and a case reads them through the handoff.
        return options.profiles
          ? json({ profiles: Object.fromEntries(options.profiles.map((name) => [name, {}])) })
          : route.continue()
      case '/api/factory/items': {
        if (route.request().method() === 'POST') {
          const request = route.request().postDataJSON() as FactoryCreate
          creates.push(request)
          const saved = (options.create ?? savedDraft)(request)
          items.push(saved)
          return json({ item: saved })
        }
        reads++
        return options.factoryStatus
          ? json({ error: 'factory store is unavailable' }, options.factoryStatus)
          : json({ items })
      }
      case '/api/events':
        if (url.searchParams.get('stream') !== 'fleet') return route.continue()
        streams++
        await held
        try {
          await route.abort()
        } catch {
          // The document went away first; nothing left to answer.
        }
        return
      default:
        return route.continue()
    }
  }

  await page.route('**/api/**', handler)
  return {
    release: async () => {
      releaseStream()
      await page.unroute('**/api/**', handler)
    },
    fleetStreams: () => streams,
    factoryReads: () => reads,
    creates: () => creates,
    commands: () => commands,
  }
}
