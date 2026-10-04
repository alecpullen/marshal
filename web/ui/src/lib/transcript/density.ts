export type Density = 'outline' | 'steps' | 'full'

/** The global ladder: outline → steps → full → outline (TUI Ctrl+G). */
export function nextGlobal(d: Density): Density {
  return d === 'outline' ? 'steps' : d === 'steps' ? 'full' : 'outline'
}

/** One node's Enter cycle: steps → full → outline → steps. */
export function nextOverride(d: Density): Density {
  return d === 'steps' ? 'full' : d === 'full' ? 'outline' : 'steps'
}

export function parseDensity(v: unknown): Density {
  return v === 'outline' || v === 'full' ? v : 'steps'
}

/**
 * A node without its own override inherits its parent's effective level, so
 * overriding a step reaches its tool rows but never its siblings.
 */
export function effective(
  nodeId: string,
  overrides: ReadonlyMap<string, Density>,
  parentOf: (id: string) => string | undefined,
  global: Density,
): Density {
  const seen = new Set<string>()
  let id: string | undefined = nodeId
  while (id !== undefined && !seen.has(id)) {
    seen.add(id)
    const o = overrides.get(id)
    if (o) return o
    id = parentOf(id)
  }
  return global
}

/** At outline a step is one row: its tool rows are hidden. */
export function visible(kind: string, density: Density): boolean {
  return !(density === 'outline' && kind === 'tool')
}
