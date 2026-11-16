/**
 * The Studio planner's wire types and pure helpers.
 *
 * These shapes mirror `web/bridge/planner_types.go`. They live here rather
 * than in `./contract.ts` because that file is generated from
 * `web/bridge/contract`, and the planner routes are not part of that contract
 * yet. Nothing in this module touches the network or the DOM: every rule a
 * planner view applies is a function of what the bridge reported, so the rules
 * can be tested without mounting a component.
 */

import type { FactoryCreate, FactoryCreateConfig } from './factory'

/** A planner's access mode. Permissive is the default; restricted is "default". */
export type PlannerMode = 'bypassPermissions' | 'default'

/** A new planner asks for the permissive mode unless the person chooses otherwise. */
export const PLANNER_DEFAULT_MODE: PlannerMode = 'bypassPermissions'

const MODE_LABELS: Record<PlannerMode, string> = {
  bypassPermissions: 'Permissive',
  default: 'Restricted',
}

/** The word the person reads for a mode, or the raw value when it is not one we know. */
export function plannerModeLabel(mode: string): string {
  return MODE_LABELS[mode as PlannerMode] ?? mode
}

/**
 * What permissive means, said once. The second sentence matters: the planner's
 * own access mode is not Marshal's tool policy, and presenting the two as the
 * same thing would misdescribe what approving a planner authorizes.
 */
export const PLANNER_PERMISSIVE_NOTE =
  "Permissive lets the planner edit files and run commands without a per-action approval. This is the planner's own access mode, not Marshal's native tool policy."
export const PLANNER_RESTRICTED_NOTE = 'Restricted asks before each action the planner takes.'

/** How a workspace looks to a new planner. Ownership is never inferred from a missing record. */
export type PlannerOccupancy = 'free' | 'in_use' | 'unknown'

/** What the planner's session reported it can do. */
export interface PlannerCapabilities {
  prompt: boolean
  cancel: boolean
  permissionRequests: boolean
  sessionLoad: boolean
  workspaceEdits: boolean
  modes: string[]
}

/** One dispatched prompt: hashes and lengths, never the text itself. */
export interface PlannerTurn {
  requestId: string
  textHash: string
  state: string
  stopReason?: string
  reason?: string
  at: string
}

/** One agent request and its single settlement. */
export interface PlannerDecision {
  requestId: string
  method?: string
  state: string
  response?: unknown
  reason?: string
  at: string
}

/** A reference the planner record carries; captured text lives on a snapshot instead. */
export interface PlannerArtifact {
  kind: string
  ref: string
  label?: string
}

/** A planner as `GET /api/planners/{id}` reports it. */
export interface PlannerView {
  id: string
  projectRoot: string
  repoId: string
  backendId: string
  workspaceId: string
  occupancy: PlannerOccupancy
  owners?: string[]
  sessionId?: string
  requestedMode: string
  confirmedMode?: string
  modeConfirmed: boolean
  state: string
  live: boolean
  eventSequence: number
  capabilities: PlannerCapabilities
  sessionLoad: boolean
  resumeAvailable: boolean
  resumeReason?: string
  trust?: string
  turns?: PlannerTurn[]
  requests?: PlannerDecision[]
  artifacts?: PlannerArtifact[]
  reason?: string
  createdAt: string
  updatedAt: string
  endedAt?: string | null
}

/** `POST /api/planners` — a workspace id is one the bridge returned earlier, never a path. */
export interface PlannerCreateRequest {
  projectRoot: string
  repoId: string
  workspaceId?: string
  proceedShared?: boolean
  mode?: PlannerMode
}

export interface PlannerPromptRequest {
  requestId: string
  text: string
  context?: string
}

/** What a prompt answers: the dispatch, or the outcome already recorded for a replay. */
export interface PlannerTurnReply {
  requestId: string
  state: string
  stopReason?: string
}

/** One journaled event. `sequence` is the planner's own counter, not the backend's. */
export interface PlannerEvent {
  sequence: number
  kind: string
  data?: unknown
  truncated?: boolean
}

/** One piece of text a snapshot froze; its sha256 is verified on every read. */
export interface PlannerSnapshotArtifact {
  kind: string
  content: string
  sha256: string
}

/** One captured file: repository-relative path, operation, hashes and its diff. */
export interface PlannerSnapshotFile {
  path: string
  operation: string
  baseSha256?: string
  contentSha256?: string
  executable: boolean
  diff: string
}

/** A path a capture refused, with the reason it refused it. */
export interface PlannerRejection {
  path: string
  reason: string
}

/** A snapshot as a review reports it. */
export interface PlannerSnapshotView {
  id: string
  sha256: string
  baseCommit: string
  artifacts: PlannerSnapshotArtifact[]
  files: PlannerSnapshotFile[]
  createdAt: string
}

