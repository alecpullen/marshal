/**
 * Factory API shapes (F6 decision 1). The wire types are generated from
 * `web/bridge/contract` and re-exported here, so this module and the Flutter
 * client are read from one declaration: a shape below that says
 * `= FactoryWire.X` is that type and nothing else.
 *
 * Two shapes stay local on purpose. `FactoryCheckKey` narrows the check's
 * `kind` to the two the bridge emits, and a command names only the fields it
 * sets — the wire's `FactoryCommand` is the *journaled* command, whose every
 * field is present, which is not what a client sends. The handoff shapes
 * (`FactoryConfig`, `FactoryCreate`, `FactoryCreateConfig`, `FactoryDefaults`,
 * `FactoryApproval`, `FactoryItem`) stay local for the same reason: a
 * configuration the user left alone is absent, not empty.
 */
import type * as FactoryWire from './contract'

export type FactoryPR = FactoryWire.FactoryPR
export type FactoryGrant = FactoryWire.FactoryGrant
export type FactoryReport = FactoryWire.FactoryReport
export type FactoryDecision = FactoryWire.FactoryDecision
export type FactoryEvent = FactoryWire.FactoryEvent
export type FactoryProvenance = FactoryWire.FactoryProvenance
export type FactoryPRLink = FactoryWire.FactoryPRLink
export type FactoryAssessment = FactoryWire.FactoryAssessment
export type FactoryCompletionRecord = FactoryWire.FactoryCompletionRecord
export type FactoryRevisionAssessment = FactoryWire.FactoryRevisionAssessment
export type FactoryRevisionDelivery = FactoryWire.FactoryRevisionDelivery
export type FactoryRevisionProposal = FactoryWire.FactoryRevisionProposal
export type FactoryCompletionPR = FactoryWire.FactoryCompletionPR
export type FactoryExcerpt = FactoryWire.FactoryExcerpt
export type FactoryNarration = FactoryWire.FactoryNarration
export type FactoryDelegatedTurn = FactoryWire.FactoryDelegatedTurn
export type FactoryEffect = FactoryWire.FactoryEffect
export type FactoryOutcome = FactoryWire.FactoryOutcome
/**
 * One role's subagent settings as a work item's draft or a project's factory
 * defaults state them. Every field is optional on purpose: an absent field
 * inherits the next source, an explicit `false` is not an absence, and an
 * explicit empty `model` is a stated choice — normal child routing — rather
 * than "follow".
 */
export type FactorySubagentOverrides = FactoryWire.FactorySubagentOverrides
/**
 * One role's resolved subagent settings: what the chain produced for a draft,
 * and what an approval revision freezes. An approval journalled before these
 * existed carries the zero value, so a zero `concurrency` means "no snapshot"
 * rather than a cap of zero.
 */
export type FactorySubagentSettings = FactoryWire.FactorySubagentSettings
export interface FactoryWorker { agentId: string; sessionId: string; role: string; spawnEffectId: string; parked: boolean }

export interface FactoryCompletionCommand {
  link?: string
  prs?: FactoryCompletionPR[]
  strategy?: FactoryMergeStrategy
  completeAfter?: boolean
  runChoice?: 'continue' | 'request_stop'
}

export interface FactoryChild { sessionId: string; id: number; label?: string; status: string; terminal: boolean }

export interface FactoryChildSession { sessionId: string; known: boolean; children: FactoryChild[] | null }
export interface FactoryRound { number: number; approvalRevision: number; implementEffectId: string; baseSha: string; headSha: string; treeSha: string; ci: FactoryCI | null; implementation: FactoryReport | null; review: FactoryReport | null; startedAt: string; finishedAt: string }
export interface FactoryMergeStep {
  repoId: string; number: number; nodeId: string; headSha: string; baseRef: string
  strategy: FactoryMergeStrategy
  state: 'unattempted' | 'prepared' | 'dispatched' | 'confirmed' | 'failed' | 'uncertain' | string
  result?: string
}
export interface FactoryMergeBatch {
  id: string; requestId: string
  state: 'active' | 'stopped' | 'complete' | string
  completeAfter: boolean
  runChoice?: 'continue' | 'request_stop' | string
  steps: FactoryMergeStep[]
}
export interface FactoryCompletion {
  status: 'open' | 'complete' | string
  links?: FactoryPRLink[] | null
  assessment?: FactoryAssessment
  records?: FactoryCompletionRecord[] | null
  batch?: FactoryMergeBatch | null
  stopState?: 'requested' | 'stopped' | 'failed' | 'unknown' | string
}
export type FactoryCheck = FactoryWire.FactoryCheck
/** One finding of a review, as a factory report and a recipe both carry it. */
export type FactoryFinding = FactoryWire.ReviewFinding

export interface FactoryCheckKey { kind: 'check_run' | 'commit_status'; name: string; appId: number }
export interface FactoryConfig {
  repoId: string; projectRoot: string; hostId: string; targetBranch: string; workspace: string; native: boolean
  implementerRouting: Record<string, unknown>; reviewerRouting: Record<string, unknown>
  reviewerInstructions: string; implementerAuto: boolean; expectedChecks: FactoryCheckKey[]
  noRequiredCiAcknowledged: boolean; maxRounds: number; ciPollSeconds: number; ciWaitSeconds: number
  /**
   * The work item's own subagent overrides, per role. Only an override the
   * item states rides here: nothing is copied onto a draft from the project's
   * defaults, so a draft follows a default it never stated while an override
   * it does state stays fixed when that default changes.
   */
  implementerSubagents?: FactorySubagentOverrides
  reviewerSubagents?: FactorySubagentOverrides
}
/**
 * The project's factory defaults. Every field is optional on purpose: an
 * absent field follows the handoff, or the project's agent defaults where
 * those apply. Do not materialise a fallback into a field the user left alone.
 */
