import { describe, expect, it } from 'vitest'
import { score } from './palette'

describe('score', () => {
  it('ranks exact > prefix > word-start > scattered > none', () => {
    const exact = score('home', 'home')
    const prefix = score('hom', 'home')
    const wordStart = score('ho', 'the home')
    const scattered = score('oe', 'home')
    const none = score('xyz', 'home')
    expect(exact).toBeGreaterThan(prefix)
    expect(prefix).toBeGreaterThan(wordStart)
    expect(wordStart).toBeGreaterThan(scattered)
    expect(scattered).toBeGreaterThan(none)
    expect(none).toBe(0)
  })
  it('is case-insensitive and treats an empty query as a weak match', () => {
    expect(score('HOME', 'home')).toBeGreaterThan(0)
    expect(score('', 'anything')).toBeGreaterThan(0)
  })
  it('requires every query char in order', () => {
    expect(score('emoh', 'home')).toBe(0)
  })
})
