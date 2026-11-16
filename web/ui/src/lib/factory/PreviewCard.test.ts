import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import PreviewCard from './PreviewCard.svelte'
import * as api from '../api'
import type { FactoryCheck, FactoryRound } from '../factory'
import type { PreviewDeployment, PreviewReport } from '../preview'
import { currentStatusLinks } from './reviewModel'

vi.mock('../api', async (actual) => ({ ...await actual<typeof import('../api')>(), getPreviewEnvironments: vi.fn() }))

afterEach(cleanup)
beforeEach(() => { vi.mocked(api.getPreviewEnvironments).mockReset() })

const previewReport = { repoId: 'repo', forge: 'github', status: 'ok', ref: 'feature/x', sha: '', capabilities: [], environments: [], deployments: [], gaps: [] } as unknown as PreviewReport
const deployment = (over: Partial<PreviewDeployment>) => ({
  id: 'd', environment: 'production', originalEnvironment: 'production', ref: 'feature/x', sha: '', task: 'deploy',
  state: '', environmentUrl: '', targetUrl: '', logUrl: '', description: '', transient: false, production: false,
  createdAt: '', updatedAt: '', ...over,
}) as PreviewDeployment
/** The card's props: the revision-scoped read, and the current revision's status links. */
const renderCard = (over: { prHead?: string; statusLinks?: FactoryCheck[] } = {}) =>
  render(PreviewCard, { repoId: 'repo', branch: 'feature/x', prHead: '', statusLinks: [], ...over })
const check = (over: Partial<FactoryCheck> = {}): FactoryCheck =>
  ({ key: { kind: 'commit_status', name: 'deploy/preview', appId: 0 }, id: 's1', sha: 'abc1234', status: 'success', conclusion: 'success', url: '', logTail: '', observedAt: '', ...over }) as FactoryCheck

it('badges preview rows against the last factory-recorded PR head', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'ABC1234', state: 'success', environmentUrl: 'https://preview.example.internal' }),
    deployment({ id: 'd2', environment: 'staging', sha: 'fff9999', state: 'pending' }),
  ] } as PreviewReport)
  renderCard({ prHead: 'abc1234' })
  expect(await screen.findByText('matches the last factory-recorded PR head')).toBeTruthy()
  expect(screen.getByText('differs from the last factory-recorded PR head')).toBeTruthy()
  for (const badge of screen.getAllByText(/factory-recorded PR head/)) expect(badge.parentElement?.textContent).toContain('abc1234')
})

it('after an external push labels the badge as the last factory-recorded head, never the current forge head', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
  renderCard({ prHead: 'fedcba9' })
  expect(await screen.findByText('differs from the last factory-recorded PR head')).toBeTruthy()
  expect(screen.getByText('fedcba9')).toBeTruthy()
  expect(screen.queryByText('matches the PR head')).toBeNull()
  expect(screen.queryByText('not the PR head')).toBeNull()
  expect(screen.queryByText(/current forge head/)).toBeNull()
})

it('shows preview rows unbadged when no PR is recorded', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
  renderCard()
  expect(await screen.findByText('abc1234')).toBeTruthy()
  expect(screen.queryByText('matches the last factory-recorded PR head')).toBeNull()
  expect(screen.queryByText('differs from the last factory-recorded PR head')).toBeNull()
})

it('lists commit-status links as provider links', async () => {
  const round = { headSha: 'abc1234', ci: { headSha: 'abc1234', complete: true, checks: [
    check({ id: 's1', key: { kind: 'commit_status', name: 'deploy/preview', appId: 0 }, sha: 'abc1234', url: 'https://preview.example.internal' }),
    check({ id: 'c1', key: { kind: 'check_run', name: 'ci/build', appId: 0 }, sha: 'abc1234', url: 'https://ci.example/run/1' }),
  ] } } as unknown as FactoryRound
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport } as PreviewReport)
  renderCard({ statusLinks: currentStatusLinks(round) })
  expect(await screen.findByText('Links reported by commit statuses:')).toBeTruthy()
  const link = screen.getByRole('link', { name: 'https://preview.example.internal' })
  expect(link.getAttribute('target')).toBe('_blank')
  expect(link.closest('li')?.textContent).toContain('deploy/preview')
  expect(link.closest('li')?.textContent).toContain('success')
  expect(screen.queryByRole('link', { name: 'https://ci.example/run/1' })).toBeNull()
})

