import { writable } from 'svelte/store'
import { getNetworkPending, listAgents, listProjects, postNetworkDecision, type AgentStatus, type NetDecisionKind, type NetDecisionResult, type GateRecord, type PendingRequest, type ProjectStatus, type RunDetail } from './api'
import type { PendingPermission, PendingQuestion, Question, QuestionOption } from './store'

export type AgentRow = AgentStatus & { name: string; mode: string; activity: string; contextPct: number; changedFiles: number; interrupted: boolean; gate?: GateRecord; run?: RunDetail; runAt?: number }
export interface FleetDelta {
  kind: 'activity' | 'telemetry' | 'mode' | 'turn' | 'gate'
  sessionId: string
  activity?: string
  mode?: string
  contextPct?: number
  changedFiles?: number
  /** The latest verify record. Its output is left out of the stream; GET …/gate has it. */
  gate?: GateRecord
  /** Optional telemetry sections; the bridge's digest carries only the two counts above today. */
  toolStats?: ToolStat[]
  rules?: string[]
}
/** The engine's `session_telemetry` tool row. */
export interface ToolStat { name: string; calls: number; errors?: number; slowestMs?: number }
/** The last `telemetry` delta of an agent, for the project page's session-sheet tab. */
export interface AgentTelemetry { contextPct: number; changedFiles: number; toolStats?: ToolStat[]; rules?: string[]; at: number }
/** A request the egress proxy blocked, awaiting Block, Allow for this agent, or Add to workspace. */
export interface NetworkBlockDelta { kind: 'network_block'; sessionId: string; agentId?: string; host: string; workspace?: string; at?: number }
export interface NetworkDecisionItem { agentId: string; host: string; workspace?: string; at: number }
/** A run's latest detail (run_progress). `at` is when the bridge saw it, in ms. */
export interface RunDelta { kind: 'run'; sessionId: string; run: RunDetail; at?: number }
/** A daily or per-agent budget check; no session, so it never touches a row. */
export interface BudgetDelta { kind: 'budget'; scope: 'daily' | 'agent'; agentId?: string; spentUsd: number; capUsd: number; action: string }
/** A watch rerouted a role; the inbox offers Undo. */
export interface RerouteDelta { kind: 'reroute'; id: string; watch: string; role: string; from: string; to: string }
/** A watch changed state (or fired). `sessionId` is the owner agent, or `studio`; the Watches page refetches on it. */
export interface WatchDelta { kind: 'watch'; sessionId?: string; event?: unknown }
/** A review draft or CI fix result; no session, so it only moves lists and notifications. */
export interface AutomationDelta { kind: 'automation'; type: string; id: string; status?: string }
export interface AutomationEvent { type: string; id: string; status?: string; at: number }
export type BudgetState = Omit<BudgetDelta, 'kind'>
export type RerouteNotice = Omit<RerouteDelta, 'kind'>
export interface ProjectRemovedDelta { kind: 'project_removed'; project: string }
/**
 * An agent parked on an approval or question. Carries only the kind — the
 * payload needed to render a decision comes from a snapshot refetch, since
 * the snapshot is the authority on what is still outstanding.
 */
export interface PendingDelta { kind: 'pending'; sessionId: string; pendingKind: 'approval' | 'question' }
export type FleetEvent = NetworkBlockDelta | FleetDelta | ProjectRemovedDelta | PendingDelta | RunDelta | BudgetDelta | RerouteDelta | WatchDelta | AutomationDelta
export function toRow(a: AgentStatus): AgentRow { return { ...a, name: a.name ?? '', mode: a.mode ?? '', activity: a.activity ?? '', contextPct: a.contextPct ?? 0, changedFiles: a.changedFiles ?? 0, interrupted: a.interrupted ?? false } }
const rank: Record<AgentRow['status'], number> = { 'awaiting-approval': 0, 'awaiting-question': 0, error: 1, running: 2, idle: 3 }
export function sortAttentionFirst(rows: AgentRow[]): AgentRow[] { return [...rows].sort((a,b) => rank[a.status] - rank[b.status] || a.id.localeCompare(b.id)) }
export type AgentGroupKey = 'needsYou' | 'running' | 'ready' | 'earlier'
export type AgentGroups = Record<AgentGroupKey, AgentRow[]>

