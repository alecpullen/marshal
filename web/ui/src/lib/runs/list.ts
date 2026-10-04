import type { RunRow } from '../api'
import type { AgentRow } from '../fleet'

/**
 * The listed runs, kept fresh by the fleet: a row's digest is replaced by
 * the agent's when the fleet saw it later, and an agent whose run started
 * after the list loaded is added. Newest first.
 */
export function mergeRuns(listed: RunRow[], agents: AgentRow[]): RunRow[] {
  const byId = new Map(agents.map((a) => [a.id, a]))
  const out = new Map<string, RunRow>()
  for (const r of listed) {
    const a = byId.get(r.agentId)
    const fresher = a?.run && (a.runAt ?? 0) >= (r.at ?? 0)
    out.set(r.agentId, {
      ...r,
      name: a?.name || r.name,
      project: a?.project ?? r.project,
      run: fresher ? a!.run! : r.run,
      at: fresher ? (a!.runAt ?? r.at) : r.at,
    })
  }
  for (const a of agents) {
    if (a.run && a.run.kind !== 'none' && !out.has(a.id)) out.set(a.id, { agentId: a.id, name: a.name, project: a.project, run: a.run, at: a.runAt })
  }
  return [...out.values()].filter((r) => r.run.kind !== 'none').sort((a, b) => (b.at ?? 0) - (a.at ?? 0))
}
