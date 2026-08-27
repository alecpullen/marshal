import { describe, expect, it } from 'vitest'
import { forgeCapabilities } from './forge'

describe('forgeCapabilities', () => {
  it('grants the rich path to a pat repo with a forge', () => {
    const c = forgeCapabilities({ forge: 'gitea', credKind: 'pat' })
    expect(c.richPRs).toBe(true)
    expect(c.issueIntake).toBe(true)
    expect(c.reason).toBe('')
  })
  it('explains why an ssh repo cannot use them', () => {
    const c = forgeCapabilities({ forge: 'gitea', credKind: 'ssh' })
    expect(c.richPRs).toBe(false)
    expect(c.issueIntake).toBe(false)
    expect(c.reason).toMatch(/SSH/i)
  })
  it('explains a missing forge separately from a missing token', () => {
    const c = forgeCapabilities({ forge: '', credKind: 'pat' })
    expect(c.issueIntake).toBe(false)
    expect(c.reason).toMatch(/forge/i)
  })
})