export interface FactoryDefaults {
  targetBranch?: string
  workspace?: string
  hostId?: string
  implementerRouting?: Record<string, unknown>
  reviewerRouting?: Record<string, unknown>
  reviewerInstructions?: string
  expectedChecks?: FactoryCheckKey[]
  maxRounds?: number
  ciPollSeconds?: number
  ciWaitSeconds?: number
  /**
   * The project's subagent overrides, per role. Pointers (optional here) for
   * the same reason the handoff's are: an absent field inherits the session's
   * reported defaults, and an explicit `false` is not an absence.
   */
  implementerSubagents?: FactorySubagentOverrides
  reviewerSubagents?: FactorySubagentOverrides
}
/**
 * A create request's configuration. The fields the bridge never derives are
 * required; everything it can resolve from the project's defaults is optional
 * and omitted when it should follow.
 */
export type FactoryCreateConfig = Pick<FactoryConfig, 'repoId' | 'projectRoot' | 'native' | 'implementerAuto' | 'noRequiredCiAcknowledged'> & Partial<Omit<FactoryConfig, 'repoId' | 'projectRoot' | 'native' | 'implementerAuto' | 'noRequiredCiAcknowledged'>>
export interface FactoryCreate { requestId: string; title: string; plan: string; spec: string; config: FactoryCreateConfig }
export interface FactoryApproval { revision: number; plan: string; spec: string; planHash: string; config: FactoryConfig; approvedAt: string
  /**
   * The subagent settings this revision froze: runtime defaults, then the
   * project's override, then the draft's override, resolved when the approval
   * was written. A revision approved before they were frozen carries the zero
   * value, so nothing re-derives them for it.
   */
  implementerSubagents?: FactorySubagentSettings
  reviewerSubagents?: FactorySubagentSettings
}
export interface FactoryCI { headSha: string; checks: FactoryCheck[] | null; complete: boolean; observedAt: string }
/**
 * One worker turn's delegated child work, mirroring FactoryDelegatedTurn: the
 * children the turn launched, and how far they have been proven. It is journalled
 * beside the effect that dispatched the turn, so a waiting turn keeps the effect
 * and its candidate identity until the children settle. A waiting turn is not an
 * outcome — the report it wrote before those children settled is early.
 */
/**
 * One child of a worker session, as that session's own status projects it,
 * mirroring FactoryChild. Its identity is the pair, never the id alone: child
 * ids are session-local, so two sessions can both contain child 1.
 */
/**
 * One worker session's child projection, mirroring FactoryChildSession.
 * `known` is false when the bridge could not read it, which is unknown work
 * rather than a settled empty set.
 */
/**
 * Who asked for one factory action, as the bridge journaled it beside the
 * action itself (spec §4). It is optional on every event: a record written
 * before provenance existed is normal history, not damage, and must render
 * exactly as it always did.
 *
 * `authority` is present only where the operation carried one — an approval or
 * an answer. `user_directed` is the calling client's own claim, so the display
 * must say so and never present it as a verified human approval.
 */
/**
 * One pull request linked to a work item's completion, with the observation
 * the bridge made of it. `fresh` is the server's own judgement of whether that
 * observation still describes the linked pull request, so a surface states
 * staleness rather than trusting a stale head.
 */
/**
 * One piece of completion evidence, assessed against exactly one set of
 * inputs. `fresh` is derived on every read from `inputDigest` and is never
 * stored: an assessment whose inputs have since moved keeps its summary and
 * loses its freshness.
 */
/**
 * What the user called done, snapshotted. The goal, the criteria and the links
 * are copied when the decision is made, so a later approval or a pull request
 * that moves cannot rewrite what this record says.
 */
/** The merge strategies a reviewed batch may carry; the bridge refuses any other as input. */
export type FactoryMergeStrategy = 'merge' | 'squash' | 'rebase'
/**
 * One reviewed merge in a batch. `state` is the batch's own receipt about it:
 * only `confirmed` means the pull request merged at the reviewed identity, and
 * `uncertain` is a merge whose outcome has to be read again, never sent again.
 */
/**
 * The ordered merges a user authorised, driven one step per pass. Every entry
 * after a stopped one stays unattempted: a stopped batch needs a new explicit
 * decision, and nothing resumes it on its own.
 */
/**
 * The item's business completion, which is independent of `state`: the work
 * counts as done as far as the user is concerned, whatever the run is doing.
 * An absent record and an empty status both mean open.
 */
/**
 * One pull request a user reviewed for a merge. It is the identity the batch
 * pins, so a head or base that no longer matches the linked pull request is
 * refused rather than adapted. `reviewedPR` is the only way to build one from
 * a link, so a caller cannot send a head the user was never shown.
 */
export interface FactoryItem { id: string; title: string; state: string; stage: string; repoId: string; projectRoot: string; hostId: string; branch: string; baseSha: string; version: number; draft: Omit<FactoryCreate, 'config'> & { config: FactoryConfig }; approvals: FactoryApproval[] | null; grants: FactoryGrant[] | null; rounds: FactoryRound[] | null; workers: FactoryWorker[] | null; prs: FactoryPR[] | null; effects: FactoryEffect[] | null; decision: FactoryDecision | null; history: FactoryEvent[] | null; createdAt: string; approvedAt: string; updatedAt: string; /** Plan-change proposals; absent on a record written before revisions existed. */ proposals?: FactoryRevisionProposal[] | null; /** Business completion, independent of `state`; absent on a record written before completion existed, and an absent record or empty status means open. */ completion?: FactoryCompletion | null }

/**
 * One proposal's assessment, as the assessor reported it. `recommendation` is
 * the assessor's advice; `strategy` is how the change can be delivered to work
 * already in flight, and `hold` means it cannot be delivered while that work
 * runs.
 */
