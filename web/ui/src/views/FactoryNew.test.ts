import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import FactoryNew from './FactoryNew.svelte'
import * as api from '../lib/api'
import type { FactoryCheckKey, FactoryConfig, FactoryCreate, FactoryItem } from '../lib/factory'
import { createNavigationGuard, type RouteLeaveGuard } from '../lib/navigationGuard'

vi.mock('../lib/api', async (actual) => ({
  ...await actual<typeof import('../lib/api')>(),
  listRepos: vi.fn(),
  listWorkspaces: vi.fn(),
  getModels: vi.fn(),
  getProjectSettings: vi.fn(),
  createFactoryItem: vi.fn(),
}))
afterEach(cleanup)

const ROOT = '/home/u/alpha'
const BUILD: FactoryCheckKey = { kind: 'check_run', name: 'build', appId: 7 }

/** A resolved configuration, the shape the bridge returns with a saved draft. */
const resolvedConfig = (over: Partial<FactoryConfig> = {}): FactoryConfig => ({
  repoId: 'repo', projectRoot: ROOT, hostId: 'local', targetBranch: 'main', workspace: '', native: true,
  implementerRouting: {}, reviewerRouting: {}, reviewerInstructions: '', implementerAuto: false,
  expectedChecks: [], noRequiredCiAcknowledged: false, maxRounds: 5, ciPollSeconds: 15, ciWaitSeconds: 1800,
  ...over,
})

/** A factory item as `GET /api/factory/items/<id>` returns it. */
const resolvedItem = (over: Partial<FactoryConfig> = {}): FactoryItem => ({
  id: 'item-1', title: 'Task', state: 'draft', stage: '', repoId: 'repo', projectRoot: ROOT, hostId: 'local',
  branch: 'factory/item-1', baseSha: '', version: 1,
  draft: { requestId: 'r1', title: 'Task', plan: 'Build it', spec: '', config: resolvedConfig(over) },
  approvals: null, grants: null, rounds: null, workers: null, prs: null, effects: null, decision: null, history: null,
  createdAt: '2026-11-15T00:00:00Z', approvedAt: '', updatedAt: '2026-11-15T00:00:00Z',
})
const projectSettings = (over: Partial<api.ProjectSettings> = {}): api.ProjectSettings => ({ intake: {}, ...over })

/** The body of the nth create request. */
const sent = (n = 0): FactoryCreate => vi.mocked(api.createFactoryItem).mock.calls[n][0]
const save = () => fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
/** The shell store's write: a saved draft is published, never re-read. */
const upsert = vi.fn()

/**
 * One approval-summary row's text: its effective value and, when the value is
 * being followed, the source it comes from. Reading the row rather than a
 * control keeps these assertions about what the person is told.
 */
const row = (id: string): string => screen.getByTestId(`config-${id}`).textContent ?? ''

/** The shell's route guard, plus the registration calls the view makes on it. */
function wiredGuard() {
  const guard = createNavigationGuard()
  const registerGuard = vi.fn((fn?: RouteLeaveGuard) => guard.register(fn))
  return { guard, registerGuard }
}

async function fillHandoff() {
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Task' } })
  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Build it' } })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listRepos).mockResolvedValue([{ id: 'repo', url: 'https://example.test', branch: 'main' }])
  vi.mocked(api.listWorkspaces).mockResolvedValue([
    { source: 'studio', name: 'go-dev', usage: 0 },
    { source: 'studio', name: 'ci-dev', usage: 0 },
  ])
  vi.mocked(api.getModels).mockResolvedValue({ profiles: { careful: {}, fast: {}, strict: {} } } as never)
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings())
  vi.mocked(api.createFactoryItem).mockResolvedValue(resolvedItem())
})

