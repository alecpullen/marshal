import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import ReviewView from './ReviewView.svelte'
import * as api from '../api'
import type { FactoryItem as Item } from '../factory'
import type { PreviewDeployment, PreviewReport } from '../preview'

vi.mock('../api', async (actual) => ({ ...await actual<typeof import('../api')>(), getPreviewEnvironments: vi.fn() }))

afterEach(cleanup)
beforeEach(() => {
  // The review route mounts the preview card, so its read is answered here.
  vi.mocked(api.getPreviewEnvironments).mockReset()
  vi.mocked(api.getPreviewEnvironments).mockResolvedValue({ ...previewReport } as PreviewReport)
})

/** One work item as the bridge reports one: the review view reads `item` only. */
const item = (over: Partial<Item> = {}): Item =>
  ({ id: 'x', title: 'Task', state: 'ready', repoId: 'repo', branch: 'feature/x', draft: { plan: '', spec: '' }, rounds: [], approvals: [], grants: [], prs: [], workers: [], history: [], ...over }) as unknown as Item

const renderReview = (over: Partial<Item> = {}) => render(ReviewView, { item: item(over) })

/** Document order: true when `second` comes after `first`. */
const follows = (first: Element, second: Element) => !!(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING)

const previewReport = { repoId: 'repo', forge: 'github', status: 'ok', ref: 'feature/x', sha: '', capabilities: [], environments: [], deployments: [], gaps: [] } as unknown as PreviewReport
const deployment = (over: Partial<PreviewDeployment>) => ({
  id: 'd', environment: 'production', originalEnvironment: 'production', ref: 'feature/x', sha: '', task: 'deploy',
  state: '', environmentUrl: '', targetUrl: '', logUrl: '', description: '', transient: false, production: false,
  createdAt: '', updatedAt: '', ...over,
}) as PreviewDeployment

it('shows failed CI before compact passes', async () => {
  renderReview({ state: 'needs_input', rounds: [{ ci: { complete: true, checks: [
    { key: { name: 'fail' }, conclusion: 'failure', logTail: 'trace' }, { key: { name: 'pass' }, conclusion: 'success' },
  ] } }] } as unknown as Partial<Item>)
  await screen.findByText(/fail: failure/)
  expect(screen.getByText('1 passing checks')).toBeTruthy()
})

it('failedChecksAndFindingsPrecedeCompactPasses', async () => {
  renderReview({ rounds: [{ headSha: 'abc1234', ci: { headSha: 'abc1234', complete: true, checks: [
    { key: { name: 'ci/build' }, conclusion: 'failure', logTail: 'build failed' },
    { key: { name: 'ci/lint' }, conclusion: 'success' },
  ] }, review: { outcome: 'complete', inputsAccessible: true, summary: 'Needs repair', findings: [
    { severity: 'high', path: 'web/a.go', line: 42, title: 'Unsafe call', body: 'Handle the error' },
  ] } }] } as unknown as Partial<Item>)
  // A failing check and the finding it left behind are both expanded, and both
  // come before the passing checks — which start collapsed.
  const failure = await screen.findByTestId('ci-failure')
  expect(failure.textContent).toContain('ci/build: failure')
  expect(failure.textContent).toContain('build failed')
  const finding = screen.getByTestId('review-finding')
  expect(finding.textContent).toContain('high: Unsafe call')
  const passes = screen.getByTestId('ci-passes')
  expect(passes.hasAttribute('open')).toBe(false)
  expect(follows(failure, finding)).toBe(true)
  expect(follows(finding, passes)).toBe(true)
  // The approved order: evidence, then the revision previews belong to, then
  // the report, then the real forge links.
  expect(follows(screen.getByTestId('ci-evidence'), screen.getByTestId('factory-preview'))).toBe(true)
  expect(follows(screen.getByTestId('factory-preview'), screen.getByTestId('engineering-report'))).toBe(true)
  expect(follows(screen.getByTestId('engineering-report'), screen.getByTestId('review-forge-links'))).toBe(true)
})