/**
 * An attempt to hand one revision to the session doing the work. Every state
 * before `delivered` is a receipt about the request, never a claim that the
 * agent applied anything.
 */
/**
 * One candidate revision of a work item's plan and specification. It is not
 * work: a proposal is authorized separately, and its delivery is a third step
 * the receipt reports on. `baseRevision` and `baseHash` name the approved
 * revision it was written against, so evidence recorded for that revision
 * cannot be read as covering this candidate.
 */
/**
 * The revision half of a command. Only the fields the command names are sent;
 * the envelope (request id, expected version, decision) stays in `FactoryCommand`.
 */
export interface FactoryRevisionCommand {
  proposalId?: string
  proposalVersion?: number
  baseRevision?: number
  baseHash?: string
  plan?: string
  spec?: string
  candidateHash?: string
  attemptId?: string
  assessment?: FactoryRevisionAssessment
  routing?: string
  strategy?: string
  followUp?: FactoryCreate
}
/** The revision lifecycle commands the bridge accepts in `FactoryCommand.revision`. */
export type FactoryRevisionCommandKind =
  | 'revision_propose'
  | 'revision_supersede'
  | 'revision_reject'
  | 'revision_assess_request'
  /** An explicit retry of an attempt whose outcome was lost (assessing/uncertain). */
  | 'revision_assess_retry'
  | 'revision_authorize'
  | 'revision_delivery_choice'
  | 'revision_follow_up_confirm'
/**
 * The completion half of a command. Only the fields the command names are
 * sent; the envelope (request id, expected version, decision) stays in
 * `FactoryCommand`. `prs` is the reviewed set a merge command authorizes, and
 * `completeAfter` must agree with the kind that carries it.
 */
/** The completion lifecycle commands the bridge accepts in `FactoryCommand.completion`. */
export type FactoryCompletionCommandKind =
  | 'complete'
  | 'reopen'
  /** Links a pull request by number or URL; the bridge observes its head itself. */
  | 'link_pr'
  | 'refresh_pr_evidence'
  | 'assess_completion'
  /** Merges the reviewed set without completing the item. */
  | 'merge_pr'
  /** Merges the reviewed set and completes the item once every merge confirms. */
  | 'merge_and_complete'
export interface FactoryCommand { requestId: string; kind: 'approve' | 'answer' | 'stop' | 'retry' | 'reconcile' | 'recover' | FactoryRevisionCommandKind | FactoryCompletionCommandKind; expectedVersion: number; decisionId?: string; answer?: string; reason?: string; additionalRounds?: number; replacement?: FactoryCreate; implementerRouting?: Record<string, unknown>; reviewerRouting?: Record<string, unknown>; confirmPreviousWorkerStopped?: boolean; revision?: FactoryRevisionCommand; completion?: FactoryCompletionCommand
  /**
   * The settings version the review surface displayed, as
   * `"<projectSettingsETag>:<subagentDefaultsDigest>"`. The bridge refuses an
   * approval whose version has moved as a conflict, because it would freeze
   * settings nobody reviewed; a command that sends none is not checked.
   */
  settingsVersion?: string }
export type FactoryFilter = 'current' | 'needs-you' | 'ready' | 'history'
export type FactoryItemView = 'approval' | 'activity' | 'review' | 'prs' | 'documents'
/**
 * One factory hash, as `lib/routes` parses it: the global or project-scoped
 * list, creation, working drafts, or one item drill-in. `view` and `agent`
 * belong to an item; `filter` to a list. `drafts` marks the working-drafts
 * routes, whose `draftId` is the bridge's draft id (never a project path).
 * Parsing and formatting live in `lib/routes`.
 */
export interface FactoryRoute {
  itemId?: string
  create?: boolean
  projectRoot?: string
  filter?: FactoryFilter
  view?: FactoryItemView
  agent?: string
  drafts?: boolean
  draftId?: string
}

/**
 * The completion requests below are pure: each takes the stable request id the
 * caller generated and the item version the user was looking at, and the
 * bridge refuses the request when that version is no longer current. None of
 * them performs a call or decides anything.
 */

/**
 * Complete the item: the user's own statement that the work counts as done.
 * It neither stops the run nor withdraws a pull request, and the bridge refuses
 * it for an item that is already complete.
 */
export function completeCommand(requestId: string, expectedVersion: number): FactoryCommand {
  return { requestId, kind: 'complete', expectedVersion }
}

/** Reopen a completed item. The records that called it done stay, unrewritten. */
export function reopenCommand(requestId: string, expectedVersion: number): FactoryCommand {
  return { requestId, kind: 'reopen', expectedVersion }
}

/**
 * Name one linked pull request by number or URL. Only the reference travels:
 * the head, base and state the surface renders are the ones the bridge
 * observes, so a link can never describe a pull request the user was not shown.
 */
export function linkPRCommand(requestId: string, expectedVersion: number, link: string): FactoryCommand {
  return { requestId, kind: 'link_pr', expectedVersion, completion: { link } }
}

/**
 * Ask the bridge to observe every linked pull request again. A link whose read
 * fails keeps the snapshot it had and comes back stale, so this is a request
 * for current evidence and never a claim that it is current.
 */
export function refreshEvidenceCommand(requestId: string, expectedVersion: number): FactoryCommand {
  return { requestId, kind: 'refresh_pr_evidence', expectedVersion }
}

/**
 * Ask for an assessment of the business outcome. It is evidence about the
 * work, never a gate on it: nothing completes or reopens because of it, and an
 * assessment that cannot run is recorded as unavailable rather than as an
 * error.
 */
export function assessCompletionCommand(requestId: string, expectedVersion: number): FactoryCommand {
  return { requestId, kind: 'assess_completion', expectedVersion }
}