it('retains form input after a network error', async () => {
  vi.mocked(api.listRepos).mockResolvedValue([{ id: 'repo', url: 'https://example.test' }])
  vi.mocked(api.listWorkspaces).mockResolvedValue([])
  vi.mocked(api.getModels).mockResolvedValue({ profiles: { chosen: {} } } as never)
  vi.mocked(api.createFactoryItem).mockRejectedValue(new Error('network down'))
  render(FactoryNew, { onNavigate: vi.fn() })
  await screen.findByRole('option', { name: 'repo' })
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Task' } })
  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Build it' } })
  await fireEvent.change(screen.getByLabelText('Implementer model profile'), { target: { value: 'chosen' } })
  await fireEvent.change(screen.getByLabelText('Reviewer model profile'), { target: { value: 'chosen' } })
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
  await screen.findByText(/network down/)
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('Build it')
})

it('groupsHandoffAndShowsReadableSummary', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    // A routing object carries more than its profile; none of it is printed.
    factoryDefaults: { implementerRouting: { profile: 'careful', model: 'opus' }, expectedChecks: [BUILD] },
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  // The handoff reads as the five approved sections, each with its own
  // controls, rather than one dense technical form.
  for (const heading of ['Scope', 'Repository and environment', 'Roles and review', 'Authority and limits', 'Approval summary']) {
    expect(screen.getByRole('heading', { name: heading })).toBeTruthy()
  }
  expect(screen.getByLabelText('Title')).toBeTruthy()
  expect(screen.getByLabelText('Repository')).toBeTruthy()
  expect(screen.getByLabelText('Target branch')).toBeTruthy()
  expect(screen.getByLabelText('Implementer model profile')).toBeTruthy()
  expect(screen.getByLabelText('Reviewer model profile')).toBeTruthy()
  expect(screen.getByLabelText('Reviewer instructions')).toBeTruthy()
  expect(screen.getByLabelText('This repository has no required CI')).toBeTruthy()
  expect(screen.getByLabelText('Total rounds')).toBeTruthy()
  // The summary beside the form states the effective value and its source.
  expect(row('target-branch')).toContain('main')
  expect(row('target-branch')).toContain('Repository branch')
  expect(row('implementer')).toContain('careful')
  expect(row('implementer')).toContain('Project factory default')
  expect(row('reviewer')).toContain('fast')
  expect(row('reviewer')).toContain('Project agent default')
  expect(row('expected-checks')).toContain('check_run:build:7')
  expect(row('expected-checks')).toContain('Project factory default')
  expect(row('auto-permission')).toContain('Asks before each action')
  expect(row('ci-wait')).toContain('1800s')
  // Nothing is shown as a routing object: only the profile name is readable.
  expect(document.body.textContent).not.toContain('opus')
  expect(document.body.textContent).not.toContain('"profile"')
})

it('focusesFirstInvalidField', async () => {
  // Nothing is configured for this project, so the roles start unchosen and
  // the first submission has several things to say.
  render(FactoryNew, { onNavigate: vi.fn() })
  await screen.findByRole('option', { name: 'repo' })
  await save()
  await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Title')))
  expect(screen.getByTestId('error-title').textContent).toBe('A title is required')
  // The existing alert summary still carries the same message.
  expect(screen.getByRole('alert').textContent).toContain('A title is required')
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Task' } })
  // Fixing a field clears its own message; the rest is untouched.
  expect(screen.queryByTestId('error-title')).toBeNull()
  await save()
  await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Plan')))
  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Build it' } })
  await save()
  // The single registered repository was chosen for us, so the next invalid
  // control is the implementer's model profile.
  await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Implementer model profile')))
  expect(screen.getByTestId('error-implementer').textContent).toContain('implementer model profile')
  // Nothing was sent: a validation failure is not a request.
  expect(api.createFactoryItem).not.toHaveBeenCalled()
})

