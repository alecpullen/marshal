import { describe, expect, it } from 'vitest'
import type { FactoryConfig } from '../factory'
import { resolveSubagentRole, subagentOverridesOf, subagentValueLine } from '../factory'
import {
  DRAFT_SUBAGENT_SOURCES,
  PROJECT_SUBAGENT_SOURCES,
  SUBAGENT_INHERIT,
  applyConcurrencyChoice,
  applyEnabledChoice,
  applyModelChoice,
  concurrencyChoice,
  concurrencyText,
  describeSubagentSource,
  enabledChoice,
  formatFactoryCheck,
  frozenSubagentRows,
  frozenSubagentText,
  keepUnavailable,
  modelChoice,
  profileOf,
  resolvedConfigRows,
  settingsVersionMoved,
  subagentConflictNotice,
  subagentFieldLines,
  subagentOverridesBody,
  subagentSummaryRows,
  subagentUnsupportedNotice,
} from './config'

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

describe('subagent settings presentation', () => {
  it('keeps inherit, explicit false and explicit empty model distinct', () => {
    // Absent is inherit — not false, and not an empty model.
    expect(enabledChoice(undefined)).toBe('inherit')
    expect(modelChoice(undefined)).toBe(SUBAGENT_INHERIT)
    expect(concurrencyChoice(undefined)).toBe(SUBAGENT_INHERIT)
    expect(concurrencyText(undefined)).toBe('')
    // An explicit false is a stated choice, and it survives a round trip.
    const off = applyEnabledChoice(undefined, 'off')
    expect(off).toEqual({ enabled: false })
    expect(enabledChoice(off)).toBe('off')
    // An explicit empty model is normal child routing, which is not inherit.
    const routed = applyModelChoice(undefined, '')
    expect(routed).toEqual({ model: '' })
    expect(modelChoice(routed)).toBe('')
    expect(resolveSubagentRole('implementer', { item: routed }).fields[1].value).toBe('Normal child routing')
  })

  it('resets one field to inherit without clearing the others', () => {
    const both = applyConcurrencyChoice(applyEnabledChoice(undefined, 'on'), '3')
    expect(both).toEqual({ enabled: true, concurrency: 3 })
    // Reset-to-inherit deletes exactly the field it belongs to.
    expect(applyEnabledChoice(both, 'inherit')).toEqual({ concurrency: 3 })
    expect(applyConcurrencyChoice(both, '')).toEqual({ enabled: true })
    expect(modelChoice(applyModelChoice(both, SUBAGENT_INHERIT))).toBe(SUBAGENT_INHERIT)
    // Nothing stated at all is nothing to write, so a default can still reach it.
    expect(subagentOverridesBody('inherit', SUBAGENT_INHERIT, '')).toBeUndefined()
    expect(subagentOverridesBody('off', '', '8')).toEqual({ enabled: false, model: '', concurrency: 8 })
  })

  it('resolves each role through its own chain', () => {
    const config = { implementerSubagents: { enabled: false }, reviewerSubagents: { enabled: true, model: 'strict' } }
    const implementer = resolveSubagentRole('implementer', { item: subagentOverridesOf(config, 'implementer') })
    const reviewer = resolveSubagentRole('reviewer', { item: subagentOverridesOf(config, 'reviewer') })
    expect(implementer.fields.map((f) => f.value)).toEqual(['Off', 'Inherited', 'Inherited'])
    expect(implementer.inherited).toBe(false)
    expect(reviewer.fields.map((f) => f.value)).toEqual(['On', 'strict', 'Inherited'])
    // One role's override never reaches the other.
    expect(reviewer.fields[0].source).toBe('item')
    expect(implementer.fields[1].source).toBe('unset')
  })

  it('refreshes only inherited fields when the defaults change', () => {
    // The item states its model and nothing else, so a project default change
    // reaches its concurrency and leaves the stated model fixed.
    const item = { model: 'careful' }
    const before = resolveSubagentRole('implementer', { item, project: { model: 'fast', concurrency: 2 } })
    expect(before.fields[1]).toMatchObject({ value: 'careful', source: 'item', explicit: true })
    expect(before.fields[2]).toMatchObject({ value: '2 at a time', source: 'project', explicit: false })
    const after = resolveSubagentRole('implementer', { item, project: { model: 'frontier', concurrency: 6 } })
    expect(after.fields[1].value).toBe('careful')
    expect(after.fields[2].value).toBe('6 at a time')
    const line = subagentValueLine(after)
    expect(line).toBe('Inherited · careful · 6 at a time')
  })

  it('names the source per surface and never invents a value', () => {
    expect(describeSubagentSource('item')).toBe(DRAFT_SUBAGENT_SOURCES.item)
    expect(describeSubagentSource('project', PROJECT_SUBAGENT_SOURCES)).toBe('Your project setting')
    expect(describeSubagentSource('unset')).toContain('cannot read')
    const view = resolveSubagentRole('reviewer', {})
    // A session that reported nothing is stated as unread, never as a value.
    expect(view.fields.every((f) => f.value === 'Inherited' && f.source === 'unset')).toBe(true)
    expect(view.inherited).toBe(true)
    expect(subagentFieldLines(view)).toContain('Enabled: Inherited — Inherited from the session')
    const rows = subagentSummaryRows([view])
    expect(rows[0].label).toBe('Reviewer subagents')
    expect(rows[0].source).toContain('cannot read')
  })

  it('shows an unenforceable concurrency as unsupported rather than clamping it', () => {
    const view = resolveSubagentRole('implementer', { item: { concurrency: 9 } })
    expect(view.fields[2].value).toBe('9 at a time')
    expect(view.fields[2].unsupported).toContain('1 to 8')
    expect(subagentUnsupportedNotice({ settings: false })).toContain('refused at approval')
    // A supported backend, and a caller that does not know, claim nothing.
    expect(subagentUnsupportedNotice({ settings: true })).toBe('')
    expect(subagentUnsupportedNotice(null)).toBe('')
  })

  it('presents a moved settings version as a conflict to reload through', () => {
    expect(settingsVersionMoved('"v1"', '"v1"')).toBe(false)
    expect(settingsVersionMoved('"v1"', '"v2"')).toBe(true)
    // An unknown version is not a move: nothing is claimed either way.
    expect(settingsVersionMoved('', '"v2"')).toBe(false)
    const notice = subagentConflictNotice('"v1"', '"v2"')
    expect(notice).toContain('Reload')
    expect(notice).toContain('"v1"')
    expect(notice).toContain('"v2"')
  })

  it('shows the settings an approved revision froze', () => {
    expect(frozenSubagentRows({
      implementerSubagents: { enabled: false, model: '', concurrency: 3 },
      reviewerSubagents: { enabled: true, model: 'strict', concurrency: 1 },
    })).toEqual([
      { label: 'Implementer subagents', value: 'Off · Normal child routing · 3 at a time' },
      { label: 'Reviewer subagents', value: 'On · strict · 1 at a time' },
    ])
    // A revision approved before the settings were frozen says so, and a zero
    // cap is never shown as a cap of zero.
    expect(frozenSubagentText(undefined)).toContain('Not recorded')
    expect(frozenSubagentText({ enabled: false, model: '', concurrency: 0 })).toContain('Not recorded')
  })
})