export function mergeCommandKind(completeAfter: boolean): 'merge_pr' | 'merge_and_complete' {
  return completeAfter ? 'merge_and_complete' : 'merge_pr'
}

/**
 * Authorise the ordered merges the user reviewed. The kind is derived from the
 * intent because the bridge refuses a merge command whose completion intent
 * contradicts its kind, so `completeAfter` is never a separate claim: an item
 * completes only when every reviewed merge confirmed.
 *
 * `runChoice` is required while the item's own run is active, so a batch can
 * neither cancel nor race a turn it did not account for.
 */
export function mergePRsCommand(
  requestId: string,
  expectedVersion: number,
  prs: FactoryCompletionPR[],
  strategy: FactoryMergeStrategy,
  completeAfter: boolean,
  runChoice?: 'continue' | 'request_stop',
): FactoryCommand {
  return {
    requestId,
    kind: mergeCommandKind(completeAfter),
    expectedVersion,
    completion: { prs, strategy, completeAfter, ...(runChoice ? { runChoice } : {}) },
  }
}

/**
 * The evidence a review covers: exactly the identity of the link the user was
 * shown. Copying it out of the link is deliberate — a caller that assembled
 * these fields itself could send a head or base the user never reviewed, and
 * the bridge would then merge something they did not approve.
 */
export function reviewedPR(link: FactoryPRLink): FactoryCompletionPR {
  return {
    repoId: link.repoId,
    number: link.pr.number,
    nodeId: link.pr.nodeId,
    headSha: link.pr.headSha,
    baseRef: link.pr.baseRef,
  }
}

/**
 * The item's business completion, read apart from its execution `state`: a
 * work item can count as done while the run is still going, and can be open
 * again after it stopped. An absent record and an empty status both mean open,
 * which is what a record written before completion existed carries.
 */
export function completionStatus(item: Pick<FactoryItem, 'completion'>): 'open' | 'complete' {
  return item.completion?.status === 'complete' ? 'complete' : 'open'
}

/** Every pull request linked to the item's completion, whichever origin linked it. */
export function completionLinks(item: Pick<FactoryItem, 'completion'>): FactoryPRLink[] {
  return item.completion?.links ?? []
}

/**
 * The links a merge can select: the pull requests the forge still reports
 * open. A closed or merged one is history, and reviewing it would promise a
 * merge the bridge refuses.
 */
export function selectableLinks(item: Pick<FactoryItem, 'completion'>): FactoryPRLink[] {
  return completionLinks(item).filter((link) => link.pr.state === 'open')
}

/** True when any of these observations no longer describes its pull request. */
export function linksStale(links: FactoryPRLink[]): boolean {
  return links.some((link) => link.fresh === false)
}

/**
 * Whether the bridge requires a decision about the item's own run before it
 * authorizes a merge batch (`factoryBatchRunChoice` → `factoryBatchRunActive`):
 * a state the item owns, with a worker that is not parked or an effect in
 * flight. An unknown state owns its run, so this asks rather than assumes.
 */
export function runNeedsChoice(item: Pick<FactoryItem, 'state' | 'workers' | 'effects'>): boolean {
  if (item.state === 'draft' || item.state === 'closed' || item.state === 'stopped') return false
  if ((item.workers ?? []).some((worker) => !worker.parked)) return true
  return (item.effects ?? []).some((effect) => effect.status === 'prepared' || effect.status === 'dispatched')
}

/** The first entry of a batch the batch may act on: the first it has not confirmed. */
export function nextBatchStep(batch: FactoryMergeBatch | null | undefined): FactoryMergeStep | null {
  for (const step of batch?.steps ?? []) if (step.state !== 'confirmed') return step
  return null
}

/**
 * The links a stopped or partly failed batch could still merge: the entries it
 * never confirmed whose pull request is still open, in the order it authorized.
 * An `uncertain` entry is never one of them — the bridge says its outcome has
 * to be read again, so sending it again could merge the same pull request
 * twice — and a `confirmed` one has already merged.
 */
export function remainingMergeLinks(item: Pick<FactoryItem, 'completion'>): FactoryPRLink[] {
  const batch = item.completion?.batch
  const links = completionLinks(item)
  if (!batch || !links.length) return []
  const remaining: FactoryPRLink[] = []
  for (const step of batch.steps) {
    if (step.state === 'confirmed' || step.state === 'uncertain') continue
    const link = links.find((candidate) => candidate.repoId === step.repoId && candidate.pr.nodeId === step.nodeId)
    if (link && link.pr.state === 'open') remaining.push(link)
  }
  return remaining
}

/** How a reviewed revision reads: the short head SHA, or that there is none. */
export function revisionLabel(sha: string | undefined): string {
  return sha ? sha.slice(0, 7) : 'unknown'
}

/** The goal a completion decision names: the work item's title, as it was created. */
export function completionGoal(item: Pick<FactoryItem, 'title'>): string {
  return item.title && item.title.trim() ? item.title : 'Untitled work item'
}

/**
 * The criteria a completion decision is judged against: the newest approved
 * revision's specification. Null means the item carries no approval — or one
 * with no specification — so the surface says the criteria are unknown instead
 * of inventing them from the working draft.
 */
export function completionCriteria(item: Pick<FactoryItem, 'approvals'>): string | null {
  const latest = item.approvals?.at(-1)
  return latest && latest.spec.trim() ? latest.spec : null
}

/**
 * How an action's authority is described. `user_directed` is worded as the
 * client's own assertion because that is all it is: the bridge records the
 * claim, it does not verify that a human asked. An authority this build does
 * not know is shown as unverified rather than guessed at.
 */
export function authorityLabel(authority: string | undefined): string {
  switch (authority) {
    case undefined:
    case '':
    case 'none':
      return ''
    case 'user_directed':
      return 'client reports user-directed'
    case 'standing':
      return 'standing authority'
    case 'delegated':
      return 'delegated planner'
    default:
      return `unrecognised authority ${authority}`
  }
}

