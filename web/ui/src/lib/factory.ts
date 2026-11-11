/** Factory API shapes mirror web/bridge/factory_types.go. */
export interface FactoryCheckKey { kind: 'check_run' | 'commit_status'; name: string; appId: number }
export interface FactoryConfig {
  repoId: string; projectRoot: string; hostId: string; targetBranch: string; workspace: string; native: boolean
  implementerRouting: Record<string, unknown>; reviewerRouting: Record<string, unknown>
  reviewerInstructions: string; implementerAuto: boolean; expectedChecks: FactoryCheckKey[]
  noRequiredCiAcknowledged: boolean; maxRounds: number; ciPollSeconds: number; ciWaitSeconds: number
}
export interface FactoryCreate { requestId: string; title: string; plan: string; spec: string; config: FactoryConfig }
export interface FactoryApproval { revision: number; plan: string; spec: string; planHash: string; config: FactoryConfig; approvedAt: string }
export interface FactoryGrant { id: string; reason: string; additionalRounds: number; at: string }
export interface FactoryWorker { agentId: string; sessionId: string; role: string; spawnEffectId: string; parked: boolean }
export interface FactoryPR { number: number; url: string; nodeId: string; headRef: string; baseRef: string; headSha: string; baseSha: string; state: string; draft: boolean }
export interface FactoryCheck { key: FactoryCheckKey; id: string; sha: string; status: string; conclusion: string; url: string; logTail: string; observedAt: string }
export interface FactoryCI { headSha: string; checks: FactoryCheck[] | null; complete: boolean; observedAt: string }
export interface FactoryFinding { [key: string]: unknown }
export interface FactoryReport { operationId: string; outcome: string; summary: string; question: string; reason: string; baseSha: string; headSha: string; inputsAccessible: boolean; findings: FactoryFinding[] | null }
export interface FactoryRound { number: number; approvalRevision: number; implementEffectId: string; baseSha: string; headSha: string; treeSha: string; ci: FactoryCI | null; implementation: FactoryReport | null; review: FactoryReport | null; startedAt: string; finishedAt: string }
export interface FactoryOutcome { effectId: string; stopReason: string; error: string; headSha: string; treeSha: string; baseSha: string; worker: FactoryWorker | null; pr: FactoryPR | null; report: FactoryReport | null; confirmed: boolean }
export interface FactoryEffect { outcome: FactoryOutcome | null; id: string; kind: string; status: string; workerRole: string; round: number; approvalRevision: number; worker: FactoryWorker | null; expectedHead: string; expectedBase: string; createdAt: string }
export interface FactoryDecision { id: string; kind: string; question: string; reason: string; effectId: string; createdAt: string }
export interface FactoryEvent { sequence: number; at: string; kind: string; message: string; effectId: string }
export interface FactoryItem { id: string; title: string; state: string; stage: string; repoId: string; projectRoot: string; hostId: string; branch: string; baseSha: string; version: number; draft: FactoryCreate; approvals: FactoryApproval[] | null; grants: FactoryGrant[] | null; rounds: FactoryRound[] | null; workers: FactoryWorker[] | null; prs: FactoryPR[] | null; effects: FactoryEffect[] | null; decision: FactoryDecision | null; history: FactoryEvent[] | null; createdAt: string; approvedAt: string; updatedAt: string }
export interface FactoryCommand { requestId: string; kind: 'approve' | 'answer' | 'stop' | 'retry' | 'reconcile' | 'recover'; expectedVersion: number; decisionId?: string; answer?: string; reason?: string; additionalRounds?: number; replacement?: FactoryCreate; implementerRouting?: Record<string, unknown>; reviewerRouting?: Record<string, unknown>; confirmPreviousWorkerStopped?: boolean }
export interface FactoryRoute { itemId?: string; create?: boolean }

interface PollSeam {
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
