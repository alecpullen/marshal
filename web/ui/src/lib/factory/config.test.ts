import { describe, expect, it } from 'vitest'
import type { FactoryConfig } from '../factory'
import { formatFactoryCheck, keepUnavailable, profileOf, resolvedConfigRows } from './config'

/** A configuration as the bridge returns one: every field already resolved. */
const resolved = (over: Partial<FactoryConfig> = {}): FactoryConfig => ({
  repoId: 'acme/widgets',
  projectRoot: '/home/u/alpha',
  hostId: 'local',
  targetBranch: 'main',
  workspace: '',
  native: true,
  implementerRouting: {},
  reviewerRouting: {},
  reviewerInstructions: '',
  implementerAuto: false,
  expectedChecks: [],
  noRequiredCiAcknowledged: false,
  maxRounds: 5,
  ciPollSeconds: 15,
  ciWaitSeconds: 1800,
  ...over,
})

it('profileOfReadsOnlyStringProfiles', () => {
  // A route that is absent, or that carries something other than a profile
  // name, contributes nothing — it never stringifies its other members.
  expect(profileOf(undefined)).toBe('')
  expect(profileOf({})).toBe('')
  expect(profileOf({ profile: 7 })).toBe('')
  expect(profileOf({ profile: { name: 'careful' } })).toBe('')
  // A routing record carries more than its profile; only the profile is read.
  const route: Record<string, unknown> = { profile: 'careful', model: 'opus' }
  expect(profileOf(route)).toBe('careful')
})

it('keepUnavailableRetainsSavedChoice', () => {
  const offered = [
    { value: 'careful', label: 'careful' },
    { value: 'fast', label: 'fast' },
  ]
  // A choice the picker still offers, or no choice at all, is left alone.
  expect(keepUnavailable(offered, 'fast')).toEqual(offered)
  expect(keepUnavailable(offered, '')).toEqual(offered)
  // A saved choice the picker no longer offers stays visible, labelled so it
  // reads as stale rather than disappearing into the follow option.
  expect(keepUnavailable(offered, 'strict')).toEqual([...offered, { value: 'strict', label: 'strict (unavailable)' }])
  // The retained choice is appended: a real option of the same name is never
  // replaced, and the caller's list is not mutated.
  expect(keepUnavailable(offered, 'strict')).toHaveLength(3)
  expect(offered).toHaveLength(2)
})

it('formatFactoryCheckUsesBothSyntaxes', () => {
  // A check run is identified by name and GitHub App, because the same name
  // from another application is a different required check.
  expect(formatFactoryCheck({ kind: 'check_run', name: 'build', appId: 7 })).toBe('check_run:build:7')
  // A commit status has no app; its context alone identifies it.
  expect(formatFactoryCheck({ kind: 'commit_status', name: 'ci/lint', appId: 0 })).toBe('commit_status:ci/lint')
})

it('resolvedConfigRowsCoverEveryApprovalField', () => {
  const rows = resolvedConfigRows(resolved({
    workspace: 'ci-dev',
    native: false,
    targetBranch: 'release',
    implementerRouting: { profile: 'careful', model: 'opus' },
    reviewerRouting: { profile: 'strict' },
    reviewerInstructions: 'Check the migration',
    expectedChecks: [
      { kind: 'check_run', name: 'build', appId: 7 },
      { kind: 'commit_status', name: 'ci/lint', appId: 0 },
    ],
    implementerAuto: true,
    maxRounds: 9,
    ciPollSeconds: 30,
    ciWaitSeconds: 60,
  }))
  // Every field approval decides on has a row, in one stable order.
  expect(rows.map((row) => row.label)).toEqual([
    'Repository',
    'Project',
    'Target branch',
    'Host',
    'Environment',
    'Implementer',
    'Reviewer',
    'Reviewer instructions',
    'Expected checks',
    'Auto permission',
    'Total rounds',
    'CI poll / wait',
  ])
  const value = (label: string): string | undefined => rows.find((row) => row.label === label)?.value
  expect(value('Repository')).toBe('acme/widgets')
  expect(value('Project')).toBe('/home/u/alpha')
  expect(value('Target branch')).toBe('release')
  expect(value('Host')).toBe('local')
  expect(value('Environment')).toBe('ci-dev')
  expect(value('Implementer')).toBe('careful')
  expect(value('Reviewer')).toBe('strict')
  expect(value('Reviewer instructions')).toBe('Check the migration')
  expect(value('Expected checks')).toBe('check_run:build:7, commit_status:ci/lint')
  expect(value('Auto permission')).toBe('Automatic')
  expect(value('Total rounds')).toBe('9')
  expect(value('CI poll / wait')).toBe('30s / 60s')
  // A routing object is read for its profile only: nothing else is printed.
  expect(JSON.stringify(rows)).not.toContain('opus')
  expect(JSON.stringify(rows)).not.toContain('"profile"')
  // The server resolved every value, so no row claims a source it cannot know.
  expect(rows.every((row) => row.source === undefined || row.label === 'Expected checks')).toBe(true)
})

describe('resolvedConfigRows empty checks', () => {
  it('marks an unacknowledged empty check list as a warning', () => {
    const row = resolvedConfigRows(resolved()).find((r) => r.label === 'Expected checks')
    expect(row?.value).toBe('None')
    expect(row?.source).toBeUndefined()
    expect(row?.warning).toBe(true)
  })

  it('records an acknowledged empty check list without warning', () => {
    const row = resolvedConfigRows(resolved({ noRequiredCiAcknowledged: true })).find((r) => r.label === 'Expected checks')
    expect(row?.value).toBe('None')
    expect(row?.source).toBe('No required CI acknowledged')
    expect(row?.warning).toBe(false)
  })
})