it('summaryTracksOverridesWithoutMaterializingThem', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    factoryDefaults: { targetBranch: 'develop', maxRounds: 9, expectedChecks: [BUILD] },
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('target-branch')).toContain('develop'))
  expect(row('target-branch')).toContain('Project factory default')
  await fireEvent.change(screen.getByLabelText('Implementer model profile'), { target: { value: 'strict' } })
  // The summary follows the choice the moment it is made ...
  expect(row('implementer')).toContain('strict')
  expect(row('implementer')).toContain('This handoff')
  // ... while an untouched field still names the default it follows.
  expect(row('reviewer')).toContain('fast')
  expect(row('total-rounds')).toContain('9')
  expect(row('total-rounds')).toContain('Project factory default')
  await fillHandoff()
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalled())
  const config = sent().config
  expect(config.implementerRouting).toEqual({ profile: 'strict' })
  // A displayed fallback is not an override: only the chosen field is sent,
  // so every field the bridge can still resolve stays omitted.
  expect('maxRounds' in config).toBe(false)
  expect('expectedChecks' in config).toBe(false)
  expect('targetBranch' in config).toBe(false)
  expect('workspace' in config).toBe(false)
  expect('reviewerRouting' in config).toBe(false)
})

it('showsInheritedSources', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    factoryDefaults: {
      targetBranch: 'develop',
      implementerRouting: { profile: 'careful' },
      expectedChecks: [BUILD],
      maxRounds: 9,
      ciPollSeconds: 30,
    },
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  // The stored workspace is followed, so native is not the default here.
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  expect(row('environment')).toContain('Project agent default')
  expect(row('implementer')).toContain('careful')
  expect(row('implementer')).toContain('Project factory default')
  expect(row('reviewer')).toContain('fast')
  expect(row('reviewer')).toContain('Project agent default')
  expect(row('target-branch')).toContain('develop')
  expect(row('target-branch')).toContain('Project factory default')
  expect(row('total-rounds')).toContain('9')
  expect(row('total-rounds')).toContain('Project factory default')
  expect(row('ci-poll')).toContain('30s')
  expect(row('ci-poll')).toContain('Project factory default')
  expect(row('ci-wait')).toContain('1800s')
  expect(row('ci-wait')).toContain('Built-in')
  expect(row('expected-checks')).toContain('check_run:build:7')
  // The in-progress form keeps its input because settings open beside it.
  const settings = screen.getByRole('link', { name: 'Open project settings in a new tab' })
  expect(settings.getAttribute('href')).toBe('#projects/%2Fhome%2Fu%2Falpha')
  expect(settings.getAttribute('target')).toBe('_blank')
  expect(settings.getAttribute('rel')).toBe('noopener noreferrer')
})

it('omitsFollowingFields', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    factoryDefaults: { targetBranch: 'develop', maxRounds: 9, expectedChecks: [{ kind: 'commit_status', name: 'ci/lint', appId: 0 }] },
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  await fillHandoff()
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalled())
  // Only what the bridge cannot resolve is explicit. A displayed fallback is
  // never sent as an override.
  expect(sent().config).toEqual({ repoId: 'repo', projectRoot: ROOT, native: false, implementerAuto: false, noRequiredCiAcknowledged: false })
})

it('independentRoleOverrideAndReset', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    factoryDefaults: { implementerRouting: { profile: 'careful' }, expectedChecks: [BUILD] },
  }))
  vi.mocked(api.createFactoryItem).mockRejectedValue(new Error('not ready'))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('implementer')).toContain('careful'))
  expect(row('reviewer')).toContain('fast')
  await fillHandoff()
  await fireEvent.change(screen.getByLabelText('Implementer model profile'), { target: { value: 'strict' } })
  expect(row('implementer')).toContain('strict')
  expect(row('implementer')).toContain('This handoff')
  // The reviewer is untouched by the implementer's own choice.
  expect(row('reviewer')).toContain('fast')
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalled())
  expect(sent(0).config.implementerRouting).toEqual({ profile: 'strict' })
  expect('reviewerRouting' in sent(0).config).toBe(false)
  // Each role returns to following on its own.
  await fireEvent.change(screen.getByLabelText('Implementer model profile'), { target: { value: '' } })
  expect(row('implementer')).toContain('careful')
  expect(row('implementer')).toContain('Project factory default')
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalledTimes(2))
  expect('implementerRouting' in sent(1).config).toBe(false)
  expect('reviewerRouting' in sent(1).config).toBe(false)
})