/** What a capture answers: the frozen snapshot and the paths it refused. */
export interface PlannerSnapshotReply extends PlannerSnapshotView {
  rejected: PlannerRejection[]
}

/** The `POST /api/planners/{id}/snapshots` body. An empty `paths` is documents-only. */
export interface PlannerSnapshotRequest {
  plan: string
  spec: string
  paths: string[]
}

export interface PlannerContextArtifact {
  kind: string
  sha256?: string
  source: string
  approved: boolean
  snapshotId?: string
  ref?: string
}

export interface PlannerContextDecision {
  source: string
  requestId: string
  method?: string
  state: string
  at: string
}

export interface PlannerContextQuestion {
  source: string
  requestId: string
  method?: string
  state: string
  at: string
}

/** The bounded briefing a planner supports; `absent` names the sources it does not hold. */
export interface PlannerContextView {
  artifacts: PlannerContextArtifact[]
  decisions: PlannerContextDecision[]
  unresolved: PlannerContextQuestion[]
  absent: string[]
}

/* ------------------------------------------------------------------ */
/* Workspace occupancy                                                 */
/* ------------------------------------------------------------------ */

/** A bridge's refusal to start a planner on a workspace that is not free. */
export interface PlannerOccupancyNotice {
  workspaceId: string
  occupancy: PlannerOccupancy
  owners: string[]
  message: string
}

/**
 * The occupancy refusal inside a 409, read structurally so this module needs
 * no import of the api layer. Anything else — a 500, a 400 for an unknown
 * mode — is not an occupancy question and returns null.
 */
export function occupancyNotice(error: unknown): PlannerOccupancyNotice | null {
  const e = error as { status?: unknown; body?: unknown } | null
  if (!e || e.status !== 409) return null
  const body = e.body as { occupancy?: unknown; workspaceId?: unknown; owners?: unknown; error?: unknown } | null
  const occupancy = body?.occupancy
  if (occupancy !== 'in_use' && occupancy !== 'unknown') return null
  const owners = Array.isArray(body?.owners) ? body.owners.filter((o): o is string => typeof o === 'string') : []
  return {
    workspaceId: typeof body?.workspaceId === 'string' ? body.workspaceId : '',
    occupancy,
    owners,
    message: typeof body?.error === 'string' ? body.error : '',
  }
}

/** The warning a shared workspace gets: who holds it, or why nobody can say. */
export function occupancyWarning(n: PlannerOccupancyNotice): string {
  const consequence = "Two planners writing one workspace can overwrite each other's files."
  if (n.occupancy === 'in_use') {
    const owner = n.owners.length ? n.owners.join(', ') : 'another planner'
    return `Workspace ${n.workspaceId} is in use by ${owner}. ${consequence}`
  }
  return `This bridge cannot confirm whether workspace ${n.workspaceId} is still in use: its owner is neither live nor gone. ${consequence}`
}

/* ------------------------------------------------------------------ */
/* Session state                                                       */
/* ------------------------------------------------------------------ */

/** True while a dispatched turn has not settled. */
export function plannerTurnInFlight(view: PlannerView | null): boolean {
  return (view?.turns ?? []).some((t) => t.state === 'dispatched' || t.state === 'running')
}

/** The first turn whose outcome the backend could not observe, if the journal has one. */
export function unknownTurn(view: PlannerView | null): PlannerTurn | null {
  return (view?.turns ?? []).find((t) => t.state === 'unknown') ?? null
}

/* ------------------------------------------------------------------ */
/* Events                                                              */
/* ------------------------------------------------------------------ */

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : {}
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/** One event as the stream renders it: a label, then the text it carries. */
export interface PlannerEventLine {
  label: string
  text: string
}

/**
 * The readable line for one journaled event. The stream is the authority on
 * what happened, so every kind gets a line — an unmodelled one is shown under
 * its own name rather than dropped.
 */
