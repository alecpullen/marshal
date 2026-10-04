import type { WSDiag } from '../api'

/** The 1-based line containing `offset`; offsets past the end land on the last line. */
export function lineOfOffset(text: string, offset: number): number {
  const end = Math.max(0, Math.min(offset, text.length))
  let line = 1
  for (let i = 0; i < end; i++) if (text.charCodeAt(i) === 10) line++
  return line
}

/** The 1-based inclusive line span a character range covers; a range ending at a line start does not include that line. */
export function rangeLines(text: string, start: number, end: number): { start: number; end: number } {
  const a = Math.min(start, end)
  const b = Math.max(start, end)
  const first = lineOfOffset(text, a)
  const last = b > a && text[b - 1] === '\n' ? lineOfOffset(text, b - 1) : lineOfOffset(text, b)
  return { start: first, end: last }
}

const RANK: Record<string, number> = { error: 2, warning: 1 }

/** Diagnostics by line; when a line has several, the most severe comes first. */
export function diagByLine(diags: WSDiag[]): Map<number, WSDiag[]> {
  const m = new Map<number, WSDiag[]>()
  for (const d of diags) m.set(d.line, [...(m.get(d.line) ?? []), d])
  for (const [k, v] of m) m.set(k, [...v].sort((x, y) => (RANK[y.severity] ?? 0) - (RANK[x.severity] ?? 0)))
  return m
}

/** Lines present in `next` that differ from the same position in `prev`, as 1-based numbers (for the change flash). */
export function changedLines(prev: string, next: string): number[] {
  const a = prev.split('\n')
  const b = next.split('\n')
  // Trim the common prefix and suffix so an insertion flashes only the inserted lines, not everything after it.
  let lo = 0
  while (lo < a.length && lo < b.length && a[lo] === b[lo]) lo++
  let ha = a.length
  let hb = b.length
  while (ha > lo && hb > lo && a[ha - 1] === b[hb - 1]) {
    ha--
    hb--
  }
  const out: number[] = []
  for (let i = lo; i < hb; i++) out.push(i + 1)
  return out
}
