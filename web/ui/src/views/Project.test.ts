import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Project from './Project.svelte'
import * as api from '../lib/api.js'
import { toRow } from '../lib/fleet'
import { createNavigationGuard, type RouteLeaveGuard } from '../lib/navigationGuard'
import { healthChecks, latestAgent, parseWorkspaceRef, autoFrom, autoOf } from '../lib/project/model'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    getVersionedProjectSettings: vi.fn(),
    putVersionedProjectSettings: vi.fn(),
    getProjectHealth: vi.fn(),
    getWorkspacePolicy: vi.fn(),
    listWorkspaces: vi.fn(),
    listRepos: vi.fn(),
    listClients: vi.fn(),
    getModels: vi.fn(),
  }
})

afterEach(cleanup)

const ROOT = '/home/u/alpha'
const settings: api.ProjectSettings = { workspace: 'go-dev', mode: 'ask', routing: { profile: 'fast', overrides: { reviewer: 'big' } }, intake: { repoId: 'r1', labels: ['agent'], clients: ['c1'] } }
const healthy: api.ProjectHealth = { gateRunnable: 'yes', mirrorFresh: [{ repoId: 'r1', present: true, ageSeconds: 120, head: 'main' }], orphanWorktrees: [], trust: 'trusted', workspaceResolves: { ref: 'go-dev', resolves: true, built: true } }

/** The versioned record a read or write returns; the etag is opaque to the client. */
const versioned = (s: api.ProjectSettings, etag = '"v1"') => ({ settings: s, etag })

const mount = (over: Record<string, unknown> = {}) => render(Project, { root: ROOT, agents: [], onNavigate: vi.fn(), ...over })

/** The body of the nth settings write. */
const written = (n = 0) => (api.putVersionedProjectSettings as Mock).mock.calls[n][1] as api.ProjectSettings

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned(settings))
  ;(api.putVersionedProjectSettings as Mock).mockImplementation(async (_r: string, s: api.ProjectSettings, etag: string) => versioned(s, `${etag}+`))
  ;(api.getProjectHealth as Mock).mockResolvedValue(healthy)
  ;(api.getWorkspacePolicy as Mock).mockResolvedValue({ mode: 'ask', allow: ['go test ./...'] })
  ;(api.listWorkspaces as Mock).mockResolvedValue([{ source: 'studio', name: 'go-dev' }, { source: 'studio', name: 'node' }, { source: 'repo', name: 'ci', project: ROOT }])
  ;(api.listRepos as Mock).mockResolvedValue([{ id: 'r1', url: 'u' }, { id: 'r2', url: 'u2' }])
  ;(api.listClients as Mock).mockResolvedValue([{ id: 'c1', name: 'Cursor' }, { id: 'c2', name: 'Zed' }])
  ;(api.getModels as Mock).mockResolvedValue({ profiles: { fast: {}, careful: {} } })
})

describe('Project automations tab', () => {
  it('links to both automations pages with their on/off state', async () => {
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({ ...settings, automations: { reviewBot: { enabled: true, repoId: 'r1', skipDrafts: true, autoPost: false, holdSeverities: [] } } }))
    const onNavigate = vi.fn()
    mount({ onNavigate })
    await fireEvent.click(screen.getByRole('tab', { name: 'Automations' }))
    await waitFor(() => expect(within(screen.getByTestId('automations-tab')).getAllByText('on')).toHaveLength(1))
    expect(within(screen.getByTestId('automations-tab')).getByText('off')).toBeTruthy()
    await fireEvent.click(screen.getByRole('link', { name: 'Open CI fixer' }))
    expect(onNavigate).toHaveBeenCalledWith('#projects/%2Fhome%2Fu%2Falpha/automations/ci-fixer')
  })
})

