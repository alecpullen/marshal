import type { GateRecord } from '../api'

/**
 * The better of two records for one agent: the later run wins, and for the
 * same run the one that carries the verify output (the fleet stream's copy
 * leaves it out).
 */
export function pickGate(a?: GateRecord | null, b?: GateRecord | null): GateRecord | null {
  if (!a || !a.result) return b?.result ? b : (a ?? b ?? null)
  if (!b || !b.result) return a
  const ta = Date.parse(a.at)
  const tb = Date.parse(b.at)
  if (ta !== tb && !Number.isNaN(ta) && !Number.isNaN(tb)) return ta > tb ? a : b
  return !a.result.output && b.result.output ? b : a
}

export type GateState = 'passed' | 'failed' | 'skipped' | 'none'

export function gateState(g?: GateRecord | null): GateState {
  const r = g?.result
  if (!r) return 'none'
  if (r.skipped) return 'skipped'
  return r.ok ? 'passed' : 'failed'
}

/** "just now", "5m ago": the age of a record. */
export function ago(iso: string, now = Date.now()): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 10) return 'just now'
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h ago` : `${Math.floor(h / 24)}d ago`
}