it('renders valid http(s) URLs as links and unsafe ones as text with no href', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport,
    deployments: [
      deployment({ id: 'd1', environment: 'script', sha: '', state: 'success', environmentUrl: 'javascript:alert(1)' }),
      deployment({ id: 'd2', environment: 'payload', sha: '', state: 'success', environmentUrl: 'data:text/html,<b>x</b>' }),
      deployment({ id: 'd3', environment: 'good', sha: '', state: 'success', environmentUrl: 'https://preview.example.internal', logUrl: 'https://ci.example/run/42/log' }),
    ],
    environments: [{ name: 'staging', id: 'e1', htmlUrl: 'gopher://staging.example.internal', branchPolicy: '', protectionRuleCount: 0 }],
  } as unknown as PreviewReport)
  renderCard()
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
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport,
    deployments: [
      deployment({ id: 'd1', environment: 'c1', sha: '', state: 'success', environmentUrl: 'https://example.test/x\u0085' }),
      deployment({ id: 'd2', environment: 'opaque', sha: '', state: 'success', environmentUrl: 'https:foo' }),
      deployment({ id: 'd3', environment: 'good', sha: '', state: 'success', environmentUrl: 'https://example.test/x' }),
    ],
  } as PreviewReport)
  renderCard()
  expect((await screen.findByRole('link', { name: 'https://example.test/x' })).getAttribute('rel')).toBe('noopener noreferrer')
  expect(screen.getByText('https://example.test/x\u0085').closest('a')).toBeNull()
  expect(screen.getByText('https:foo').closest('a')).toBeNull()
})

it('shows the preview empty state', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce({ ...previewReport } as PreviewReport)
  renderCard()
  expect(await screen.findByText('No preview deployments or test environments are configured for this revision.')).toBeTruthy()
})

it('shows the preview error state', async () => {
  vi.mocked(api.getPreviewEnvironments).mockRejectedValueOnce(new Error('forge unreachable'))
  renderCard()
  expect(await screen.findByText('Preview and test environments could not be read.')).toBeTruthy()
  expect(screen.getByText('forge unreachable')).toBeTruthy()
})

it('refreshes the preview report on demand', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const report = { ...previewReport, deployments: [deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' })] } as PreviewReport
  vi.mocked(api.getPreviewEnvironments).mockResolvedValueOnce(report).mockResolvedValueOnce(report)
  renderCard()
  await screen.findByText('abc1234')
  expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(1)
  await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await vi.waitFor(() => { expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(2) })
})

it('loadsPreviewOnceAcrossPropUpdates', async () => {
  vi.mocked(api.getPreviewEnvironments).mockResolvedValue({ ...previewReport, deployments: [
    deployment({ id: 'd1', environment: 'production', sha: 'abc1234', state: 'success' }),
  ] } as PreviewReport)
    const view = renderCard()
  expect(await screen.findByText('abc1234')).toBeTruthy()
  expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(1)
  // A parent's redraw — the same item, newer status links — is not a re-read:
  // the mount is the only automatic preview request.
  await view.rerender({ statusLinks: [check({ url: 'https://preview.example.internal' })], prHead: 'fedcba9' })
  expect(await screen.findByText('Links reported by commit statuses:')).toBeTruthy()
  expect(api.getPreviewEnvironments).toHaveBeenCalledTimes(1)
  expect(api.getPreviewEnvironments).toHaveBeenCalledWith('repo', 'feature/x')
})

it('keeps the newest preview response when an older in-flight request resolves later', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const stale = { ...previewReport, deployments: [deployment({ id: 'd1', environment: 'stale', sha: '', state: 'success' })] } as unknown as PreviewReport
  const fresh = { ...previewReport, deployments: [deployment({ id: 'd2', environment: 'fresh', sha: '', state: 'success' })] } as unknown as PreviewReport
  let releaseStale: () => void = () => {}
  const gate = new Promise<void>((resolve) => { releaseStale = resolve })
  vi.mocked(api.getPreviewEnvironments).mockImplementationOnce(() => gate.then(() => stale)).mockImplementationOnce(async () => fresh)
  renderCard()
  await screen.findByText('Reading preview environments…')
  await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await screen.findByText('fresh')
  releaseStale()
  await new Promise((resolve) => setTimeout(resolve, 0))
  expect(screen.queryByText('stale')).toBeNull()
  expect(screen.getByText('fresh')).toBeTruthy()
})