describe('Project overview', () => {
  it('loads the saved settings into the grouped workspace, model and intake cards', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
    expect(api.getVersionedProjectSettings).toHaveBeenCalledWith(ROOT)
    await waitFor(() => expect((screen.getByLabelText('Workspace') as HTMLSelectElement).value).toBe('go-dev'))
    await waitFor(() => expect([...(screen.getByLabelText('Workspace') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'go-dev', 'node', 'repo:ci']))
    expect((screen.getByLabelText('Model profile') as HTMLSelectElement).value).toBe('fast')
    expect((screen.getByLabelText('Intake repo') as HTMLSelectElement).value).toBe('r1')
    expect(screen.getByText('agent')).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: 'Cursor' }) as HTMLInputElement).checked).toBe(true)
    expect((screen.getByRole('checkbox', { name: 'Zed' }) as HTMLInputElement).checked).toBe(false)
  })

  it('saves defaults with the right body and the version it read', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('Workspace') as HTMLSelectElement).options.length).toBe(4))
    await fireEvent.change(screen.getByLabelText('Workspace'), { target: { value: 'repo:ci' } })
    await fireEvent.change(screen.getByLabelText('Model profile'), { target: { value: 'careful' } })
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'act' } })
    await fireEvent.change(screen.getByLabelText('Auto'), { target: { value: 'off' } })
    await fireEvent.change(screen.getByLabelText('Isolation'), { target: { value: 'isolated' } })
    await fireEvent.change(screen.getByLabelText('Ship target'), { target: { value: 'patch' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    expect(api.putVersionedProjectSettings).toHaveBeenCalledWith(ROOT, {
      workspace: 'repo:ci',
      routing: { profile: 'careful', overrides: { reviewer: 'big' } },
      mode: 'act',
      auto: false,
      isolated: true,
      shipTarget: 'patch',
      intake: { repoId: 'r1', labels: ['agent'], clients: ['c1'] },
    }, '"v1"')
    expect((await screen.findByRole('status')).textContent).toBe('Defaults saved')
  })

  it('leaves unset defaults out of the body', async () => {
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({ intake: {} }))
    mount()
    // The save is only enabled once the record (and its version) arrived.
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save defaults' }) as HTMLButtonElement).disabled).toBe(false))
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    // What goes on the wire: nothing but the intake.
    expect(JSON.parse(JSON.stringify(written()))).toEqual({ intake: {} })
  })

  it('saves intake: repo, labels as chips, allowed clients', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('Intake repo') as HTMLSelectElement).value).toBe('r1'))
    await waitFor(() => expect(screen.getByRole('checkbox', { name: 'Zed' })).toBeTruthy())
    await fireEvent.input(screen.getByLabelText('New label'), { target: { value: 'bug' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    // One label per repo: adding replaces the saved one.
    expect(screen.queryByText('agent')).toBeNull()
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Zed' }))
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Cursor' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Save intake' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    expect(written().intake).toEqual({ repoId: 'r1', labels: ['bug'], clients: ['c2'] })
  })

  it('shows a refused save, such as an unregistered repo, and keeps the form', async () => {
    ;(api.putVersionedProjectSettings as Mock).mockRejectedValue(new api.APIError(400, { error: 'intake.repoId "r9" is not a registered repo' }))
    mount()
    // Both repos are selectable once the picker has loaded.
    await waitFor(() => expect([...(screen.getByLabelText('Intake repo') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'r1', 'r2']))
    await fireEvent.change(screen.getByLabelText('Intake repo'), { target: { value: 'r2' } })
    expect((screen.getByLabelText('Intake repo') as HTMLSelectElement).value).toBe('r2')
    await fireEvent.click(screen.getByRole('button', { name: 'Save intake' }))
    expect((await screen.findByRole('alert')).textContent).toContain('is not a registered repo')
    // A failed write keeps every edit.
    expect((screen.getByLabelText('Intake repo') as HTMLSelectElement).value).toBe('r2')
  })

  it('renders the health checks with a dot each, and Refresh fetches again', async () => {
    ;(api.getProjectHealth as Mock).mockResolvedValueOnce({ ...healthy, gateRunnable: 'no', orphanWorktrees: ['wt-1'], mirrorFresh: [{ repoId: 'r1', present: false }] })
    mount()
    const rows = await screen.findAllByTestId('health-check')
    const dotOf = (label: string) => within(rows.find((r) => r.textContent?.includes(label))!).getByRole('img').getAttribute('aria-label')
    expect(dotOf('Verify gate')).toBe('err')
    expect(dotOf('Mirror r1')).toBe('err')
    expect(dotOf('Orphan worktrees')).toBe('warn')
    expect(dotOf('Folder trust')).toBe('ok')
    expect(dotOf('Default workspace')).toBe('ok')
    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(api.getProjectHealth).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(within(screen.getAllByTestId('health-check').find((r) => r.textContent?.includes('Verify gate'))!).getByRole('img').getAttribute('aria-label')).toBe('ok'))
  })

  it('shows the default workspace policy read-only with a designer link', async () => {
    const onNavigate = vi.fn()
    mount({ onNavigate })
    const card = await screen.findByTestId('policy-card')
    await waitFor(() => expect(card.textContent).toContain('go test ./...'))
    expect(api.getWorkspacePolicy).toHaveBeenCalledWith('go-dev', undefined)
    expect(within(card).queryByRole('textbox')).toBeNull()
    await fireEvent.click(within(card).getByRole('link', { name: 'Edit in designer' }))
    expect(onNavigate).toHaveBeenCalledWith('#workspaces/go-dev/edit')
  })

  it('says so when the workspace is a repo template or none is set', async () => {
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({ workspace: 'repo:ci', intake: {} }))
    const { unmount } = mount()
    await waitFor(() => expect(screen.getByTestId('policy-card').textContent).toContain('comes from the repo'))
    expect(api.getWorkspacePolicy).not.toHaveBeenCalled()
    expect(screen.queryByRole('link', { name: 'Edit in designer' })).toBeNull()
    unmount()
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({ intake: {} }))
    mount()
    await waitFor(() => expect(screen.getByTestId('policy-card').textContent).toContain('No default workspace'))
  })
})

