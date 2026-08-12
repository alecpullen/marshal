import { writable } from 'svelte/store'
import { listAgents, listProjects, type AgentStatus, type ProjectStatus } from './api'

export type AgentRow = AgentStatus & { name: string; mode: string; activity: string; contextPct: number; changedFiles: number; interrupted: boolean }
export interface FleetDelta { kind: 'activity' | 'telemetry' | 'mode' | 'turn'; sessionId: string; activity?: string; mode?: string; contextPct?: number; changedFiles?: number }
export function toRow(a: AgentStatus): AgentRow { return { ...a, name: a.name ?? '', mode: a.mode ?? '', activity: a.activity ?? '', contextPct: a.contextPct ?? 0, changedFiles: a.changedFiles ?? 0, interrupted: a.interrupted ?? false } }
const rank: Record<AgentRow['status'], number> = { 'awaiting-approval': 0, 'awaiting-question': 0, error: 1, running: 2, idle: 3 }
export function sortAttentionFirst(rows: AgentRow[]): AgentRow[] { return [...rows].sort((a,b) => rank[a.status] - rank[b.status] || a.id.localeCompare(b.id)) }
export function applyDeltaTo(rows: AgentRow[], d: FleetDelta): AgentRow[] { let changed = false; const out = rows.map(r => { if (r.id !== d.sessionId) return r; changed = true; if (d.kind === 'activity') return { ...r, activity: d.activity ?? r.activity }; if (d.kind === 'mode') return { ...r, mode: d.mode ?? r.mode }; if (d.kind === 'telemetry') return { ...r, contextPct: d.contextPct ?? r.contextPct, changedFiles: d.changedFiles ?? r.changedFiles }; return r }); return changed ? out : rows }
export function createFleetStore() {
  const state = writable({ agents: [] as AgentRow[], projects: [] as ProjectStatus[], loading: false, error: null as string | null })
  async function refresh() { state.update(s => ({ ...s, loading: true, error: null })); try { const [a,p] = await Promise.all([listAgents(), listProjects()]); state.set({ agents: a.map(toRow), projects: p, loading: false, error: null }) } catch (e) { state.update(s => ({ ...s, loading: false, error: e instanceof Error ? e.message : String(e) })) } }
  function applyDelta(d: FleetDelta) { state.update(s => ({ ...s, agents: applyDeltaTo(s.agents, d) })) }
  return { state, actions: { refresh, applyDelta } }
}