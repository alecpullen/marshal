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
  /** The workspace template the agent runs in (W4.2); unset for profile/devcontainer agents. */
  workspace?: { name: string; version: number; source: 'studio' | 'repo' }
}

export interface ProjectStatus {
  root: string
  available: boolean
  error?: string
  trust?: 'na' | 'untrusted' | 'trusted'
  isolation?: string
  orphanWorktrees?: string[]
}

/** A spawn's model choice: a routing profile, per-role preset overrides, or both (session/new `routing`). */
export interface RoutingChoice { profile?: string; overrides?: Record<string, string> }
export interface SpawnRequest { project: string; name?: string; mode?: string; prompt?: string; isolated?: boolean; branch?: string; baseRef?: string; routing?: RoutingChoice; workspace?: string }
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

export interface ReviewComment {
  id: string
  agentId: string
  path: string
  line: number
  side: 'old' | 'new'
  quote: string
  body: string
  createdAt: string
  sentAt: string
  resolvedAt?: string
}

/** The agent's drafted commit message, or 'unsupported' for an agent that cannot draft one. */
export async function getCommitDraft(agentId: string): Promise<string | 'unsupported'> {
  const r = await orUnsupported(() => request<{ message?: string }>('GET', `/api/agents/${encodeURIComponent(agentId)}/commit-draft`))
  return r === 'unsupported' ? r : (r?.message ?? '')
}

export async function listReviewComments(agentId: string): Promise<ReviewComment[]> {
  const r = await request<{ comments?: ReviewComment[] }>('GET', `/api/agents/${encodeURIComponent(agentId)}/review/comments`)
  return r?.comments ?? []
}

export async function postReviewComment(
  agentId: string,
  c: { path: string; line: number; side: 'old' | 'new'; quote: string; body: string },
): Promise<ReviewComment> {
  return request('POST', `/api/agents/${encodeURIComponent(agentId)}/review/comments`, c)
}

export async function resolveReviewComment(agentId: string, id: string): Promise<void> {
  return request('POST', `/api/agents/${encodeURIComponent(agentId)}/review/comments/${encodeURIComponent(id)}/resolve`)
}

export async function recentPrompts(project: string, limit = 20): Promise<string[]> {
  const r = await request<{ prompts?: string[] }>('GET', `/api/prompts/recent?project=${encodeURIComponent(project)}&limit=${limit}`)
  return r?.prompts ?? []
}

// Library, models, usage, budgets and watches (W3.2 bridge routes over the W3.1 ACP methods).

const q = encodeURIComponent
/** `?a=1&b=2` from the defined entries, or ''. */
function query(params: Record<string, string | undefined>): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v) p.set(k, v)
  const s = p.toString()
  return s ? `?${s}` : ''
}

export type LibraryScope = 'global' | 'project'
export interface SkillEntry { name: string; description: string; risk: string; scope: LibraryScope }
export interface SkillPreview { stagingToken: string; name: string; description: string; risk: string; source: string }
export interface PluginEntry { name: string; source: string; ref?: string; commit: string; contentHash: string; installedAt: string; scope: LibraryScope }
export interface PluginContents { hasManifest: boolean; skillCount: number; commandCount: number; hookCount: number; mcpServerCount: number; mcpPolicyCount: number }
export interface PluginScan { scanToken: string; name: string; source: string; ref?: string; commit: string; contents: PluginContents }
export type MemoryConfidence = 'tentative' | 'confirmed' | 'stale'
export interface MemoryEntry { id: number; kind: string; content: string; confidence: MemoryConfidence | string; sourceSessionId?: string; createdAt: string; updatedAt: string }