/**
 * One history entry's origin: the client's name when the client list knows the
 * ID and the stable ID otherwise, so a renamed or deleted client is still
 * attributable.
 */
export function provenanceLine(p: FactoryProvenance, clientName?: string): string {
  const who = clientName ? `${clientName} (${p.clientId})` : p.clientId
  const parts = [`Requested by ${who}`, `project ${p.projectId}`]
  if (p.action) parts.push(p.action)
  const authority = authorityLabel(p.authority)
  if (authority) parts.push(authority)
  if (p.decisionId) parts.push(`decision ${p.decisionId}`)
  return parts.join(' · ')
}
/** The two roles whose subagent settings a project or a work item states. */
export type FactorySubagentRole = 'implementer' | 'reviewer'

/** One role's names: the desktop label may be longer than the phone one. */
export interface FactorySubagentRoleSpec {
  role: FactorySubagentRole
  label: string
  shortLabel: string
}

export const FACTORY_SUBAGENT_ROLES: FactorySubagentRoleSpec[] = [
  { role: 'implementer', label: 'Implementer subagents', shortLabel: 'Implementer' },
  { role: 'reviewer', label: 'Reviewer subagents', shortLabel: 'Reviewer' },
]

/** The concurrency range the backend enforces; anything else is refused, never clamped. */
export const SUBAGENT_CONCURRENCY_MIN = 1
export const SUBAGENT_CONCURRENCY_MAX = 8

export function subagentConcurrencySupported(n: number): boolean {
  return Number.isInteger(n) && n >= SUBAGENT_CONCURRENCY_MIN && n <= SUBAGENT_CONCURRENCY_MAX
}

/** One role's stated overrides out of a record that carries both roles. */
export function subagentOverridesOf(
  source: { implementerSubagents?: FactorySubagentOverrides; reviewerSubagents?: FactorySubagentOverrides } | null | undefined,
  role: FactorySubagentRole,
): FactorySubagentOverrides | undefined {
  if (!source) return undefined
  return role === 'implementer' ? source.implementerSubagents : source.reviewerSubagents
}

/** True when an override record states anything at all. */
export function subagentOverridesPresent(o: FactorySubagentOverrides | null | undefined): boolean {
  if (!o) return false
  return o.enabled !== undefined || o.model !== undefined || o.concurrency !== undefined
}

/**
 * Where one resolved subagent field came from. `unset` means nothing in the
 * chain states it, which is not the same as an inherited value the caller
 * could read: the surface has to say it cannot see the session's defaults.
 */
export type SubagentSource = 'item' | 'project' | 'runtime' | 'unset'

export type FactorySubagentField = 'enabled' | 'model' | 'concurrency'

/** One field of one role, as a surface states it: the value and where it came from. */
export interface SubagentFieldView {
  field: FactorySubagentField
  label: string
  /** The effective value, or `Inherited` when nothing before the runtime states it. */
  value: string
  source: SubagentSource
  /** True only where the draft or the project states this field itself. */
  explicit: boolean
  /** Set when the value cannot be enforced: the backend refuses it rather than clamping. */
  unsupported?: string
}

/** One role's resolved subagent settings as a surface presents them. */
export interface SubagentRoleView {
  role: FactorySubagentRole
  label: string
  shortLabel: string
  fields: SubagentFieldView[]
  /** True when this role states nothing of its own anywhere before the runtime. */
  inherited: boolean
}

/** The chain a draft's settings resolve through, most specific first. */
export interface SubagentChain {
  /**
   * The most specific layer the surface presents: the work item's own override
   * on a draft, or the project's own record on the settings screen. It is what
   * a defaults refresh leaves fixed.
   */
  item?: FactorySubagentOverrides | null
  /** The project's factory default for this role. */
  project?: FactorySubagentOverrides | null
  /** The session's reported defaults, when the caller has them. */
  runtime?: FactorySubagentSettings | null
}

const SUBAGENT_FIELD_LABELS: Record<FactorySubagentField, string> = {
  enabled: 'Enabled',
  model: 'Model',
  concurrency: 'Concurrency',
}

/** How one effective value reads. `Inherited` is stated, never a guessed value. */
function subagentValueText(field: FactorySubagentField, value: boolean | string | number | undefined): string {
  if (value === undefined) return 'Inherited'
  switch (field) {
    case 'enabled':
      return value === true ? 'On' : 'Off'
    case 'model':
      // An explicit empty model is a choice: the child routes normally.
      return value === '' ? 'Normal child routing' : String(value)
    case 'concurrency':
      return typeof value === 'number' ? `${value} at a time` : String(value)
  }
}

/**
 * One role's settings, resolved through the chain a draft follows: the item's
 * own override, then the project's factory default, then the session's
 * reported defaults. An explicit field at the item stays fixed when a default
 * refreshes; a field it does not state follows the default it inherits.
 *
 * A session that reported nothing leaves the last fields `unset` rather than
 * inventing a value — the surface says it cannot read them.
 */