it('nativeIgnoresAgentWorkspace', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({ workspace: 'go-dev', routing: { profile: 'fast' }, factoryDefaults: { expectedChecks: [BUILD] } }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  expect(row('environment')).toContain('Project agent default')
  await fillHandoff()
  await fireEvent.click(screen.getByLabelText('Native environment'))
  expect(row('environment')).toContain('Native')
  // A project agent workspace is suppressed, not a conflict.
  expect(screen.queryByTestId('workspace-conflict')).toBeNull()
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalled())
  expect(sent().config.native).toBe(true)
  expect('workspace' in sent().config).toBe(false)
})

it('nativeConflictsWithFactoryWorkspace', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    factoryDefaults: { workspace: 'ci-dev', expectedChecks: [BUILD] },
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('environment')).toContain('ci-dev'))
  expect(row('environment')).toContain('Project factory default')
  await fillHandoff()
  await fireEvent.click(screen.getByLabelText('Native environment'))
  const conflict = screen.getByTestId('workspace-conflict')
  expect(conflict.textContent).toContain('ci-dev')
  expect(conflict.textContent).toContain('native')
  await save()
  expect((await screen.findByRole('alert')).textContent).toContain('ci-dev')
  expect(api.createFactoryItem).not.toHaveBeenCalled()
  // Choosing the non-native environment resolves it, and the workspace is
  // inherited rather than sent.
  await fireEvent.click(screen.getByLabelText('Native environment'))
  expect(screen.queryByTestId('workspace-conflict')).toBeNull()
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalled())
  expect(sent().config.native).toBe(false)
  expect('workspace' in sent().config).toBe(false)
})

it('distinguishesInheritedAndExplicitEmptyChecks', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    workspace: 'go-dev',
    routing: { profile: 'fast' },
    factoryDefaults: { expectedChecks: [BUILD] },
  }))
  vi.mocked(api.createFactoryItem).mockRejectedValue(new Error('still checking'))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('expected-checks')).toContain('check_run:build:7'))
  await fillHandoff()
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalledTimes(1))
  // Inheritance omits the field entirely, so the project default stays in force.
  expect('expectedChecks' in sent(0).config).toBe(false)
  expect(sent(0).config.noRequiredCiAcknowledged).toBe(false)
  // The deliberate "none" choice is an explicit empty list, and needs the
  // per-handoff acknowledgement.
  await fireEvent.change(screen.getByLabelText('Expected checks source'), { target: { value: 'none' } })
  expect(row('expected-checks')).toContain('no required checks')
  await save()
  expect((await screen.findByRole('alert')).textContent).toContain('no required CI')
  expect(api.createFactoryItem).toHaveBeenCalledTimes(1)
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalledTimes(2))
  expect(sent(1).config.expectedChecks).toEqual([])
  expect(sent(1).config.noRequiredCiAcknowledged).toBe(true)
})

it('upsertsTheSavedDraftIntoTheSharedStore', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({ workspace: 'go-dev', routing: { profile: 'fast' } }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn(), upsert })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  await fillHandoff()
  // Nothing is inherited for checks, so the handoff acknowledges it explicitly.
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await save()
  await screen.findByTestId('draft-preview')
  // The shared store is the list every view reads, so the saved draft is
  // published as the response lands — not left for the next list read.
  expect(upsert).toHaveBeenCalledTimes(1)
  expect(upsert).toHaveBeenCalledWith(resolvedItem())
})

