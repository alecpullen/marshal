import { describe, expect, it } from 'vitest'
import { parseFleetEvent } from './sse'
describe('parseFleetEvent', () => {
  it('parses deltas and overflow', () => {
    expect(parseFleetEvent('{"kind":"activity","sessionId":"s1"}')).toEqual({kind:'activity',sessionId:'s1'})
    expect(parseFleetEvent('{"type":"replay_overflow"}')).toBe('overflow')
    expect(parseFleetEvent('{nope')).toBeNull()
  })
})