import type { ProjectStatus } from '../api'

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

export interface Remembered { project?: string; mode?: string; isolated?: boolean }

export const canIsolate = (p?: ProjectStatus) => (p?.isolation ?? 'available') === 'available'

/** The starting choice: remembered values where still valid, otherwise the first available project. */
export function defaults(projects: ProjectStatus[], remembered: Remembered = {}): Choice {
  const usable = projects.filter((p) => p.available)
  const project = usable.find((p) => p.root === remembered.project) ?? usable[0]
  const mode = (MODES as readonly string[]).includes(remembered.mode ?? '') ? (remembered.mode as NewMode) : 'edit'
  const isolated = canIsolate(project) && (remembered.isolated ?? true)
  return { project: project?.root ?? '', mode, isolated, branch: '', baseRef: '' }
}

export function remember(c: Choice): void {
  try {
    localStorage.setItem(KEY, JSON.stringify({ project: c.project, mode: c.mode, isolated: c.isolated }))
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