describe('Project factory and agent defaults', () => {
  it('linksToHandoffAndScopedFactory', async () => {
    const onNavigate = vi.fn()
    mount({ onNavigate })
    // The header carries both factory destinations: creating work for this
    // project, and the project's own factory dashboard.
    await fireEvent.click(await screen.findByRole('link', { name: /Create factory work item for this project/ }))
    expect(onNavigate).toHaveBeenLastCalledWith('#factory/new?project=%2Fhome%2Fu%2Falpha')
    await fireEvent.click(screen.getByRole('link', { name: 'Open project factory' }))
    expect(onNavigate).toHaveBeenLastCalledWith('#projects/%2Fhome%2Fu%2Falpha/factory')
  })

  it('followsAgentDefaultsUntilOverridden', async () => {
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({ workspace: 'go-dev', routing: { profile: 'fast' }, intake: {} }))
    mount()
    await waitFor(() => expect(screen.getByTestId('factory-workspace-effective').textContent).toContain('Following the project default: go-dev'))
    expect(screen.getByTestId('implementer-effective').textContent).toContain('Following the project default: fast')
    expect(screen.getByTestId('reviewer-effective').textContent).toContain('Following the project default: fast')
    // Moving the agent default moves what the factory follows.
    await waitFor(() => expect([...(screen.getByLabelText('Workspace') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'go-dev', 'node', 'repo:ci']))
    await fireEvent.change(screen.getByLabelText('Workspace'), { target: { value: 'node' } })
    await waitFor(() => expect(screen.getByTestId('factory-workspace-effective').textContent).toContain('Following the project default: node'))
    // Following writes no factory field at all.
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    expect(written().workspace).toBe('node')
    expect(written().factoryDefaults).toBeUndefined()
    // An explicit choice overrides just that field, and says so.
    await waitFor(() => expect([...(screen.getByLabelText('Factory workspace') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'go-dev', 'node', 'repo:ci']))
    await fireEvent.change(screen.getByLabelText('Factory workspace'), { target: { value: 'repo:ci' } })
    expect(screen.getByTestId('factory-workspace-effective').textContent).toContain('repo:ci (own choice)')
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(2))
    expect(written(1).factoryDefaults).toEqual({ workspace: 'repo:ci' })
    // The second write names the version the first write returned, not the one
    // the page originally read: the token follows every successful save.
    expect((api.putVersionedProjectSettings as Mock).mock.calls[1][2]).toBe('"v1"+')
  })

  it('aSaveInFlightDoesNotRevertWhatWasTypedAfterIt', async () => {
    let release!: (v: unknown) => void
    ;(api.putVersionedProjectSettings as Mock).mockImplementationOnce(
      () => new Promise((resolve) => { release = resolve }),
    )
    mount()
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'act' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(1))

    // The user keeps typing while the write is still in flight.
    await fireEvent.change(screen.getByLabelText('Ship target'), { target: { value: 'patch' } })
    expect((screen.getByLabelText('Ship target') as HTMLSelectElement).value).toBe('patch')

    // The response describes what was sent, so adopting it wholesale would
    // silently undo the Ship target edit made after the request left.
    release(versioned({ ...settings, mode: 'act' }, '"v1+"'))
    await waitFor(() => expect(screen.queryByRole('status')).not.toBeNull())
    expect((screen.getByLabelText('Ship target') as HTMLSelectElement).value).toBe('patch')
    expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('act')

    // The stored record and the version token still advanced, so the next
    // write is not refused as stale.
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(2))
    expect((api.putVersionedProjectSettings as Mock).mock.calls[1][2]).toBe('"v1+"')
    expect(written(1).shipTarget).toBe('patch')
  })

  it('roleOverridesIndependentAndReset', async () => {
    mount()
    await waitFor(() => expect([...(screen.getByLabelText('Implementer profile') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'careful', 'fast']))
    expect((screen.getByLabelText('Model profile') as HTMLSelectElement).value).toBe('fast')
    expect((screen.getByLabelText('Implementer profile') as HTMLSelectElement).value).toBe('')
    expect(screen.getByTestId('implementer-effective').textContent).toContain('Following the project default: fast')
    await fireEvent.change(screen.getByLabelText('Implementer profile'), { target: { value: 'careful' } })
    expect(screen.getByTestId('implementer-effective').textContent).toContain('careful (own choice)')
    // The reviewer is untouched by the implementer's choice.
    expect(screen.getByTestId('reviewer-effective').textContent).toContain('Following the project default: fast')
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    expect(written().factoryDefaults).toEqual({ implementerRouting: { profile: 'careful' } })
    // Each role resets to following on its own.
    await fireEvent.change(screen.getByLabelText('Implementer profile'), { target: { value: '' } })
    await fireEvent.change(screen.getByLabelText('Reviewer profile'), { target: { value: 'careful' } })
    await waitFor(() => expect(screen.getByTestId('implementer-effective').textContent).toContain('Following the project default: fast'))
    expect(screen.getByTestId('reviewer-effective').textContent).toContain('careful (own choice)')
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(2))
    expect(written(1).factoryDefaults).toEqual({ reviewerRouting: { profile: 'careful' } })
  })

  it('preservesNestedRoutingAndOtherSettings', async () => {
    const rich = {
      workspace: 'go-dev',
      routing: { profile: 'fast', overrides: { reviewer: 'big' }, futureMember: { nested: true } },
      intake: { repoId: 'r1', labels: ['agent'] },
      automations: { reviewBot: { enabled: true, repoId: 'r1', skipDrafts: true, autoPost: false, holdSeverities: [] } },
      futureSetting: { keep: ['me'] },
      factoryDefaults: { targetBranch: 'develop', implementerRouting: { profile: 'fast', overrides: { reviewer: 'big' }, futureMember: { nested: true } } },
    } as unknown as api.ProjectSettings
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned(rich))
    mount()
    await waitFor(() => expect([...(screen.getByLabelText('Model profile') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'careful', 'fast']))
    await fireEvent.change(screen.getByLabelText('Model profile'), { target: { value: 'careful' } })
    await fireEvent.change(screen.getByLabelText('Implementer profile'), { target: { value: 'careful' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    const body = written()
    // The whole routing object survives a profile change, unknown members included.
    expect(body.routing).toEqual({ profile: 'careful', overrides: { reviewer: 'big' }, futureMember: { nested: true } })
    const factory = body.factoryDefaults ?? {}
    expect(factory.implementerRouting).toEqual({ profile: 'careful', overrides: { reviewer: 'big' }, futureMember: { nested: true } })
    expect(factory.targetBranch).toBe('develop')
    // Settings this screen does not model are carried through untouched.
    expect(body.intake).toEqual(rich.intake)
    expect(body.automations).toEqual(rich.automations)
    expect((body as unknown as Record<string, unknown>).futureSetting).toEqual({ keep: ['me'] })
  })

  it('keepsARoleRouteThatHasNoProfileWhenSomethingElseIsSaved', async () => {
    // A stored role route need not carry a profile: it can hold overrides, and
    // members a build does not know. Saving an unrelated field must keep it —
    // an absent profile is not the user asking to delete the route.
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({
      workspace: 'go-dev',
      intake: {},
      factoryDefaults: {
        implementerRouting: { overrides: { reviewer: 'big' }, futureMember: { nested: true } },
        reviewerRouting: { overrides: { implementer: 'small' } },
      },
    } as unknown as api.ProjectSettings))
    mount()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save defaults' }) as HTMLButtonElement).disabled).toBe(false))
    await fireEvent.change(screen.getByLabelText('Ship target'), { target: { value: 'patch' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    const defaults = written().factoryDefaults ?? {}
    expect(defaults.implementerRouting).toEqual({ overrides: { reviewer: 'big' }, futureMember: { nested: true } })
    expect(defaults.reviewerRouting).toEqual({ overrides: { implementer: 'small' } })
    // No profile member is invented for a route that never had one.
    expect(defaults.implementerRouting).not.toHaveProperty('profile')
    expect(defaults.reviewerRouting).not.toHaveProperty('profile')
  })

  it('unavailableChoicesRemainVisible', async () => {
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({
      workspace: 'gone-ws',
      routing: { profile: 'gone-profile' },
      intake: {},
      factoryDefaults: { workspace: 'repo:gone', implementerRouting: { profile: 'gone-impl' }, hostId: 'remote' },
    }))
    mount()
    await waitFor(() => expect((screen.getByLabelText('Workspace') as HTMLSelectElement).value).toBe('gone-ws'))
    const agentWs = screen.getByLabelText('Workspace') as HTMLSelectElement
    expect([...agentWs.options].map((o) => o.textContent)).toContain('gone-ws (unavailable)')
    const factoryWs = screen.getByLabelText('Factory workspace') as HTMLSelectElement
    expect(factoryWs.value).toBe('repo:gone')
    expect([...factoryWs.options].map((o) => o.textContent)).toContain('repo:gone (unavailable)')
    expect((screen.getByLabelText('Implementer profile') as HTMLSelectElement).value).toBe('gone-impl')
    expect(screen.getByTestId('implementer-effective').textContent).toContain('gone-impl (own choice)')
    expect(screen.getByTestId('factory-host').textContent).toContain('remote')
    // Saving keeps them rather than clearing what it cannot offer.
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalled())
    expect(written().workspace).toBe('gone-ws')
    expect(written().routing).toEqual({ profile: 'gone-profile' })
    expect(written().factoryDefaults).toMatchObject({ workspace: 'repo:gone', hostId: 'remote', implementerRouting: { profile: 'gone-impl' } })
  })

  it('failedLoadCannotSave', async () => {
    ;(api.getVersionedProjectSettings as Mock).mockRejectedValue(new api.APIError(503, { error: 'settings unavailable' }))
    mount()
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('settings unavailable'))
    expect((screen.getByRole('button', { name: 'Save defaults' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Save intake' }) as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    expect(api.putVersionedProjectSettings).not.toHaveBeenCalled()
  })

  it('staleSaveRetainsDraft', async () => {
    ;(api.putVersionedProjectSettings as Mock).mockRejectedValue(new api.APIError(412, { error: 'project settings changed' }))
    mount()
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'act' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('changed elsewhere')
    // The edit survives the refusal, and nothing was silently retried.
    expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('act')
    expect(screen.getByTestId('unsaved')).toBeTruthy()
    expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(1)
    // Only an explicit reload replaces the draft, and it adopts what it reads.
    await fireEvent.click(within(alert).getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(api.getVersionedProjectSettings).toHaveBeenCalledTimes(2))
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
  })

  it('factoryFieldsRoundTrip', async () => {
    const defaults: NonNullable<api.ProjectSettings['factoryDefaults']> = {
      targetBranch: 'develop',
      workspace: 'repo:ci',
      implementerRouting: { profile: 'careful' },
      reviewerRouting: { profile: 'fast' },
      reviewerInstructions: 'Be strict',
      expectedChecks: [{ kind: 'check_run', name: 'build', appId: 7 }, { kind: 'commit_status', name: 'lint', appId: 0 }],
      maxRounds: 4,
      ciPollSeconds: 20,
      ciWaitSeconds: 900,
    }
    ;(api.getVersionedProjectSettings as Mock).mockResolvedValue(versioned({ intake: {}, factoryDefaults: defaults }))
    mount()
    await waitFor(() => expect((screen.getByLabelText('Target branch') as HTMLInputElement).value).toBe('develop'))
    expect((screen.getByLabelText('Expected CI checks') as HTMLTextAreaElement).value).toBe('check_run:build:7\ncommit_status:lint')
    expect((screen.getByLabelText('Reviewer instructions') as HTMLTextAreaElement).value).toBe('Be strict')
    expect((screen.getByLabelText('Max rounds') as HTMLInputElement).value).toBe('4')
    expect((screen.getByLabelText('CI poll seconds') as HTMLInputElement).value).toBe('20')
    expect((screen.getByLabelText('CI wait seconds') as HTMLInputElement).value).toBe('900')
    // The handoff form's check syntax is enforced here too.
    await fireEvent.input(screen.getByLabelText('Expected CI checks'), { target: { value: 'workflow:build' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    expect((await screen.findByRole('alert')).textContent).toContain('check_run:name:appId or commit_status:context')
    expect(api.putVersionedProjectSettings).not.toHaveBeenCalled()
    // The whole record round trips unchanged.
    await fireEvent.input(screen.getByLabelText('Expected CI checks'), { target: { value: 'check_run:build:7\ncommit_status:lint' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(1))
    expect(written().factoryDefaults).toEqual(defaults)
    // A cleared field is deleted, not replaced by a fallback.
    await fireEvent.input(screen.getByLabelText('Target branch'), { target: { value: '' } })
    await fireEvent.input(screen.getByLabelText('Max rounds'), { target: { value: '' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putVersionedProjectSettings).toHaveBeenCalledTimes(2))
    const cleared = written(1).factoryDefaults ?? {}
    expect(cleared.targetBranch).toBeUndefined()
    expect(cleared.maxRounds).toBeUndefined()
    expect(cleared.reviewerInstructions).toBe('Be strict')
  })

})

describe('Project route guard', () => {
  /** The shell's guard, plus the registration calls the view makes on it. */
  const wired = () => {
    const guard = createNavigationGuard()
    const registerGuard = vi.fn((fn?: RouteLeaveGuard) => guard.register(fn))
    return { guard, registerGuard }
  }

  it('registersAndClearsDirtyGuard', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { guard, registerGuard } = wired()
    const { unmount } = mount({ registerGuard })
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
    // A pristine screen registers itself, and leaving asks nothing.
    expect(registerGuard).toHaveBeenCalledTimes(1)
    expect(guard.allow()).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
    // An edit is protected by the shell's guard and by the browser's warning.
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'act' } })
    await waitFor(() => expect(screen.getByTestId('unsaved')).toBeTruthy())
    const unload = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(unload)
    expect(unload.defaultPrevented).toBe(true)
    expect(guard.allow()).toBe(false)
    expect(confirm).toHaveBeenCalledWith('You have unsaved project settings. Leave without saving them?')
    // A screen that is gone answers for nothing.
    unmount()
    expect(registerGuard).toHaveBeenLastCalledWith(undefined)
    expect(guard.allow()).toBe(true)
    confirm.mockRestore()
  })

  it('savedDraftClearsNavigationWarning', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { guard, registerGuard } = wired()
    mount({ registerGuard })
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'act' } })
    await waitFor(() => expect(screen.getByTestId('unsaved')).toBeTruthy())
    expect(guard.allow()).toBe(false)
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(screen.queryByTestId('unsaved')).toBeNull())
    // The stored record matches the drafts again, so leaving is free.
    expect(guard.allow()).toBe(true)
    expect(confirm).toHaveBeenCalledTimes(1)
    confirm.mockRestore()
  })

  it('cancelledNavigationKeepsAllInput', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { guard, registerGuard } = wired()
    const onNavigate = vi.fn()
    mount({ onNavigate, registerGuard })
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('ask'))
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'act' } })
    await fireEvent.change(screen.getByLabelText('Ship target'), { target: { value: 'patch' } })
    // The shell asks the guard before it changes the route; a refusal leaves
    // the route alone.
    expect(guard.allow()).toBe(false)
    expect(onNavigate).not.toHaveBeenCalled()
    // Every draft is still as the person left it, and nothing was written.
    expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('act')
    expect((screen.getByLabelText('Ship target') as HTMLSelectElement).value).toBe('patch')
    expect(api.putVersionedProjectSettings).not.toHaveBeenCalled()
    confirm.mockRestore()
  })
})

