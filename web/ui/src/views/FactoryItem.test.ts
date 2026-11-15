import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import FactoryItem from './FactoryItem.svelte'
import * as api from '../lib/api'
import type { FactoryApproval, FactoryItem as Item, FactoryRoute } from '../lib/factory'
import type { FactoryState } from '../lib/factoryStore'
import type { PreviewDeployment, PreviewReport } from '../lib/preview'
vi.mock('../lib/api', async (actual) => ({ ...await actual<typeof import('../lib/api')>(), commandFactoryItem: vi.fn(), getPreviewEnvironments: vi.fn(), getModels: vi.fn() }))
afterEach(cleanup)
beforeEach(() => {
  getItem.mockReset()
  upsert.mockReset()
  onRefresh.mockReset()
  onNavigate.mockReset()
  // The store's own refresh resolves unless a test says its read failed.
  onRefresh.mockImplementation(async () => {})
  vi.mocked(api.commandFactoryItem).mockReset()
  vi.mocked(api.getPreviewEnvironments).mockReset()
  // The retry's route pickers offer the real profiles the models API reports.
  vi.mocked(api.getModels).mockReset()
  vi.mocked(api.getModels).mockResolvedValue({ profiles: {
    'cheap-review': {}, frontier: {}, 'new-impl': {}, 'new-review': {}, old: {}, 'old-review': {},
  } } as never)
})
const base = { id: 'x', title: 'Task', repoId: 'repo', state: 'draft', stage: '', version: 1, draft: { plan: 'Build', spec: '', config: { targetBranch: 'main', repoId: 'repo', maxRounds: 5, implementerRouting: {}, reviewerRouting: {}, expectedChecks: [] } }, rounds: [], grants: [], prs: [], workers: [], history: [] } as unknown as Item
/** One recorded approval revision, as the bridge reports one. */
const approval = (revision: number, plan: string, spec = '') => ({ revision, plan, spec, planHash: `hash-${revision}`, config: base.draft.config, approvedAt: `2026-01-0${revision}T00:00:00Z` }) as FactoryApproval
/** The shell store's projection, as App hands it to a view. */
const factoryState = (over: Partial<FactoryState> = {}): FactoryState => ({ items: [], loading: false, refreshing: false, error: null, lastUpdated: null, ...over })
/**
 * The shell's callbacks. The item reads through the shared store: `getItem`
 * answers the first read, `upsert` publishes a command's result, `onRefresh`
 * is the store's own refresh — the one the stale-version path awaits.
 */
const getItem = vi.fn<(id: string) => Promise<Item>>(), upsert = vi.fn<(item: Item) => void>(), onRefresh = vi.fn<() => Promise<void>>(), onNavigate = vi.fn<(hash: string) => void>()
/**
 * The item at a route. The route is the shell's parse, so a test opens a view
 * exactly as a deep link does; nothing here writes the hash itself.
 */
const renderItem = (state: FactoryState = factoryState(), route: Partial<FactoryRoute> = {}) =>
  render(FactoryItem, { itemId: 'x', route: { itemId: 'x', ...route }, factory: state, getItem, upsert, onRefresh, onNavigate })
/** The CI-first review view: CI evidence, the report and previews live at the review route. */
const renderReview = (state: FactoryState = factoryState()) => renderItem(state, { view: 'review' })
/**
 * One retry route picker: a real select, so the choice is made from the
 * profiles the models API reports once that read has landed.
 */
