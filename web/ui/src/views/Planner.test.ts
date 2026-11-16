import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Planner from './Planner.svelte'
import * as api from '../lib/api'
import type { FactoryConfig, FactoryItem } from '../lib/factory'
import type { PlannerEvent, PlannerSnapshotReply, PlannerSnapshotView, PlannerView } from '../lib/planner'

vi.mock('../lib/api', async (actual) => ({
  ...await actual<typeof import('../lib/api')>(),
  listRepos: vi.fn(),
  getProjectSettings: vi.fn(),
  createPlanner: vi.fn(),
  getPlanner: vi.fn(),
  promptPlanner: vi.fn(),
  cancelPlanner: vi.fn(),
  answerPlanner: vi.fn(),
  plannerEvents: vi.fn(),
  plannerContext: vi.fn(),
  createPlannerSnapshot: vi.fn(),
  getPlannerSnapshot: vi.fn(),
  handoffPlanner: vi.fn(),
}))
afterEach(cleanup)

const ROOT = '/home/u/alpha'
/** A planner id is the 32-hex identity the bridge minted. */
const ID = 'a'.repeat(32)
const NEXT_ID = 'b'.repeat(32)
const NOW = '2026-11-15T00:00:00Z'
const SNAPSHOT_SHA = 'd'.repeat(64)

/** A planner as `GET /api/planners/{id}` reports it. */
const planner = (over: Partial<PlannerView> = {}): PlannerView => ({
  id: ID,
  projectRoot: ROOT,
  repoId: 'repo',
  backendId: 'backend-1',
  workspaceId: 'ws-1',
  occupancy: 'free',
  sessionId: 'session-1',
  requestedMode: 'bypassPermissions',
  confirmedMode: 'bypassPermissions',
  modeConfirmed: true,
  state: 'active',
  live: true,
  eventSequence: 0,
  capabilities: { prompt: true, cancel: true, permissionRequests: true, sessionLoad: true, workspaceEdits: true, modes: ['bypassPermissions', 'default'] },
  sessionLoad: true,
  resumeAvailable: false,
  resumeReason: 'this backend cannot load a session',
  createdAt: NOW,
  updatedAt: NOW,
  ...over,
})

const resolvedConfig = (over: Partial<FactoryConfig> = {}): FactoryConfig => ({
  repoId: 'repo', projectRoot: ROOT, hostId: 'local', targetBranch: 'main', workspace: '', native: true,
  implementerRouting: {}, reviewerRouting: {}, reviewerInstructions: '', implementerAuto: false,
  expectedChecks: [], noRequiredCiAcknowledged: false, maxRounds: 5, ciPollSeconds: 15, ciWaitSeconds: 1800,
  ...over,
})

/** The draft the handoff created. */
const factoryItem = (): FactoryItem => ({
  id: 'item-1', title: 'Migration', state: 'draft', stage: '', repoId: 'repo', projectRoot: ROOT, hostId: 'local',
  branch: 'factory/item-1', baseSha: '', version: 1,
  draft: { requestId: 'r1', title: 'Migration', plan: 'Approved plan', spec: 'Draft spec', config: resolvedConfig() },
  approvals: null, grants: null, rounds: null, workers: null, prs: null, effects: null, decision: null, history: null,
  createdAt: NOW, approvedAt: '', updatedAt: NOW,
})

/** What a capture answers: the frozen snapshot and the paths it refused. */
const snapshotReply = (over: Partial<PlannerSnapshotReply> = {}): PlannerSnapshotReply => ({
  id: 'snap-1',
  sha256: SNAPSHOT_SHA,
  baseCommit: 'deadbeef',
  createdAt: NOW,
  artifacts: [
    { kind: 'plan', content: 'Approved plan', sha256: 'e'.repeat(64) },
    { kind: 'spec', content: 'Draft spec', sha256: 'f'.repeat(64) },
  ],
  files: [
    { path: 'src/a.ts', operation: 'modify', baseSha256: '1'.repeat(64), contentSha256: '2'.repeat(64), executable: false, diff: '@@ -1 +1 @@\n-old\n+new' },
  ],
  rejected: [{ path: 'missing.ts', reason: 'no such file in the planner workspace' }],
  ...over,
})

