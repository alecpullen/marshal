import { describe, expect, it } from 'vitest'
import { diffTotals, mergeRefusalMessage } from './diff'

describe('diffTotals', () => {
  it('sums files and line counts', () => {
    expect(diffTotals([
      { path: 'a.go', added: 3, removed: 1 },
      { path: 'b.go', added: 0, removed: 7 },
    ])).toEqual({ files: 2, added: 3, removed: 8 })
  })

  it('handles an empty list', () => {
    expect(diffTotals([])).toEqual({ files: 0, added: 0, removed: 0 })
  })
})

describe('mergeRefusalMessage', () => {
  it('explains a dirty worktree and what to do', () => {
    const msg = mergeRefusalMessage({ merged: false, branch: 'f', target: 'main', reason: 'dirty' })
    expect(msg).toMatch(/uncommitted/i)
  })

  it('explains a dirty project checkout', () => {
    const msg = mergeRefusalMessage({ merged: false, branch: 'f', target: 'main', reason: 'project_dirty' })
    expect(msg).toMatch(/project/i)
  })

  it('names the target branch when it moved', () => {
    const msg = mergeRefusalMessage({ merged: false, branch: 'f', target: 'main', reason: 'target_moved' })
    expect(msg).toContain('main')
  })

  it('lists conflicting files', () => {
    const msg = mergeRefusalMessage({
      merged: false, branch: 'f', target: 'main', reason: 'conflicts', conflicts: ['a.go', 'b.go'],
    })
    expect(msg).toContain('a.go')
    expect(msg).toContain('b.go')
  })

  it('falls back for an unrecognised reason', () => {
    expect(mergeRefusalMessage({ merged: false, branch: 'f', target: 'main', reason: 'weird' })).toBeTruthy()
  })

  it('returns empty for a successful merge', () => {
    expect(mergeRefusalMessage({ merged: true, branch: 'f', target: 'main' })).toBe('')
  })
})
