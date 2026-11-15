import { expect, type Page, type Route } from '@playwright/test'
import type { AccessState, AgentStatus, Inbox, ProjectStatus } from '../src/lib/contract'
import type {
  FactoryApproval,
  FactoryCheck,
  FactoryCI,
  FactoryCommand,
  FactoryConfig,
  FactoryCreate,
  FactoryDecision,
  FactoryEvent,
  FactoryItem,
  FactoryPR,
  FactoryReport,
  FactoryRound,
  FactoryWorker,
} from '../src/lib/factory'
import type { PreviewDeployment, PreviewEnvironment, PreviewReport } from '../src/lib/preview'
import type { StackSnapshot } from '../src/lib/stack'
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
export const worker = (agentId: string, overrides: Partial<FactoryWorker> = {}): FactoryWorker => ({
  agentId,
  sessionId: `session-${agentId}`,
  role: 'implementer',
  spawnEffectId: `effect-${agentId}`,
  parked: false,
  ...overrides,
})

/** The base every round starts from, and the head this item's work is on. */
export const BASE_SHA = 'a'.repeat(40)
export const HEAD_SHA = 'b'.repeat(40)

export function factoryDecision(overrides: Partial<FactoryDecision> = {}): FactoryDecision {
  return {
    id: 'decision-1',
    kind: 'question',
    question: 'Should the limiter reject the request outright, or queue it and answer 202?',
    reason: '',
    effectId: 'effect-impl-1',
    createdAt: AT,
    ...overrides,
  }
}

export function factoryApproval(overrides: Partial<FactoryApproval> = {}): FactoryApproval {
  return {
    revision: 1,
    plan: 'Plan body',
    spec: 'Spec body',
    planHash: 'plan-hash-1',
    config: factoryConfig(),
    approvedAt: AT,
    ...overrides,
  }
}

export function factoryCheck(name: string, overrides: Partial<FactoryCheck> = {}): FactoryCheck {
  return {
    key: { kind: 'check_run', name, appId: 7 },
    id: `check-${name}`,
    sha: HEAD_SHA,
    status: 'completed',
    conclusion: 'success',
    url: '',
    logTail: '',
    observedAt: AT,
    ...overrides,
  }
}

export function factoryCI(checks: FactoryCheck[] | null, overrides: Partial<FactoryCI> = {}): FactoryCI {
  return { headSha: HEAD_SHA, checks, complete: true, observedAt: AT, ...overrides }
}

export function factoryReport(overrides: Partial<FactoryReport> = {}): FactoryReport {
  return {
    operationId: 'op-1',
    outcome: 'complete',
    summary: 'Implemented the limiter behind the existing intake handler.',
    question: '',
    reason: '',
    baseSha: BASE_SHA,
    headSha: HEAD_SHA,
    inputsAccessible: true,
    findings: null,
    ...overrides,
  }
}

export function factoryRound(overrides: Partial<FactoryRound> = {}): FactoryRound {
  return {
    number: 1,
    approvalRevision: 1,
    implementEffectId: 'effect-impl-1',
    baseSha: BASE_SHA,
    headSha: HEAD_SHA,
    treeSha: 'c'.repeat(40),
    ci: factoryCI([]),
    implementation: factoryReport(),
    review: factoryReport({ operationId: 'review-1', summary: 'Review complete with no findings.' }),
    startedAt: AT,
    finishedAt: AT,
    ...overrides,
  }
}

export function factoryPR(overrides: Partial<FactoryPR> = {}): FactoryPR {
  return {
    number: 42,
    url: 'https://forge.example/acme/rocket/pull/42',
    nodeId: 'PR_node42',
    headRef: 'factory/item-ready',
    baseRef: 'main',
    headSha: HEAD_SHA,
    baseSha: BASE_SHA,
    state: 'open',
    draft: false,
    ...overrides,
  }
}

export function factoryEvent(sequence: number, message: string, overrides: Partial<FactoryEvent> = {}): FactoryEvent {
  return { sequence, at: AT, kind: 'progress', message, effectId: 'effect-impl-1', ...overrides }
}

export function previewDeployment(overrides: Partial<PreviewDeployment> = {}): PreviewDeployment {
  return {
    id: 'deployment-1',
    environment: 'preview',
    originalEnvironment: 'preview',
    ref: 'factory/item-ready',
    sha: HEAD_SHA,
    task: 'deploy',
    state: 'success',
    environmentUrl: 'https://preview.example/acme/rocket/42',
    targetUrl: '',
    logUrl: 'https://preview.example/acme/rocket/42/logs',
    description: '',
    transient: true,
    production: false,
    createdAt: AT,
    updatedAt: AT,
    ...overrides,
  }
}