it('incompleteOrStaleCIIsNotSuccess', async () => {
  renderReview({ rounds: [{ headSha: 'abc1234', ci: { headSha: 'abc1234', complete: false, checks: [
    { key: { name: 'ci/build' }, conclusion: 'success' },
  ] } }] } as unknown as Partial<Item>)
  const incomplete = await screen.findByTestId('ci-state')
  expect(incomplete.textContent).toContain('CI evidence unavailable or incomplete')
  expect(incomplete.className).toContain('text-attention')
  cleanup()
  // Evidence observed at another head is not evidence for this revision.
  renderReview({ rounds: [{ headSha: 'abc1234', ci: { headSha: 'older00', complete: true, checks: [
    { key: { name: 'ci/build' }, conclusion: 'success' },
  ] } }] } as unknown as Partial<Item>)
  const stale = await screen.findByTestId('ci-state')
  expect(stale.textContent).toContain('belongs to a different revision than this round')
  expect(stale.className).toContain('text-attention')
  // Neither warning ever reads as a success statement about this revision.
  expect(screen.queryByText('CI evidence is recorded for this revision.')).toBeNull()
  expect(screen.queryByText(/success/i)).toBeNull()
})

it('previewHeadMismatchIsWarning', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
  renderReview({ prs: [{ number: 7, url: 'https://forge.example/pull/7', headSha: 'fedcba9', headRef: 'feat/x', baseRef: 'main', state: 'open', draft: false }] } as unknown as Partial<Item>)
  const badge = await screen.findByTestId('head-badge')
  expect(badge.textContent).toContain('differs from the last factory-recorded PR head')
  expect(badge.className).toContain('text-attention')
})

it('reviewFailureIsNotCleanReview', async () => {
  renderReview({ rounds: [{
    headSha: 'abc1234',
    ci: { headSha: 'abc1234', complete: true, checks: [{ key: { name: 'ci/build' }, conclusion: 'success' }] },
    implementation: { summary: 'Implemented the change' },
    review: { outcome: 'needs_input', inputsAccessible: true, summary: 'Not ready', findings: [{ severity: 'high', title: 'Unsafe call' }] },
  }] } as unknown as Partial<Item>)
  const state = await screen.findByTestId('review-state')
  expect(state.textContent).toContain('not a clean review')
  expect(state.className).toContain('text-attention')
  expect(screen.queryByText('Review complete with no findings.')).toBeNull()
  cleanup()
  // A complete review with no findings is the only clean one.
  renderReview({ rounds: [{
    headSha: 'abc1234',
    ci: { headSha: 'abc1234', complete: true, checks: [{ key: { name: 'ci/build' }, conclusion: 'success' }] },
    implementation: { summary: 'Implemented the change' },
    review: { outcome: 'complete', inputsAccessible: true, summary: 'Clean', findings: [] },
  }] } as unknown as Partial<Item>)
  expect((await screen.findByTestId('review-state')).textContent).toBe('Review complete with no findings.')
})

it('distinguishes empty from unavailable CI evidence', async () => {
  renderReview({ state: 'checking', rounds: [{ ci: { complete: false, checks: [] } }] } as unknown as Partial<Item>)
  expect(await screen.findByText(/CI evidence unavailable or incomplete/)).toBeTruthy()
  cleanup()
  renderReview({ state: 'checking', rounds: [{ ci: { complete: true, checks: [] } }] } as unknown as Partial<Item>)
  expect(await screen.findByText('Observation complete; no checks reported.')).toBeTruthy()
})

