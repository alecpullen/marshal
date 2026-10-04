import type { BuildStatus, TemplateVersion, WSDoc, WorkspaceListItem } from '../api'

export const STARTERS = [
  { id: 'go-service', label: 'Go service' },
  { id: 'node-app', label: 'Node app' },
  { id: 'python-uv', label: 'Python (uv)' },
  { id: 'rust-crate', label: 'Rust crate' },
  { id: 'minimal', label: 'Minimal' },
] as const

/** The bridge's template-name rule (spec §5.1); checked here so the form can say so before a round trip. */
export const NAME_RE = /^[a-z0-9][a-z0-9-]{0,40}$/

/** Short content chips for a card: the base image, each toolchain and the package count. */
export function contentChips(doc: WSDoc | undefined): string[] {
  if (!doc) return []
  const chips: string[] = []
  if (doc.workspace.base) chips.push(doc.workspace.base)
  chips.push(...doc.workspace.toolchains)
  const p = doc.packages
  const pkgs = p.apt.length + p.go.length + p.npm.length + p.pip.length
  if (pkgs > 0) chips.push(`${pkgs} package${pkgs === 1 ? '' : 's'}`)
  return chips
}

/** The newest version's build state, or null when nothing is published. */
export function latestVersion(item: { versions?: TemplateVersion[]; published?: number }): TemplateVersion | null {
  const vs = item.versions ?? []
  if (vs.length === 0) return null
  return vs.find((v) => v.n === item.published) ?? vs[vs.length - 1]
}

export function buildTone(s: BuildStatus | undefined): 'ok' | 'err' | 'warn' | 'neutral' {
  return s === 'ok' ? 'ok' : s === 'failed' ? 'err' : s === 'building' || s === 'pending' ? 'warn' : 'neutral'
}

/** The reference a spawn body carries: bare name for Studio (latest), `repo:<name>` for a repo template. */
export function workspaceRef(item: Pick<WorkspaceListItem, 'source' | 'name'>): string {
  return item.source === 'repo' ? `repo:${item.name}` : item.name
}

/** Pool badge text ("2 warm"), or '' when no pool is configured. */
export function poolLabel(pool: number | undefined): string {
  return pool && pool > 0 ? `${pool} warm` : ''
}

// The language a gate command's first word needs (plan task 4).
const GATE_LANG: Record<string, string> = { go: 'go', npm: 'node', node: 'node', pytest: 'python', python: 'python', cargo: 'rust' }

/**
 * Whether the toolchains cover the project's gate command. 'unknown' when
 * there is no command or its first word is not in the table, so the UI
 * says nothing rather than guessing.
 */
export function gateRunnable(toolchains: string[], gateCommands: string | undefined | (string | undefined)[]): 'runnable' | 'may-skip' | 'unknown' {
  const cmds = Array.isArray(gateCommands) ? gateCommands : [gateCommands]
  const langs = cmds.map((c) => GATE_LANG[c?.trim().split(/\s+/)[0] ?? '']).filter((l): l is string => !!l)
  if (langs.length === 0) return 'unknown'
  const have = new Set(toolchains.map((t) => t.split('@')[0]))
  return langs.every((l) => have.has(l)) ? 'runnable' : 'may-skip'
}

/** `▦ name@v3`, the tag the session header and Live wall show for an agent's workspace. */
export function workspaceTag(w: { name: string; version: number }): string {
  return `▦ ${w.name}@v${w.version}`
}