export function previewEnvironment(overrides: Partial<PreviewEnvironment> = {}): PreviewEnvironment {
  return {
    name: 'staging',
    id: 'env-staging',
    htmlUrl: 'https://preview.example/acme/rocket/environments/staging',
    branchPolicy: 'selected',
    protectionRuleCount: 1,
    ...overrides,
  }
}

export function previewReport(overrides: Partial<PreviewReport> = {}): PreviewReport {
  return {
    repoId: 'acme/rocket',
    forge: 'github',
    status: 'ok',
    ref: 'factory/item-ready',
    sha: HEAD_SHA,
    capabilities: [],
    environments: [],
    deployments: [],
    gaps: [],
    ...overrides,
  }
}

/** The empty transcript snapshot a fixture session answers with. */
export function stackSnapshot(sessionId: string, overrides: Partial<StackSnapshot> = {}): StackSnapshot {
  return { sessionId, rev: 0, roots: [], nodes: [], ...overrides }
}

/** The committed interaction tuple a fixture session reports. */
export function accessState(overrides: Partial<AccessState> = {}): AccessState {
  return { mode: 'act', auto: false, system_access: false, access_revision: 1, ...overrides }
}

// ---------------------------------------------------------------------------
// Mocked-presentation work items
//
// One builder per state slice 3 presents: active execution, a pending
// question, an exhausted retry, a named-worker recovery, failed CI, a ready
// review, several workers and PRs, and a preview whose revision disagrees with
// the recorded PR head. Each carries a long title, a long log or real code, and
// at least one genuinely absent optional field, because the surfaces have to
// survive all three. None of them is a claim that the bridge produced the
// state: the reads are answered from here.
// ---------------------------------------------------------------------------

/** A title long enough that a header has to wrap rather than clip it. */
export const LONG_TITLE =
  'Add per-tenant rate limiting, burst accounting and fair-share queueing to the intake route without changing the 202 fast path'

/** A log long enough that its container has to scroll instead of the page. */
export const LONG_LOG = Array.from(
  { length: 120 },
  (_, i) => `intake_test.go:${120 + i}: tenant t${i}: expected 429 after the burst, got 202 in ${i + 1}ms`,
).join('\n')

/** Real code, for the engineering report's selected-code section. */
export const CODE_EXCERPT = [
  'func (h *Intake) ServeHTTP(w http.ResponseWriter, r *http.Request) {',
  '\tt := tenant(r)',
  '\tif !h.limiter.Allow(t, time.Now()) {',
  '\t\thttp.Error(w, "rate limited", http.StatusTooManyRequests)',
  '\t\treturn',
  '\t}',
  '\tw.WriteHeader(http.StatusAccepted)',
  '}',
].join('\n')

const PLAN =
  'Add a token bucket per tenant, keep the 202 fast path, and record the burst so the fair-share queue can be added later.'
const SPEC = 'A rejected request answers 429 with a Retry-After header. No request already accepted is dropped.'

const NARRATION = {
  issue: 'One tenant can exhaust the intake budget for every other tenant.',
  approach: 'A token bucket per tenant, checked before the handler writes its fast path.',
  existingBehaviour: 'The intake handler answers 202 for every request it accepts.',
  alternatives: 'A global limiter was rejected: it starves a small tenant behind a large one.',
  tradeoffs: 'A per-tenant map costs memory proportional to the tenants seen in the window.',
  choice: 'Per-tenant buckets with a bounded map, because it is the smallest change that removes the starvation.',
  verification: 'Unit tests cover the burst and the Retry-After header; the intake suite passes.',
  limitations: 'The bucket is in-process, so two hosts keep separate budgets.',
  excerpts: [{ path: 'internal/intake/limiter.go', lang: 'go', code: CODE_EXCERPT }],
}

