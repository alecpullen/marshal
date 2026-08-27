import { describe, expect, it } from 'vitest'
import { pendingSummary } from './pending'

describe('pendingSummary', () => {
  it('reports a plan submission by task count', () => {
    const plan = '## Global Constraints\n\n- none\n\n### Task 1: A\n\n### Task 2: B\n'
    expect(pendingSummary({ title: 'x', plan })).toBe('plan · 2 tasks')
  })
  it('reports a prompt submission', () => {
    expect(pendingSummary({ title: 'x', prompt: 'do it' })).toBe('prompt')
  })
})
