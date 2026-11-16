import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import DraftApproval from './DraftApproval.svelte'
import * as api from '../api'
import type { FactoryApproval, FactoryConfig, FactoryItem, FactorySubagentOverrides } from '../factory'

vi.mock('../api', async (importActual) => ({
  ...(await importActual<typeof import('../api')>()),
  // The version a review is bound to is read from the bridge, never invented.
  getVersionedProjectSettings: vi.fn(),
  getFactorySettingsVersion: vi.fn(),
}))

afterEach(cleanup)

/** A saved draft as the bridge returns one: every configuration field resolved. */
const config = (over: Partial<FactoryConfig> = {}): FactoryConfig => ({
  repoId: 'acme/widgets',
  projectRoot: '/home/u/alpha',
  hostId: 'local',
  targetBranch: 'release',
  workspace: 'ci-dev',
  native: false,
  implementerRouting: { profile: 'careful', model: 'opus' },
  reviewerRouting: { profile: 'strict', model: 'sonnet' },
  reviewerInstructions: 'Check the migration',
  implementerAuto: true,
  expectedChecks: [{ kind: 'check_run', name: 'build', appId: 7 }],
  noRequiredCiAcknowledged: false,
  maxRounds: 9,
  ciPollSeconds: 30,
  ciWaitSeconds: 60,
  ...over,
})

const draft = (over: { plan?: string; spec?: string; config?: FactoryConfig; version?: number; approvals?: FactoryApproval[] | null } = {}): FactoryItem =>
  ({
    id: 'x',
    title: 'Task',
    state: 'draft',
    version: 1,
    draft: { requestId: 'r-1', title: 'Task', plan: 'Build it', spec: '', config: config(), ...over },
    approvals: over.approvals ?? null,
  }) as unknown as FactoryItem

/** The project's version and factory defaults, as a review reads them. */
const projectSettings = (version: string, projectDefaults: { implementerSubagents?: FactorySubagentOverrides; reviewerSubagents?: FactorySubagentOverrides } | null = null) =>
  vi.fn(async () => ({ version, projectDefaults }))

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.getVersionedProjectSettings).mockResolvedValue({ settings: { intake: {} }, etag: '"p1"' } as never)
  // The version a review binds itself to is the combined one the bridge
  // composes, so the default reader reads it from its own route.
  vi.mocked(api.getFactorySettingsVersion).mockResolvedValue('"p1":digest-1')
})