it('usesResolvedServerDraft', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({ workspace: 'go-dev', routing: { profile: 'fast' } }))
  // The bridge resolved more than the form sent: the preview shows its answer.
  vi.mocked(api.createFactoryItem).mockResolvedValue(resolvedItem({
    targetBranch: 'release',
    workspace: 'ci-dev',
    native: false,
    implementerRouting: { profile: 'careful' },
    reviewerRouting: { profile: 'strict' },
    expectedChecks: [BUILD, { kind: 'commit_status', name: 'ci/lint', appId: 0 }],
    maxRounds: 9,
    ciPollSeconds: 30,
    ciWaitSeconds: 60,
    noRequiredCiAcknowledged: true,
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  await fillHandoff()
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await save()
  const preview = await screen.findByTestId('draft-preview')
  expect(screen.getByTestId('config-target-branch').textContent).toBe('release')
  expect(screen.getByTestId('config-environment').textContent).toBe('ci-dev')
  expect(screen.getByTestId('config-implementer').textContent).toBe('careful')
  expect(screen.getByTestId('config-reviewer').textContent).toBe('strict')
  expect(screen.getByTestId('config-total-rounds').textContent).toBe('9')
  // Every resolved check is shown before approval, not just a count.
  expect(screen.getByTestId('config-expected-checks').textContent).toContain('check_run:build:7')
  expect(screen.getByTestId('config-expected-checks').textContent).toContain('commit_status:ci/lint')
  expect(preview.textContent).toContain('30s / 60s')
})

it('savedDraftUsesServerResolvedRows', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({ workspace: 'go-dev', routing: { profile: 'fast' } }))
  // The saved draft's snapshot differs from the form in every field it can.
  vi.mocked(api.createFactoryItem).mockResolvedValue(resolvedItem({
    targetBranch: 'release',
    workspace: 'ci-dev',
    native: false,
    implementerRouting: { profile: 'careful' },
    reviewerRouting: { profile: 'strict' },
    reviewerInstructions: 'Check the migration',
    expectedChecks: [BUILD, { kind: 'commit_status', name: 'ci/lint', appId: 0 }],
    implementerAuto: true,
    noRequiredCiAcknowledged: true,
    maxRounds: 9,
    ciPollSeconds: 30,
    ciWaitSeconds: 60,
  }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  await fillHandoff()
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await save()
  const preview = await screen.findByTestId('draft-preview')
  // Every approval field is shown from the server's resolved snapshot, not
  // from the form's own effective values, and none of it is raw JSON.
  expect(screen.getByTestId('config-repository').textContent).toBe('repo')
  expect(screen.getByTestId('config-project').textContent).toBe(ROOT)
  expect(screen.getByTestId('config-target-branch').textContent).toBe('release')
  expect(screen.getByTestId('config-host').textContent).toBe('local')
  expect(screen.getByTestId('config-environment').textContent).toBe('ci-dev')
  expect(screen.getByTestId('config-implementer').textContent).toBe('careful')
  expect(screen.getByTestId('config-reviewer').textContent).toBe('strict')
  expect(screen.getByTestId('config-reviewer-instructions').textContent).toBe('Check the migration')
  expect(screen.getByTestId('config-expected-checks').textContent).toBe('check_run:build:7, commit_status:ci/lint')
  expect(screen.getByTestId('config-auto-permission').textContent).toBe('Automatic')
  expect(screen.getByTestId('config-total-rounds').textContent).toBe('9')
  expect(screen.getByTestId('config-ci-poll-wait').textContent).toBe('30s / 60s')
  // The plan is readable in place, and the routing objects never appear.
  expect(screen.getByTestId('summary-plan').textContent).toBe('Build it')
  expect(preview.textContent).not.toContain('"profile"')
  expect(preview.textContent).not.toContain('{')
})

it('preservesInputOnErrors', async () => {
  vi.mocked(api.getProjectSettings).mockRejectedValue(new Error('settings unavailable'))
  vi.mocked(api.createFactoryItem).mockRejectedValue(new Error('network down'))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  expect((await screen.findByTestId('settings-warning')).textContent).toContain('settings unavailable')
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Task' } })
  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Build it' } })
  await fireEvent.input(screen.getByLabelText('Specification (optional)'), { target: { value: 'Keep this' } })
  // Nothing was inherited, so both profiles and the acknowledgement are chosen.
  await fireEvent.change(screen.getByLabelText('Implementer model profile'), { target: { value: 'fast' } })
  await fireEvent.change(screen.getByLabelText('Reviewer model profile'), { target: { value: 'fast' } })
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await save()
  await screen.findByText(/network down/)
  // A failed read and a failed save both leave the handoff as it was.
  expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Task')
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('Build it')
  expect((screen.getByLabelText('Specification (optional)') as HTMLTextAreaElement).value).toBe('Keep this')
})

it('reusesRequestIdOnIdenticalRetry', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({
    routing: { profile: 'fast' },
    factoryDefaults: { expectedChecks: [BUILD] },
  }))
  vi.mocked(api.createFactoryItem).mockRejectedValue(new Error('offline'))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn() })
  await waitFor(() => expect(row('implementer')).toContain('fast'))
  await fillHandoff()
  await save()
  await screen.findByText(/offline/)
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalledTimes(2))
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalledTimes(3))
  const ids = vi.mocked(api.createFactoryItem).mock.calls.map((call) => call[0].requestId)
  expect(ids[0]).toBeTruthy()
  expect(ids[1]).toBe(ids[0])
  expect(ids[2]).toBe(ids[0])
  // A changed handoff is a new request, not a replay of the old one.
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Task two' } })
  await save()
  await waitFor(() => expect(api.createFactoryItem).toHaveBeenCalledTimes(4))
  expect(vi.mocked(api.createFactoryItem).mock.calls[3][0].requestId).not.toBe(ids[0])
})

