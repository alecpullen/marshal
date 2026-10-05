import { beforeEach, describe, expect, it } from 'vitest'
import { DEFAULT_MODEL, FAST_ROLES, MODES, canIsolate, defaults, loadRemembered, modelLabel, remember, routingFor, validModel } from './newAgent'
import type { ProjectStatus } from '../api'

const p = (root: string, over: Partial<ProjectStatus> = {}): ProjectStatus => ({ root, available: true, isolation: 'available', ...over })

describe('defaults', () => {
  it('picks the first available project in edit mode, isolated when supported', () => {
    const d = defaults([p('/gone', { available: false }), p('/a'), p('/b')])
    expect(d).toEqual({ project: '/a', mode: 'edit', isolated: true, branch: '', baseRef: '' })
  })
  it('is not isolated when the project cannot isolate', () => {
    expect(defaults([p('/a', { isolation: 'not a git repository' })]).isolated).toBe(false)
  })
  it('honours a remembered project and mode, and ignores stale ones', () => {
    const projects = [p('/a'), p('/b')]
    expect(defaults(projects, { project: '/b', mode: 'plan', isolated: false })).toMatchObject({ project: '/b', mode: 'plan', isolated: false })
    expect(defaults(projects, { project: '/zzz', mode: 'read' })).toMatchObject({ project: '/a', mode: 'edit' })
  })
  it('has no project when none is available', () => {
    expect(defaults([p('/a', { available: false })]).project).toBe('')
  })
})

describe('remembered choice', () => {
  beforeEach(() => localStorage.clear())
  it('round-trips and survives bad data', () => {
    remember({ project: '/a', mode: 'auto', isolated: false, branch: 'x', baseRef: 'y' })
    expect(loadRemembered()).toEqual({ project: '/a', mode: 'auto', isolated: false })
    localStorage.setItem('marshal.ui.newagent', '{oops')
    expect(loadRemembered()).toEqual({})
  })
})

describe('modes', () => {
  it('is the ACP set, without read', () => {
    expect([...MODES]).toEqual(['plan', 'default', 'edit', 'copilot', 'auto'])
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
    remember({ project: '/a', mode: 'edit', isolated: true, branch: '', baseRef: '' }, { kind: 'preset', name: 'big' })
    expect(loadRemembered().model).toEqual({ kind: 'preset', name: 'big' })
  })
})