const pickProfile = async (label: string, profile: string) => {
  const { fireEvent } = await import('@testing-library/svelte')
  await vi.waitFor(() => {
    const select = screen.getByLabelText(label) as HTMLSelectElement
    expect([...select.options].map((option) => option.value)).toContain(profile)
  })
  await fireEvent.change(screen.getByLabelText(label), { target: { value: profile } })
}
it('requires explicit approval before a draft starts', async () => {
  getItem.mockResolvedValue({ ...base, draft: { ...base.draft, config: { ...base.draft.config, implementerRouting: { profile: 'careful', model: 'opus' }, reviewerRouting: { profile: 'strict' } } } } as Item)
  renderItem()
  expect(await screen.findByText('Review draft before starting')).toBeTruthy()
  expect(api.commandFactoryItem).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Approve & start' })).toBeTruthy()
  // The approval reads the resolved rows creation previews, so the draft's
  // routing objects never appear here as JSON.
  const approvalBlock = screen.getByTestId('draft-approval')
  expect(screen.getByTestId('config-implementer').textContent).toBe('careful')
  expect(screen.getByTestId('config-reviewer').textContent).toBe('strict')
  expect(approvalBlock.textContent).not.toContain('"profile"')
  expect(approvalBlock.textContent).not.toContain('{')
})
it('shows failed CI before compact passes', async () => {
  const item = { ...base, state: 'needs_input', rounds: [{ ci: { complete: true, checks: [
    { key: { name: 'fail' }, conclusion: 'failure', logTail: 'trace' }, { key: { name: 'pass' }, conclusion: 'success' },
  ] } }] } as Item
  getItem.mockResolvedValue(item)
  renderReview()
  await screen.findByText(/fail: failure/)
  expect(screen.getByText('1 passing checks')).toBeTruthy()
})
it('answers the current decision and refreshes on a stale version', async () => {
  const item = { ...base, state: 'needs_input', decision: { id: 'q1', kind: 'question', question: 'Which route?', reason: '' } } as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockRejectedValueOnce(new api.APIError(409, { error: 'stale version', currentVersion: 2 }))
  renderItem()
  await screen.findByText('Which route?')
  await import('@testing-library/svelte').then(async ({ fireEvent }) => {
    await fireEvent.input(screen.getByLabelText('Answer'), { target: { value: 'Use A' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
  })
  await screen.findByText(/Item changed. Review the refreshed version/)
  expect(vi.mocked(api.commandFactoryItem).mock.calls[0][1]).toMatchObject({ decisionId: 'q1', answer: 'Use A', expectedVersion: 1 })
  // The stale path asks the shared store to catch up rather than reading here.
  expect(onRefresh).toHaveBeenCalledTimes(1)
})
it('commandResponsesUpsertTheSharedStore', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'needs_input', decision: { id: 'q1', kind: 'question', question: 'Which route?', reason: '' } } as Item
  const answered = { ...item, version: 2, decision: null, state: 'running' } as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(answered)
  renderItem()
  await screen.findByText('Which route?')
  await fireEvent.input(screen.getByLabelText('Answer'), { target: { value: 'Use A' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
  await screen.findByText('answer accepted')
  // The command's own result is the newest truth, so it is published rather
  // than left for the next list read to rediscover.
  expect(upsert).toHaveBeenCalledTimes(1)
  expect(upsert).toHaveBeenCalledWith(answered)
})
it('requires a named worker and deliberate recovery attestation', async () => {
  getItem.mockResolvedValue({ ...base, state: 'recovering', decision: { id: 'd', kind: 'recovery', question: 'Worker unknown' } } as Item)
  renderItem()
  await screen.findByText('Worker unknown')
  expect((screen.getByRole('button', { name: 'Record named-worker attestation' }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByLabelText('I confirm this named previous worker has stopped') as HTMLInputElement).checked).toBe(false)
})
it('labels a stopping worker as unconfirmed', async () => {
  getItem.mockResolvedValue({ ...base, state: 'stopping' } as Item)
  renderItem()
  expect(await screen.findByText('Stop requested. Worker shutdown is not yet confirmed.')).toBeTruthy()
})
it('requires an explicit replacement plan and retry grant', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  getItem.mockResolvedValue({ ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Allowance spent' }, grants: [{ additionalRounds: 5 }], rounds: Array(5).fill({}) } as Item)
  renderItem()
  await screen.findByText('Allowance spent')
  expect(screen.getByText(/5 rounds used · 5 granted total/)).toBeTruthy()
  await fireEvent.click(screen.getByLabelText('Change approved plan or reviewer instructions'))
  expect(screen.getByLabelText('Replacement plan')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Grant rounds and retry' }) as HTMLButtonElement).disabled).toBe(true)
})
it('sends route changes in the replacement approval config', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, reviewerInstructions: 'Prior', implementerRouting: { profile: 'old' }, reviewerRouting: { profile: 'old' } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(item)
  renderItem()
  await screen.findByText('Try again')
  await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change route' } })
  await fireEvent.click(screen.getByLabelText('Change approved plan or reviewer instructions'))
  expect((screen.getByLabelText('Replacement plan') as HTMLTextAreaElement).value).toBe('Approved plan')
  await pickProfile('Implementer profile', 'new-impl')
  await pickProfile('Reviewer profile', 'new-review')
  await fireEvent.input(screen.getByLabelText('Reviewer instructions'), { target: { value: 'Review carefully' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Grant rounds and retry' }))
  expect(vi.mocked(api.commandFactoryItem).mock.calls.at(-1)?.[1]).toMatchObject({
    kind: 'retry', expectedVersion: 1, decisionId: 'd', additionalRounds: 1,
    replacement: { plan: 'Approved plan', spec: 'Approved spec', config: { reviewerInstructions: 'Review carefully', implementerRouting: { profile: 'new-impl' }, reviewerRouting: { profile: 'new-review' } } },
  })
  const cmd = vi.mocked(api.commandFactoryItem).mock.calls.at(-1)?.[1]
  expect(cmd?.implementerRouting).toBeUndefined()
  expect(cmd?.reviewerRouting).toBeUndefined()
  expect(cmd?.replacement?.requestId).toBeTruthy()
})
it('sends a route-only retry without the plan-replacement checkbox', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, implementerRouting: { profile: 'old' }, reviewerRouting: { profile: 'old-review' } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(item)
  renderItem()
  await screen.findByText('Try again')
  await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change route' } })
  await pickProfile('Implementer profile', 'new-impl')
  await fireEvent.click(screen.getByRole('button', { name: 'Grant rounds and retry' }))
  const cmd = vi.mocked(api.commandFactoryItem).mock.calls.at(-1)?.[1]
  expect(cmd).toMatchObject({ kind: 'retry', implementerRouting: { profile: 'new-impl' } })
  expect(cmd?.reviewerRouting).toBeUndefined()
  expect(cmd?.replacement).toBeUndefined()
})
it('stale409RefreshesWithoutClearingRetryInput', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, implementerRouting: { profile: 'old' }, reviewerRouting: { profile: 'old-review' } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockRejectedValueOnce(new api.APIError(409, { error: 'stale version', currentVersion: 2 }))
  renderItem()
  await screen.findByText('Try again')
  await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change route' } })
  await fireEvent.input(screen.getByLabelText('Additional rounds'), { target: { value: '3' } })
  await pickProfile('Implementer profile', 'new-impl')
  await fireEvent.click(screen.getByRole('button', { name: 'Grant rounds and retry' }))
  await screen.findByText(/Item changed. Review the refreshed version/)
  // The store's refresh is awaited, and a stale version costs nobody their input.
  expect(onRefresh).toHaveBeenCalledTimes(1)
  expect((screen.getByLabelText('Reason') as HTMLTextAreaElement).value).toBe('Change route')
  expect((screen.getByLabelText('Additional rounds') as HTMLInputElement).value).toBe('3')
  expect((screen.getByLabelText('Implementer profile') as HTMLSelectElement).value).toBe('new-impl')
  expect(upsert).not.toHaveBeenCalled()
})
it('sends both routes in a route-only retry', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, implementerRouting: { profile: 'old' }, reviewerRouting: { profile: 'old-review' } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(item)
  renderItem()
  await screen.findByText('Try again')
  await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change both routes' } })
  await pickProfile('Implementer profile', 'new-impl')
  await pickProfile('Reviewer profile', 'new-review')
  await fireEvent.click(screen.getByRole('button', { name: 'Grant rounds and retry' }))
  const cmd = vi.mocked(api.commandFactoryItem).mock.calls.at(-1)?.[1]
  expect(cmd).toMatchObject({ kind: 'retry', implementerRouting: { profile: 'new-impl' }, reviewerRouting: { profile: 'new-review' } })
  expect(cmd?.replacement).toBeUndefined()
})
it('shows the routes currently in force as the retry pickers start', async () => {
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, implementerRouting: { profile: 'frontier' }, reviewerRouting: { profile: 'cheap-review' } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderItem()
  await screen.findByText('Try again')
  expect(screen.getByText('Routes in force: implementer frontier · reviewer cheap-review')).toBeTruthy()
  // The pickers start at the routes that actually run, so touching one is a
  // deliberate change rather than an accidental loss of the current route.
  await pickProfile('Implementer profile', 'frontier')
  await pickProfile('Reviewer profile', 'cheap-review')
  expect((screen.getByLabelText('Implementer profile') as HTMLSelectElement).value).toBe('frontier')
  expect((screen.getByLabelText('Reviewer profile') as HTMLSelectElement).value).toBe('cheap-review')
})
it('states that a route-only retry starts a fresh round', async () => {
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, implementerRouting: { profile: 'old' }, reviewerRouting: { profile: 'old-review' } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderItem()
  await screen.findByText('Try again')
  expect(screen.getByText("A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.")).toBeTruthy()
})
it('requires exact last agent ID before recovery attestation', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'recovering', decision: { id: 'd', kind: 'recovery', question: 'Worker unknown' }, workers: [{ role: 'implementer', agentId: 'agent-2' }] } as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(item)
  renderItem()
  await screen.findByText('Last worker agent ID: agent-2')
  const button = screen.getByRole('button', { name: 'Record named-worker attestation' }) as HTMLButtonElement
  await fireEvent.input(screen.getByLabelText('Type the exact last worker agent ID'), { target: { value: 'agent-1' } })
  await fireEvent.input(screen.getByLabelText('How you confirmed it'), { target: { value: 'Process stopped' } })
  await fireEvent.click(screen.getByLabelText('I confirm this named previous worker has stopped'))
  expect(button.disabled).toBe(true)
  await fireEvent.input(screen.getByLabelText('Type the exact last worker agent ID'), { target: { value: 'agent-2' } })
  expect(button.disabled).toBe(false)
  await fireEvent.click(button)
  expect(vi.mocked(api.commandFactoryItem).mock.calls.at(-1)?.[1]).toMatchObject({ kind: 'recover', confirmPreviousWorkerStopped: true, reason: 'agent-2: Process stopped' })
})
it('shows structured review findings, question and reason, and ordinary agent drill-in', async () => {
  const item = { ...base, state: 'needs_input', workers: [{ agentId: 'agent/1', role: 'reviewer' }], rounds: [{
    review: { summary: 'Needs repair', question: 'Which API?', reason: 'Inputs missing', findings: [{ severity: 'high', path: 'web/a.go', line: 42, title: 'Unsafe call', body: 'Handle the error' }] },
  }] } as Item
  getItem.mockResolvedValue(item)
  const view = renderReview()
  await screen.findByText('high: Unsafe call')
  expect(screen.getByText('web/a.go:42')).toBeTruthy()
  expect(screen.getByText('Handle the error')).toBeTruthy()
  expect(screen.getByText('Reviewer question: Which API?')).toBeTruthy()
  expect(screen.getByText('Reviewer reason: Inputs missing')).toBeTruthy()
  // The worker's own conversation is still opened from the activity view.
  view.unmount()
  renderItem()
  expect((await screen.findByRole('link', { name: 'reviewer: agent/1' })).getAttribute('href')).toBe('#chat/agent%2F1')
})
it('distinguishes empty from unavailable CI evidence', async () => {
  getItem.mockResolvedValue({ ...base, state: 'checking', rounds: [{ ci: { complete: false, checks: [] } }] } as unknown as Item)
  const view = renderReview()
  expect(await screen.findByText(/CI evidence unavailable or incomplete/)).toBeTruthy()
  view.unmount()
  getItem.mockResolvedValue({ ...base, state: 'checking', rounds: [{ ci: { complete: true, checks: [] } }] } as unknown as Item)
  renderReview()
  expect(await screen.findByText('Observation complete; no checks reported.')).toBeTruthy()
})
it('previews material environment and permission settings before approval', async () => {
  getItem.mockResolvedValue({ ...base, draft: { ...base.draft, config: { ...base.draft.config, native: false, workspace: 'go-dev', implementerAuto: true, reviewerInstructions: 'Check migration safety' } } } as Item)
  renderItem()
  expect(await screen.findByText('Review draft before starting')).toBeTruthy()
  expect(screen.getByTestId('config-environment').textContent).toBe('go-dev')
  expect(screen.getByTestId('config-auto-permission').textContent).toBe('Automatic')
  expect(screen.getByTestId('config-reviewer-instructions').textContent).toBe('Check migration safety')
})
const narration = {
  issue: 'The report hides the reasoning', approach: 'Narrate the change in the report',
  existingBehaviour: 'Only a summary paragraph existed', alternatives: 'Extend the summary',
  tradeoffs: 'A longer report body', choice: 'Add a structured narration',
  verification: 'go test ./web/bridge', limitations: 'Excerpts are selections, not diffs',
  excerpts: [{ path: 'web/bridge/factory_types.go', lang: 'go', code: 'type FactoryNarration struct{}' }],
}
it('renders the narration sections in order', async () => {
  const item = { ...base, state: 'checking', rounds: [{ implementation: { summary: 'Bare summary', narration } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderReview()
  await screen.findByText('Issue')
  expect(screen.getAllByRole('heading', { level: 3 }).map((heading) => heading.textContent)).toEqual([
    'Issue', 'Approach', 'Existing behaviour', 'Alternatives and trade-offs', 'Implementation choice', 'Verification', 'Limitations', 'Selected code',
  ])
  expect(screen.getByText('The report hides the reasoning')).toBeTruthy()
  expect(screen.getByText('Narrate the change in the report')).toBeTruthy()
  expect(screen.getByText('Extend the summary')).toBeTruthy()
  expect(screen.getByText('A longer report body')).toBeTruthy()
  expect(screen.getByText('Add a structured narration')).toBeTruthy()
  expect(screen.getByText('go test ./web/bridge')).toBeTruthy()
  expect(screen.getByText('Excerpts are selections, not diffs')).toBeTruthy()
})
it('renders each excerpt with its path and code', async () => {
  const item = { ...base, state: 'checking', rounds: [{ implementation: { summary: 'Bare summary', narration: { ...narration,
    excerpts: [{ path: 'web/bridge/factory_types.go', lang: 'go', code: 'type FactoryNarration struct{}' }, { path: 'web/ui/src/lib/factory.ts', code: 'interface FactoryExcerpt {}' }] } } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderReview()
  await screen.findByText('web/bridge/factory_types.go')
  expect(screen.getByText('type FactoryNarration struct{}')).toBeTruthy()
  expect(screen.getByText('web/ui/src/lib/factory.ts')).toBeTruthy()
  expect(screen.getByText('interface FactoryExcerpt {}')).toBeTruthy()
  expect(screen.getByText('go')).toBeTruthy()
})
it('renders the narration alongside the CI card', async () => {
  const item = { ...base, state: 'checking', rounds: [{
    ci: { complete: true, checks: [{ key: { name: 'fail' }, conclusion: 'failure', logTail: 'trace' }, { key: { name: 'pass' }, conclusion: 'success' }] },
    implementation: { summary: 'Bare summary', narration },
  }] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderReview()
  expect((await screen.findByText(/fail: failure/)).textContent).toContain('failure')
  expect(screen.getByText('trace')).toBeTruthy()
  expect(screen.getByText('1 passing checks').closest('details')?.hasAttribute('open')).toBe(false)
  expect(screen.getByText('Issue')).toBeTruthy()
  expect(screen.getByText('The report hides the reasoning')).toBeTruthy()
})
it('falls back to the summary when there is no narration', async () => {
  const item = { ...base, state: 'checking', rounds: [{ implementation: { summary: 'Bare summary' } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderReview()
  expect(await screen.findByText('Bare summary')).toBeTruthy()
  expect(screen.queryByText('Issue')).toBeNull()
})
const previewReport = { repoId: 'repo', forge: 'github', status: 'ok', ref: 'feature/x', sha: '', capabilities: [], environments: [], deployments: [], gaps: [] } as unknown as PreviewReport
const deployment = (over: Partial<PreviewDeployment>) => ({
  id: 'd', environment: 'production', originalEnvironment: 'production', ref: 'feature/x', sha: '', task: 'deploy',
  state: '', environmentUrl: '', targetUrl: '', logUrl: '', description: '', transient: false, production: false,
  createdAt: '', updatedAt: '', ...over,
}) as PreviewDeployment
it('badges preview rows against the last factory-recorded PR head', async () => {
  const item = { ...base, state: 'checking', prs: [{ number: 7, url: 'https://forge/pull/7', headSha: 'abc1234', state: 'open', draft: false }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'ABC1234', state: 'success', environmentUrl: 'https://preview.example.internal' }),
    deployment({ id: 'd2', environment: 'staging', sha: 'fff9999', state: 'pending' }),
  ] } as PreviewReport)
  renderReview()
  expect(await screen.findByText('matches the last factory-recorded PR head')).toBeTruthy()
  expect(screen.getByText('differs from the last factory-recorded PR head')).toBeTruthy()
  for (const badge of screen.getAllByText(/factory-recorded PR head/)) expect(badge.parentElement?.textContent).toContain('abc1234')
})
it('after an external push labels the badge as the last factory-recorded head, never the current forge head', async () => {
  const item = { ...base, state: 'checking', prs: [{ number: 7, url: 'https://forge/pull/7', headSha: 'fedcba9', state: 'open', draft: false }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
  renderReview()
  expect(await screen.findByText('differs from the last factory-recorded PR head')).toBeTruthy()
  expect(screen.getByText('fedcba9')).toBeTruthy()
  expect(screen.queryByText('matches the PR head')).toBeNull()
  expect(screen.queryByText('not the PR head')).toBeNull()
  expect(screen.queryByText(/current forge head/)).toBeNull()
})
it('shows preview rows unbadged when no PR is recorded', async () => {
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
  renderReview()
  expect(await screen.findByText('abc1234')).toBeTruthy()
  expect(screen.queryByText('matches the last factory-recorded PR head')).toBeNull()
  expect(screen.queryByText('differs from the last factory-recorded PR head')).toBeNull()
})
it('lists commit-status links as provider links', async () => {
  const item = { ...base, state: 'checking', rounds: [{ headSha: 'abc1234', ci: { headSha: 'abc1234', complete: true, checks: [
    { id: 's1', key: { kind: 'commit_status', name: 'deploy/preview', appId: 0 }, sha: 'abc1234', status: 'success', conclusion: 'success', url: 'https://preview.example.internal', logTail: '', observedAt: '' },
    { id: 'c1', key: { kind: 'check_run', name: 'ci/build', appId: 0 }, sha: 'abc1234', status: 'completed', conclusion: 'success', url: 'https://ci.example/run/1', logTail: '', observedAt: '' },
  ] } }] } as unknown as Item
  getItem.mockResolvedValue(item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport } as PreviewReport)
  renderReview()
  expect(await screen.findByText('Links reported by commit statuses:')).toBeTruthy()
  const link = screen.getByRole('link', { name: 'https://preview.example.internal' })
  expect(link.getAttribute('target')).toBe('_blank')
  expect(link.closest('li')?.textContent).toContain('deploy/preview')
  expect(link.closest('li')?.textContent).toContain('success')
  expect(screen.queryByRole('link', { name: 'https://ci.example/run/1' })).toBeNull()
})
it('renders valid http(s) URLs as links and unsafe ones as text with no href', async () => {
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport,
    deployments: [
      deployment({ id: 'd1', environment: 'script', sha: '', state: 'success', environmentUrl: 'javascript:alert(1)' }),
      deployment({ id: 'd2', environment: 'payload', sha: '', state: 'success', environmentUrl: 'data:text/html,<b>x</b>' }),
      deployment({ id: 'd3', environment: 'good', sha: '', state: 'success', environmentUrl: 'https://preview.example.internal', logUrl: 'https://ci.example/run/42/log' }),
    ],
    environments: [{ name: 'staging', id: 'e1', htmlUrl: 'gopher://staging.example.internal', branchPolicy: '', protectionRuleCount: 0 }],
  } as unknown as PreviewReport)
  renderReview()
  const link = await screen.findByRole('link', { name: 'https://preview.example.internal' })
  expect(link.getAttribute('target')).toBe('_blank')
  expect(link.getAttribute('rel')).toBe('noopener noreferrer')
  expect(screen.getByRole('link', { name: 'https://ci.example/run/42/log' }).getAttribute('rel')).toBe('noopener noreferrer')
  expect(screen.getByText('javascript:alert(1)').closest('a')).toBeNull()
  expect(screen.getByText('data:text/html,<b>x</b>').closest('a')).toBeNull()
  expect(screen.getByText('gopher://staging.example.internal').closest('a')).toBeNull()
  const hrefs = [...document.querySelectorAll('a')].map((a) => a.getAttribute('href') ?? '')
  expect(hrefs.filter((href) => href.startsWith('javascript:') || href.startsWith('data:')).length).toBe(0)
})
it('matches the bridge D10 validator: C1 controls and opaque https: URLs are plain text, absolute http(s) is a link', async () => {
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport,
    deployments: [
      deployment({ id: 'd1', environment: 'c1', sha: '', state: 'success', environmentUrl: 'https://example.test/x\u0085' }),
      deployment({ id: 'd2', environment: 'opaque', sha: '', state: 'success', environmentUrl: 'https:foo' }),
      deployment({ id: 'd3', environment: 'good', sha: '', state: 'success', environmentUrl: 'https://example.test/x' }),
    ],
  } as PreviewReport)
  renderReview()
  expect((await screen.findByRole('link', { name: 'https://example.test/x' })).getAttribute('rel')).toBe('noopener noreferrer')
  expect(screen.getByText('https://example.test/x\u0085').closest('a')).toBeNull()
  expect(screen.getByText('https:foo').closest('a')).toBeNull()
})
it('shows the preview empty state', async () => {
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport } as PreviewReport)
  renderReview()
  expect(await screen.findByText('No preview deployments or test environments are configured for this revision.')).toBeTruthy()
})
it('shows the preview error state', async () => {
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  vi.mocked(api.getPreviewEnvironments).mockRejectedValueOnce(new Error('forge unreachable'))
  renderReview()
  expect(await screen.findByText('Preview and test environments could not be read.')).toBeTruthy()
  expect(screen.getByText('forge unreachable')).toBeTruthy()
})
it('refreshes the preview report on demand', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  const report = { ...previewReport, deployments: [deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' })] } as PreviewReport
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce(report).mockResolvedValueOnce(report)
  renderReview()
  await screen.findByText('abc1234')
  expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(1)
  await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await vi.waitFor(() => { expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(2) })
})
it('loadsPreviewOnceAcrossStoreUpdates', async () => {
  const first = { ...base, state: 'checking', stage: 'first' } as Item
  const second = { ...first, stage: 'second', title: 'Renamed' } as Item
  getItem.mockResolvedValue(first)
  vi.mocked(api.getPreviewEnvironments).mockResolvedValue({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
  const view = renderReview(factoryState({ items: [first] }))
  expect(await screen.findByText('abc1234')).toBeTruthy()
  expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(1)
  // A store update redraws the item in place, without remounting it...
  await view.rerender({ factory: factoryState({ items: [second] }) })
  expect(await screen.findByRole('heading', { name: 'Renamed' })).toBeTruthy()
  expect(screen.getByText('second')).toBeTruthy()
  // ...and never re-reads the item or the preview.
  expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(1)
  expect(getItem).toHaveBeenCalledTimes(1)
})
it('keeps the newest preview response when an older in-flight request resolves later', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  getItem.mockResolvedValue({ ...base, state: 'checking' } as Item)
  const stale = { ...previewReport, deployments: [deployment({ id: 'd1', environment: 'stale', sha: '', state: 'success' })] } as unknown as PreviewReport
  const fresh = { ...previewReport, deployments: [deployment({ id: 'd2', environment: 'fresh', sha: '', state: 'success' })] } as unknown as PreviewReport
  let releaseStale: () => void = () => {}
  const gate = new Promise<void>((resolve) => { releaseStale = resolve })
  vi.mocked(api.getPreviewEnvironments).mockImplementationOnce(() => gate.then(() => stale)).mockImplementationOnce(async () => fresh)
  renderReview()
  await screen.findByText('Reading preview environments…')
  await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await screen.findByText('fresh')
  releaseStale()
  await new Promise((resolve) => setTimeout(resolve, 0))
  expect(screen.queryByText('stale')).toBeNull()
  expect(screen.getByText('fresh')).toBeTruthy()
})
it('initialViewFollowsStateOnlyOnce', async () => {
  const ready = { ...base, state: 'ready' } as Item
  getItem.mockResolvedValue(ready)
  const view = renderItem()
  // Ready work opens on its review, because that is what a person does next.
  expect(await screen.findByRole('heading', { name: 'CI evidence' })).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'Review' }).getAttribute('aria-selected')).toBe('true')
  // A poll reports the next state; the tab the reader is on does not move with it.
  await view.rerender({ factory: factoryState({ items: [{ ...ready, state: 'implementing', stage: 'second' } as Item] }) })
  expect(await screen.findByText('second')).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'Review' }).getAttribute('aria-selected')).toBe('true')
})
it('explicitViewSurvivesStatePolling', async () => {
  const item = { ...base, state: 'checking', approvals: [approval(1, 'First plan'), approval(2, 'Second plan')] } as Item
  getItem.mockResolvedValue(item)
  const view = renderItem(factoryState(), { view: 'documents' })
  expect(await screen.findByTestId('documents-view')).toBeTruthy()
  await view.rerender({ factory: factoryState({ items: [{ ...item, state: 'ready', stage: 'second' } as Item] }) })
  expect(await screen.findByText('second')).toBeTruthy()
  // The route named this view explicitly, so a state change cannot take it away.
  expect(screen.getByTestId('documents-view')).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'Documents' }).getAttribute('aria-selected')).toBe('true')
})
it('approvalOnNonDraftFallsBackToActivity', async () => {
  const item = { ...base, state: 'implementing', history: [{ sequence: 1, at: '2026-01-01T00:00:00Z', kind: 'factory', message: 'started', effectId: '' }] } as Item
  getItem.mockResolvedValue(item)
  renderItem(factoryState(), { view: 'approval' })
  expect(await screen.findByTestId('activity-timeline')).toBeTruthy()
  expect(screen.queryByTestId('draft-approval')).toBeNull()
  // There is nothing to approve, so the tab is not offered at all.
  expect(screen.queryByRole('tab', { name: 'Approval' })).toBeNull()
  expect(screen.getByRole('tab', { name: 'Activity' }).getAttribute('aria-selected')).toBe('true')
})
it('tabsNavigateWithoutRemountingItem', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'implementing', prs: [{ number: 7, url: 'https://forge.example/pull/7', headRef: 'feat/x', baseRef: 'main', headSha: 'abc1234ff', baseSha: 'def5678aa', state: 'open', draft: false }] } as unknown as Item
  getItem.mockResolvedValue(item)
  const view = renderItem()
  await screen.findByTestId('activity-timeline')
  await fireEvent.click(screen.getByRole('tab', { name: 'Pull requests' }))
  // A tab is a route, not local state: the shell is told where to go and keeps
  // this same keyed item mounted for a query-only change.
  expect(onNavigate).toHaveBeenCalledWith('#factory/x?view=prs')
  await view.rerender({ route: { itemId: 'x', view: 'prs' } })
  expect(await screen.findByTestId('pull-requests-view')).toBeTruthy()
  expect(getItem).toHaveBeenCalledTimes(1)
})
it('documentsShowCurrentAndPriorApprovalRevisions', async () => {
  const item = { ...base, state: 'checking', approvals: [approval(1, 'First plan', 'First spec'), approval(2, 'Second plan', 'Second spec')] } as Item
  getItem.mockResolvedValue(item)
  renderItem(factoryState(), { view: 'documents' })
  // The latest revision is what governs, so it is the document shown.
  expect(await screen.findByText('Second plan')).toBeTruthy()
  expect(screen.getByText('Second spec')).toBeTruthy()
  expect(screen.getByText('Approved revision 2')).toBeTruthy()
  // The superseded revision stays readable, collapsed behind its own summary.
  const earlier = screen.getByTestId('previous-revision')
  expect(earlier.hasAttribute('open')).toBe(false)
  expect(earlier.textContent).toContain('Revision 1')
  expect(earlier.textContent).toContain('First plan')
  expect(earlier.textContent).toContain('First spec')
})
it('pullRequestsShowRealRevisionAndState', async () => {
  const item = { ...base, state: 'ready', prs: [
    { number: 7, url: 'https://forge.example/pull/7', headRef: 'feat/x', baseRef: 'main', headSha: 'abc1234ff', baseSha: 'def5678aa', state: 'open', draft: false },
    { number: 8, url: 'javascript:alert(1)', headRef: 'feat/y', baseRef: 'main', headSha: '9999999aa', baseSha: '8888888bb', state: 'open', draft: true },
  ] } as unknown as Item
  getItem.mockResolvedValue(item)
  renderItem(factoryState(), { view: 'prs' })
  const rows = await screen.findAllByTestId('pull-request')
  expect(rows[0].textContent).toContain('PR #7')
  expect(rows[0].textContent).toContain('open')
  expect(rows[0].textContent).toContain('feat/x')
  expect(rows[0].textContent).toContain('main')
  // The recorded revision, not a live forge read.
  expect(rows[0].textContent).toContain('head abc1234 · base def5678')
  const link = screen.getByRole('link', { name: 'https://forge.example/pull/7' })
  expect(link.getAttribute('target')).toBe('_blank')
  expect(link.getAttribute('rel')).toBe('noreferrer noopener')
  // A draft PR says so, and a URL that is not absolute http(s) stays text.
  expect(rows[1].textContent).toContain('draft')
  expect(rows[1].querySelector('a')).toBeNull()
})
