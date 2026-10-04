import { describe, expect, it } from 'vitest'
import { ago, gateState, pickGate } from './gate'

const rec = (at: string, o: Partial<{ ok: boolean; skipped: boolean; output: string }> = {}) => ({
  result: { ok: false, skipped: false, ...o },
  at,
})

describe('gate helpers', () => {
  it('picks the later run, and the one with output for the same run', () => {
    const old = rec('2026-10-04T00:00:00Z', { output: 'old' })
    const fresh = rec('2026-10-04T00:05:00Z')
    expect(pickGate(old, fresh)).toBe(fresh)
    const slim = rec('2026-10-04T00:05:00Z')
    const full = rec('2026-10-04T00:05:00Z', { output: 'boom' })
    expect(pickGate(slim, full)).toBe(full)
    expect(pickGate(null, full)).toBe(full)
    expect(pickGate(null, null)).toBeNull()
  })

  it('classifies a record', () => {
    expect(gateState(null)).toBe('none')
    expect(gateState(rec('x', { ok: true }))).toBe('passed')
    expect(gateState(rec('x', { skipped: true }))).toBe('skipped')
    expect(gateState(rec('x'))).toBe('failed')
  })

  it('words the age', () => {
    const now = Date.parse('2026-10-04T01:00:00Z')
    expect(ago('2026-10-04T00:59:58Z', now)).toBe('just now')
    expect(ago('2026-10-04T00:55:00Z', now)).toBe('5m ago')
    expect(ago('2026-10-03T22:00:00Z', now)).toBe('3h ago')
  })
})
