import { beforeEach, describe, expect, it } from 'vitest'
import { MODES, canIsolate, defaults, loadRemembered, remember } from './newAgent'
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