describe('Project session sheet tab', () => {
  const rows = [
    toRow({ id: 'old', project: ROOT, status: 'idle', updatedAt: '2026-10-01T00:00:00Z', name: 'old-agent' }),
    toRow({ id: 'new', project: ROOT, status: 'running', updatedAt: '2026-10-04T00:00:00Z', name: 'new-agent', contextPct: 10, changedFiles: 1 }),
    toRow({ id: 'other', project: '/home/u/beta', status: 'running', updatedAt: '2026-10-05T00:00:00Z', name: 'other-agent' }),
  ]

  it("shows the project's most recent agent's telemetry, read-only", async () => {
    const telemetry = { new: { contextPct: 64, changedFiles: 3, toolStats: [{ name: 'shell.run', calls: 7, errors: 1, slowestMs: 950 }], rules: ['no-network'], at: 1 } }
    mount({ agents: rows, telemetry })
    await fireEvent.click(screen.getByRole('tab', { name: 'Session sheet' }))
    const sheet = screen.getByTestId('session-sheet')
    expect(sheet.textContent).toContain('new-agent')
    expect(screen.getByTestId('context-pct').textContent).toBe('64% used')
    expect(screen.getByTestId('changed-files').textContent).toBe('3 files')
    expect(sheet.textContent).toContain('shell.run')
    expect(sheet.textContent).toContain('(1 failed)')
    expect(sheet.textContent).toContain('slowest 950 ms')
    expect(sheet.textContent).toContain('no-network')
    expect(within(sheet).queryByRole('button')).toBeNull()
  })

  it('falls back to the row counts when no telemetry delta arrived, and says what is not reported', async () => {
    mount({ agents: rows })
    await fireEvent.click(screen.getByRole('tab', { name: 'Session sheet' }))
    expect(screen.getByTestId('context-pct').textContent).toBe('10% used')
    expect(screen.getByTestId('changed-files').textContent).toBe('1 file')
    expect(screen.getAllByText('Not reported yet.')).toHaveLength(2)
  })

  it('says so when the project has no agent', async () => {
    mount({ agents: [rows[2]] })
    await fireEvent.click(screen.getByRole('tab', { name: 'Session sheet' }))
    expect(screen.getByText('No agent has run in this project yet.')).toBeTruthy()
  })
})