const snapshotView = (over: Partial<PlannerSnapshotView> = {}): PlannerSnapshotView => {
  const { rejected: _rejected, ...rest } = snapshotReply()
  return { ...rest, ...over }
}

const permissionEvent = (sequence: number, requestId = 'req-1'): PlannerEvent => ({
  sequence,
  kind: 'turn.permission',
  data: { requestId, method: 'session/request_permission', params: { payload: { toolName: 'shell', command: 'npm test', risk: 'medium' } } },
})

const renderNew = (onNavigate = vi.fn()) => render(Planner, { route: { kind: 'new', project: ROOT }, onNavigate })
const renderSession = (onNavigate = vi.fn()) => render(Planner, { route: { kind: 'session', id: ID }, onNavigate })

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listRepos).mockResolvedValue([{ id: 'repo', url: 'https://example.test', branch: 'main' }])
  vi.mocked(api.getProjectSettings).mockResolvedValue({ intake: {} })
  vi.mocked(api.getPlanner).mockResolvedValue(planner())
  vi.mocked(api.plannerEvents).mockResolvedValue([])
  vi.mocked(api.plannerContext).mockResolvedValue({ artifacts: [], decisions: [], unresolved: [], absent: [] })
})

it('showsPermissiveModeAndRestrictedChoice', async () => {
  const onNavigate = vi.fn()
  vi.mocked(api.createPlanner).mockResolvedValue(planner())
  const { unmount } = renderNew(onNavigate)
  await screen.findByRole('option', { name: 'repo' })

  // A new planner asks for the mode that can do the work, and says what that
  // means: the planner's own access mode, not Marshal's tool policy.
  const permissive = screen.getByRole('radio', { name: /Permissive/ }) as HTMLInputElement
  expect(permissive.checked).toBe(true)
  expect(screen.getByText(/without a per-action approval/)).toBeTruthy()
  expect(screen.getByText(/not Marshal's native tool policy/)).toBeTruthy()

  // The restricted choice is offered beside it, and choosing it is a click.
  const restricted = screen.getByRole('radio', { name: /Restricted/ }) as HTMLInputElement
  expect(restricted.checked).toBe(false)

  // The entry point's project is the scope the planner starts in.
  expect((screen.getByLabelText('Project root') as HTMLInputElement).value).toBe(ROOT)

  await fireEvent.click(screen.getByRole('button', { name: 'Start planner' }))
  await waitFor(() => expect(api.createPlanner).toHaveBeenCalledTimes(1))
  const sent = vi.mocked(api.createPlanner).mock.calls[0][0]
  expect(sent).toMatchObject({ projectRoot: ROOT, repoId: 'repo', mode: 'bypassPermissions' })
  expect(sent.workspaceId).toBeUndefined()
  expect(onNavigate).toHaveBeenCalledWith(`#planner/${ID}`)
  unmount()

  // During the session the requested and the confirmed mode are both stated.
  renderSession()
  await waitFor(() => expect(screen.getByTestId('planner-mode').textContent).toBe('Permissive'))
  expect(screen.getByTestId('planner-confirmed-mode').textContent).toBe('Permissive')
})

it('warnsButPermitsSharedWorkspace', async () => {
  vi.mocked(api.createPlanner).mockRejectedValue(
    new api.APIError(409, {
      error: 'workspace ws-1 is in_use; set proceedShared to use it anyway',
      workspaceId: 'ws-1',
      occupancy: 'in_use',
      owners: ['planner-b'],
    }),
  )
  renderNew()
  await screen.findByRole('option', { name: 'repo' })
  await fireEvent.click(screen.getByLabelText('Use an existing workspace'))
  await fireEvent.input(screen.getByLabelText('Existing workspace id'), { target: { value: 'ws-1' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Start planner' }))

  // The refusal names the owner and the consequence, and nothing was retried.
  const warning = await screen.findByTestId('planner-occupancy')
  expect(warning.textContent).toContain('in use')
  expect(warning.textContent).toContain('planner-b')
  expect(warning.textContent).toContain('overwrite')
  expect(api.createPlanner).toHaveBeenCalledTimes(1)
  expect(vi.mocked(api.createPlanner).mock.calls[0][0].proceedShared).toBeUndefined()

  // Proceeding with shared use is its own explicit click.
  vi.mocked(api.createPlanner).mockResolvedValue(planner())
  await fireEvent.click(screen.getByRole('button', { name: 'Proceed with shared use' }))
  await waitFor(() => expect(api.createPlanner).toHaveBeenCalledTimes(2))
  expect(vi.mocked(api.createPlanner).mock.calls[1][0]).toMatchObject({ workspaceId: 'ws-1', proceedShared: true })
})

it('rendersQuestionsAndAnswersOnce', async () => {
  vi.mocked(api.getPlanner).mockResolvedValue(
    planner({ requests: [{ requestId: 'req-1', method: 'session/request_permission', state: 'pending', at: NOW }] }),
  )
  // The journal replays the same request; it is one question, not two.
  const journal = [permissionEvent(1), permissionEvent(2)]
  vi.mocked(api.plannerEvents).mockImplementation(async (_id: string, after = 0) => journal.filter((e) => e.sequence > after))
  vi.mocked(api.answerPlanner).mockResolvedValue()
  renderSession()

  const card = await screen.findByTestId('planner-request')
  expect(screen.getAllByTestId('planner-request')).toHaveLength(1)
  // The card is one request, and its detail comes from the journaled params.
  await waitFor(() => expect(screen.getByTestId('planner-request').textContent).toContain('npm test'))
  expect(card.textContent).toContain('Permission requested')

  await fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
  await waitFor(() => expect(api.answerPlanner).toHaveBeenCalledTimes(1))
  expect(vi.mocked(api.answerPlanner).mock.calls[0]).toEqual([ID, 'req-1', { approved: true }])
  // Answered once: the request is settled and cannot be posted a second time.
  await waitFor(() => expect(screen.queryByTestId('planner-request')).toBeNull())
  expect(api.answerPlanner).toHaveBeenCalledTimes(1)
})

it('disconnectDoesNotResend', async () => {
  vi.mocked(api.promptPlanner).mockResolvedValue({ requestId: 'p1', state: 'dispatched' })
  const journal: PlannerEvent[] = [{ sequence: 1, kind: 'turn.text', data: { text: 'working' } }]
  vi.mocked(api.plannerEvents)
    .mockResolvedValueOnce([])
    // The read after the prompt is the one that cannot reach the bridge.
    .mockRejectedValueOnce(new Error('network down'))
    .mockImplementation(async (_id: string, after = 0) => journal.filter((e) => e.sequence > after))
  renderSession()
  await screen.findByRole('button', { name: 'Send prompt' })
  await fireEvent.input(screen.getByLabelText('Prompt'), { target: { value: 'Plan the migration' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send prompt' }))
  await waitFor(() => expect(api.promptPlanner).toHaveBeenCalledTimes(1))

  const banner = await screen.findByTestId('planner-disconnected')
  expect(banner.textContent).toContain('Disconnected — retrying reads')

  // The retry replays the journal from the last sequence shown; it never
  // dispatches the prompt again.
  await fireEvent.click(screen.getByRole('button', { name: 'Retry reads' }))
  await waitFor(() => expect(screen.queryByTestId('planner-disconnected')).toBeNull())
  expect(screen.getByText('working')).toBeTruthy()
  expect(api.promptPlanner).toHaveBeenCalledTimes(1)
})

it('showsUnknownAndFreshSession', async () => {
  vi.mocked(api.getPlanner).mockResolvedValue(
    planner({
      turns: [{ requestId: 'r1', textHash: 'h', state: 'unknown', reason: 'the backend could not observe the turn', at: NOW }],
    }),
  )
  vi.mocked(api.createPlanner).mockResolvedValue(planner({ id: NEXT_ID }))
  const onNavigate = vi.fn()
  renderSession(onNavigate)

  const card = await screen.findByTestId('planner-unknown')
  expect(card.textContent).toContain('unknown')
  expect(card.textContent).toContain('not resent')

  await fireEvent.click(screen.getByRole('button', { name: 'Start a fresh session with the project context' }))
  await waitFor(() => expect(api.createPlanner).toHaveBeenCalledTimes(1))
  // The fresh planner is scoped to the same project and context.
  expect(vi.mocked(api.createPlanner).mock.calls[0][0]).toMatchObject({ projectRoot: ROOT, repoId: 'repo' })
  expect(onNavigate).toHaveBeenCalledWith(`#planner/${NEXT_ID}`)
  // The unknown turn was never resent.
  expect(api.promptPlanner).not.toHaveBeenCalled()
})

it('contextCanBeExpanded', async () => {
  vi.mocked(api.plannerContext).mockResolvedValue({
    artifacts: [{ kind: 'plan', sha256: 'c0ffee', source: 'snapshot:snap-1', approved: true, snapshotId: 'snap-1' }],
    decisions: [{ source: 'journal', requestId: 'req-1', method: 'session/request_permission', state: 'answered', at: NOW }],
    unresolved: [],
    absent: ['files'],
  })
  renderSession()

  await screen.findByTestId('planner-context-summary')
  await waitFor(() => expect(screen.getByTestId('planner-context-summary').textContent).toContain('1 artifact'))
  expect(screen.getByTestId('planner-context-summary').textContent).toContain('1 absent source')
  // Collapsed until asked: the summary is the whole answer at first.
  expect(screen.queryByTestId('planner-context-detail')).toBeNull()

  await fireEvent.click(screen.getByRole('button', { name: 'Show context' }))
  const detail = await screen.findByTestId('planner-context-detail')
  expect(detail.textContent).toContain('snapshot:snap-1')
  expect(detail.textContent).toContain('c0ffee')
  expect(detail.textContent).toContain('files')

  await fireEvent.click(screen.getByRole('button', { name: 'Hide context' }))
  expect(screen.queryByTestId('planner-context-detail')).toBeNull()
})

it('documentsOnlyDefault', async () => {
  vi.mocked(api.createPlannerSnapshot).mockResolvedValue(snapshotReply({ files: [], rejected: [] }))
  renderSession()

  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Draft plan' } })
  await fireEvent.input(screen.getByLabelText('Specification'), { target: { value: 'Draft spec' } })
  expect(screen.getByText(/Nothing is selected by default/)).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Capture snapshot' }))

  await waitFor(() => expect(api.createPlannerSnapshot).toHaveBeenCalledTimes(1))
  // Nothing was selected, so the capture freezes the documents only.
  expect(vi.mocked(api.createPlannerSnapshot).mock.calls[0]).toEqual([ID, { plan: 'Draft plan', spec: 'Draft spec', paths: [] }])
  await screen.findByTestId('planner-snapshot')
})

it('reviewSelectedFilesBeforeDraft', async () => {
  vi.mocked(api.createPlannerSnapshot).mockResolvedValue(snapshotReply())
  vi.mocked(api.getPlannerSnapshot).mockResolvedValue(snapshotView())
  vi.mocked(api.handoffPlanner).mockResolvedValue(factoryItem())
  const onNavigate = vi.fn()
  renderSession(onNavigate)

  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Approved plan' } })
  await fireEvent.input(screen.getByLabelText('Add a file path'), { target: { value: 'src/a.ts' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Add file' }))
  expect((await screen.findByTestId('planner-file-option')).textContent).toBe('src/a.ts')
  // A path is listed, not selected: the default capture has no files.
  const checkbox = screen.getByRole('checkbox', { name: 'src/a.ts' }) as HTMLInputElement
  expect(checkbox.checked).toBe(false)
  await fireEvent.click(checkbox)

  await fireEvent.click(screen.getByRole('button', { name: 'Capture snapshot' }))
  await waitFor(() => expect(api.createPlannerSnapshot).toHaveBeenCalledTimes(1))
  expect(vi.mocked(api.createPlannerSnapshot).mock.calls[0][1]).toEqual({ plan: 'Approved plan', spec: '', paths: ['src/a.ts'] })

  // Capturing is not reviewing, and reviewing is not creating the draft.
  expect(api.handoffPlanner).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('button', { name: 'Review captured contents' }))
  await waitFor(() => expect(api.getPlannerSnapshot).toHaveBeenCalledWith(ID, 'snap-1'))
  // The review shows the frozen bytes, the base, the hash and each file's diff.
  expect((await screen.findByTestId('planner-artifact-plan')).textContent).toBe('Approved plan')
  expect(screen.getByTestId('planner-base-commit').textContent).toBe('deadbeef')
  expect(screen.getByTestId('planner-review-hash').textContent).toBe(SNAPSHOT_SHA)
  expect(screen.getByTestId('planner-file-path').textContent).toBe('src/a.ts')
  expect(screen.getByTestId('planner-file-diff').textContent).toContain('@@ -1 +1 @@')
  expect(api.handoffPlanner).not.toHaveBeenCalled()

  await fireEvent.input(screen.getByLabelText('Draft title'), { target: { value: 'Migration' } })
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm and create factory draft' }))
  await waitFor(() => expect(api.handoffPlanner).toHaveBeenCalledTimes(1))
  const [plannerId, snapshotId, body] = vi.mocked(api.handoffPlanner).mock.calls[0]
  expect([plannerId, snapshotId]).toEqual([ID, 'snap-1'])
  // The body is a normal factory create built from the captured artifacts.
  expect(body).toMatchObject({ title: 'Migration', plan: 'Approved plan', spec: 'Draft spec' })
  expect(body.config).toMatchObject({ repoId: 'repo', projectRoot: ROOT, targetBranch: 'main', native: true })
  // The handoff opens the actual draft, where the existing approval lives.
  expect(onNavigate).toHaveBeenCalledWith('#factory/item-1')
})

it('snapshotErrorRetainsInput', async () => {
  vi.mocked(api.createPlannerSnapshot).mockRejectedValue(
    new api.APIError(409, { error: 'the workspace changed: src/a.ts has a different base' }),
  )
  renderSession()

  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Keep this plan' } })
  await fireEvent.input(screen.getByLabelText('Specification'), { target: { value: 'Keep this spec' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Capture snapshot' }))

  const error = await screen.findByTestId('planner-snapshot-error')
  expect(error.textContent).toContain('the workspace changed')
  // A refused capture is not a reason to lose what was typed.
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('Keep this plan')
  expect((screen.getByLabelText('Specification') as HTMLTextAreaElement).value).toBe('Keep this spec')
})

it('plannerShellIsMobileSafe', async () => {
  renderSession()
  const shell = await screen.findByTestId('planner-shell')
  // The shell is its own class and cannot widen the page at 390px.
  expect(shell.className).toContain('planner-shell')
  expect(shell.className).toContain('min-w-0')
  expect(shell.className).toContain('break-words')

  // A workspace id is a long unbroken token, so it renders in a breakable
  // element rather than overflowing the phone layout.
  const workspace = await screen.findByTestId('planner-workspace')
  expect(workspace.className).toContain('break-all')

  await fireEvent.input(screen.getByLabelText('Add a file path'), { target: { value: 'src/some/deeply/nested/path/with/a/long/name.ts' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Add file' }))
  const option = await screen.findByTestId('planner-file-option')
  expect(option.className).toContain('break-all')

  vi.mocked(api.createPlannerSnapshot).mockResolvedValue(snapshotReply())
  await fireEvent.click(screen.getByRole('button', { name: 'Capture snapshot' }))
  const hash = await screen.findByTestId('planner-snapshot-hash')
  expect(hash.className).toContain('break-all')
})
