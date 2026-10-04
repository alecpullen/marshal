import type { AuditEvent } from './audit'

const TOKEN_KEY = 'marshal:token'

let memoryToken: string | null = null

export function getToken(): string | null {
  if (memoryToken) return memoryToken
  try {
    memoryToken = sessionStorage.getItem(TOKEN_KEY)
  } catch {
    // sessionStorage may be unavailable in private/test environments.
  }
  return memoryToken
}

export function setToken(token: string): void {
  memoryToken = token
  try {
    sessionStorage.setItem(TOKEN_KEY, token)
  } catch {
    // ignore
  }
}

export function ensureToken(): string {
  const token = getToken()
  if (token) return token
  const entered = window.prompt('Enter the Marshal webbridge bearer token:')
  if (!entered) throw new AuthError('Token is required')
  setToken(entered)
  return entered
}

export class AuthError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'AuthError'
  }
}

export class APIError extends Error {
  status: number
  body: unknown
  constructor(status: number, body: unknown) {
    super(`API error ${status}`)
    this.name = 'APIError'
    this.status = status
    this.body = body
  }
}

/**
 * A 429 `{"error":"budget_exceeded","scope":…}`: a daily or per-agent cap
 * stopped the request. Callers show `budgetMessage(e)` instead of a generic
 * failure.
 */
export class BudgetError extends APIError {
  scope: string
  constructor(body: { scope?: string; agentId?: string }) {
    super(429, body)
    this.name = 'BudgetError'
    this.scope = body.scope ?? 'daily'
  }
}

export const budgetMessage = (e: BudgetError) => `Budget reached (${e.scope})`

// The bridge returns its reasons as {"error": ...}; this unwraps them for display.
export function errMessage(e: unknown): string {
  if (e instanceof BudgetError) return budgetMessage(e)
  if (e instanceof APIError) {
    const b = e.body as { error?: string } | undefined
    if (b && typeof b.error === 'string' && b.error) return b.error
    return e.message
  }
  return e instanceof Error ? e.message : String(e)
}

async function request<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const token = ensureToken()
  const init: RequestInit = {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body ? { 'Content-Type': 'application/json' } : {}),
    },
  }
  if (body) init.body = JSON.stringify(body)

  const res = await fetch(path, init)
  let data: unknown
  const text = await res.text()
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (res.status === 401) {
    memoryToken = null
    try {
      sessionStorage.removeItem(TOKEN_KEY)
    } catch {
      // ignore
    }
    throw new AuthError('Unauthorized')
  }
  if (!res.ok) {
    if (res.status === 429 && (data as { error?: string } | undefined)?.error === 'budget_exceeded') {
      throw new BudgetError(data as { scope?: string; agentId?: string })
    }
    throw new APIError(res.status, data)
  }
  return data as T
}

/**
 * The approval or question an agent is parked on. Present only while it is
 * genuinely outstanding, so its presence is enough to offer a decision.
 * `params` is the raw ACP request payload — shape varies by tool, so read
 * it defensively (see describePending in fleet.ts).
 */
export interface PendingRequest {
  kind: 'approval' | 'question'
  /** toolCallId for an approval, questionId for a question. */
  id: string
  params?: Record<string, unknown>
}

export interface AgentStatus {
  id: string
  project: string
  name?: string
  mode?: string
  status: 'idle' | 'running' | 'awaiting-approval' | 'awaiting-question' | 'error'
  activity?: string
  contextPct?: number
  changedFiles?: number
  interrupted?: boolean
  isolated?: boolean
  branch?: string
  sourceKind?: string
  readOnly?: boolean
  targetBranch?: string
  prUrl?: string
  pushedAt?: string
  gateOverride?: { reason: string; at: string; by: string; failedCommand?: string; skipped?: boolean }
  updatedAt: string
  pending?: PendingRequest
  /** Who owns the agent; unset or 'local' on a single-user bridge. */
  ownerId?: string
  /** Where the agent was started: ui, cli, mcp or issue. */
  origin?: string
  clientId?: string
}

export interface ProjectStatus {
  root: string
  available: boolean
  error?: string
  trust?: 'na' | 'untrusted' | 'trusted'
  isolation?: string
  orphanWorktrees?: string[]
}

export interface SpawnRequest { project: string; name?: string; mode?: string; prompt?: string; isolated?: boolean; branch?: string; baseRef?: string }
export async function listAgents(): Promise<AgentStatus[]> { return request('GET', '/api/agents') }
export async function listProjects(): Promise<ProjectStatus[]> { return request('GET', '/api/projects') }
export async function addProject(root: string): Promise<ProjectStatus[]> { return request('POST', '/api/projects', { root }) }
export async function removeProject(root: string): Promise<ProjectStatus[]> { return request('DELETE', '/api/projects', { root }) }
export async function spawnAgent(req: SpawnRequest): Promise<{ agentId: string; warning?: string }> { return request('POST', '/api/agents', req) }