const pendingRank = (a: AgentRow) => (a.pending?.kind === 'approval' ? 0 : a.pending ? 1 : 2)
const byUrgency = (a: AgentRow, b: AgentRow) =>
  pendingRank(a) - pendingRank(b) || rank[a.status] - rank[b.status] || b.updatedAt.localeCompare(a.updatedAt)

/**
 * Bucket agents for the sidebar and inbox. An agent lands in the first group
 * it qualifies for: Needs you (pending), Running, Ready to ship (idle with
 * changes and no PR), else Earlier. Each group is sorted by urgency, then by
 * most recently updated.
 */
export function groupAgents(agents: AgentRow[]): AgentGroups {
  const out: AgentGroups = { needsYou: [], running: [], ready: [], earlier: [] }
  for (const a of agents) {
    if (a.pending) out.needsYou.push(a)
    else if (a.status === 'running') out.running.push(a)
    else if (a.status === 'idle' && a.changedFiles > 0 && !a.prUrl) out.ready.push(a)
    else out.earlier.push(a)
  }
  for (const k of Object.keys(out) as AgentGroupKey[]) out[k].sort(byUrgency)
  return out
}

export function applyDeltaTo(rows: AgentRow[], d: FleetEvent): AgentRow[] {
  if (d.kind === 'project_removed') return rows.filter((r) => r.project !== d.project)
  // Budget, reroute and watch deltas are fleet-wide; the store handles them.
  if (d.kind === 'budget' || d.kind === 'reroute' || d.kind === 'watch' || d.kind === 'network_block' || d.kind === 'automation') return rows
  let changed = false
  const out = rows.map((r) => {
    if (r.id !== d.sessionId) return r
    changed = true
    if (d.kind === 'activity') return { ...r, activity: d.activity ?? r.activity }
    if (d.kind === 'mode') return { ...r, mode: d.mode ?? r.mode }
    if (d.kind === 'gate') return { ...r, gate: d.gate ?? r.gate }
    if (d.kind === 'run') return { ...r, run: d.run, runAt: d.at ?? Date.now() }
    if (d.kind === 'telemetry') return { ...r, contextPct: d.contextPct ?? r.contextPct, changedFiles: d.changedFiles ?? r.changedFiles }
    if (d.kind === 'pending') {
      const status: AgentRow['status'] = d.pendingKind === 'approval' ? 'awaiting-approval' : 'awaiting-question'
      return { ...r, status }
    }
    return r
  })
  return changed ? out : rows
}

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
  const state = writable({
    agents: [] as AgentRow[],
    projects: [] as ProjectStatus[],
    loading: false,
    error: null as string | null,
    budget: null as BudgetState | null,
    notices: [] as RerouteNotice[],
    /** Bumped on every watch delta; views that list watches refetch when it moves. */
    watchTick: 0,
    /** Bumped on every budget delta; views that show spend refetch when it moves. */
    budgetTick: 0,
    /** Blocked requests waiting on a person, oldest first, one per (agent, host). */
    decisions: [] as NetworkDecisionItem[],
    /** The last telemetry delta per agent. */
    telemetry: {} as Record<string, AgentTelemetry>,
    /** Review drafts and CI results as they arrive, newest last; views refetch their lists when it grows. */
    automations: [] as AutomationEvent[],
    /** Bumped on every automation delta; views that list drafts or CI results refetch when it moves. */
    automationTick: 0,
  })
  async function refresh() {
    state.update((s) => ({ ...s, loading: true, error: null }))
    try {
      // Pending prompts survive a reload through the bridge; a bridge without the route just has none to restore.
      const [a, p, pend] = await Promise.all([listAgents(), listProjects(), getNetworkPending().catch(() => [])])
      // The snapshot's rows have no run digest; keep the one deltas gave us.
      state.update((s) => {
        const prev = new Map(s.agents.map((r) => [r.id, r]))
        const agents = a.map(toRow).map((r) => {
          const old = prev.get(r.id)
          return old?.run ? { ...r, run: old.run, runAt: old.runAt } : r
        })
        // A request for an agent that is gone has nobody left to decide for.
        const live = new Set(agents.map((r) => r.id))
        const byKey = new Map<string, NetworkDecisionItem>()
        for (const d of s.decisions) byKey.set(`${d.agentId}|${d.host}`, d)
        for (const d of pend) byKey.set(`${d.agentId}|${d.host}`, { agentId: d.agentId, host: d.host, workspace: d.workspace, at: d.at })
        const decisions = [...byKey.values()].filter((d) => live.has(d.agentId)).sort((x, y) => x.at - y.at)
        return { ...s, agents, projects: p, loading: false, error: null, decisions }
      })
    } catch (e) {
      state.update((s) => ({ ...s, loading: false, error: e instanceof Error ? e.message : String(e) }))
    }
  }
  function applyDelta(d: FleetEvent) {
    state.update((s) => {
      if (d.kind === 'budget') {
        const { kind: _k, ...b } = d
        return { ...s, budget: b, budgetTick: s.budgetTick + 1 }
      }
      if (d.kind === 'watch') return { ...s, watchTick: s.watchTick + 1 }
      if (d.kind === 'automation') return { ...s, automations: [...s.automations, { type: d.type, id: d.id, status: d.status, at: Date.now() }].slice(-50), automationTick: s.automationTick + 1 }
      if (d.kind === 'network_block') {
        const agentId = d.agentId ?? d.sessionId
        const item: NetworkDecisionItem = { agentId, host: d.host, workspace: d.workspace, at: d.at ?? Date.now() }
        // A later delta for the same (agent, host) replaces the earlier one in place.
        const i = s.decisions.findIndex((x) => x.agentId === agentId && x.host === d.host)
        const decisions = i < 0 ? [...s.decisions, item] : s.decisions.map((x, j) => (j === i ? item : x))
        return { ...s, decisions: decisions.sort((a, b) => a.at - b.at) }
      }
      if (d.kind === 'telemetry') {
        const t: AgentTelemetry = { contextPct: d.contextPct ?? 0, changedFiles: d.changedFiles ?? 0, toolStats: d.toolStats, rules: d.rules, at: Date.now() }
        return { ...s, telemetry: { ...s.telemetry, [d.sessionId]: t }, agents: applyDeltaTo(s.agents, d), projects: s.projects }
      }
      if (d.kind === 'reroute') {
        const { kind: _k, ...n } = d
        return { ...s, notices: [...s.notices.filter((x) => x.id !== n.id), n] }
      }
      return {
        ...s,
        agents: applyDeltaTo(s.agents, d),
        projects: d.kind === 'project_removed' ? s.projects.filter((p) => p.root !== d.project) : s.projects,
      }
    })
  }
  function dismissNotice(id: string) {
    state.update((s) => ({ ...s, notices: s.notices.filter((n) => n.id !== id) }))
  }
  /** Posts the decision, then drops the item; a failed post keeps it so the person can retry. */
  async function decideNetwork(agentId: string, host: string, decision: NetDecisionKind): Promise<NetDecisionResult> {
    const res = await postNetworkDecision(agentId, host, decision)
    state.update((s) => ({ ...s, decisions: s.decisions.filter((x) => !(x.agentId === agentId && x.host === host)) }))
    return res
  }
  return { state, actions: { refresh, applyDelta, dismissNotice, decideNetwork } }
}