export async function listSkills(scope: LibraryScope, project?: string): Promise<SkillEntry[] | 'unsupported'> {
  const r = await orUnsupported(() => request<{ skills?: SkillEntry[] }>('GET', `/api/library/skills${query({ scope, project })}`))
  return r === 'unsupported' ? r : (r?.skills ?? [])
}
/** Staged on the session that will own the skill, so confirm must use the same scope and project. */
export async function previewSkill(source: string, scope: LibraryScope = 'global', project?: string): Promise<SkillPreview | 'unsupported'> {
  return orUnsupported(() => request('POST', '/api/library/skills/preview', { source, scope, ...(project ? { project } : {}) }))
}
export async function confirmSkill(stagingToken: string, scope: LibraryScope, project?: string): Promise<void> {
  await request('POST', '/api/library/skills/confirm', { stagingToken, scope, ...(project ? { project } : {}) })
}
export async function discardSkill(stagingToken: string): Promise<void> {
  await request('POST', '/api/library/skills/discard', { stagingToken })
}
export async function removeSkill(name: string, scope: LibraryScope, project?: string): Promise<void> {
  await request('DELETE', `/api/library/skills/${q(name)}${query({ scope, project })}`)
}

export async function listPlugins(scope: LibraryScope, project?: string): Promise<PluginEntry[] | 'unsupported'> {
  const r = await orUnsupported(() => request<{ plugins?: PluginEntry[] }>('GET', `/api/library/plugins${query({ scope, project })}`))
  return r === 'unsupported' ? r : (r?.plugins ?? [])
}
export async function scanPlugin(source: string, ref?: string, scope: LibraryScope = 'global', project?: string): Promise<PluginScan | 'unsupported'> {
  return orUnsupported(() => request('POST', '/api/library/plugins/scan', { source, scope, ...(ref ? { ref } : {}), ...(project ? { project } : {}) }))
}
export async function confirmPlugin(scanToken: string, scope: LibraryScope, project?: string): Promise<void> {
  await request('POST', '/api/library/plugins/confirm', { scanToken, scope, ...(project ? { project } : {}) })
}
export async function discardPlugin(scanToken: string): Promise<void> {
  await request('POST', '/api/library/plugins/discard', { scanToken })
}
export async function removePlugin(name: string, scope: LibraryScope, project?: string): Promise<void> {
  await request('DELETE', `/api/library/plugins/${q(name)}${query({ scope, project })}`)
}

export async function listMemory(project: string): Promise<MemoryEntry[] | 'unsupported'> {
  const r = await orUnsupported(() => request<{ entries?: MemoryEntry[] }>('GET', `/api/library/memory${query({ project })}`))
  return r === 'unsupported' ? r : (r?.entries ?? [])
}
export async function deleteMemory(id: number, project: string): Promise<void> {
  await request('DELETE', `/api/library/memory/${id}${query({ project })}`)
}
export async function setMemoryConfidence(id: number, project: string, confidence: string): Promise<void> {
  await request('POST', `/api/library/memory/${id}/confidence${query({ project })}`, { confidence })
}

export type KeySource = 'config' | 'env' | 'none'
export interface ProviderWire {
  type: string
  baseUrl: string
  apiKeyEnv?: string
  toolCalling?: boolean
  template?: string
  auth?: string
}
export interface ProviderView extends ProviderWire { hasKey: boolean; keySource: KeySource }
export interface PresetWire {
  provider: string
  model: string
  contextWindow?: number
  maxOutputTokens?: number
  toolCalling?: string
  localOnly?: boolean
  thinking?: string
}
export interface Binding { preset?: string; customAgent?: string }
export type CapAction = 'warn' | 'block' | 'pause'
export interface Budgets { dailyUsd: number; perAgentUsd: number; onDailyCap: string; onAgentCap: string }
export interface ModelsConfig {
  providers: Record<string, ProviderView>
  presets: Record<string, PresetWire>
  profiles: Record<string, Record<string, Binding>>
  customAgents: string[]
  defaultProfile: string
  activePreset: string
  roles: string[]
  budgets: Budgets
}
export interface ProbeResult { models: { id: string; contextWindow?: number }[]; error?: string }
export interface BudgetAgent { agentId: string; spentUsd: number; paused: boolean; overridden: boolean }
/** `GET /api/budgets` (bridge budgetReport). `loaded` is false while the caps could not be read, so `budgets` is zeroes, not the real caps. */
export interface BudgetStatus {
  budgets: Budgets
  daily: { day: string; spentUsd: number; blocked: boolean }
  agents: BudgetAgent[]
  loaded: boolean
}

