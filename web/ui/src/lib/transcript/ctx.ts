import type { WireNode } from '../stack'
import type { Density } from './density'

/** What every node renderer needs to know about the transcript around it. */
export interface TranscriptCtx {
  nodes: Map<string, WireNode>
  global: Density
  overrides: ReadonlyMap<string, Density>
  cursor: string | null
  /** Wall clock in ms, ticked while anything is live. */
  now: number
  onToggleDensity?: (id: string) => void
}
