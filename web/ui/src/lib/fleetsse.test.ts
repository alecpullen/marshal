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

  it('parses a budget delta, nested as the bridge sends it or flat', () => {
    const flat = { kind: 'budget', scope: 'daily', spentUsd: 1, capUsd: 2, action: 'warn' }
    expect(parseFleetEvent(JSON.stringify(flat))).toEqual(flat)
    expect(parseFleetEvent('{"kind":"budget","sessionId":"","budget":{"scope":"agent","agentId":"a1","spentUsd":3,"capUsd":2,"action":"pause"}}')).toEqual({
      kind: 'budget',
      scope: 'agent',
      agentId: 'a1',
      spentUsd: 3,
      capUsd: 2,
      action: 'pause',
    })
    expect(parseFleetEvent('{"kind":"budget","budget":{"scope":"hourly"}}')).toBeNull()
  })

  it('parses the bridge reroute delta, labelling bindings', () => {
    expect(
      parseFleetEvent('{"kind":"reroute","sessionId":"","reroute":{"id":"r1","watchId":"w1","watch":"cost","role":"reviewer","profile":"p","from":{"Preset":"big"},"to":"small","at":"2026-10-04T00:00:00Z"}}'),
    ).toEqual({ kind: 'reroute', id: 'r1', watch: 'cost', role: 'reviewer', from: 'big', to: 'small' })
  })

  it('accepts the flat reroute shape and a null previous binding', () => {
    expect(parseFleetEvent('{"kind":"reroute","id":"r1","watch":"w","role":"x","from":null,"to":{"CustomAgent":"mine"}}')).toEqual({
      kind: 'reroute',
      id: 'r1',
      watch: 'w',
      role: 'x',
      from: 'no binding',
      to: 'mine',
    })
  })

  it('reads B4 flat flat reroute with display names, and an empty name as no binding', () => {
    expect(
      parseFleetEvent('{"kind":"reroute","sessionId":"a1","agentId":"a1","id":"r1","watchId":"w1","watch":"cost","role":"reviewer","from":"","to":"small","at":1760000000000}'),
    ).toEqual({ kind: 'reroute', id: 'r1', watch: 'cost', role: 'reviewer', from: 'no binding', to: 'small' })
  })

  it('drops a reroute missing id, role or a binding', () => {
    expect(parseFleetEvent('{"kind":"reroute","reroute":{"watchId":"w"}}')).toBeNull()
    expect(parseFleetEvent('{"kind":"reroute","id":"r1","role":"x","from":"a"}')).toBeNull()
    expect(parseFleetEvent('{"kind":"reroute","id":7,"role":"x","from":"a","to":"b"}')).toBeNull()
  })

  it('addresses a run delta by agentId when no sessionId is given', () => {
    expect(parseFleetEvent('{"kind":"run","agentId":"a1","run":{"kind":"none"}}')).toMatchObject({ kind: 'run', sessionId: 'a1' })
  })

  it('accepts a watch delta with no session', () => {
    expect(parseFleetEvent('{"kind":"watch","sessionId":"studio","event":{"State":"fired"}}')).toEqual({ kind: 'watch', sessionId: 'studio', event: { State: 'fired' } })
  })
})