const narration = {
  issue: 'The report hides the reasoning', approach: 'Narrate the change in the report',
  existingBehaviour: 'Only a summary paragraph existed', alternatives: 'Extend the summary',
  tradeoffs: 'A longer report body', choice: 'Add a structured narration',
  verification: 'go test ./web/bridge', limitations: 'Excerpts are selections, not diffs',
  excerpts: [{ path: 'web/bridge/factory_types.go', lang: 'go', code: 'type FactoryNarration struct{}' }],
}

it('renders the narration sections in order', async () => {
  renderReview({ state: 'checking', rounds: [{ implementation: { summary: 'Bare summary', narration } }] } as unknown as Partial<Item>)
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
  renderReview({ state: 'checking', rounds: [{ implementation: { summary: 'Bare summary', narration: { ...narration,
    excerpts: [{ path: 'web/bridge/factory_types.go', lang: 'go', code: 'type FactoryNarration struct{}' }, { path: 'web/ui/src/lib/factory.ts', code: 'interface FactoryExcerpt {}' }] } } }] } as unknown as Partial<Item>)
  await screen.findByText('web/bridge/factory_types.go')
  expect(screen.getByText('type FactoryNarration struct{}')).toBeTruthy()
  expect(screen.getByText('web/ui/src/lib/factory.ts')).toBeTruthy()
  expect(screen.getByText('interface FactoryExcerpt {}')).toBeTruthy()
  expect(screen.getByText('go')).toBeTruthy()
})

it('renders the narration alongside the CI card', async () => {
  renderReview({ state: 'checking', rounds: [{
    ci: { complete: true, checks: [{ key: { name: 'fail' }, conclusion: 'failure', logTail: 'trace' }, { key: { name: 'pass' }, conclusion: 'success' }] },
    implementation: { summary: 'Bare summary', narration },
  }] } as unknown as Partial<Item>)
  expect((await screen.findByText(/fail: failure/)).textContent).toContain('failure')
  expect(screen.getByText('trace')).toBeTruthy()
  expect(screen.getByText('1 passing checks').closest('details')?.hasAttribute('open')).toBe(false)
  expect(screen.getByText('Issue')).toBeTruthy()
  expect(screen.getByText('The report hides the reasoning')).toBeTruthy()
})

it('falls back to the summary when there is no narration', async () => {
  renderReview({ state: 'checking', rounds: [{ implementation: { summary: 'Bare summary' } }] } as unknown as Partial<Item>)
  expect(await screen.findByText('Bare summary')).toBeTruthy()
  expect(screen.queryByText('Issue')).toBeNull()
})

it('links the real recorded revision of each pull request, and offers no merge', async () => {
  renderReview({ prs: [
    { number: 7, url: 'https://forge.example/pull/7', headRef: 'feat/x', baseRef: 'main', headSha: 'abc1234ff', baseSha: 'def5678aa', state: 'open', draft: false },
    { number: 8, url: 'javascript:alert(1)', headRef: 'feat/y', baseRef: 'main', headSha: '9999999aa', baseSha: '8888888bb', state: 'open', draft: true },
  ] } as unknown as Partial<Item>)
  const rows = await screen.findAllByTestId('review-pr')
  expect(rows[0].textContent).toContain('PR #7')
  expect(rows[0].textContent).toContain('open')
  // The recorded revision, not a live forge read.
  expect(rows[0].textContent).toContain('head abc1234 · base def5678')
  const link = screen.getByRole('link', { name: 'https://forge.example/pull/7' })
  expect(link.getAttribute('target')).toBe('_blank')
  expect(link.getAttribute('rel')).toBe('noreferrer noopener')
  // A URL that is not absolute http(s) stays text, and there is no merge action.
  expect(rows[1].querySelector('a')).toBeNull()
  expect(screen.queryByRole('button', { name: /merge/i })).toBeNull()
  expect(screen.queryByText(/^Merge$/i)).toBeNull()
})

it('says so when no pull request has been recorded', async () => {
  renderReview()
  expect(await screen.findByText('No pull request recorded for this work item yet.')).toBeTruthy()
})