export async function getModels(): Promise<ModelsConfig> { return request('GET', '/api/models') }
/** Sends only the named providers; `null` removes one. */
export async function setProviders(providers: Record<string, Partial<ProviderWire> | null>): Promise<void> {
  await request('PUT', '/api/models/providers', { providers })
}
export async function setProviderKey(name: string, key: string): Promise<void> {
  await request('PUT', `/api/models/providers/${q(name)}/key`, { key })
}
export async function setPresets(presets: Record<string, PresetWire | null>): Promise<void> {
  await request('PUT', '/api/models/presets', { presets })
}
export async function setRouting(r: { profiles?: Record<string, Record<string, Binding>>; defaultProfile?: string; activePreset?: string }): Promise<void> {
  await request('PUT', '/api/models/routing', r)
}
export async function probeProvider(name: string): Promise<ProbeResult> {
  return request('POST', '/api/models/probe', { name })
}
function toBudgetStatus(r: Partial<BudgetStatus> | undefined): BudgetStatus {
  return {
    budgets: r?.budgets ?? { dailyUsd: 0, perAgentUsd: 0, onDailyCap: 'warn', onAgentCap: 'warn' },
    daily: r?.daily ?? { day: '', spentUsd: 0, blocked: false },
    agents: r?.agents ?? [],
    loaded: r?.loaded ?? false,
  }
}
export async function getBudgets(): Promise<BudgetStatus> {
  return toBudgetStatus(await request<Partial<BudgetStatus>>('GET', '/api/budgets'))
}
/** The bridge re-reads what the engine stored (it may clamp or default), so adopt the returned report. */
export async function setBudgets(budgets: Budgets): Promise<BudgetStatus> {
  return toBudgetStatus(await request<Partial<BudgetStatus>>('PUT', '/api/budgets', { budgets }))
}
export async function overrideBudget(agentId: string): Promise<void> {
  await request('POST', `/api/agents/${q(agentId)}/budget/override`)
}

export type UsageBy = 'day' | 'project' | 'role' | 'model'
export interface UsageSeries { key: string; costUsd: number; tokens: number }
export interface UsageReport {
  range: string
  totals: { costUsd: number; promptTokens: number; completionTokens: number; agentHours: number; prsShipped: number }
  series: UsageSeries[]
}
export async function getUsage(range: '7d' | '30d', by: UsageBy): Promise<UsageReport> {
  const r = await request<UsageReport>('GET', `/api/usage${query({ range, by })}`)
  return { ...r, series: r?.series ?? [] }
}

export interface WatchSample { at: number; value: number; tripped: boolean }
export interface RerouteRule { role: string; preset: string }
/** On a watch row, the rule the bridge started it with; `reroute` drops off once it has fired. On a create request only `reroute` is sent. */
export interface OnTrip { notify?: boolean; resume?: boolean; reroute?: RerouteRule }
export interface WatchInfo {
  /** The agent id, or `studio` for a Studio-owned watch. */
  agentId: string
  id: string
  name: string
  kind: 'command' | 'job' | 'file' | string
  state: 'watching' | 'fired' | 'stopped' | 'error' | string
  condition?: string
  mode: string
  intervalMs: number
  owner?: string
  fireCount: number
  lastSample?: string
  lastError?: string
  createdAt: number
  lastFiredAt?: number
  samples: WatchSample[]
  /** Held by the bridge, so only on watches it started; others have none. */
  onTrip?: OnTrip
  notify?: boolean
  resume?: boolean
}
export interface WatchSpec {
  name: string
  kind: 'command' | 'job' | 'file'
  command?: string
  jobId?: string
  path?: string
  condition?: string
  mode: string
  intervalMs: number
  notify?: boolean
  resume?: boolean
}
export async function listWatches(): Promise<WatchInfo[]> {
  return (await request<WatchInfo[] | null>('GET', '/api/watches')) ?? []
}
export async function createWatch(req: { agentId?: string; spec: WatchSpec; onTrip?: OnTrip }): Promise<{ id: string }> {
  return request('POST', '/api/watches', req)
}
export async function stopWatch(owner: string, id: string): Promise<void> {
  await request('DELETE', `/api/watches/${q(owner)}/${q(id)}`)
}