export interface DiffFile {
  path: string
  added: number
  removed: number
}

export interface DiffResult {
  files: DiffFile[]
  diff?: string
}

export interface MergeResult {
  merged: boolean
  branch: string
  target: string
  reason?: string
  conflicts?: string[]
}

export async function getDiff(id: string, path?: string): Promise<DiffResult> {
  const q = path ? `?path=${encodeURIComponent(path)}` : ''
  return request('GET', `/api/agents/${encodeURIComponent(id)}/diff${q}`)
}

/** A refusal arrives as a 409; request throws APIError whose body is the MergeResult. */
export async function mergeAgent(id: string, commitMessage?: string): Promise<MergeResult> {
  return request('POST', `/api/agents/${encodeURIComponent(id)}/merge`, { commitMessage })
}

export async function discardAgent(id: string): Promise<void> {
  return request('POST', `/api/agents/${encodeURIComponent(id)}/discard`)
}

export interface GateResult {
  ok: boolean
  skipped: boolean
  failedCommand?: string
  output?: string
}

export interface ExitResult {
  destination: string
  branch?: string
  prUrl?: string
  verify?: GateResult
  blocked?: boolean
}

export async function exitAgent(id: string, opts: { commitMessage: string; override?: { reason: string } }): Promise<ExitResult> {
  return request('POST', `/api/agents/${encodeURIComponent(id)}/exit`, opts)
}

export function patchUrl(id: string): string {
  return `/api/agents/${encodeURIComponent(id)}/patch`
}

export interface SessionSummary {
  sessionId: string
  title?: string
  updated?: string
  messageCount?: number
  [key: string]: unknown
}

export interface Event {
  id: number
  sessionId: string
  data: unknown
}

export interface LoadResult {
  sessionId: string
  events: Event[]
}

export async function getConfig(): Promise<{ cwdRoot: string }> {
  return request('GET', '/api/config')
}

export async function listSessions(cwd: string): Promise<SessionSummary[]> {
  return request('GET', `/api/sessions?cwd=${encodeURIComponent(cwd)}`)
}

export async function newSession(cwd: string, sessionId?: string): Promise<{ sessionId: string }> {
  return request('POST', '/api/sessions', { cwd, sessionId })
}

export async function loadSession(id: string, cwd?: string): Promise<LoadResult> {
  return request('POST', `/api/sessions/${encodeURIComponent(id)}/load`, cwd ? { cwd } : undefined)
}

export async function deleteSession(id: string): Promise<void> {
  await request('DELETE', `/api/sessions/${encodeURIComponent(id)}`)
}

export async function promptSession(id: string, text: string): Promise<void> {
  await request('POST', `/api/sessions/${encodeURIComponent(id)}/prompt`, { text })
}

export async function steerSession(id: string, text: string): Promise<void> {
  await request('POST', `/api/sessions/${encodeURIComponent(id)}/steer`, { text })
}

/** Maps a 501 `{"error":"<feature>_unsupported"}` to the sentinel; anything else rethrows. */
async function orUnsupported<T>(call: () => Promise<T>): Promise<T | 'unsupported'> {
  try {
    return await call()
  } catch (e) {
    if (e instanceof APIError && e.status === 501) return 'unsupported'
    throw e
  }
}

/** The stack snapshot, or 'unsupported' for an agent that predates session/stack. */
export async function getStack(sessionId: string, subagentId?: number): Promise<import('./stack').StackSnapshot | 'unsupported'> {
  const q = subagentId ? `?subagent=${subagentId}` : ''
  return orUnsupported(() => request('GET', `/api/sessions/${encodeURIComponent(sessionId)}/stack${q}`))
}

export interface CallDetail {
  callId?: string
  stepId?: number
  toolName: string
  args?: string
  originalArgs?: string
  output?: string
  diff?: string
  error?: string
  exitCode?: number
  model?: string
  finishReason?: string
  rewritten?: boolean
  hooks?: unknown[]
  sandbox?: unknown
  symbols?: { file: string; name: string; kind?: string }[]
  notice?: { kind: string; text: string }
}
export interface ThoughtDetail { text: string; durationMs?: number }
export interface TodoDetail { id: string; content: string; status: string; startedAt?: number; completedAt?: number }
export interface RelationDetail { causedBy?: string[]; fixedBy?: string[] }
export interface NodeDetail {
  calls?: CallDetail[]
  narration?: string[]
  thinking?: ThoughtDetail[]
  todo?: TodoDetail
  relations?: RelationDetail
}
export interface NodeDetailResponse { node: import('./stack').WireNode; detail: NodeDetail }

