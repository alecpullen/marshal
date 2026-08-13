import { describe, expect, it } from 'vitest'
import { cn } from './utils'

describe('cn', () => {
  it('joins class names', () => {
    expect(cn('a', 'b')).toBe('a b')
  })

  it('drops falsy values', () => {
    expect(cn('a', false && 'b', undefined, null, 'c')).toBe('a c')
  })

  it('lets a later tailwind utility win over an earlier conflicting one', () => {
    expect(cn('p-2', 'p-4')).toBe('p-4')
  })

  it('keeps non-conflicting utilities', () => {
    expect(cn('p-2', 'm-4')).toBe('p-2 m-4')
  })

  it('supports conditional object syntax', () => {
    expect(cn({ 'is-on': true, 'is-off': false })).toBe('is-on')
  })

  it('flattens arrays', () => {
    expect(cn(['a', 'b'], 'c')).toBe('a b c')
  })

  it('returns an empty string for no input', () => {
    expect(cn()).toBe('')
  })
})