describe('project model helpers', () => {
  it('parses workspace refs', () => {
    expect(parseWorkspaceRef('go-dev')).toEqual({ source: 'studio', name: 'go-dev' })
    expect(parseWorkspaceRef('go-dev@3')).toEqual({ source: 'studio', name: 'go-dev', version: 3 })
    expect(parseWorkspaceRef('repo:ci')).toEqual({ source: 'repo', name: 'ci' })
  })
  it('picks the latest agent of a project', () => {
    const a = toRow({ id: 'a', project: '/p', status: 'idle', updatedAt: '2026-01-01T00:00:00Z' })
    const b = toRow({ id: 'b', project: '/p', status: 'idle', updatedAt: '2026-01-02T00:00:00Z' })
    expect(latestAgent([a, b], '/p')?.id).toBe('b')
    expect(latestAgent([a, b], '/q')).toBeUndefined()
  })
  it('grades mirror age and unbuilt workspaces', () => {
    const checks = healthChecks({ gateRunnable: 'unknown', mirrorFresh: [{ repoId: 'r', present: true, ageSeconds: 7200 }], orphanWorktrees: [], trust: 'untrusted', workspaceResolves: { ref: 'w', resolves: true, built: false, error: 'not built' } })
    const dot = (id: string) => checks.find((c) => c.id === id)?.dot
    expect(dot('gate')).toBe('unknown')
    expect(dot('mirror:r')).toBe('warn')
    expect(dot('trust')).toBe('warn')
    expect(dot('workspace')).toBe('warn')
  })
  it('preserves inherited Auto versus an explicit false', () => {
    expect(autoOf({})).toBe('default')
    expect(autoOf({ auto: undefined })).toBe('default')
    expect(autoOf({ auto: true })).toBe('on')
    expect(autoOf({ auto: false })).toBe('off')
    expect(autoFrom('default')).toBeUndefined()
    expect(autoFrom('on')).toBe(true)
    expect(autoFrom('off')).toBe(false)
    // Inherit stays omitted on the wire; an explicit false is a real value.
    expect(JSON.stringify({ auto: autoFrom('default') })).toBe('{}')
    expect(JSON.stringify({ auto: autoFrom('off') })).toBe('{"auto":false}')
  })
})