export function resolveSubagentRole(role: FactorySubagentRole, chain: SubagentChain = {}): SubagentRoleView {
  const spec = FACTORY_SUBAGENT_ROLES.find((candidate) => candidate.role === role) ?? FACTORY_SUBAGENT_ROLES[0]
  const layers: { overrides: FactorySubagentOverrides | null | undefined; source: SubagentSource }[] = [
    { overrides: chain.item, source: 'item' },
    { overrides: chain.project, source: 'project' },
  ]
  const runtime = chain.runtime ?? null
  const fields = (['enabled', 'model', 'concurrency'] as FactorySubagentField[]).map((field): SubagentFieldView => {
    const stated = layers.find((layer) => layer.overrides && layer.overrides[field] !== undefined)
    const value = stated ? stated.overrides?.[field] : runtime ? runtime[field] : undefined
    const source: SubagentSource = stated ? stated.source : runtime ? 'runtime' : 'unset'
    const unsupported = field === 'concurrency' && typeof value === 'number' && !subagentConcurrencySupported(value)
      ? `Concurrency must be ${SUBAGENT_CONCURRENCY_MIN} to ${SUBAGENT_CONCURRENCY_MAX}; the backend refuses anything else rather than clamping it.`
      : undefined
    return {
      field,
      label: SUBAGENT_FIELD_LABELS[field],
      value: subagentValueText(field, value),
      source,
      // "Explicit" is the most specific layer the caller passed: the draft's own
      // override on a review, the project's own record on the settings screen. A
      // field a less specific layer supplies is inherited, because the surface
      // showing it did not state it.
      explicit: stated?.source === 'item',
      unsupported,
    }
  })
  return { role: spec.role, label: spec.label, shortLabel: spec.shortLabel, fields, inherited: fields.every((f) => !f.explicit) }
}

/**
 * One role's effective settings in one line, for a read-only summary row.
 * Nothing here resolves a fallback of its own: it prints the fields the chain
 * already produced.
 */
export function subagentValueLine(view: SubagentRoleView): string {
  return view.fields.map((f) => f.value).join(' · ')
}

/** How a worker's role is named on the factory screen. */
export function workerRoleLabel(role: string): string {
  switch (role) {
    case 'implementer': return 'Implementer'
    case 'reviewer': return 'Independent factory reviewer'
    default: return role || 'Worker'
  }
}

/**
 * True for the factory's own reviewer. It is not a child of the implementer:
 * its session is its own worker, so it is never nested under a worker's child
 * list and never read as work the implementer delegated.
 */
export function workerIsReviewer(worker: Pick<FactoryWorker, 'role'>): boolean {
  return worker.role === 'reviewer'
}

/** How one delegated turn reads: whether work is waiting, and what it means. */
export interface DelegatedState { state: string; waiting: boolean; headline: string; meaning: string }

/**
 * A delegated turn's presentation, or null when there is nothing to mark. Every
 * state before `settled` means the turn's children are not proven finished, so
 * the report the turn wrote before them is early; `uncertain` says the bridge
 * cannot tell either way and is never shown as settled.
 */
export function delegatedState(turn: FactoryDelegatedTurn | null | undefined): DelegatedState | null {
  if (!turn || turn.state === 'settled') return null
  switch (turn.state) {
    case 'waiting':
      return { state: turn.state, waiting: true, headline: 'Waiting for delegated work', meaning: 'This turn launched children it has not proven settled. The report it wrote before them is early, so it is not the turn\'s result.' }
    case 'prepared':
      return { state: turn.state, waiting: true, headline: 'Continuation prepared', meaning: 'The children have not been proven settled. A continuation is recorded but has not been sent, so nothing has resumed yet.' }
    case 'dispatched':
      return { state: turn.state, waiting: true, headline: 'Continuation dispatched', meaning: 'The continuation was handed to the worker session. Its children are still not proven settled.' }
    case 'uncertain':
      return { state: turn.state, waiting: true, headline: 'Delegated work uncertain', meaning: 'The bridge cannot tell whether this turn\'s children finished. Treat the work as unfinished until it says otherwise.' }
    default:
      return { state: turn.state, waiting: true, headline: `Delegated work ${turn.state}`, meaning: 'This build does not know this delegated state, so the work is unconfirmed rather than finished.' }
  }
}

/**
 * The delegated turn one worker still owes work for: the newest of its effects'
 * turns that is not settled. Null means nothing is waiting, which includes a
 * record written before delegated turns existed.
 */
export function workerDelegated(
  item: Pick<FactoryItem, 'effects'>,
  worker: Pick<FactoryWorker, 'agentId'>,
): FactoryDelegatedTurn | null {
  const effects = item.effects ?? []
  for (let n = effects.length - 1; n >= 0; n--) {
    const turn = effects[n].delegated
    if (!turn || turn.workerId !== worker.agentId) continue
    if (turn.state !== 'settled') return turn
  }
  return null
}

/** One child of a worker, as the worker's delegated turn names it. */
export interface FactoryChildLink {
  sessionId: string
  id: number
  label: string
  /** The child's status as its session reported it; empty means unreported, never finished. */
  status: string
  terminal: boolean
  /** True only when the child's own session reported this child. */
  known: boolean
}

/**
 * The children under one worker, from the delegated turn that launched them and,
 * when the caller has it, the worker session's own child projection. A child the
 * projection does not name keeps an empty status: an unread projection is
 * unknown work, never an empty settled set.
 */
export function workerChildren(
  item: Pick<FactoryItem, 'effects'>,
  worker: Pick<FactoryWorker, 'agentId'>,
  sessions: FactoryChildSession[] = [],
): FactoryChildLink[] {
  const turn = workerDelegated(item, worker)
  if (!turn) return []
  const projection = sessions.find((session) => session.sessionId === turn.sessionId)
  const ids = [...(turn.childIds ?? [])].sort((a, b) => a - b)
  return ids.map((id) => {
    const reported = projection?.children?.find((child) => child.id === id)
    return {
      sessionId: turn.sessionId,
      id,
      label: reported?.label || `child ${id}`,
      status: reported?.status ?? '',
      terminal: reported?.terminal === true,
      known: reported !== undefined,
    }
  })
}

/**
 * The proposal a screen shows: the newest one the item carries, which is the
 * one an action would apply to. Nothing here decides what the bridge accepts;
 * the proposal's own `status` says whether it is still live.
 */
export function latestProposal(item: Pick<FactoryItem, 'proposals'>): FactoryRevisionProposal | null {
  const list = item.proposals ?? []
  return list.length ? list[list.length - 1] : null
}

