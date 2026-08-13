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
})
