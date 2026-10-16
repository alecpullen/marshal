import { describe, expect, it } from 'vitest'
import { subjectFirst } from './format'

describe('subjectFirst', () => {
  it('leads with the subject for the symbols tool', () => {
    expect(subjectFirst('symbols')).toBe(true)
  })

  it('keeps the retired symbols.find name for persisted transcripts', () => {
    expect(subjectFirst('symbols.find')).toBe(true)
  })

  it('is false for a verb-first tool', () => {
    expect(subjectFirst('repo.search')).toBe(false)
  })
})
