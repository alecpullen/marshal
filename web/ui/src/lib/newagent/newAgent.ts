import type { ProjectStatus, RoutingChoice } from '../api'

/** The ACP mode set. store.ts's MODES also lists `read`, which a new agent cannot start in. */
export const MODES = ['plan', 'default', 'edit', 'copilot', 'auto'] as const
export type NewMode = (typeof MODES)[number]

export interface Choice {
  project: string
  mode: NewMode
  isolated: boolean
  branch: string
  baseRef: string
}

const KEY = 'marshal.ui.newagent'

export interface Remembered { project?: string; mode?: string; isolated?: boolean; model?: ModelChoice }

export const canIsolate = (p?: ProjectStatus) => (p?.isolation ?? 'available') === 'available'

/** The starting choice: remembered values where still valid, otherwise the first available project. */
export function defaults(projects: ProjectStatus[], remembered: Remembered = {}): Choice {
  const usable = projects.filter((p) => p.available)
  const project = usable.find((p) => p.root === remembered.project) ?? usable[0]
  const mode = (MODES as readonly string[]).includes(remembered.mode ?? '') ? (remembered.mode as NewMode) : 'edit'
  const isolated = canIsolate(project) && (remembered.isolated ?? true)
  return { project: project?.root ?? '', mode, isolated, branch: '', baseRef: '' }
}

export function remember(c: Choice, model?: ModelChoice): void {
  try {
    localStorage.setItem(KEY, JSON.stringify({ project: c.project, mode: c.mode, isolated: c.isolated, ...(model ? { model } : {}) }))
  } catch {
    // Not persisted; the next visit starts from the defaults.
  }
}

export function loadRemembered(): Remembered {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? (JSON.parse(raw) as Remembered) : {}
  } catch {
    return {}
  }
}

const WS_KEY = 'marshal.ui.newagent.workspace'

/** The workspace last chosen for a project, kept apart from the main choice because it is per project. */
export function loadWorkspace(project: string): string {
  try {
    const m = JSON.parse(localStorage.getItem(WS_KEY) ?? '{}') as Record<string, string>
    return m[project] ?? ''
  } catch {
    return ''
  }
}

export function rememberWorkspace(project: string, ref: string): void {
  try {
    const m = JSON.parse(localStorage.getItem(WS_KEY) ?? '{}') as Record<string, string>
    if (ref) m[project] = ref
    else delete m[project]
    localStorage.setItem(WS_KEY, JSON.stringify(m))
  } catch {
    // Not persisted; the project default applies next time.
  }
}

/**
 * The roles that resolve through the cheap fast binding, so one chosen preset
 * leaves them alone. Mirrors `routing.FastRoles`
 * (internal/llm/routing/types.go:50): keep the two in sync.
 */
export const FAST_ROLES: readonly string[] = ['router', 'title', 'summarizer', 'repo_scout']

/** What the Model chip picks: the server default, a routing profile, or one preset for the main roles. */
export type ModelChoice = { kind: 'default' } | { kind: 'profile'; name: string } | { kind: 'preset'; name: string }

export const DEFAULT_MODEL: ModelChoice = { kind: 'default' }

/**
 * The spawn request's `routing`, or undefined for the default (which sends
 * nothing). A preset becomes an override on every role except the fast ones.
 */
export function routingFor(choice: ModelChoice, roles: readonly string[]): RoutingChoice | undefined {
  if (choice.kind === 'profile') return { profile: choice.name }
  if (choice.kind === 'preset') {
    const overrides = Object.fromEntries(roles.filter((r) => !FAST_ROLES.includes(r)).map((r) => [r, choice.name]))
    return { overrides }
  }
  return undefined
}

/** A remembered choice, or the default when its profile or preset no longer exists. */
export function validModel(choice: ModelChoice | undefined, cfg: { profiles: Record<string, unknown>; presets: Record<string, unknown> } | null): ModelChoice {
  if (!choice || !cfg) return DEFAULT_MODEL
  if (choice.kind === 'profile' && choice.name in cfg.profiles) return choice
  if (choice.kind === 'preset' && choice.name in cfg.presets) return choice
  return DEFAULT_MODEL
}

export function modelLabel(choice: ModelChoice): string {
  return choice.kind === 'default' ? 'Default profile' : choice.kind === 'profile' ? `${choice.name} profile` : `${choice.name} preset`
}