// ---- Workspaces (W4.2 templates and builds, W4.3 secrets and network) ----

export interface WSWorkspace { name: string; base: string; toolchains: string[]; extends: string }
export interface WSPackages { apt: string[]; go: string[]; npm: string[]; pip: string[] }
export interface WSMount { repo: string; volume: string; target: string; readonly: boolean }
export interface WSFileMount { target: string; readonly: boolean }
export interface WSInject { ref: string; header: string; format: string }
export type WSNetworkMode = 'open' | 'allowlist' | 'off'
export interface WSNetwork { mode: WSNetworkMode | ''; egress: string[] }
export interface WSResources { cpu: number; memory: string; disk: string; timeout: string }
export interface WSPolicy { mode: string; allow: string[] }
/** The typed workspace file, as `workspace/parse` returns it (spec §4.2). */
export interface WSDoc {
  workspace: WSWorkspace
  packages: WSPackages
  mounts: WSMount[]
  files: Record<string, WSFileMount>
  secretsEnv: Record<string, string>
  inject: Record<string, WSInject>
  network: WSNetwork
  resources: WSResources
  policy: WSPolicy
  setup: { run: string }
}
/** One layer's 1-based inclusive source line range. Layers 1 and 2 share `[workspace]`. */
export interface WSSection { layer: number; key: string; startLine: number; endLine: number }
export interface WSDiag { line: number; message: string; severity: 'error' | 'warning' }
export type BuildStatus = 'pending' | 'building' | 'ok' | 'failed'
export interface TemplateVersion { n: number; at: string | number; by?: string; sha256?: string; imageTag?: string; buildStatus: BuildStatus; sizeBytes?: number; buildMs?: number }
export interface TemplateMeta { name: string; ownerId?: string; createdAt?: string | number; published: number; pool: number; versions: TemplateVersion[] }
export interface WorkspaceListItem {
  source: 'studio' | 'repo'
  name: string
  /** Repo templates: the project root they live in. */
  project?: string
  published?: number
  pool?: number
  usage: number
  /** True when a draft exists that differs from the published version. */
  draftChanges?: boolean
  versions?: TemplateVersion[]
  /** The latest parsed doc, for the content chips; absent when it did not parse. */
  doc?: WSDoc
}
export interface WSLoaded { source: string; doc: WSDoc; sections: WSSection[]; diagnostics: WSDiag[]; /** 0 when the draft was read, else the published version. */ version?: number; meta?: TemplateMeta }
export interface PoolStatus { size: number; idle: number; starting: number }
export interface BuildsInfo { versions: TemplateVersion[] | null; pool?: PoolStatus; starts?: { coldMs: number; warmMs: number } }
export type WorkspaceFrom = 'blank' | `starter:${string}` | `devcontainer:${string}` | `snapshot:${string}`
export interface NetRow { host: string; requests: number; blocked: number; bytesUp: number; bytesDown: number; lastSeen: number; decision: string; injected?: boolean; rule?: string }
export type RepoInfo = RepoRow

const wsPath = (name: string) => `/api/workspaces/${encodeURIComponent(name)}`

