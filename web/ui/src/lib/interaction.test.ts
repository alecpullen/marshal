import { describe, expect, it } from 'vitest'
import {
  accessLabel,
  accessPatchBody,
  applyAccessReport,
  migrateStoredDefaults,
  parseAccessState,
  parseMode,
  parseModeAccess,
  sameAccess,
  type AccessHolder,
  type AccessState,
} from './interaction'

const wire = (over: Record<string, unknown> = {}) => ({
  mode: 'act',
  auto: false,
  system_access: false,
  access_revision: 1,
  ...over,
})

describe('parseAccessState', () => {
  it('parses a complete access report', () => {
    expect(parseAccessState(wire({ mode: 'act', auto: true, system_access: true, access_revision: 7 }))).toEqual({
      mode: 'act',
      auto: true,
      system: true,
      revision: 7,
    })
  })

  it('rejects malformed and unknown live modes', () => {
    expect(parseAccessState(null)).toBeNull()
    expect(parseAccessState('act')).toBeNull()
    expect(parseAccessState(wire({ mode: 'edit' }))).toBeNull()
    expect(parseAccessState(wire({ mode: 'auto' }))).toBeNull()
    expect(parseAccessState(wire({ mode: '' }))).toBeNull()
    // A missing or mistyped modifier is not an implicit false.
    expect(parseAccessState({ mode: 'act', system_access: false, access_revision: 1 })).toBeNull()
    expect(parseAccessState(wire({ auto: 'no' }))).toBeNull()
    expect(parseAccessState(wire({ system_access: 0 }))).toBeNull()
    expect(parseAccessState(wire({ access_revision: '2' }))).toBeNull()
    expect(parseAccessState(wire({ access_revision: 1.5 }))).toBeNull()
    // Ask cannot enable Auto; that combination is impossible, not defaulted.
    expect(parseAccessState(wire({ mode: 'ask', auto: true }))).toBeNull()
  })

  it('keeps explicit revocations', () => {
    const got = parseAccessState(wire({ mode: 'act', auto: false, system_access: false, access_revision: 3 }))!
    expect(got.auto).toBe(false)
    expect(got.system).toBe(false)
    expect(accessLabel(got)).toBe('Act')
    // A true flag is shown; a false one is not advertised at all.
    expect(accessLabel({ ...got, auto: true, system: true })).toBe('Act + Auto + System')
  })
})

describe('applyAccessReport', () => {
  const state = (rev: number, over: Partial<AccessState> = {}): AccessHolder => ({
    sessionId: 's1',
    access: { mode: 'act', auto: false, system: false, revision: rev, ...over },
  })

  it('ignores older revisions within one session', () => {
    const current = state(5)
    expect(applyAccessReport(current, 's1', wire({ access_revision: 4 }))).toBeNull()
    expect(applyAccessReport(current, 's1', wire({ access_revision: 5 }))?.access.revision).toBe(5)
    expect(applyAccessReport(current, 's1', wire({ access_revision: 6 }))?.access.revision).toBe(6)
  })

  it('resets revision for a new session', () => {
    const current = state(9)
    const next = applyAccessReport(current, 's2', wire({ access_revision: 1, mode: 'ask' }))
    expect(next).toEqual({ sessionId: 's2', access: { mode: 'ask', auto: false, system: false, revision: 1 } })
  })

  it('never lets a stale frame restore a revoked capability', () => {
    // The session revoked Auto and System at revision 4.
    const revoked = state(4, { auto: false, system: false })
    // A replayed frame from revision 2 still claims both.
    expect(applyAccessReport(revoked, 's1', wire({ auto: true, system: true, access_revision: 2 }))).toBeNull()
    expect(revoked.access?.auto).toBe(false)
    expect(revoked.access?.system).toBe(false)
  })
})

describe('parseMode', () => {
  it('accepts only the two canonical spellings', () => {
    expect(parseMode('ask')).toBe('ask')
    expect(parseMode('ACT')).toBe('act')
    expect(parseMode(' plan ')).toBeNull()
    expect(parseMode(undefined)).toBeNull()
  })
})

describe('stored defaults', () => {
  it('migrates retired spellings without inventing Auto presence', () => {
    expect(migrateStoredDefaults({ mode: 'edit' })).toEqual({ mode: 'ask' })
    expect(migrateStoredDefaults({ mode: 'plan' })).toEqual({ mode: 'ask' })
    expect(migrateStoredDefaults({ mode: 'default' })).toEqual({ mode: 'ask' })
    expect(migrateStoredDefaults({ mode: 'copilot' })).toEqual({ mode: 'act' })
    expect(migrateStoredDefaults({ mode: 'auto' })).toEqual({ mode: 'act', auto: true })
  })

  it('preserves an absent Auto versus an explicit false', () => {
    const absent = migrateStoredDefaults({ mode: 'act' })!
    expect('auto' in absent).toBe(false)
    const revoked = migrateStoredDefaults({ mode: 'act', auto: false })!
    expect('auto' in revoked).toBe(true)
    expect(revoked.auto).toBe(false)
  })

  it('returns null for an unknown spelling rather than promoting it', () => {
    expect(migrateStoredDefaults({ mode: 'yolo' })).toBeNull()
    expect(migrateStoredDefaults({ mode: 'act', auto: 'yes' })).toBeNull()
  })
})

describe('accessPatchBody', () => {
  it('omits absent fields so leave-alone stays distinct from false', () => {
    expect(accessPatchBody({ mode: 'ask' })).toEqual({ mode: 'ask' })
    expect(accessPatchBody({ auto: false })).toEqual({ auto: false })
    expect(accessPatchBody({ system_access: false })).toEqual({ system_access: false })
    expect(accessPatchBody({})).toEqual({})
  })
})

describe('sameAccess', () => {
  it('compares the whole tuple including the revision', () => {
    const a = parseAccessState(wire())!
    expect(sameAccess(a, { ...a })).toBe(true)
    expect(sameAccess(a, { ...a, auto: true })).toBe(false)
    expect(sameAccess(a, { ...a, revision: 2 })).toBe(false)
    expect(sameAccess(null, a)).toBe(false)
    expect(sameAccess(null, null)).toBe(true)
  })
})

describe('parseModeAccess', () => {
  it('parses the typed capability presentation', () => {
    const got = parseModeAccess({
      requestId: 'r-1',
      sessionId: 's-1',
      reason: 'need act',
      current: { mode: 'ask', auto: false, system_access: false, access_revision: 2 },
      missingEditing: true,
      missingSystem: false,
    })
    expect(got).toMatchObject({ requestId: 'r-1', sessionId: 's-1', reason: 'need act', missingEditing: true, missingSystem: false })
    expect(got?.current).toEqual({ mode: 'ask', auto: false, system_access: false, access_revision: 2 })
  })

  it('rejects a bundle whose current state is malformed', () => {
    expect(parseModeAccess({ requestId: 'r', sessionId: 's', current: { mode: 'plan' } })).toBeNull()
    expect(parseModeAccess(undefined)).toBeNull()
  })
})
