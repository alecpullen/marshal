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