export async function listWorkspaces(): Promise<WorkspaceListItem[]> { return request('GET', '/api/workspaces') }
export async function createWorkspace(name: string, from: WorkspaceFrom): Promise<{ name: string }> { return request('POST', '/api/workspaces', { name, from }) }
/** Without `version` the draft is read when it exists, otherwise the published version. */
export async function getWorkspace(name: string, version?: number): Promise<WSLoaded> {
  return request('GET', `${wsPath(name)}${version ? `?version=${version}` : ''}`)
}
export async function saveWorkspaceDraft(name: string, source: string): Promise<WSLoaded> { return request('PUT', `${wsPath(name)}/draft`, { source }) }
export async function patchWorkspace(name: string, layer: number, value: unknown): Promise<WSLoaded> { return request('POST', `${wsPath(name)}/patch`, { layer, value }) }
export async function publishWorkspace(name: string): Promise<TemplateVersion> { return request('POST', `${wsPath(name)}/publish`, {}) }
export async function diffWorkspace(name: string, a: number, b: number): Promise<string> {
  const r = await request<string | { diff: string }>('GET', `${wsPath(name)}/diff?a=${a}&b=${b}`)
  return typeof r === 'string' ? r : r.diff
}
export async function deleteWorkspace(name: string): Promise<void> { await request('DELETE', wsPath(name)) }
export async function setWorkspacePool(name: string, size: number): Promise<void> { await request('PUT', `${wsPath(name)}/pool`, { size }) }
export async function rotateWorkspaceCA(name: string): Promise<void> { await request('POST', `${wsPath(name)}/ca/rotate`, {}) }
export async function listBuilds(name: string): Promise<BuildsInfo> { return request('GET', `${wsPath(name)}/builds`) }
export async function startBuild(name: string, version?: number): Promise<{ version: number }> {
  return request('POST', `${wsPath(name)}/builds`, version ? { version } : {})
}
// Network inspector, secrets, credentials, repos and project settings (W4.2 and W4.3 bridge routes).

export type NetRule = 'allowlisted' | 'granted' | 'open' | 'injected' | 'blocked' | 'allowed'
/** One row of `GET /api/network?view=hosts`. `lastSeen` is Unix ms. */
export interface NetHostRow {
  workspace?: string
  agentId?: string
  host: string
  rule: NetRule
  requests: number
  blocked: number
  bytesUp: number
  bytesDown: number
  lastSeen: number
  decision: 'allow' | 'block'
  injected?: boolean
}
export interface NetHosts { processMode: boolean; rows: NetHostRow[] }
/** One connection record (`view=requests`). */
export interface NetRecord {
  at: number
  agentId: string
  workspace?: string
  host: string
  port: number
  decision: 'allow' | 'block'
  injected?: boolean
  bytesUp: number
  bytesDown: number
  durationMs: number
}
/** One row of `view=agents`. */
export interface NetAgentRow {
  agentId: string
  workspace?: string
  hosts: number
  requests: number
  blocked: number
  bytesUp: number
  bytesDown: number
  lastSeen: number
}
export type NetView = 'hosts' | 'requests' | 'agents'
export type NetDecisionKind = 'block' | 'allow-agent' | 'add-to-workspace'
/**
 * What `add-to-workspace` returns. A repo template gets a `patch` to copy; a
 * Studio template's draft changed, so there is nothing to show but a toast.
 */
export interface NetDecisionResult { ok?: boolean; patch?: string; workspace?: string; source?: string }

const netQuery = (view: NetView, scope: { workspace?: string; agent?: string }) => query({ view, workspace: scope.workspace, agent: scope.agent })
export async function getNetworkHosts(scope: { workspace?: string; agent?: string } = {}): Promise<NetHosts> {
  const r = await request<Partial<NetHosts> | null>('GET', `/api/network${netQuery('hosts', scope)}`)
  return { processMode: r?.processMode === true, rows: r?.rows ?? [] }
}
export async function getNetworkRequests(scope: { workspace?: string; agent?: string } = {}): Promise<NetRecord[]> {
  return (await request<NetRecord[] | null>('GET', `/api/network${netQuery('requests', scope)}`)) ?? []
}
export async function getNetworkAgents(scope: { workspace?: string } = {}): Promise<NetAgentRow[]> {
  return (await request<NetAgentRow[] | null>('GET', `/api/network${netQuery('agents', scope)}`)) ?? []
}
/** `GET /api/network/pending`: blocked requests still awaiting a decision, oldest first, shaped like the `network_block` delta. */
export interface NetPending { kind: 'network_block'; sessionId: string; agentId: string; host: string; workspace?: string; at: number }
export async function getNetworkPending(agent?: string): Promise<NetPending[]> {
  return (await request<{ pending?: NetPending[] } | null>('GET', `/api/network/pending${query({ agent })}`))?.pending ?? []
}
export async function postNetworkDecision(agentId: string, host: string, decision: NetDecisionKind): Promise<NetDecisionResult> {
  return (await request<NetDecisionResult | undefined>('POST', '/api/network/decisions', { agentId, host, decision })) ?? {}
}

