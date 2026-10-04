import { describe, expect, it } from 'vitest'
import { changedLines, diagByLine, lineOfOffset, rangeLines } from './sourceEditor'

const t = 'a\nbb\nccc\n'

describe('lineOfOffset', () => {
  it('counts newlines before the offset, 1-based', () => {
    expect(lineOfOffset(t, 0)).toBe(1)
    expect(lineOfOffset(t, 1)).toBe(1)
    expect(lineOfOffset(t, 2)).toBe(2)
    expect(lineOfOffset(t, 5)).toBe(3)
  })
  it('clamps out-of-range offsets', () => {
    expect(lineOfOffset(t, -4)).toBe(1)
    expect(lineOfOffset(t, 999)).toBe(4)
  })
})

describe('rangeLines', () => {
  it('spans the lines a selection touches', () => {
    expect(rangeLines(t, 0, 4)).toEqual({ start: 1, end: 2 })
    expect(rangeLines(t, 4, 0)).toEqual({ start: 1, end: 2 })
  })
  it('does not include the line a selection merely ends at the start of', () => {
    expect(rangeLines(t, 0, 2)).toEqual({ start: 1, end: 1 })
  })
  it('a collapsed range is one line', () => {
    expect(rangeLines(t, 3, 3)).toEqual({ start: 2, end: 2 })
  })
})

describe('diagByLine', () => {
  it('groups by line with the most severe first', () => {
    const m = diagByLine([
      { line: 2, message: 'w', severity: 'warning' },
      { line: 2, message: 'e', severity: 'error' },
      { line: 5, message: 'x', severity: 'warning' },
    ])
    expect(m.get(2)?.map((d) => d.message)).toEqual(['e', 'w'])
    expect(m.get(5)).toHaveLength(1)
    expect(m.has(3)).toBe(false)
  })
})

describe('changedLines', () => {
  it('reports only the lines that differ', () => {
    expect(changedLines('a\nb\nc', 'a\nB\nc')).toEqual([2])
  })
  it('an insertion flashes only the new lines', () => {
    expect(changedLines('a\nc', 'a\nb\nc')).toEqual([2])
  })
  it('identical text changes nothing', () => {
    expect(changedLines('a\nb', 'a\nb')).toEqual([])
  })
  it('appended lines are reported', () => {
    expect(changedLines('a', 'a\nb\nc')).toEqual([2, 3])
  })
})
