import { describe, expect, it } from 'vitest'
import { sessionsProjectFromHash, isScopedSessions } from './routes'

describe('sessionsProjectFromHash', () => {
  it('returns null for the unscoped route and non-sessions hashes', () => {
    expect(sessionsProjectFromHash('#sessions')).toBeNull()
    expect(sessionsProjectFromHash('#chat/abc')).toBeNull()
    expect(sessionsProjectFromHash('#')).toBeNull()
  })

  it('decodes an encoded project root', () => {
    expect(sessionsProjectFromHash('#sessions/%2Fhome%2Fu%2Falpha')).toBe('/home/u/alpha')
  })

  it('keeps a raw segment with no escapes intact', () => {
    expect(sessionsProjectFromHash('#sessions//home/u/alpha')).toBe('/home/u/alpha')
  })

  it('degrades a malformed escape to the raw string instead of throwing', () => {
    expect(sessionsProjectFromHash('#sessions/%E0%A4%A')).toBe('%E0%A4%A')
  })

  it('treats an empty scope as unscoped', () => {
    // `#sessions/` does not match the `(.+)` group, so it falls to the
    // picker — an empty scope must never become an empty-string project.
    expect(sessionsProjectFromHash('#sessions/')).toBeNull()
  })

  it('round-trips the characters hash routing cares about', () => {
    expect(sessionsProjectFromHash('#sessions/%23tag%20dir%3Fq')).toBe('#tag dir?q')
  })

  it('decodes exactly once, so a double-escaped root stays escaped', () => {
    expect(sessionsProjectFromHash('#sessions/%252F')).toBe('%2F')
  })

  it('leaves a literal plus intact — decodeURIComponent is not query-string decoding', () => {
    expect(sessionsProjectFromHash('#sessions/a+b')).toBe('a+b')
  })
})

describe('isScopedSessions', () => {
  it('is true only for a non-empty scope', () => {
    expect(isScopedSessions('#sessions/%2Fproj%2Fa')).toBe(true)
    expect(isScopedSessions('#sessions')).toBe(false)
    expect(isScopedSessions('#sessions/')).toBe(false)
    expect(isScopedSessions('#chat/x')).toBe(false)
    expect(isScopedSessions('#')).toBe(false)
  })
})
