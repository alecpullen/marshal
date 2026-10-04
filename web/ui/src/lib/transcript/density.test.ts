import { describe, expect, it } from 'vitest'
import { effective, nextGlobal, nextOverride, parseDensity, visible } from './density'

describe('density', () => {
  it('cycles the global ladder', () => {
    expect(nextGlobal('outline')).toBe('steps')
    expect(nextGlobal('steps')).toBe('full')
    expect(nextGlobal('full')).toBe('outline')
  })
  it('cycles a node override', () => {
    expect(nextOverride('steps')).toBe('full')
    expect(nextOverride('full')).toBe('outline')
    expect(nextOverride('outline')).toBe('steps')
  })
  it('falls back to steps for unknown values', () => {
    expect(parseDensity('x')).toBe('steps')
    expect(parseDensity('full')).toBe('full')
  })
  it('inherits an override from the nearest ancestor', () => {
    const parents: Record<string, string> = { tool: 'step', step: 'turn', sib: 'turn' }
    const parentOf = (id: string) => parents[id]
    const overrides = new Map([['step', 'full' as const]])
    expect(effective('tool', overrides, parentOf, 'steps')).toBe('full')
    expect(effective('step', overrides, parentOf, 'steps')).toBe('full')
    expect(effective('sib', overrides, parentOf, 'steps')).toBe('steps')
  })
  it('survives a parent cycle', () => {
    expect(effective('a', new Map(), (id) => (id === 'a' ? 'b' : 'a'), 'outline')).toBe('outline')
  })
  it('hides tool rows at outline only', () => {
    expect(visible('tool', 'outline')).toBe(false)
    expect(visible('tool', 'steps')).toBe(true)
    expect(visible('step', 'outline')).toBe(true)
  })
})
