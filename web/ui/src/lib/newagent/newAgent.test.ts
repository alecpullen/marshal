import { beforeEach, describe, expect, it } from 'vitest'
import { DEFAULT_MODEL, FAST_ROLES, MODES, canIsolate, defaults, loadRemembered, modelLabel, remember, rememberedReset, routingFor, validModel } from './newAgent'
import type { ProjectStatus } from '../api'

const p = (root: string, over: Partial<ProjectStatus> = {}): ProjectStatus => ({ root, available: true, isolation: 'available', ...over })

describe('defaults', () => {
  it('picks the first available project in Act with Auto off, isolated when supported', () => {
    const d = defaults([p('/gone', { available: false }), p('/a'), p('/b')])
    expect(d).toEqual({ project: '/a', mode: 'act', auto: false, isolated: true, branch: '', baseRef: '' })
  })
  it('is not isolated when the project cannot isolate', () => {
    expect(defaults([p('/a', { isolation: 'not a git repository' })]).isolated).toBe(false)
  })
  it('migrates a remembered tuple and ignores stale projects', () => {
    const projects = [p('/a'), p('/b')]
    expect(defaults(projects, { project: '/b', mode: 'plan', isolated: false })).toMatchObject({ project: '/b', mode: 'ask', auto: false, isolated: false })
    expect(defaults(projects, { project: '/zzz', mode: 'read' })).toMatchObject({ project: '/a', mode: 'ask', auto: false })
  })
  it('has no project when none is available', () => {
    expect(defaults([p('/a', { available: false })]).project).toBe('')
  })
  it('migrates every remembered legacy mode', () => {
    const projects = [p('/a')]
    const cases: Array<[string, string, boolean]> = [
      ['read', 'ask', false],
      ['default', 'ask', false],
      ['plan', 'ask', false],
      ['edit', 'ask', false],
      ['copilot', 'act', false],
      ['auto', 'act', true],
    ]
    for (const [legacy, mode, auto] of cases) {
      expect(defaults(projects, { mode: legacy }), legacy).toMatchObject({ mode, auto })
    }
  })
  it('starts fresh Act without Auto, and rejects an unknown spelling without granting Act', () => {
    const projects = [p('/a')]
    const fresh = defaults(projects)
    expect(fresh).toMatchObject({ mode: 'act', auto: false })
    expect(rememberedReset({ mode: 'yolo' })).toBe(true)
    expect(defaults(projects, { mode: 'yolo' })).toMatchObject({ mode: 'act', auto: false })
  })
  it('rejects Ask with Auto rather than carrying an impossible pair', () => {
    expect(defaults([p('/a')], { mode: 'ask', auto: true })).toMatchObject({ mode: 'act', auto: false })
    expect(rememberedReset({ mode: 'ask', auto: true })).toBe(true)
  })
})

describe('remembered choice', () => {
  beforeEach(() => localStorage.clear())
  it('round-trips starting metadata and survives bad data', () => {
    remember({ project: '/a', mode: 'act', auto: true, isolated: false, branch: 'x', baseRef: 'y' })
    expect(loadRemembered()).toEqual({ project: '/a', mode: 'act', auto: true, isolated: false })
    localStorage.setItem('marshal.ui.newagent', '{oops')
    expect(loadRemembered()).toEqual({})
  })
  it('never remembers System or the access revision', () => {
    remember({ project: '/a', mode: 'act', auto: true, isolated: false, branch: '', baseRef: '' })
    const stored = JSON.parse(localStorage.getItem('marshal.ui.newagent')!)
    expect(stored).not.toHaveProperty('system')
    expect(stored).not.toHaveProperty('system_access')
    expect(stored).not.toHaveProperty('revision')
    expect(stored).not.toHaveProperty('access_revision')
  })
})

describe('modes', () => {
  it('is the two-mode set, with Auto as a modifier', () => {
    expect([...MODES]).toEqual(['ask', 'act'])
    expect(canIsolate(p('/a'))).toBe(true)
    expect(canIsolate(p('/a', { isolation: 'no git' }))).toBe(false)
  })
})

describe('model choice', () => {
  const roles = ['implementer', 'reviewer', 'router', 'title', 'summarizer', 'repo_scout', 'planner']

  it('sends nothing for the default', () => {
    expect(routingFor(DEFAULT_MODEL, roles)).toBeUndefined()
  })

  it('names the profile for a profile choice', () => {
    expect(routingFor({ kind: 'profile', name: 'cheap' }, roles)).toEqual({ profile: 'cheap' })
  })

  it('maps a preset onto every non-fast role', () => {
    expect(routingFor({ kind: 'preset', name: 'big' }, roles)).toEqual({ overrides: { implementer: 'big', reviewer: 'big', planner: 'big' } })
    expect([...FAST_ROLES].sort()).toEqual(['repo_scout', 'router', 'summarizer', 'title'])
  })

  it('sends nothing for a preset when no role can take an override', () => {
    expect(routingFor({ kind: 'preset', name: 'big' }, [])).toBeUndefined()
    expect(routingFor({ kind: 'preset', name: 'big' }, ['router', 'title'])).toBeUndefined()
  })

  it('validates a remembered choice against the config', () => {
    const cfg = { profiles: { cheap: {} }, presets: { big: {} } }
    expect(validModel({ kind: 'profile', name: 'cheap' }, cfg)).toEqual({ kind: 'profile', name: 'cheap' })
    expect(validModel({ kind: 'preset', name: 'gone' }, cfg)).toEqual(DEFAULT_MODEL)
    expect(validModel({ kind: 'preset', name: 'big' }, null)).toEqual(DEFAULT_MODEL)
    expect(validModel(undefined, cfg)).toEqual(DEFAULT_MODEL)
  })

  it('labels each kind', () => {
    expect(modelLabel(DEFAULT_MODEL)).toBe('Default profile')
    expect(modelLabel({ kind: 'profile', name: 'cheap' })).toBe('cheap profile')
    expect(modelLabel({ kind: 'preset', name: 'big' })).toBe('big preset')
  })

  it('stores the choice with the other chips', () => {
    localStorage.clear()
    remember({ project: '/a', mode: 'act', auto: false, isolated: true, branch: '', baseRef: '' }, { kind: 'preset', name: 'big' })
    expect(loadRemembered().model).toEqual({ kind: 'preset', name: 'big' })
  })
})