export interface SecretsStatus { backend: 'env' | 'local' | 'openbao' | string; healthy: boolean; error?: string }
export type CredentialKind = 'none' | 'pat' | 'ssh' | 'vault'
export interface CredentialRow { id: string; kind: CredentialKind; envVar?: string; keyPath?: string; ref?: string; user?: string; set: boolean }
export interface CredentialInput { id: string; kind: CredentialKind; envVar?: string; keyPath?: string; ref?: string; user?: string }
export interface RepoRow { id: string; url: string; branch?: string; forge?: string; apiBase?: string; credRef?: string; watch?: boolean; watchLabel?: string }

export async function getSecretsStatus(): Promise<SecretsStatus> { return request('GET', '/api/secrets/status') }
/** Refs only, as `vault:<path>`; a value has no route out. */
export async function listSecrets(prefix?: string): Promise<string[]> {
  return (await request<{ refs?: string[] }>('GET', `/api/secrets${query({ prefix })}`)).refs ?? []
}
/** `ref` is `vault:<path>` or the bare path; slashes in it stay slashes. */
const secretPath = (ref: string) => ref.replace(/^vault:/, '').split('/').map(q).join('/')
export async function putSecret(ref: string, value: string): Promise<void> {
  await request('PUT', `/api/secrets/${secretPath(ref)}`, { value })
}
export async function deleteSecret(ref: string): Promise<void> {
  await request('DELETE', `/api/secrets/${secretPath(ref)}`)
}
export async function listCredentials(): Promise<CredentialRow[]> {
  return (await request<CredentialRow[] | null>('GET', '/api/credentials')) ?? []
}
export async function putCredential(c: CredentialInput): Promise<CredentialRow> { return request('POST', '/api/credentials', c) }
export async function deleteCredential(id: string): Promise<void> { await request('DELETE', `/api/credentials/${q(id)}`) }
export async function listRepos(): Promise<RepoRow[]> {
  return (await request<RepoRow[] | null>('GET', '/api/repos')) ?? []
}
export async function registerRepo(r: RepoRow): Promise<RepoRow> { return request('POST', '/api/repos', r) }
export async function removeRepo(id: string): Promise<void> { await request('DELETE', `/api/repos/${q(id)}`) }

/** `GET`/`PUT /api/projects/settings?root=`. `routing` is a W3 routing object (profile and overrides). */
export interface ProjectSettings {
  workspace?: string
  routing?: RoutingChoice
  mode?: string
  isolated?: boolean
  shipTarget?: 'merge' | 'push' | 'patch' | ''
  intake: { repoId?: string; labels?: string[]; clients?: string[] }
}
export interface ProjectHealth {
  /** The project's verify-gate commands; both empty means the gate has nothing to run. */
  verify?: { build: string; test: string }
  gateRunnable: 'yes' | 'no' | 'unknown'
  mirrorFresh: { repoId: string; present: boolean; ageSeconds?: number; head?: string }[]
  orphanWorktrees: string[]
  trust: string
  workspaceResolves?: { ref: string; resolves: boolean; built: boolean; error?: string }
}
export async function getProjectSettings(root: string): Promise<ProjectSettings> {
  const s = await request<Partial<ProjectSettings> | null>('GET', `/api/projects/settings${query({ root })}`)
  return { ...s, intake: s?.intake ?? {} }
}
export async function putProjectSettings(root: string, s: ProjectSettings): Promise<ProjectSettings> {
  const r = await request<Partial<ProjectSettings> | null>('PUT', `/api/projects/settings${query({ root })}`, s)
  return { ...r, intake: r?.intake ?? {} }
}
export async function getProjectHealth(root: string): Promise<ProjectHealth> {
  return request('GET', `/api/projects/health${query({ root })}`)
}

/** The `[policy]` of a Studio template (its draft, or `version`); null when there is none to show. */
export async function getWorkspacePolicy(name: string, version?: number): Promise<{ mode?: string; allow?: string[] } | null> {
  return (await getWorkspace(name, version)).doc?.policy ?? null
}