/** Active implementation: a running item holding one worker and its stream. */
export function activeImplementation(): FactoryItem {
  return factoryItem({
    id: 'item-active',
    title: LONG_TITLE,
    state: 'running',
    stage: 'implement',
    version: 4,
    branch: 'factory/item-active',
    draft: { requestId: 'req-item-active', title: LONG_TITLE, plan: PLAN, spec: SPEC, config: factoryConfig({ maxRounds: 3 }) },
    approvals: [
      factoryApproval({
        plan: PLAN,
        spec: SPEC,
        config: factoryConfig({ maxRounds: 3, implementerRouting: { profile: 'alpha' }, reviewerRouting: { profile: 'beta' } }),
      }),
    ],
    // The last worker is the one a wide screen opens on entry.
    workers: [worker('worker-1')],
    effects: [
      {
        outcome: null,
        id: 'effect-impl-1',
        kind: 'implement',
        status: 'running',
        workerRole: 'implementer',
        round: 1,
        approvalRevision: 1,
        worker: worker('worker-1'),
        expectedHead: '',
        expectedBase: BASE_SHA,
        createdAt: AT,
      },
    ],
    rounds: [
      factoryRound({
        // No review yet, and no narration: the report falls back to its summary.
        ci: factoryCI([factoryCheck('build', { status: 'in_progress', conclusion: '' })], { complete: false }),
        implementation: factoryReport({ outcome: '', summary: 'Implementing the limiter; the unit suite is still running.' }),
        review: null,
      }),
    ],
    history: [
      factoryEvent(1, 'Approved revision 1.'),
      factoryEvent(2, 'Implementer worker-1 started on factory/item-active.'),
      factoryEvent(3, 'Read internal/intake/handler.go and internal/intake/handler_test.go.'),
      factoryEvent(4, 'Editing internal/intake/limiter.go — adding the per-tenant bucket.'),
      factoryEvent(5, 'Running go test ./internal/intake/...'),
      factoryEvent(6, `${LONG_LOG.split('\n').length} test lines reported so far; the suite has not finished.`),
    ],
  })
}

/** A pending question: the one decision kind that asks for typed input. */
export function pendingQuestion(): FactoryItem {
  return factoryItem({
    id: 'item-question',
    title: 'Choose the rejection path for over-budget intake requests',
    state: 'needs_input',
    stage: 'implement',
    version: 5,
    branch: 'factory/item-question',
    draft: { requestId: 'req-item-question', title: 'Choose the rejection path', plan: PLAN, spec: SPEC, config: factoryConfig() },
    decision: factoryDecision({
      id: 'decision-question',
      kind: 'question',
      // Multi-line, so the panel's wrapping is exercised rather than assumed.
      question:
        'A request that arrives over the tenant budget can be rejected outright with 429, or queued and answered 202 with a deferred result.\nBoth keep the fast path. Which one should this item implement?',
      reason: '',
    }),
    workers: [worker('worker-1')],
    rounds: [factoryRound({ number: 1, ci: factoryCI([factoryCheck('build')], { complete: false }) })],
    history: [
      factoryEvent(1, 'Approved revision 1.'),
      factoryEvent(2, 'Implementer worker-1 needs a decision before it can continue.'),
    ],
  })
}

/** An exhausted retry: the round allowance is spent and a new one needs granting. */
export function exhaustedRetry(): FactoryItem {
  return factoryItem({
    id: 'item-exhausted',
    title: 'Repair the two remaining findings on the intake limiter',
    state: 'needs_input',
    stage: 'review',
    version: 8,
    branch: 'factory/item-exhausted',
    draft: { requestId: 'req-item-exhausted', title: 'Repair the intake limiter', plan: PLAN, spec: SPEC, config: factoryConfig({ maxRounds: 3 }) },
    approvals: [
      factoryApproval({
        plan: PLAN,
        spec: SPEC,
        config: factoryConfig({ maxRounds: 3, implementerRouting: { profile: 'alpha' }, reviewerRouting: { profile: 'beta' } }),
      }),
    ],
    decision: factoryDecision({
      id: 'decision-exhausted',
      kind: 'exhausted',
      // No question: an exhausted decision asks for an allowance, not an answer.
      question: '',
      reason: 'Round 3 ended with two findings still open, and the approved round allowance is spent.',
    }),
    rounds: [factoryRound({ number: 1 }), factoryRound({ number: 2 }), factoryRound({ number: 3 })],
    grants: [],
    workers: [worker('worker-1')],
    history: [factoryEvent(1, 'Approved revision 1.'), factoryEvent(2, 'Round 3 exhausted the approved allowance.')],
  })
}

