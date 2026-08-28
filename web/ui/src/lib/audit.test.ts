import { describe, expect, it } from 'vitest'
import { describeAuditEvent } from './audit'

describe('describeAuditEvent', () => {
  it('renders a spawn with its origin', () => {
    expect(describeAuditEvent({ event: 'spawn', agentId: 'a1', origin: 'mcp', clientId: 'cc' }))
      .toBe('spawned a1 via mcp (cc)')
  })
  it('renders a gate override with its reason', () => {
    expect(describeAuditEvent({ event: 'gate_override', agentId: 'a1', reason: 'flaky suite' }))
      .toBe('overrode the gate on a1: flaky suite')
  })
  it('falls back to the raw event name rather than dropping the record', () => {
    expect(describeAuditEvent({ event: 'something_new', agentId: 'a1' })).toContain('something_new')
  })
})
