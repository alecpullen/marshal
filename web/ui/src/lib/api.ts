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