/**
 * The project's automatic-assessment preference: absent (or a project that
 * could not be read) means on, because that is the handoff's default. An
 * explicit false is the user's override and is the only value that turns it
 * off.
 */
export function effectiveAutoAssess(settings: { autoAssessPlanChanges?: boolean } | null | undefined): boolean {
  return settings?.autoAssessPlanChanges !== false
}

/** Assessment states that mean no assessment is coming without a person asking again. */
const ASSESSMENT_UNAVAILABLE = new Set(['unavailable', 'uncertain', 'failed'])

/** True when the assessor did not answer, so the person has to decide without it. */
export function assessmentUnavailable(proposal: Pick<FactoryRevisionProposal, 'assessmentState'>): boolean {
  return ASSESSMENT_UNAVAILABLE.has(proposal.assessmentState)
}

/** True while the assessment is still coming. */
export function assessmentPending(proposal: Pick<FactoryRevisionProposal, 'assessmentState'>): boolean {
  return proposal.assessmentState === 'pending' || proposal.assessmentState === 'assessing'
}

/**
 * The states the bridge accepts a first assessment request from
 * (factoryApplyRevisionAssessRequest). Every other state is refused, so no
 * control is offered for it.
 */
export function assessmentRequestable(proposal: Pick<FactoryRevisionProposal, 'assessmentState'>): boolean {
  return proposal.assessmentState === 'not_requested' || proposal.assessmentState === 'unavailable'
}

/**
 * The states the bridge accepts an explicit retry from
 * (factoryApplyRevisionAssessRetry). A `failed` assessment is in neither set:
 * the bridge retires an attempt that was lost, not one that answered badly, so
 * a failed assessment needs a fresh proposal rather than a dead control.
 */
export function assessmentRetryable(proposal: Pick<FactoryRevisionProposal, 'assessmentState'>): boolean {
  return proposal.assessmentState === 'assessing' || proposal.assessmentState === 'uncertain'
}

/** The proposal states that still accept a decision. */
const LIVE_PROPOSAL = new Set(['proposed', 'awaiting_authorization', 'authorized'])

/** True when this proposal is still the one an approve, amend or follow-up applies to. */
export function proposalIsLive(proposal: Pick<FactoryRevisionProposal, 'status'>): boolean {
  return LIVE_PROPOSAL.has(proposal.status)
}

/**
 * The statuses the bridge counts as awaiting a decision
 * (factoryRevisionUnresolved): only one may exist per item, so a new proposal
 * has to supersede it explicitly instead of being refused.
 */
const UNRESOLVED_PROPOSAL = new Set(['proposed', 'awaiting_authorization', 'authorized', 'follow_up_pending'])

/** True when the bridge still counts this proposal as awaiting a decision. */
export function proposalUnresolved(proposal: Pick<FactoryRevisionProposal, 'status'>): boolean {
  return UNRESOLVED_PROPOSAL.has(proposal.status)
}

/**
 * The newest proposal that still awaits a decision, which is the one a save
 * must supersede before it can propose a replacement. Null when the item is
 * free to take a new proposal.
 */
export function unresolvedProposal(item: Pick<FactoryItem, 'proposals'>): FactoryRevisionProposal | null {
  const list = item.proposals ?? []
  for (let n = list.length - 1; n >= 0; n--) if (proposalUnresolved(list[n])) return list[n]
  return null
}

/**
 * The assessment that judged exactly this candidate at exactly this base,
 * which is the only one the bridge lets an authorization take its strategy
 * from. Anything else is history: it stays readable, but it cannot decide how
 * these bytes are delivered.
 */
export function matchingAssessment(proposal: FactoryRevisionProposal | null): FactoryRevisionAssessment | null {
  const assessment = proposal?.assessment
  if (!proposal || !assessment) return null
  return assessment.candidateHash === proposal.candidateHash && assessment.baseRevision === proposal.baseRevision
    ? assessment
    : null
}

/**
 * The factory's approval digest, mirrored from `FactoryPlanHash`
 * (web/bridge/factory_store.go): SHA-256 over each of the plan and the
 * specification, prefixed with its own 8-byte big-endian UTF-8 byte length.
 *
 * It is computed here because the bridge refuses a proposal whose
 * `candidateHash` does not bind the bytes it carries. web/bridge's
 * TestFactoryPlanHashVectors pins this algorithm with literal digests, and the
 * TypeScript test asserts the same two values, so the two sides cannot drift.
 */
export async function factoryPlanHash(plan: string, spec: string): Promise<string> {
  const encoder = new TextEncoder()
  const parts = [encoder.encode(plan), encoder.encode(spec)]
  const bytes = new Uint8Array(16 + parts[0].length + parts[1].length)
  const view = new DataView(bytes.buffer)
  // setBigUint64 is big-endian by default, which is what Go's binary.BigEndian
  // writes; the length is the UTF-8 byte count, never a character count.
  view.setBigUint64(0, BigInt(parts[0].length))
  bytes.set(parts[0], 8)
  view.setBigUint64(8 + parts[0].length, BigInt(parts[1].length))
  bytes.set(parts[1], 16 + parts[0].length)
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('')
}

export function proposalStatusLabel(status: string): string {
  switch (status) {
    case 'proposed': return 'Proposed — not approved'
    case 'awaiting_authorization': return 'Waiting for your approval'
    case 'authorized': return 'Approved — not delivered'
    case 'activated': return 'Active'
    case 'rejected': return 'Rejected'
    case 'superseded': return 'Superseded'
    case 'follow_up_pending': return 'Follow-up requested'
    case 'followed_up': return 'Moved to a follow-up item'
    default: return status
  }
}

