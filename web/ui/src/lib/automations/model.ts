import type { CIHistory, Finding, RepoRow } from '../api'
import type { Tone } from '../glyphs'

export const SEVERITIES = ['blocking', 'should-fix', 'nit'] as const

const rank = (s: string) => {
  const i = (SEVERITIES as readonly string[]).indexOf(s)
  return i < 0 ? SEVERITIES.length : i
}

export function severityTone(s: string): Tone {
  return s === 'blocking' ? 'err' : s === 'should-fix' ? 'warn' : 'neutral'
}

/** Findings grouped by severity, most severe first; severities the bridge invents sort last. */
export function groupBySeverity(findings: Finding[]): { severity: string; findings: Finding[] }[] {
  const by = new Map<string, Finding[]>()
  for (const f of findings) by.set(f.severity, [...(by.get(f.severity) ?? []), f])
  return [...by.entries()].sort((a, b) => rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0])).map(([severity, findings]) => ({ severity, findings }))
}

export const severityCounts = (findings: Finding[]) => groupBySeverity(findings).map((g) => ({ severity: g.severity, count: g.findings.length }))

/** The PR number at the end of a forge PR URL (`…/pull/12`, `…/pulls/12`), or undefined. */
export function prNumber(url?: string): number | undefined {
  const m = /\/(?:pulls?|pr)\/(\d+)(?:[/?#].*)?$/.exec(url ?? '')
  return m ? Number(m[1]) : undefined
}

/** `https://host/owner/name` for an https or scp-style git URL, or null. */
function webBase(url: string): string | null {
  const scp = /^[\w.-]+@([^:/]+):(.+?)(?:\.git)?\/?$/.exec(url)
  if (scp) return `https://${scp[1]}/${scp[2]}`
  const m = /^(https?|ssh|git):\/\/(?:[^@/]+@)?([^/]+)\/(.+?)(?:\.git)?\/?$/.exec(url)
  if (!m) return null
  // An ssh port is not the web port.
  return `https://${m[1].startsWith('http') ? m[2] : m[2].replace(/:\d+$/, '')}/${m[3]}`
}

/** The registered repo a PR URL belongs to, or undefined when none of them matches. */
export function repoForPR(repos: Pick<RepoRow, 'id' | 'url'>[], prUrl?: string): string | undefined {
  const lower = (prUrl ?? '').toLowerCase()
  return repos.find((r) => {
    const base = webBase(r.url)
    return base && lower.startsWith(base.toLowerCase() + '/')
  })?.id
}

/** A link to `path` (and `line`) at `sha` on the forge, or null when the repo URL cannot be read. */
export function forgeFileUrl(repo: Pick<RepoRow, 'url' | 'forge'> | undefined, sha: string, path: string, line?: number): string | null {
  const base = repo ? webBase(repo.url) : null
  if (!base || !sha) return null
  const where = repo?.forge === 'gitea' ? `src/commit/${sha}` : `blob/${sha}`
  return `${base}/${where}/${path.split('/').map(encodeURIComponent).join('/')}${line ? `#L${line}` : ''}`
}

/** `fixed` is ok, `didn't reproduce` is neutral, anything that gave up is an error. */
export function ciTone(status: string): Tone {
  return status === 'fixed' ? 'ok' : status.startsWith('gave up') ? 'err' : 'neutral'
}

const DAY = 24 * 3600 * 1000
/** Results for the inbox: fixes that opened a PR within the last day. One with no timestamp is not shown; the history table still has it. */
export function recentFixes(history: CIHistory[], now: number): CIHistory[] {
  return history.filter((h) => h.status === 'fixed' && h.prUrl && h.createdAt && now - Date.parse(h.createdAt) < DAY)
}