/** A recovery: the named previous worker has to be attested as stopped. */
export function namedWorkerRecovery(): FactoryItem {
  return factoryItem({
    id: 'item-recovery',
    title: 'Reconcile the intake limiter after its worker stopped reporting',
    state: 'needs_input',
    stage: 'implement',
    version: 6,
    branch: 'factory/item-recovery',
    draft: { requestId: 'req-item-recovery', title: 'Reconcile the intake limiter', plan: PLAN, spec: SPEC, config: factoryConfig() },
    approvals: [factoryApproval({ plan: PLAN, spec: SPEC })],
    decision: factoryDecision({
      id: 'decision-recovery',
      kind: 'recovery',
      question: '',
      reason: 'The last worker stopped reporting. Confirm independently that it is gone before a new round starts.',
    }),
    rounds: [factoryRound({ number: 1 })],
    // The last worker is the one the attestation must name exactly.
    workers: [worker('worker-7'), worker('worker-8', { role: 'reviewer', parked: true })],
    history: [factoryEvent(1, 'Approved revision 1.'), factoryEvent(2, 'Worker worker-8 stopped reporting.')],
  })
}

/** Failing CI: failures and findings lead, with a long log and real code behind. */
export function failedCI(): FactoryItem {
  return factoryItem({
    id: 'item-failed',
    title: 'Fix the flaky intake test before the limiter can land',
    state: 'needs_input',
    stage: 'review',
    version: 6,
    branch: 'factory/item-failed',
    draft: { requestId: 'req-item-failed', title: 'Fix the flaky intake test', plan: PLAN, spec: SPEC, config: factoryConfig() },
    approvals: [factoryApproval({ plan: PLAN, spec: SPEC })],
    rounds: [
      factoryRound({
        ci: factoryCI([
          factoryCheck('build'),
          factoryCheck('unit', { conclusion: 'failure', url: 'https://forge.example/acme/rocket/runs/7', logTail: LONG_LOG }),
          factoryCheck('lint'),
          // A failing check with neither a URL nor a log: both are optional.
          factoryCheck('e2e', { conclusion: 'failure', url: '', logTail: '' }),
        ]),
        implementation: factoryReport({ summary: 'Added the limiter and its unit test.', narration: NARRATION }),
        review: factoryReport({
          operationId: 'review-1',
          outcome: 'complete',
          summary: 'Two findings remain: the burst is not accounted for and the 429 carries no Retry-After.',
          findings: [
            { severity: 'high', path: 'internal/intake/limiter.go', line: 42, title: 'Burst is not accounted for', body: 'The bucket refills before the burst is measured.' },
            // A finding with no path, line or body: all three are optional.
            { severity: 'medium', title: 'The 429 carries no Retry-After header' },
          ],
        }),
      }),
    ],
    prs: [factoryPR()],
    workers: [worker('worker-1')],
    history: [factoryEvent(1, 'Approved revision 1.'), factoryEvent(2, 'unit failed on the pulled revision.')],
  })
}

/** A ready review: complete evidence, a clean review, and a matching preview. */
export function readyReview(): FactoryItem {
  return factoryItem({
    id: 'item-ready',
    title: 'Land per-tenant intake rate limiting with burst accounting',
    state: 'ready',
    stage: 'ready',
    version: 7,
    branch: 'factory/item-ready',
    draft: { requestId: 'req-item-ready', title: 'Land per-tenant intake rate limiting', plan: PLAN, spec: SPEC, config: factoryConfig() },
    approvals: [factoryApproval({ revision: 2, plan: PLAN, spec: SPEC, config: factoryConfig({ implementerRouting: { profile: 'alpha' }, reviewerRouting: { profile: 'beta' } }) })],
    rounds: [
      factoryRound({
        number: 2,
        approvalRevision: 2,
        ci: factoryCI([factoryCheck('build'), factoryCheck('lint'), factoryCheck('unit'), factoryCheck('e2e')]),
        implementation: factoryReport({ summary: 'Implemented per-tenant buckets and their tests.', narration: NARRATION }),
        review: factoryReport({ operationId: 'review-2', outcome: 'complete', inputsAccessible: true, findings: null, summary: 'Review complete with no findings.' }),
      }),
    ],
    prs: [factoryPR()],
    workers: [worker('worker-1')],
    history: [factoryEvent(1, 'Approved revision 2.'), factoryEvent(2, 'Round 2 finished with a clean review.')],
  })
}

