import type { ProjectHealth, ProjectSettings } from '../api'
import type { AgentRow } from '../fleet'

export type Dot = 'ok' | 'warn' | 'err' | 'unknown'
export interface HealthCheck { id: string; label: string; dot: Dot; detail: string }

const age = (s: number) => (s < 90 ? `${s}s` : s < 5400 ? `${Math.round(s / 60)}m` : s < 172800 ? `${Math.round(s / 3600)}h` : `${Math.round(s / 86400)}d`)
// A mirror fetched within the last hour counts as fresh.
const MIRROR_FRESH_SECONDS = 3600

/** The §7 checks as rows with a status dot; absent checks (no repos, no default workspace) are left out. */
export function healthChecks(h: ProjectHealth): HealthCheck[] {
  const out: HealthCheck[] = [
    {
      id: 'gate',
      label: 'Verify gate',
      dot: h.gateRunnable === 'yes' ? 'ok' : h.gateRunnable === 'no' ? 'err' : 'unknown',
      detail: h.gateRunnable === 'yes' ? 'Runs on this project' : h.gateRunnable === 'no' ? 'The last verify was skipped: nothing to run' : 'No agent has verified yet',
    },
  ]
  for (const m of h.mirrorFresh ?? []) {
    out.push({
      id: `mirror:${m.repoId}`,
      label: `Mirror ${m.repoId}`,
      dot: !m.present ? 'err' : (m.ageSeconds ?? 0) <= MIRROR_FRESH_SECONDS ? 'ok' : 'warn',
      detail: !m.present ? 'No mirror yet' : `Fetched ${age(m.ageSeconds ?? 0)} ago${m.head ? ` · ${m.head}` : ''}`,
    })
  }
  const orphans = h.orphanWorktrees ?? []
  out.push({
    id: 'orphans',
    label: 'Orphan worktrees',
    dot: orphans.length === 0 ? 'ok' : 'warn',
    detail: orphans.length === 0 ? 'None' : `${orphans.length}: ${orphans.join(', ')}`,
  })
  out.push({
    id: 'trust',
    label: 'Folder trust',
    dot: h.trust === 'trusted' ? 'ok' : h.trust === 'untrusted' ? 'warn' : 'unknown',
    detail: h.trust === 'na' ? 'Not applicable' : h.trust,
  })
  const w = h.workspaceResolves
  if (w) {
    out.push({
      id: 'workspace',
      label: 'Default workspace',
      dot: !w.resolves ? 'err' : w.built ? 'ok' : 'warn',
      detail: !w.resolves ? (w.error ?? 'Does not resolve') : w.built ? `${w.ref} resolves and is built` : `${w.ref} resolves, not built yet${w.error ? `: ${w.error}` : ''}`,
    })
  }
  return out
}

/** A workspace reference is `name[@version]` for a Studio template or `repo:name`. */
export function parseWorkspaceRef(ref: string): { source: 'studio' | 'repo'; name: string; version?: number } {
  if (ref.startsWith('repo:')) return { source: 'repo', name: ref.slice(5) }
  const [name, v] = ref.split('@')
  const version = v ? Number.parseInt(v, 10) : NaN
  return { source: 'studio', name, ...(Number.isFinite(version) && version > 0 ? { version } : {}) }
}

/** The agent of `root` touched most recently, or undefined when there is none. */
export function latestAgent(agents: AgentRow[], root: string): AgentRow | undefined {
  return agents.filter((a) => a.project === root).sort((a, b) => b.updatedAt.localeCompare(a.updatedAt) || a.id.localeCompare(b.id))[0]
}

export const MODES = ['plan', 'default', 'edit', 'copilot', 'auto'] as const
export const SHIP_TARGETS = ['merge', 'push', 'patch'] as const

/** `isolated` is a tri-state: unset leaves the caller's default. */
export type IsolationChoice = 'default' | 'isolated' | 'shared'
export const isolationOf = (s: Pick<ProjectSettings, 'isolated'>): IsolationChoice => (s.isolated === true ? 'isolated' : s.isolated === false ? 'shared' : 'default')
export const isolatedFrom = (c: IsolationChoice): boolean | undefined => (c === 'isolated' ? true : c === 'shared' ? false : undefined)

/** A picker's data is optional context: a failed load gives the fallback instead of failing the page. */
export function soft<T>(p: Promise<T>, fallback: T): Promise<T> {
  return p.catch(() => fallback)
}
