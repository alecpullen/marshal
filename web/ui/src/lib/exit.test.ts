import { describe, expect, it } from 'vitest'
import { exitDestination } from './exit'

describe('exitDestination', () => {
  it('routes a local agent to merge', () => {
    expect(exitDestination({ sourceKind: 'local', readOnly: false })).toBe('merge')
  })
  it('routes a writable git agent to push', () => {
    expect(exitDestination({ sourceKind: 'git', readOnly: false })).toBe('push')
  })
  it('routes a read-only git agent to patch', () => {
    expect(exitDestination({ sourceKind: 'git', readOnly: true })).toBe('patch')
  })
})