export async function getNode(sessionId: string, nodeId: string, subagentId?: number): Promise<NodeDetailResponse | 'unsupported'> {
  const q = subagentId ? `?subagent=${subagentId}` : ''
  return orUnsupported(() =>
    request('GET', `/api/sessions/${encodeURIComponent(sessionId)}/nodes/${encodeURIComponent(nodeId)}${q}`),
  )
}

export interface RequestMessage {
  role: string
  content: string
  toolCalls?: { id: string; name: string; args: string }[]
  toolCallId?: string
  truncated?: boolean
  omittedBytes?: number
}
export interface RequestJSON {
  attemptId: number
  at?: number
  provider?: string
  model?: string
  messages: RequestMessage[]
  tools: { name: string; description?: string; parameters?: string }[]
  options: { thinking?: string; streaming: boolean; maxTokens?: number; temperature?: number; responseFormat?: string; toolChoice?: string }
  outcome: { status: string; err?: string; at?: number }
  truncated?: boolean
  packTokens?: number
  packWindow?: number
  packKnown?: boolean
}

export async function getLastRequest(sessionId: string): Promise<RequestJSON | 'unsupported'> {
  return orUnsupported(() => request('GET', `/api/sessions/${encodeURIComponent(sessionId)}/last-request`))
}

export interface StepDiff {
  stepNode: string
  turnNode: string
  taskNode?: string
  headline: string
  at?: number
  files: string[]
  diff: string
}

export async function getStepDiffs(sessionId: string): Promise<StepDiff[] | 'unsupported'> {
  return orUnsupported(() => request('GET', `/api/sessions/${encodeURIComponent(sessionId)}/step-diffs`))
}

export interface FileList { root?: string; path?: string; entries: { name: string; dir: boolean; size: number }[] }
export interface FileView { path: string; size: number; binary: boolean; truncated: boolean; content: string }

export async function listFiles(agentId: string, path = ''): Promise<FileList | 'unsupported'> {
  return orUnsupported(() => request('GET', `/api/agents/${encodeURIComponent(agentId)}/files?path=${encodeURIComponent(path)}`))
}

export async function readFile(agentId: string, path: string): Promise<FileView | 'unsupported'> {
  return orUnsupported(() => request('GET', `/api/agents/${encodeURIComponent(agentId)}/file?path=${encodeURIComponent(path)}`))
}

export interface GateRecord { result: GateResult; at: string }

/** The stored verify record, or null when none has run (204). */
export async function getGate(agentId: string): Promise<GateRecord | null> {
  const r = await request<GateRecord | undefined>('GET', `/api/agents/${encodeURIComponent(agentId)}/gate`)
  return r ?? null
}

export async function runGate(agentId: string): Promise<GateRecord> {
  return request('POST', `/api/agents/${encodeURIComponent(agentId)}/verify`)
}

export async function cancelSession(id: string): Promise<void> {
  await request('POST', `/api/sessions/${encodeURIComponent(id)}/cancel`)
}

export async function setMode(id: string, mode: string): Promise<void> {
  await request('POST', `/api/sessions/${encodeURIComponent(id)}/mode`, { mode })
}

export interface Decision {
  approved: boolean
  edited?: string
}

export async function resolvePermission(toolCallId: string, decision: Decision): Promise<void> {
  await request('POST', `/api/permissions/${encodeURIComponent(toolCallId)}`, decision)
}

export interface Answer {
  question: string
  answer: string | string[]
}

export interface Answers {
  answers?: Answer[]
  declined?: boolean
}

export async function resolveQuestion(questionId: string, answers: Answers): Promise<void> {
  await request('POST', `/api/questions/${encodeURIComponent(questionId)}`, answers)
}

export interface MCPClient {
  id: string
  name: string
  autonomous: boolean
  maxConcurrent: number
  maxPerDay: number
  allowedRepos: string[]
  ownerId: string
  createdAt: string
}

export interface CreateClientResult {
  id: string
  name: string
  token: string
  autonomous: boolean
}

export interface PendingSubmission {
  id: string
  origin: string
  clientId?: string
  /** Not sent by the bridge yet; unset counts as local. */
  ownerId?: string
  title: string
  repoId: string
  ref?: string
  prompt?: string
  plan?: string
  mode?: string
  createdAt: string
  expiresAt: string
}

export async function listClients(): Promise<MCPClient[]> {
  return request('GET', '/api/clients')
}

export async function createClient(opts: {
  name: string
  autonomous?: boolean
  maxConcurrent?: number
  maxPerDay?: number
  allowedRepos?: string[]
}): Promise<CreateClientResult> {
  return request('POST', '/api/clients', opts)
}

export async function deleteClient(id: string): Promise<void> {
  await request('DELETE', `/api/clients/${encodeURIComponent(id)}`)
}

export async function listPending(): Promise<PendingSubmission[]> {
  return request('GET', '/api/pending')
}

