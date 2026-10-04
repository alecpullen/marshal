export type Line = { kind: 'ctx' | 'add' | 'del'; old?: number; new?: number; text: string }
export type Hunk = { oldStart: number; oldLines: number; newStart: number; newLines: number; lines: Line[] }
export type FileDiff = { path: string; hunks: Hunk[] }

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

/** Parses `git diff` output into files and hunks. Unknown header lines are skipped. */
export function parseUnified(diff: string): FileDiff[] {
  const files: FileDiff[] = []
  let file: FileDiff | null = null
  let hunk: Hunk | null = null
  let oldPath = ''
  let oldNo = 0
  let newNo = 0
  const begin = () => {
    file = { path: '', hunks: [] }
    files.push(file)
    hunk = null
    oldPath = ''
  }
  const strip = (p: string) => p.replace(/^[ab]\//, '')
  for (const raw of diff.split('\n')) {
    if (raw.startsWith('diff --git ')) {
      begin()
      // Fall back to the b/ path here; the ---/+++ lines refine it.
      const m = /^diff --git a\/(.*) b\/(.*)$/.exec(raw)
      if (m && file) (file as FileDiff).path = m[2]
      continue
    }
    if (!hunk && raw.startsWith('--- ')) {
      if (!file) begin()
      oldPath = raw.slice(4).trim()
      continue
    }
    if (!hunk && raw.startsWith('+++ ')) {
      if (!file) begin()
      const np = raw.slice(4).trim()
      ;(file as unknown as FileDiff).path = np === '/dev/null' ? strip(oldPath) : strip(np)
      continue
    }
    const h = HUNK.exec(raw)
    if (h) {
      if (!file) begin()
      hunk = { oldStart: +h[1], oldLines: h[2] === undefined ? 1 : +h[2], newStart: +h[3], newLines: h[4] === undefined ? 1 : +h[4], lines: [] }
      ;(file as unknown as FileDiff).hunks.push(hunk)
      oldNo = hunk.oldStart
      newNo = hunk.newStart
      continue
    }
    if (!hunk) continue
    const ch = raw[0]
    if (ch === '\\') continue
    if (ch === '+') hunk.lines.push({ kind: 'add', new: newNo++, text: raw.slice(1) })
    else if (ch === '-') hunk.lines.push({ kind: 'del', old: oldNo++, text: raw.slice(1) })
    else if (ch === ' ') hunk.lines.push({ kind: 'ctx', old: oldNo++, new: newNo++, text: raw.slice(1) })
  }
  return files.filter((f) => f.path)
}

/** Side-by-side rows: each run of deletions pairs with the additions that follow it. */
export function toSplit(h: Hunk): { left?: Line; right?: Line }[] {
  const rows: { left?: Line; right?: Line }[] = []
  let i = 0
  while (i < h.lines.length) {
    const l = h.lines[i]
    if (l.kind === 'ctx') {
      rows.push({ left: l, right: l })
      i++
      continue
    }
    const dels: Line[] = []
    const adds: Line[] = []
    while (i < h.lines.length && h.lines[i].kind === 'del') dels.push(h.lines[i++])
    while (i < h.lines.length && h.lines[i].kind === 'add') adds.push(h.lines[i++])
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) rows.push({ left: dels[k], right: adds[k] })
  }
  return rows
}

/** FNV-1a over a file's hunks; changes whenever the diff does. */
export function fileHash(f: FileDiff): string {
  let h = 0x811c9dc5
  const feed = (s: string) => {
    for (let i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i)
      h = Math.imul(h, 0x01000193) >>> 0
    }
  }
  for (const hk of f.hunks) {
    feed(`@@${hk.oldStart},${hk.oldLines},${hk.newStart},${hk.newLines}\n`)
    for (const l of hk.lines) feed(`${l.kind[0]}${l.text}\n`)
  }
  return h.toString(16).padStart(8, '0')
}

export function viewedKey(agentId: string, path: string): string {
  return `marshal.review.viewed.${agentId}.${path}`
}

export function isViewed(agentId: string, f: FileDiff): boolean {
  try {
    return localStorage.getItem(viewedKey(agentId, f.path)) === fileHash(f)
  } catch {
    return false
  }
}

export function setViewed(agentId: string, f: FileDiff, on: boolean): void {
  try {
    if (on) localStorage.setItem(viewedKey(agentId, f.path), fileHash(f))
    else localStorage.removeItem(viewedKey(agentId, f.path))
  } catch {
    // Per-browser convenience only.
  }
}

export const hunkKey = (stepNode: string, path: string, newStart: number) => `${stepNode}|${path}|${newStart}`

/**
 * Maps a hunk key to the later step that rewrote it: a later step has a hunk
 * in the same file whose old range overlaps this hunk's new range.
 * Steps are in chronological order.
 */
export function rewrittenBy(steps: { stepNode: string; files: FileDiff[] }[]): Map<string, string> {
  const out = new Map<string, string>()
  steps.forEach((s, i) => {
    for (const f of s.files) {
      for (const h of f.hunks) {
        const aStart = h.newStart
        const aEnd = h.newStart + Math.max(h.newLines, 1)
        search: for (const later of steps.slice(i + 1)) {
          for (const lf of later.files) {
            if (lf.path !== f.path) continue
            for (const lh of lf.hunks) {
              const bStart = lh.oldStart
              const bEnd = lh.oldStart + Math.max(lh.oldLines, 1)
              if (bStart < aEnd && aStart < bEnd) {
                out.set(hunkKey(s.stepNode, f.path, h.newStart), later.stepNode)
                break search
              }
            }
          }
        }
      }
    }
  })
  return out
}