export function describePlannerEvent(event: PlannerEvent): PlannerEventLine {
  const data = asRecord(event.data)
  switch (event.kind) {
    case 'turn.text':
      return { label: 'Planner', text: str(data.text) }
    case 'turn.tool':
      return { label: 'Tool', text: toolLine(data) }
    case 'turn.prompt':
      return { label: 'Prompt sent', text: 'The prompt was dispatched to the planner.' }
    case 'turn.permission':
      return { label: 'Permission request', text: requestLine('permission', data.params ?? data.update) }
    case 'turn.question':
      return { label: 'Question', text: requestLine('question', data.params ?? data.update) }
    case 'turn.cancelled':
      return { label: 'Turn cancelled', text: str(data.reason) }
    case 'turn.uncertain':
      return { label: 'Turn outcome unknown', text: str(data.reason) || 'The backend could not observe the turn; it was not resent.' }
    case 'turn.unknown':
      return { label: 'Unrecognised update', text: str(data.method) || str(data.kind) || 'The adapter sent an update this bridge does not model.' }
    case 'session.mode':
      return { label: 'Mode', text: str(data.modeId) }
    case 'session.started':
      return { label: 'Session', text: str(data.sessionId) }
    case 'planner.started':
      return { label: 'Started', text: `${plannerModeLabel(str(data.mode))} in workspace ${str(data.workspaceId)}` }
    case 'planner.ended':
      return { label: 'Ended', text: 'The planner has ended.' }
    case 'unsupported.request':
      return { label: 'Unsupported request', text: str(data.method) }
    default:
      return { label: event.kind, text: event.truncated ? 'This event exceeded the journal bound.' : '' }
  }
}

/** A tool update's own words: its title, else its kind. */
function toolLine(data: Record<string, unknown>): string {
  const update = asRecord(data.update)
  const payload = asRecord(update.payload)
  const title = str(payload.title) || str(payload.toolName) || str(payload.name)
  const status = str(payload.status)
  const kind = str(data.kind) || str(update.kind)
  const head = title || kind || 'tool activity'
  return status ? `${head} (${status})` : head
}

/** The extra context a bounded event still carries, or a preview of what was cut. */
function paramsPreview(raw: unknown): { payload: Record<string, unknown>; preview: string } {
  const rec = asRecord(raw)
  const payload = asRecord(rec.payload)
  // A request-shaped event nests its payload; an update-shaped one (the raw
  // session update) carries the fields directly. Reading both keeps a question
  // readable whichever path emitted it.
  const effective = Object.keys(payload).length ? payload : rec
  return { payload: effective, preview: str(rec.preview) }
}

function requestLine(kind: 'permission' | 'question', raw: unknown): string {
  const { payload, preview } = paramsPreview(raw)
  if (kind === 'permission') {
    const parts = [
      str(payload.toolName) ? `tool: ${str(payload.toolName)}` : '',
      str(payload.command) ? `command: ${str(payload.command)}` : '',
      str(payload.risk) ? `risk: ${str(payload.risk)}` : '',
      str(payload.reason),
    ].filter(Boolean)
    return parts.join(' · ') || preview || 'The planner is waiting for permission.'
  }
  const questions = Array.isArray(payload.questions) ? payload.questions : []
  const first = asRecord(questions[0])
  const text = str(first.question) || str(payload.message) || str(payload.prompt)
  if (text) return text
  // A payload the journal had to cut carries no question text. Showing its raw
  // preview would post that JSON back as the answer's question, so say what
  // happened instead.
  if (asRecord(raw).truncated === true) {
    return 'The planner is waiting for an answer (its request exceeded the journal limit).'
  }
  return preview || 'The planner is waiting for an answer.'
}

/* ------------------------------------------------------------------ */
/* Requests the person has to answer                                   */
/* ------------------------------------------------------------------ */

/** One open request, as a card the person answers once. */
export interface PlannerRequestCard {
  requestId: string
  method: string
  kind: 'permission' | 'question'
  title: string
  /** The command, the question, or whatever the payload still carried. */
  detail: string
  /**
   * Every question the request asks, in the order the answers must be posted.
   * A request may ask several, and one answer per question is the only shape
   * `session/request_question` settles with; anything else is a decline.
   */
  questions: string[]
}

function requestKindOf(method: string, kind: string): 'permission' | 'question' {
  if (kind === 'turn.permission' || kind === 'turn.question') return kind === 'turn.question' ? 'question' : 'permission'
  return /permission/i.test(method) ? 'permission' : 'question'
}

/**
 * Every request that still needs an answer, exactly once per request ID.
 *
 * The journal is the source of what was asked (it carries the params), and the
 * record is the source of what is settled: a request the record shows as
 * answered is not asked again, and a replayed journal entry for one request is
 * one card, not two.
 */
