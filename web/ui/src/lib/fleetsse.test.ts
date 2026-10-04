import { describe, expect, it } from 'vitest'
import { parseFleetEvent } from './sse'

describe('parseFleetEvent', () => {
  it('parses an activity delta', () => {
    expect(parseFleetEvent('{"kind":"activity","sessionId":"s1","activity":"file.read"}')).toEqual({
      kind: 'activity',
      sessionId: 's1',
      activity: 'file.read',
    })
  })

  it('parses a pending delta so the card can flip before the refetch', () => {
    expect(parseFleetEvent('{"kind":"pending","sessionId":"s1","pendingKind":"approval"}')).toEqual({
      kind: 'pending',
      sessionId: 's1',
      pendingKind: 'approval',
    })
  })

  it('parses a project_removed delta, which carries no sessionId', () => {
    expect(parseFleetEvent('{"kind":"project_removed","project":"/gone"}')).toEqual({
      kind: 'project_removed',
      project: '/gone',
    })
  })

  it('recognises the replay overflow nudge', () => {
    expect(parseFleetEvent('{"type":"replay_overflow"}')).toBe('overflow')
  })

  it('returns null for malformed json rather than throwing', () => {
    expect(parseFleetEvent('{nope')).toBeNull()
  })

  it('returns null when kind is missing', () => {
    expect(parseFleetEvent('{"sessionId":"s1"}')).toBeNull()
  })

  it('returns null when sessionId is missing on a session-scoped delta', () => {
    expect(parseFleetEvent('{"kind":"activity"}')).toBeNull()
  })

  it('returns null for a project_removed delta with no project', () => {
    expect(parseFleetEvent('{"kind":"project_removed"}')).toBeNull()
  })

  it('returns null for a JSON scalar', () => {
    expect(parseFleetEvent('42')).toBeNull()
  })

  it('parses fleet-wide budget and reroute deltas that carry no session', () => {
    expect(parseFleetEvent('{"kind":"budget","scope":"daily","spentUsd":1,"capUsd":2,"action":"warn"}')).toMatchObject({ kind: 'budget' })
    expect(parseFleetEvent('{"kind":"reroute","id":"r1","watch":"w","role":"x","from":"a","to":"b"}')).toMatchObject({ kind: 'reroute', id: 'r1' })
  })

  it('addresses a run delta by agentId when no sessionId is given', () => {
    expect(parseFleetEvent('{"kind":"run","agentId":"a1","run":{"kind":"none"}}')).toMatchObject({ kind: 'run', sessionId: 'a1' })
  })
})