export function assessmentStateLabel(state: string): string {
  switch (state) {
    case 'not_requested': return 'No assessment requested'
    case 'pending': return 'Assessment pending'
    case 'assessing': return 'Assessing…'
    case 'assessed': return 'Assessed'
    case 'unavailable': return 'Planner unavailable'
    case 'uncertain': return 'Assessment uncertain'
    case 'failed': return 'Assessment failed'
    default: return state
  }
}

export function classificationLabel(classification: string): string {
  switch (classification) {
    case 'routine_clarification': return 'Routine clarification'
    case 'scope_change': return 'Scope change'
    case 'needs_user': return 'Needs your decision'
    default: return classification
  }
}

export function recommendationLabel(recommendation: string): string {
  switch (recommendation) {
    case 'amend': return 'Amend the running work'
    case 'follow_up': return 'Start a follow-up item'
    default: return recommendation
  }
}

export function strategyLabel(strategy: string): string {
  switch (strategy) {
    case 'wait_safe': return 'Wait for a safe boundary'
    case 'interrupt': return 'Interrupt the running step'
    case 'hold': return 'Hold until the work finishes'
    default: return strategy
  }
}

/**
 * How each delivery state reads to a person. Only `delivered` is a delivery:
 * everything before it is a receipt about the request, and saying otherwise
 * would tell someone their change is in when the agent has not seen it.
 */
export interface DeliveryReceipt { tone: 'ok' | 'warn' | 'neutral'; headline: string; meaning: string }
export function deliveryReceipt(delivery: FactoryRevisionDelivery): DeliveryReceipt {
  switch (delivery.state) {
    case 'delivered':
      // Delivered is the runtime accepting the input, never the agent acting on
      // it: the spec draws that line and the wording has to draw it too.
      return {
        tone: 'ok',
        headline: 'Delivered',
        meaning: 'The agent has this revision. This is the only state that means it was delivered, and delivery into the runtime is not proof the agent followed it: check the work before relying on it.',
      }
    case 'accepted':
      return { tone: 'warn', headline: 'Accepted — not delivered', meaning: 'The session accepted the request and recorded it. That is a receipt, not a delivery: the agent has not necessarily read or applied anything yet.' }
    case 'pending':
      return { tone: 'neutral', headline: 'Queued — not delivered', meaning: 'The request is waiting to be sent. Nothing has reached the agent.' }
    case 'dispatching':
      return { tone: 'neutral', headline: 'Sending — not delivered', meaning: 'The request is on its way to the session. It has not been confirmed as delivered.' }
    case 'interrupting':
      return { tone: 'warn', headline: 'Interrupting — not delivered', meaning: 'The running step is being interrupted so the revision can be handed over. It is not delivered yet.' }
    case 'waiting_safe_boundary':
      return { tone: 'neutral', headline: 'Waiting for a safe boundary — not delivered', meaning: 'The revision is held until the agent reaches a boundary where it can be applied safely.' }
    case 'waiting_for_resume':
      return { tone: 'neutral', headline: 'Waiting for the work to resume — not delivered', meaning: 'The revision is queued behind paused work and will not be handed over until that work resumes.' }
    case 'undelivered':
      return { tone: 'warn', headline: 'Not delivered', meaning: 'The agent did not receive this revision. Nothing on it has been applied.' }
    case 'uncertain':
      return { tone: 'warn', headline: 'Uncertain', meaning: 'The bridge cannot tell whether the agent received it. Check the session before assuming either way.' }
    default:
      return { tone: 'neutral', headline: delivery.state, meaning: 'This build does not know this delivery state, so treat the outcome as unconfirmed.' }
  }
}

/** The delivery strategies a person can choose when the recommendation cannot be followed. */
export const DELIVERY_STRATEGIES: FactoryRevisionDelivery['strategy'][] = ['wait_safe', 'interrupt', 'hold']

/**
 * One revision command's trip to the bridge: the item the bridge answered with
 * when it accepted the command, `null` when it refused (which leaves the
 * person's text where it is). A multi-step path — supersede then propose,
 * authorize then confirm — takes the next command's payload from the item the
 * previous one returned, because that is the only read carrying the versions
 * the bridge now holds.
 */
export type FactoryRevisionRun = (kind: FactoryRevisionCommandKind, revision: FactoryRevisionCommand) => Promise<FactoryItem | null>

export interface PollSeam {
  document?: Pick<Document, 'hidden' | 'addEventListener' | 'removeEventListener'>
  setTimeout?: typeof setTimeout
  clearTimeout?: typeof clearTimeout
}
/** One in-flight read, with visibility suspension and bounded error backoff. */
export function createFactoryPoller(load: () => Promise<void>, active: () => boolean, onError: (error: unknown) => void, seam: PollSeam = {}) {
  const doc = seam.document ?? (typeof document === 'undefined' ? undefined : document)
  const later = seam.setTimeout ?? setTimeout
  const cancel = seam.clearTimeout ?? clearTimeout
  let timer: ReturnType<typeof setTimeout> | undefined
  let started = false
  let busy = false
  let failures = 0
  const clear = () => { if (timer !== undefined) cancel(timer); timer = undefined }
  const schedule = (delay: number) => { clear(); if (started && !doc?.hidden) timer = later(() => { void tick() }, delay) }
  async function tick() {
    if (!started || doc?.hidden || busy) return
    busy = true
    try { await load(); failures = 0 }
    catch (e) { failures++; onError(e) }
    finally {
      busy = false
      if (started && !doc?.hidden) schedule(Math.min(120000, (active() ? 2000 : 15000) * 2 ** Math.min(failures, 4)))
    }
  }
  function visibility() { if (doc?.hidden) clear(); else if (started && !busy) schedule(0) }
  return {
    start() { if (started) return; started = true; doc?.addEventListener('visibilitychange', visibility); schedule(0) },
    stop() { started = false; clear(); doc?.removeEventListener('visibilitychange', visibility) },
  }
}