describe('DraftApproval', () => {
  it('showsResolvedConfigAndConsequence', async () => {
    render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    // The approval reads the same resolved rows creation previews.
    expect(screen.getByTestId('config-repository').textContent).toBe('acme/widgets')
    expect(screen.getByTestId('config-target-branch').textContent).toBe('release')
    expect(screen.getByTestId('config-environment').textContent).toBe('ci-dev')
    expect(screen.getByTestId('config-implementer').textContent).toBe('careful')
    expect(screen.getByTestId('config-reviewer').textContent).toBe('strict')
    expect(screen.getByTestId('config-expected-checks').textContent).toBe('check_run:build:7')
    expect(screen.getByTestId('config-total-rounds').textContent).toBe('9')
    // The consequence names what approving authorizes, and claims no progress.
    const consequence = screen.getByTestId('config-consequence').textContent ?? ''
    expect(consequence).toContain('authorizes execution under the resolved configuration')
    expect(consequence).toContain('starts nothing')
    // A routing object is only ever read for its profile, never printed.
    const approval = screen.getByTestId('effective-config')
    expect(approval.textContent).not.toContain('"profile"')
    expect(approval.textContent).not.toContain('{')
  })

  it('showsPlanAndOptionalSpecification', () => {
    render(DraftApproval, { item: draft({ plan: 'Build the widget', spec: 'Keep the migration safe' }), busy: false, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    expect(screen.getByTestId('summary-plan').textContent).toBe('Build the widget')
    expect(screen.getByTestId('summary-spec').textContent).toBe('Keep the migration safe')
    cleanup()
    // A draft without a specification shows no specification disclosure.
    render(DraftApproval, { item: draft({ plan: 'Build the widget' }), busy: false, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    expect(screen.getByTestId('summary-plan').textContent).toBe('Build the widget')
    expect(screen.queryByTestId('summary-spec')).toBeNull()
  })

  it('approvesOnlyOnExplicitAction', async () => {
    const onApprove = vi.fn()
    render(DraftApproval, { item: draft(), busy: false, onApprove, readSettings: projectSettings('"p1"') })
    // Rendering the approval decides nothing on its own.
    expect(onApprove).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Approve & start' }))
    expect(onApprove).toHaveBeenCalledTimes(1)
  })

  it('approvesAgainstTheCombinedVersionItDisplayed', async () => {
    const onApprove = vi.fn()
    // The default reader is the one under test: the version must come from the
    // bridge's combined route, not from the project's ETag, or a change to the
    // session's defaults would not move it.
    vi.mocked(api.getFactorySettingsVersion).mockResolvedValue('"p1":digest-1')
    render(DraftApproval, { item: draft(), busy: false, onApprove })
    await waitFor(() => expect(screen.getByTestId('subagent-settings-version').textContent).toContain('"p1":digest-1'))
    expect(api.getFactorySettingsVersion).toHaveBeenCalledWith('/home/u/alpha')
    // The project's defaults are still read from the project's own settings.
    expect(api.getVersionedProjectSettings).toHaveBeenCalledWith('/home/u/alpha')
    await fireEvent.click(screen.getByRole('button', { name: 'Approve & start' }))
    // The version handed to the approval is the one this review displayed.
    expect(onApprove).toHaveBeenCalledWith('"p1":digest-1')
  })

  it('failsClosedWhenTheSettingsVersionCannotBeRead', async () => {
    const onApprove = vi.fn()
    vi.mocked(api.getFactorySettingsVersion).mockRejectedValue(new api.APIError(503, { error: 'fleet unavailable' }))
    render(DraftApproval, { item: draft(), busy: false, onApprove })
    // An approval bound to nothing is not offered: the control is inert and
    // the review says why and what to do.
    const disabled = await screen.findByTestId('approve-disabled')
    expect(disabled.textContent).toContain('could not be read')
    expect(disabled.textContent).toContain('Reload')
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.click(screen.getByRole('button', { name: 'Approve & start' }))
    expect(onApprove).not.toHaveBeenCalled()
  })

  it('disablesWhileBusy', () => {
    render(DraftApproval, { item: draft(), busy: true, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    // A command in flight owns the approval: the control is inert until it
    // settles, so the same decision cannot be sent twice.
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(true)
    cleanup()
    render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('showsEachRolesSettingsAndWhereTheyCameFrom', async () => {
    const item = draft({
      config: config({
        // The draft states one field for the implementer and one for the reviewer.
        implementerSubagents: { enabled: false },
        reviewerSubagents: { model: 'strict' },
      }),
    })
    render(DraftApproval, {
      item,
      busy: false,
      onApprove: vi.fn(),
      readSettings: projectSettings('"p1:d1"', { implementerSubagents: { model: 'careful', concurrency: 3 } }),
    })
    // An explicit false is shown as off, and marked as stated here rather than
    // inherited from anything.
    const enabled = await screen.findByTestId('subagent-implementer-enabled')
    expect(enabled.textContent).toContain('Off')
    expect(screen.getByTestId('subagent-implementer-enabled-explicit')).toBeTruthy()
    expect(enabled.textContent).toContain("This draft's own setting")
    // The implementer's model is inherited from the project's factory defaults,
    // and its concurrency from the same place.
    await waitFor(() => expect(screen.getByTestId('subagent-implementer-model').textContent).toContain('careful'))
    const model = screen.getByTestId('subagent-implementer-model')
    expect(model.textContent).toContain('Inherited from the project factory defaults')
    expect(screen.queryByTestId('subagent-implementer-model-explicit')).toBeNull()
    await waitFor(() => expect(screen.getByTestId('subagent-implementer-concurrency').textContent).toContain('3 at a time'))
    // The reviewer is a different role: its own stated model, nothing else.
    expect(screen.getByTestId('subagent-reviewer-model-explicit')).toBeTruthy()
    expect(screen.getByTestId('subagent-reviewer-enabled').textContent).toContain('Inherited')
    // A field nothing states says the session's defaults decide, and that this
    // screen cannot read them.
    expect(screen.getByTestId('subagent-reviewer-concurrency').textContent).toContain('cannot read')
    // The version the review is bound to is shown, not just used.
    expect(screen.getByTestId('subagent-settings-version').textContent).toContain('"p1:d1"')
  })

  it('showsTheSettingsAnApprovedRevisionFroze', async () => {
    const approval = {
      revision: 2,
      plan: 'Build it',
      spec: '',
      planHash: 'h',
      config: config(),
      approvedAt: '2026-11-15T00:00:00Z',
      implementerSubagents: { enabled: true, model: 'careful', concurrency: 4 },
      reviewerSubagents: { enabled: false, model: '', concurrency: 1 },
    } as FactoryApproval
    render(DraftApproval, { item: draft({ approvals: [approval] }), busy: false, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    const frozen = await screen.findByTestId('approved-subagents')
    expect(frozen.textContent).toContain('Frozen on approved revision 2')
    expect(within(frozen).getByTestId('frozen-implementer-subagents').textContent).toBe('On · careful · 4 at a time')
    expect(within(frozen).getByTestId('frozen-reviewer-subagents').textContent).toBe('Off · Normal child routing · 1 at a time')
    // A later default change is not applied to a revision already approved.
    expect(frozen.textContent).toContain('does not reach work that')
  })

  it('presentsASettingsVersionConflictAsSomethingToReloadThrough', async () => {
    const onReload = vi.fn()
    render(DraftApproval, {
      item: draft(),
      busy: false,
      onApprove: vi.fn(),
      onReload,
      conflict: { reviewed: '"p1:d1"', current: '"p1:d2"' },
      readSettings: projectSettings('"p1:d2"'),
    })
    // A conflict names both versions and the way out, and never reads as a
    // generic failure.
    const card = await screen.findByTestId('subagent-conflict')
    expect(card.textContent).toContain('"p1:d1"')
    expect(card.textContent).toContain('"p1:d2"')
    expect(card.textContent).toContain('Reload')
    // Approving would freeze settings nobody read, so it is not offered.
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.click(within(card).getByRole('button', { name: 'Reload and review again' }))
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it('noticesASettingsChangeWhileTheReviewIsOpen', async () => {
    const reads = ['"p1:d1"', '"p1:d2"']
    const readSettings = vi.fn(async () => ({ version: reads.shift() ?? '"p1:d2"' }))
    const view = render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn(), readSettings })
    await waitFor(() => expect(screen.getByTestId('subagent-settings-version').textContent).toContain('"p1:d1"'))
    // Nothing has moved yet, so the approval still stands.
    expect(screen.queryByTestId('subagent-conflict')).toBeNull()
    // The shell's poll moves the item, and the settings moved with it.
    await view.rerender({ item: draft({ version: 2 }), busy: false, onApprove: vi.fn(), readSettings })
    await waitFor(() => expect(screen.getByTestId('subagent-conflict')).toBeTruthy())
    expect(screen.getByTestId('subagent-conflict').textContent).toContain('"p1:d2"')
  })

  it('treatsADigestMoveAsASettingsChangeOnItsOwn', async () => {
    // The project's ETag is unchanged; only the session's reported defaults
    // moved. A review bound to the ETag alone would approve settings it never
    // read, which is exactly what the combined version exists to prevent.
    const reads = ['"p1":digest-1', '"p1":digest-2']
    const readSettings = vi.fn(async () => ({ version: reads.shift() ?? '"p1":digest-2' }))
    const view = render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn(), readSettings })
    await waitFor(() => expect(screen.getByTestId('subagent-settings-version').textContent).toContain('"p1":digest-1'))
    await view.rerender({ item: draft({ version: 2 }), busy: false, onApprove: vi.fn(), readSettings })
    const card = await screen.findByTestId('subagent-conflict')
    expect(card.textContent).toContain('"p1":digest-1')
    expect(card.textContent).toContain('"p1":digest-2')
    expect((screen.getByRole('button', { name: 'Approve & start' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('keepsAShortRoleLabelForPhonesBesideTheDesktopOne', async () => {
    render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn(), readSettings: projectSettings('"p1"') })
    const section = await screen.findByTestId('subagent-implementer')
    // Both labels are rendered; the stylesheet shows one per width, so the
    // short one is never a truncation of the long one.
    expect(within(section).getByText('Implementer')).toBeTruthy()
    expect(within(section).getByText('Implementer subagents')).toBeTruthy()
    expect(within(screen.getByTestId('subagent-reviewer')).getByText('Reviewer')).toBeTruthy()
  })

  it('explainsAnUnsupportedBackendInsteadOfHidingTheOverrides', async () => {
    render(DraftApproval, {
      item: draft({ config: config({ implementerSubagents: { enabled: true } }) }),
      busy: false,
      onApprove: vi.fn(),
      capabilities: { settings: false, steer: true, stop: true, observe: true },
      readSettings: projectSettings('"p1"'),
    })
    // The override stays visible; what the backend cannot enforce is stated.
    expect(await screen.findByTestId('subagent-implementer-enabled')).toBeTruthy()
    expect(screen.getByTestId('subagent-unsupported').textContent).toContain('refused at approval')
    cleanup()
    // A backend that supports settings, or one that said nothing, claims nothing.
    render(DraftApproval, { item: draft(), busy: false, onApprove: vi.fn(), capabilities: { settings: true, steer: true, stop: true, observe: true }, readSettings: projectSettings('"p1"') })
    await waitFor(() => expect(screen.getByTestId('subagent-settings-version').textContent).toContain('"p1"'))
    expect(screen.queryByTestId('subagent-unsupported')).toBeNull()
  })
})
