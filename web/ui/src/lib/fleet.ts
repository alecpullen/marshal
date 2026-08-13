import { writable } from 'svelte/store'
import { listAgents, listProjects, type AgentStatus, type PendingRequest, type ProjectStatus } from './api'
import type { PendingPermission, PendingQuestion, Question, QuestionOption } from './store'

export type AgentRow = AgentStatus & { name: string; mode: string; activity: string; contextPct: number; changedFiles: number; interrupted: boolean }
export interface FleetDelta { kind: 'activity' | 'telemetry' | 'mode' | 'turn'; sessionId: string; activity?: string; mode?: string; contextPct?: number; changedFiles?: number }
export interface ProjectRemovedDelta { kind: 'project_removed'; project: string }
/**
 * An agent parked on an approval or question. Carries only the kind — the
 * payload needed to render a decision comes from a snapshot refetch, since
 * the snapshot is the authority on what is still outstanding.
 */
export interface PendingDelta { kind: 'pending'; sessionId: string; pendingKind: 'approval' | 'question' }
export type FleetEvent = FleetDelta | ProjectRemovedDelta | PendingDelta
export function toRow(a: AgentStatus): AgentRow { return { ...a, name: a.name ?? '', mode: a.mode ?? '', activity: a.activity ?? '', contextPct: a.contextPct ?? 0, changedFiles: a.changedFiles ?? 0, interrupted: a.interrupted ?? false } }
const rank: Record<AgentRow['status'], number> = { 'awaiting-approval': 0, 'awaiting-question': 0, error: 1, running: 2, idle: 3 }
export function sortAttentionFirst(rows: AgentRow[]): AgentRow[] { return [...rows].sort((a,b) => rank[a.status] - rank[b.status] || a.id.localeCompare(b.id)) }
export function applyDeltaTo(rows: AgentRow[], d: FleetEvent): AgentRow[] { if (d.kind === 'project_removed') return rows.filter(r => r.project !== d.project); let changed = false; const out = rows.map(r => { if (r.id !== d.sessionId) return r; changed = true; if (d.kind === 'activity') return { ...r, activity: d.activity ?? r.activity }; if (d.kind === 'mode') return { ...r, mode: d.mode ?? r.mode }; if (d.kind === 'telemetry') return { ...r, contextPct: d.contextPct ?? r.contextPct, changedFiles: d.changedFiles ?? r.changedFiles }; if (d.kind === 'pending') { const status: AgentRow['status'] = d.pendingKind === 'approval' ? 'awaiting-approval' : 'awaiting-question'; return { ...r, status } } return r }); return changed ? out : rows }

/**
 * A one-line summary of what an agent is waiting on, for the attention
 * list. `params` is a raw ACP payload whose shape varies by tool, so every
 * field is probed defensively and a generic label is the floor — this must
 * never throw, or one odd payload blanks the whole list.
 */
export function describePending(p: PendingRequest): string {
  const fallback = p.kind === 'approval' ? 'Approval required' : 'Answer required'
  const params = p.params
  if (!params || typeof params !== 'object') return fallback

  if (p.kind === 'question') {
    const questions = (params as { questions?: unknown }).questions
    if (Array.isArray(questions) && questions.length > 0) {
      const first = questions[0]
      if (first && typeof first === 'object') {
        const text = (first as { question?: unknown }).question
        if (typeof text === 'string' && text.trim() !== '') return text
      }
    }
    return fallback
  }

  // Approvals: the command is the most informative thing when present,
  // since that is what the user is actually being asked to allow.
  const command = (params as { command?: unknown }).command
  if (typeof command === 'string' && command.trim() !== '') return command
  const toolName = (params as { toolName?: unknown }).toolName
  if (typeof toolName === 'string' && toolName.trim() !== '') return toolName
  return fallback
}
/**
 * Adapt a pending approval onto PermissionModal's prop shape, so the
 * dashboard reuses the chat's decision UI instead of duplicating it.
 * Optional fields are left undefined rather than set to '' — the modal
 * branches on their presence.
 */
export function toPendingPermission(p: PendingRequest): PendingPermission {
  const params = (p.params ?? {}) as Record<string, unknown>
  const str = (k: string): string | undefined => (typeof params[k] === 'string' && params[k] !== '' ? (params[k] as string) : undefined)
  return { toolCallId: p.id, toolName: str('toolName') ?? '', command: str('command'), diff: str('diff') }
}

/** Normalise one option, accepting both {label,value} and a bare string. */
function toOption(raw: unknown): QuestionOption | null {
  if (typeof raw === 'string') return { label: raw, value: raw }
  if (raw && typeof raw === 'object') {
    const o = raw as { label?: unknown; value?: unknown }
    const value = typeof o.value === 'string' ? o.value : typeof o.label === 'string' ? o.label : null
    if (value === null) return null
    return { label: typeof o.label === 'string' ? o.label : value, value }
  }
  return null
}

/**
 * Adapt a pending question onto QuestionModal's prop shape. An unusable
 * payload yields an empty question list rather than throwing, so one bad
 * request cannot break the dashboard.
 */
export function toPendingQuestion(sessionId: string, p: PendingRequest): PendingQuestion {
  const raw = (p.params as { questions?: unknown } | undefined)?.questions
  const questions: Question[] = Array.isArray(raw)
    ? raw.flatMap((q) => {
        if (!q || typeof q !== 'object') return []
        const src = q as { question?: unknown; options?: unknown; multi?: unknown; allowOther?: unknown }
        if (typeof src.question !== 'string') return []
        const out: Question = { question: src.question }
        if (Array.isArray(src.options)) {
          const options = src.options.map(toOption).filter((o): o is QuestionOption => o !== null)
          if (options.length > 0) out.options = options
        }
        if (src.multi === true) out.multi = true
        if (src.allowOther === true) out.allowOther = true
        return [out]
      })
    : []
  return { questionId: p.id, sessionId, questions }
}

export function createFleetStore() {
  const state = writable({ agents: [] as AgentRow[], projects: [] as ProjectStatus[], loading: false, error: null as string | null })
  async function refresh() { state.update(s => ({ ...s, loading: true, error: null })); try { const [a,p] = await Promise.all([listAgents(), listProjects()]); state.set({ agents: a.map(toRow), projects: p, loading: false, error: null }) } catch (e) { state.update(s => ({ ...s, loading: false, error: e instanceof Error ? e.message : String(e) })) } }
  function applyDelta(d: FleetEvent) { state.update(s => ({ ...s, agents: applyDeltaTo(s.agents, d), projects: d.kind === 'project_removed' ? s.projects.filter(p => p.root !== d.project) : s.projects })) }
  return { state, actions: { refresh, applyDelta } }
}