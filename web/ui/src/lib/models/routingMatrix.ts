import type { Binding, ModelsConfig } from '../api'

export interface MatrixCell { profile: string; preset?: string; customAgent?: string }
export interface MatrixRow { role: string; cells: MatrixCell[] }
export interface Matrix { profiles: string[]; rows: MatrixRow[] }

type RoutingCfg = Pick<ModelsConfig, 'profiles' | 'roles' | 'defaultProfile'>

/** Profile names, the default first, then alphabetical. */
function profileNames(cfg: RoutingCfg): string[] {
  const names = Object.keys(cfg.profiles).sort()
  return cfg.defaultProfile && names.includes(cfg.defaultProfile)
    ? [cfg.defaultProfile, ...names.filter((n) => n !== cfg.defaultProfile)]
    : names
}

/** Roles down the side, profiles across; an unbound cell has neither field. */
export function matrix(cfg: RoutingCfg): Matrix {
  const profiles = profileNames(cfg)
  const rows = cfg.roles.map((role) => ({
    role,
    cells: profiles.map((profile) => {
      const b = cfg.profiles[profile]?.[role]
      return { profile, preset: b?.preset || undefined, customAgent: b?.customAgent || undefined }
    }),
  }))
  return { profiles, rows }
}

/**
 * The `profiles` body for `PUT /api/models/routing` with one cell changed.
 * The server replaces the whole map, so every profile is carried over, and
 * none is removed, which keeps the default profile valid. An empty preset
 * clears the binding. `cfg` is not modified.
 */
export function setCell(cfg: Pick<ModelsConfig, 'profiles'>, profile: string, role: string, preset: string): Record<string, Record<string, Binding>> {
  const next: Record<string, Record<string, Binding>> = {}
  for (const [name, roles] of Object.entries(cfg.profiles)) next[name] = { ...roles }
  const roles = { ...(next[profile] ?? {}) }
  if (preset) roles[role] = { preset }
  else delete roles[role]
  next[profile] = roles
  return next
}