/** Several workers and PRs, one parked and one PR with nothing recorded. */
export function multiWorkerPRs(): FactoryItem {
  return factoryItem({
    id: 'item-multi',
    title: 'Split the limiter across two workers and open a repair PR',
    state: 'running',
    stage: 'review',
    version: 9,
    branch: 'factory/item-multi',
    draft: { requestId: 'req-item-multi', title: 'Split the limiter across two workers', plan: PLAN, spec: SPEC, config: factoryConfig() },
    approvals: [factoryApproval({ plan: PLAN, spec: SPEC })],
    workers: [
      worker('worker-1'),
      worker('worker-2', { role: 'reviewer' }),
      worker('worker-3', { role: 'implementer', parked: true }),
    ],
    rounds: [factoryRound({ number: 1 }), factoryRound({ number: 2, headSha: 'd'.repeat(40) })],
    prs: [
      factoryPR(),
      // Closed, draft, and with no URL, head or base recorded.
      factoryPR({ number: 43, url: '', headRef: '', baseRef: '', headSha: 'd'.repeat(40), state: 'closed', draft: true }),
    ],
    history: [factoryEvent(1, 'Approved revision 1.'), factoryEvent(2, 'Worker worker-3 parked.')],
  })
}


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
   * Answer the preview-environment read a review's PreviewCard makes. Omitted
   * leaves it to the fixture bridge, which has no forge and answers with an
   * error the card reports.
   */
  preview?: PreviewReport
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
  /**
   * Session event streams opened for one agent, or for every agent when the
   * argument is omitted. A conversation that is not mounted must open none.
   */
  sessionStreams(sessionId?: string): number
  /** Factory list reads, from the shell's poller or from a view. */
  factoryReads(): number
  /** The list the next read will answer, replaced wholesale. */
  setItems(items: FactoryItem[]): void
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
  /** Session event streams, per agent, so a case can name the one it expects. */
  const sessionStreams = new Map<string, number>()
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
    // The review's preview card reads one repository's environments. Answered
    // only when a case supplied a report: without one the fixture bridge's own
    // answer (an unreadable forge) is what the card should show.
    if (options.preview && /^\/api\/repos\/[^/]+\/preview-environments$/.test(url.pathname)) {
      return json(options.preview)
    }
    /*
      The reads an agent conversation mounts. The fixture bridge has no agent
      behind these ids, so the answers are the bridge's own shapes and empty:
      they exist so a factory-item navigation case is not obscured by dock
      errors from reads that belong to the full session. `last-request` and
      `roster` answer 501, which is the bridge's own "this agent predates it".
    */
    const session = /^\/api\/sessions\/([^/]+)\/(load|stack|access|prompt|steer|cancel|mode|last-request|step-diffs|roster)$/.exec(url.pathname)
    if (session) {
      const id = decodeURIComponent(session[1])
      switch (session[2]) {
        case 'load':
          return json({ sessionId: id, events: [] })
        case 'stack':
          return json(stackSnapshot(id))
        case 'access':
          return json(accessState())
        case 'step-diffs':
          return json([])
        case 'last-request':
        case 'roster':
          return json({ error: 'unsupported' }, 501)
        default:
          // prompt, steer, cancel and mode: accepted, with nothing to report.
          return json({})
      }
    }
    const agentRead = /^\/api\/agents\/([^/]+)\/(diff|gate|files|file)$/.exec(url.pathname)
    if (agentRead) {
      switch (agentRead[2]) {
        case 'diff':
          return json({ files: [] })
        case 'gate':
          // 204 is the bridge's answer for an agent with no verify record.
          return route.fulfill({ status: 204, body: '' })
        case 'files':
          return json({ entries: [] })
        default:
          return json({ path: url.searchParams.get('path') ?? '', size: 0, binary: false, truncated: false, content: '' })
      }
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
        // Two streams arrive here: the fleet's (`stream=fleet`) and a
        // conversation's (`sessionId=<id>`). Both are held open rather than
        // completed, because a completed stream is one the SPA reconnects to
        // on a timer, and that timer would make a request count meaningless.
        {
          const stream = url.searchParams.get('stream')
          const conversation = url.searchParams.get('sessionId')
          if (stream ? stream !== 'fleet' : !conversation) return route.continue()
          if (stream) streams++
          else sessionStreams.set(conversation!, (sessionStreams.get(conversation!) ?? 0) + 1)
        }
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
    sessionStreams: (sessionId?: string) =>
      sessionId === undefined
        ? [...sessionStreams.values()].reduce((total, count) => total + count, 0)
        : (sessionStreams.get(sessionId) ?? 0),
    factoryReads: () => reads,
    setItems: (next: FactoryItem[]) => {
      // Replaced in place: the single-item read, a create and a command all
      // answer from this same list, so they agree with the next poll.
      items.splice(0, items.length, ...next)
    },
    creates: () => creates,
    commands: () => commands,
  }
}