export async function approvePending(id: string): Promise<{ agentId: string; status: string }> {
  return request('POST', `/api/pending/${encodeURIComponent(id)}/approve`)
}

export async function denyPending(id: string): Promise<void> {
  await request('POST', `/api/pending/${encodeURIComponent(id)}/deny`)
}

export interface Issue {
  number: number
  title: string
  body: string
  url: string
  labels: string[]
}

export interface SubmitResult {
  agentId?: string
  pendingId?: string
  status: string
}

export async function listIssues(repoId: string): Promise<Issue[]> {
  return request('GET', `/api/repos/${encodeURIComponent(repoId)}/issues`)
}

export async function spawnFromIssue(repoId: string, number: number): Promise<SubmitResult> {
  return request('POST', `/api/repos/${encodeURIComponent(repoId)}/issues/${number}/spawn`)
}

export type { AuditEvent }

export async function listAudit(limit = 50): Promise<AuditEvent[]> {
  return request('GET', `/api/audit?limit=${limit}`)
}

export interface DiskStatus { repos: number; work: number; total: number; measuredAt: string; budgetMB: number }
export interface PruneResult { reclaimed: number; total: number; warning?: string }
export async function getDiskUsage(): Promise<DiskStatus> { return request('GET', '/api/disk') }
export async function pruneDisk(): Promise<PruneResult> { return request('POST', '/api/prune') }
// Runs (W3.1 RunDetail, served by the bridge's /api/runs).

export interface RunStage { name: string; state: 'pending' | 'active' | 'done' | 'failed' | 'skipped'; detail?: string }
export interface RunTask {
  n: number
  title: string
  dependsOn: number[]
  status: 'pending' | 'active' | 'done' | 'failed'
  startedAt?: number
  endedAt?: number
  execType?: string
  commit?: { base: string; head: string }
  fixRounds: number
  stages: RunStage[]
}
export interface SDDRun {
  active: boolean
  planName?: string
  planPath?: string
  branch?: string
  totalTasks: number
  doneTasks: number
  currentTask: number
  phase?: string
  detail?: string
  fixRound: number
  maxFixRounds: number
  tokensUsed: number
  tokensMax: number
  finished: boolean
  succeeded: boolean
  baseRef?: string
  startedAt?: number
  endedAt?: number
  phaseStartedAt?: number
  error?: string
  tasks: RunTask[]
  gate?: { taskN: number; question: string }
}
export interface SwarmRole { name: string; status: string; detail?: string; tokens: number; startedAt?: string }
export interface SwarmRun { goal?: string; active: boolean; roles: SwarmRole[]; tokensUsed: number; tokensMax: number }
export interface RunDetail { kind: 'sdd' | 'swarm' | 'none'; sdd?: SDDRun; swarm?: SwarmRun }

/** `at` is when the bridge last saw the run, in ms; `error` is a final run error the bridge recorded. */
export interface RunRow { agentId: string; name?: string; project: string; run: RunDetail; at?: number; error?: string }
export interface RunRequest { agentId?: string; project?: string; kind: 'sdd' | 'swarm'; plan?: string; planPath?: string; goal?: string }

export async function listRuns(): Promise<RunRow[]> {
  // The bridge stamps `at` as RFC3339; everything here compares milliseconds.
  const rows = (await request<(Omit<RunRow, 'at'> & { at?: string | number })[] | null>('GET', '/api/runs')) ?? []
  return rows.map((r) => {
    const at = typeof r.at === 'string' ? Date.parse(r.at) : r.at
    return { ...r, at: Number.isFinite(at) ? at : undefined }
  })
}
export async function getRun(agentId: string): Promise<RunDetail | 'unsupported'> {
  return orUnsupported(() => request('GET', `/api/runs/${encodeURIComponent(agentId)}`))
}
export async function startRun(req: RunRequest): Promise<{ agentId: string }> {
  return request('POST', '/api/runs', req)
}
export async function answerRun(agentId: string, answer: string): Promise<void> {
  await request('POST', `/api/runs/${encodeURIComponent(agentId)}/answer`, { answer })
}
export async function undoReroute(id: string): Promise<void> {
  await request('POST', `/api/reroutes/${encodeURIComponent(id)}/undo`)
}

export interface RosterRole { role: string; profile: string; provider?: string; model?: string; presetName?: string; customAgent?: string; localOnly: boolean; error?: string }
export interface Roster { roles: RosterRole[]; swarmBudget: { maxFixRounds: number; maxTotalTokens: number }; sddBudget: { maxFixRounds: number; maxTotalTokens: number } }
export async function getRoster(sessionId: string): Promise<Roster | 'unsupported'> {
  return orUnsupported(() => request('GET', `/api/sessions/${encodeURIComponent(sessionId)}/roster`))
}
