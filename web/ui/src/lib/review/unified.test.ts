import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fileHash, isViewed, parseUnified, rewrittenBy, setViewed, toSplit, type FileDiff, type Hunk } from './unified'

const DIFF = `diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 package a
-var x = 1
+var x = 2
+var y = 3
 func f() {}
\\ No newline at end of file
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+hello
+world
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`

describe('parseUnified', () => {
  it('parses files, hunks and line numbers', () => {
    const f = parseUnified(DIFF)
    expect(f.map((x) => x.path)).toEqual(['a.go', 'new.txt', 'gone.txt'])
    const h = f[0].hunks[0]
    expect([h.oldStart, h.oldLines, h.newStart, h.newLines]).toEqual([1, 3, 1, 4])
    expect(h.lines.map((l) => l.kind)).toEqual(['ctx', 'del', 'add', 'add', 'ctx'])
    expect(h.lines[1]).toMatchObject({ old: 2, text: 'var x = 1' })
    expect(h.lines[3]).toMatchObject({ new: 3, text: 'var y = 3' })
    expect(h.lines[4]).toMatchObject({ old: 3, new: 4 })
    expect(f[1].hunks[0].lines.every((l) => l.kind === 'add')).toBe(true)
    expect(f[2].hunks[0].lines[0]).toMatchObject({ kind: 'del', old: 1 })
  })
  it('returns nothing for empty input', () => {
    expect(parseUnified('')).toEqual([])
  })
})

describe('toSplit', () => {
  it('pairs uneven runs row by row', () => {
    const h = parseUnified(DIFF)[0].hunks[0]
    const rows = toSplit(h)
    expect(rows).toHaveLength(4)
    expect(rows[0].left).toBe(rows[0].right)
    expect(rows[1].left?.text).toBe('var x = 1')
    expect(rows[1].right?.text).toBe('var x = 2')
    expect(rows[2].left).toBeUndefined()
    expect(rows[2].right?.text).toBe('var y = 3')
  })
})

describe('viewed marks', () => {
  const store = new Map<string, string>()
  beforeEach(() => {
    store.clear()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('resets when the file diff changes', () => {
    const f = parseUnified(DIFF)[0]
    expect(isViewed('a1', f)).toBe(false)
    setViewed('a1', f, true)
    expect(isViewed('a1', f)).toBe(true)
    const changed: FileDiff = { ...f, hunks: [{ ...f.hunks[0], lines: [...f.hunks[0].lines, { kind: 'add', text: 'more' }] }] }
    expect(fileHash(changed)).not.toBe(fileHash(f))
    expect(isViewed('a1', changed)).toBe(false)
    setViewed('a1', f, false)
    expect(isViewed('a1', f)).toBe(false)
  })

  it('survives a throwing localStorage', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('blocked')
      },
      setItem: () => {
        throw new Error('blocked')
      },
    })
    const f = parseUnified(DIFF)[0]
    expect(() => setViewed('a1', f, true)).not.toThrow()
    expect(isViewed('a1', f)).toBe(false)
  })
})

describe('rewrittenBy', () => {
  const file = (path: string, h: Partial<Hunk>): FileDiff => ({
    path,
    hunks: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [], ...h }],
  })
  it('flags a hunk a later step overlaps', () => {
    const m = rewrittenBy([
      { stepNode: 's1', files: [file('a.go', { newStart: 10, newLines: 5 })] },
      { stepNode: 's2', files: [file('a.go', { oldStart: 12, oldLines: 2 })] },
    ])
    expect(m.get('s1|a.go|10')).toBe('s2')
    expect(m.size).toBe(1)
  })
  it('ignores adjacent, other-file and earlier hunks', () => {
    const m = rewrittenBy([
      { stepNode: 's1', files: [file('a.go', { newStart: 10, newLines: 5 })] },
      { stepNode: 's2', files: [file('a.go', { oldStart: 15, oldLines: 2 }), file('b.go', { oldStart: 10, oldLines: 5 })] },
    ])
    expect(m.size).toBe(0)
  })
})