it('registersAndClearsDirtyGuard', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
  const { guard, registerGuard } = wiredGuard()
  const { unmount } = render(FactoryNew, { onNavigate: vi.fn(), registerGuard })
  await screen.findByRole('option', { name: 'repo' })
  // An untouched handoff registers itself, and leaving asks nothing.
  expect(registerGuard).toHaveBeenCalledTimes(1)
  expect(guard.allow()).toBe(true)
  expect(confirm).not.toHaveBeenCalled()
  await fillHandoff()
  expect(guard.allow()).toBe(false)
  expect(confirm).toHaveBeenCalledWith('You have an unsaved factory handoff. Leave without saving it?')
  // A screen that is gone answers for nothing.
  unmount()
  expect(registerGuard).toHaveBeenLastCalledWith(undefined)
  expect(guard.allow()).toBe(true)
  confirm.mockRestore()
})

it('savedDraftClearsNavigationWarning', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
  const { guard, registerGuard } = wiredGuard()
  vi.mocked(api.getProjectSettings).mockResolvedValue(projectSettings({ workspace: 'go-dev', routing: { profile: 'fast' } }))
  render(FactoryNew, { projectRoot: ROOT, onNavigate: vi.fn(), registerGuard })
  await waitFor(() => expect(row('environment')).toContain('go-dev'))
  await fillHandoff()
  // Nothing is inherited for checks, so the handoff acknowledges it explicitly.
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  expect(guard.allow()).toBe(false)
  await save()
  await screen.findByTestId('draft-preview')
  // A saved draft is not unsaved input: leaving it, or reloading, is free.
  const unload = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(unload)
  expect(unload.defaultPrevented).toBe(false)
  expect(guard.allow()).toBe(true)
  expect(confirm).toHaveBeenCalledTimes(1)
  confirm.mockRestore()
})

it('cancelledNavigationKeepsAllInput', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
  const { guard, registerGuard } = wiredGuard()
  const onNavigate = vi.fn()
  render(FactoryNew, { onNavigate, registerGuard })
  await screen.findByRole('option', { name: 'repo' })
  await fillHandoff()
  await fireEvent.input(screen.getByLabelText('Specification (optional)'), { target: { value: 'Keep this' } })
  // The shell asks the guard before it changes the route; a refusal leaves
  // the handoff exactly as it was.
  expect(guard.allow()).toBe(false)
  expect(confirm).toHaveBeenCalledTimes(1)
  expect(onNavigate).not.toHaveBeenCalled()
  expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Task')
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('Build it')
  expect((screen.getByLabelText('Specification (optional)') as HTMLTextAreaElement).value).toBe('Keep this')
  confirm.mockRestore()
})
