import type { ProjectStatus, RoutingChoice } from '../api'
import { migrateStoredDefaults, MODES, type Mode } from '../interaction'

/**
 * The two interaction modes. Auto is a modifier, not a third mode, so a
 * remembered or configured "auto" migrates to Act + Auto rather than lingering
 * as a mode of its own.
 */
export { MODES }
export type NewMode = Mode

export interface Choice {
  project: string
  mode: Mode
  /** The starting Auto modifier. Valid only with Act; Ask clears it. */
  auto: boolean
  isolated: boolean
  branch: string
  baseRef: string
}

const KEY = 'marshal.ui.newagent'

export interface Remembered { project?: string; mode?: string; auto?: boolean; isolated?: boolean; model?: ModelChoice }

export const canIsolate = (p?: ProjectStatus) => (p?.isolation ?? 'available') === 'available'

/**
 * The starting choice: remembered values where still valid, otherwise a fresh
 * Act with Auto off. A remembered spelling the ask/act model cannot read is
 * rejected rather than promoted to Act as if it were a working default;
 * `rememberedReset` tells the form to offer clearing it.
 */
export function defaults(projects: ProjectStatus[], remembered: Remembered = {}): Choice {
  const usable = projects.filter((p) => p.available)
  const project = usable.find((p) => p.root === remembered.project) ?? usable[0]
  const migrated = migrateStoredDefaults({ mode: remembered.mode, auto: remembered.auto })
  const mode = migrated?.mode ?? 'act'
  const auto = mode === 'act' ? migrated?.auto ?? false : false
  const isolated = canIsolate(project) && (remembered.isolated ?? true)
  return { project: project?.root ?? '', mode, auto, isolated, branch: '', baseRef: '' }
}

/** Whether the remembered starting tuple was unreadable and was therefore reset. */
export function rememberedReset(remembered: Remembered): boolean {
  if (remembered.mode === undefined && remembered.auto === undefined) return false
  return migrateStoredDefaults({ mode: remembered.mode, auto: remembered.auto }) === null
}

/** Forget the remembered starting defaults so the next visit starts fresh. */
export function clearRemembered(): void {
  try {
    localStorage.removeItem(KEY)
  } catch {
    // Not persisted.
  }
}

export function remember(c: Choice, model?: ModelChoice): void {
  try {
    // Starting metadata only: the mode and Auto this agent was asked to start
    // with. System and the access revision are live grants and are never stored.
    localStorage.setItem(
      KEY,
      JSON.stringify({ project: c.project, mode: c.mode, auto: c.auto, isolated: c.isolated, ...(model ? { model } : {}) }),
    )
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

/** The roles a preset applies to: every role except the fast ones. */
export const overridableRoles = (roles: readonly string[]): string[] => roles.filter((r) => !FAST_ROLES.includes(r))

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
    // No overridable roles means nothing would be sent, which the server treats as the default profile.
    const overrides = Object.fromEntries(overridableRoles(roles).map((r) => [r, choice.name]))
    return Object.keys(overrides).length > 0 ? { overrides } : undefined
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