export function plannerRequests(view: PlannerView | null, events: PlannerEvent[]): PlannerRequestCard[] {
  const settled = new Map<string, PlannerDecision>()
  for (const request of view?.requests ?? []) settled.set(request.requestId, request)
  const cards = new Map<string, PlannerRequestCard>()
  for (const event of events) {
    if (event.kind !== 'turn.permission' && event.kind !== 'turn.question') continue
    const data = asRecord(event.data)
    const requestId = str(data.requestId)
    if (!requestId || cards.has(requestId)) continue
    // A settled request is done, whatever the journal still replays.
    const record = settled.get(requestId)
    if (record && record.state !== 'pending') continue
    const method = str(data.method)
    const kind = requestKindOf(method, event.kind)
    const asked = questionTexts(data.params)
    cards.set(requestId, requestCard(requestId, method, kind, requestLine(kind, data.params), asked))
  }
  // A request the record knows about but the journal did not replay is still
  // open: it gets a card with what the record can say, rather than vanishing.
  for (const record of settled.values()) {
    if (record.state !== 'pending' || cards.has(record.requestId)) continue
    const method = record.method ?? ''
    const kind = requestKindOf(method, '')
    cards.set(record.requestId, requestCard(record.requestId, method, kind, '', []))
  }
  return [...cards.values()]
}

/** The question texts one request payload asks, in their own order. */
function questionTexts(raw: unknown): string[] {
  const payload = asRecord(asRecord(raw).payload)
  if (!Array.isArray(payload.questions)) return []
  return payload.questions.map((q) => str(asRecord(q).question)).filter((q) => q !== '')
}

function requestCard(requestId: string, method: string, kind: 'permission' | 'question', detail: string, questions: string[]): PlannerRequestCard {
  return {
    requestId,
    method,
    kind,
    title: kind === 'permission' ? 'Permission requested' : 'Question from the planner',
    detail,
    // A question card with no readable text still needs an answer slot: the
    // one the person types stands in for the question it could not name.
    questions: questions.length ? questions : kind === 'question' ? [detail] : [],
  }
}

/** The response a chosen permission answer posts. */
export function permissionAnswer(allow: boolean): unknown {
  return { approved: allow }
}

/** The response one question answer posts, in the shape `session/request_question` settles with. */
export function questionAnswer(question: string, answer: string): unknown {
  return { answers: [{ question, answer }] }
}

/**
 * The response a card with several questions posts: one answer per question,
 * in the order the request asked them. A request settled with any other shape
 * is treated as declined, so the answers are never merged into one.
 */
export function questionAnswers(questions: string[], answers: string[]): unknown {
  return { answers: questions.map((question, i) => ({ question, answer: answers[i] ?? '' })) }
}

/* ------------------------------------------------------------------ */
/* Snapshots and context                                               */
/* ------------------------------------------------------------------ */

/** The exact captured text of one artifact kind, or '' when the snapshot has none. */
export function artifactText(snapshot: PlannerSnapshotView | null, kind: string): string {
  return snapshot?.artifacts.find((a) => a.kind === kind)?.content ?? ''
}

/** One line summarising what a briefing holds, for the collapsed view. */
export function contextSummary(context: PlannerContextView | null): string {
  if (!context) return 'Context has not been read yet.'
  const count = (n: number, what: string) => `${n} ${what}${n === 1 ? '' : 's'}`
  return [
    count(context.artifacts.length, 'artifact'),
    count(context.decisions.length, 'settled decision'),
    count(context.unresolved.length, 'unresolved request'),
    count(context.absent.length, 'absent source'),
  ].join(' · ')
}

/** Everything the handoff needs that the planner view does not carry by itself. */
export interface PlannerHandoffInputs {
  requestId: string
  title: string
  plan: string
  spec: string
  repoId: string
  projectRoot: string
  /** The project's agent workspace, which its factory defaults may override. */
  projectWorkspace?: string
  factoryWorkspace?: string
  factoryTargetBranch?: string
  repoBranch?: string
  noRequiredCiAcknowledged: boolean
}

/**
 * The factory create a planner handoff posts.
 *
 * It follows the same resolution the manual handoff form follows, so a
 * planner's draft is the draft that form would have made: the project's
 * factory workspace wins over its agent workspace, the target branch is the
 * factory default and then the repository's own branch, and a native run is
 * the fallback only when nothing configured a workspace. Fields the bridge can
 * resolve itself are left out rather than materialised.
 */
export function plannerHandoffRequest(input: PlannerHandoffInputs): FactoryCreate {
  const workspace = input.factoryWorkspace || input.projectWorkspace || ''
  // A branch the caller could not resolve is left out, so the bridge applies
  // the factory's own default instead of being pinned to a guess.
  const targetBranch = input.factoryTargetBranch || input.repoBranch || ''
  const config: FactoryCreateConfig = {
    repoId: input.repoId,
    projectRoot: input.projectRoot,
    native: !workspace,
    implementerAuto: false,
    noRequiredCiAcknowledged: input.noRequiredCiAcknowledged,
    ...(targetBranch ? { targetBranch } : {}),
    ...(workspace ? { workspace } : {}),
  }
  return { requestId: input.requestId, title: input.title, plan: input.plan, spec: input.spec, config }
}
