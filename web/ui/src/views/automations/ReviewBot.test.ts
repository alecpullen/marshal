import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import ReviewBot from './ReviewBot.svelte'
import * as api from '../../lib/api.js'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return {
    ...actual,
    getProjectSettings: vi.fn(),
    putProjectSettings: vi.fn(),
    listRepos: vi.fn(),
    listSecrets: vi.fn(),
    createWebhookSecret: vi.fn(),
    listReviewDrafts: vi.fn(),
    editReviewDraft: vi.fn(),
    postReviewDraft: vi.fn(),
    discardReviewDraft: vi.fn(),
    sendDraftToAuthor: vi.fn(),
  }
})

afterEach(cleanup)

const ROOT = '/home/u/alpha'
const draft = (o: Partial<api.ReviewDraft> = {}): api.ReviewDraft => ({
  id: 'd1',
  repoId: 'r1',
  number: 12,
  headSha: 'abc123',
  agentId: 'a9',
  summary: 'Two things to look at.',
  status: 'draft',
  createdAt: '2026-10-05T00:00:00Z',
  findings: [
    { id: 'f1', severity: 'nit', title: 'Naming', body: 'rename x', path: 'a.go', line: 3 },
    { id: 'f2', severity: 'blocking', title: 'Leaks a goroutine', body: 'close the channel', path: 'src/b.go', line: 40, stepNode: 'step:7' },
  ],
  ...o,
})

const mount = (over: Record<string, unknown> = {}) => render(ReviewBot, { root: ROOT, onNavigate: vi.fn(), ...over })

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {}, mode: 'edit', automations: { ciFixer: { enabled: true, repoId: 'r1', branches: ['main'], maxMinutes: 10, maxUsd: 1, push: false, pushBranches: [] } } })
  ;(api.putProjectSettings as Mock).mockImplementation(async (_r: string, s: api.ProjectSettings) => s)
  ;(api.listRepos as Mock).mockResolvedValue([{ id: 'r1', url: 'https://github.com/o/n.git', forge: 'github' }, { id: 'r2', url: 'u2' }])
  ;(api.listSecrets as Mock).mockResolvedValue([])
  ;(api.listReviewDrafts as Mock).mockResolvedValue([draft()])
})

describe('Review bot settings', () => {
  it('saves nested automations.reviewBot and keeps the CI fixer settings', async () => {
    mount()
    await waitFor(() => expect(screen.getByLabelText('Review repo')).toBeTruthy())
    await waitFor(() => expect((screen.getByLabelText('Review repo') as HTMLSelectElement).options.length).toBe(3))
    await fireEvent.click(screen.getByLabelText('Review new pull requests'))
    await fireEvent.change(screen.getByLabelText('Review repo'), { target: { value: 'r1' } })
    await fireEvent.input(screen.getByLabelText('Labels'), { target: { value: 'ready, bot' } })
    await fireEvent.click(screen.getByLabelText('Post reviews automatically'))
    await fireEvent.click(screen.getByLabelText('blocking'))
    await fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    await waitFor(() => expect(api.putProjectSettings).toHaveBeenCalled())
    const [root, body] = (api.putProjectSettings as Mock).mock.calls[0]
    expect(root).toBe(ROOT)
    expect(body.mode).toBe('edit')
    expect(body.automations.ciFixer.repoId).toBe('r1')
    expect(body.automations.reviewBot).toMatchObject({ enabled: true, repoId: 'r1', labels: ['ready', 'bot'], skipDrafts: true, autoPost: true, holdSeverities: ['blocking'] })
  })

  it('shows the webhook secret once, with the payload URL', async () => {
    ;(api.createWebhookSecret as Mock).mockResolvedValue('s3cret')
    ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {}, automations: { reviewBot: { enabled: true, repoId: 'r1', skipDrafts: true, autoPost: false, holdSeverities: [] } } })
    mount()
    await waitFor(() => expect(screen.getByText('no secret')).toBeTruthy())
    expect(api.listSecrets).toHaveBeenCalledWith('hooks/')
    await fireEvent.click(screen.getByRole('button', { name: 'Create secret' }))
    await waitFor(() => expect(screen.getByTestId('webhook-secret').textContent).toBe('s3cret'))
    expect(api.createWebhookSecret).toHaveBeenCalledWith('r1')
    expect(screen.getByText(`${location.origin}/hooks/forge/r1`)).toBeTruthy()
    cleanup()
    // A fresh mount (the secret now exists) never shows the value again.
    ;(api.listSecrets as Mock).mockResolvedValue(['vault:hooks/r1'])
    mount()
    await waitFor(() => expect(screen.getByText('secret set')).toBeTruthy())
    expect(screen.queryByTestId('webhook-secret')).toBeNull()
  })

  it('asks twice before replacing an existing secret', async () => {
    ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {}, automations: { reviewBot: { enabled: true, repoId: 'r1', skipDrafts: true, autoPost: false, holdSeverities: [] } } })
    ;(api.listSecrets as Mock).mockResolvedValue(['vault:hooks/r1'])
    mount()
    await waitFor(() => expect(screen.getByText('secret set')).toBeTruthy())
    await fireEvent.click(screen.getByRole('button', { name: 'Replace secret' }))
    expect(api.createWebhookSecret).not.toHaveBeenCalled()
    ;(api.createWebhookSecret as Mock).mockResolvedValue('new')
    await fireEvent.click(screen.getByRole('button', { name: 'Replace? Click again' }))
    await waitFor(() => expect(api.createWebhookSecret).toHaveBeenCalledTimes(1))
  })
})

describe('Drafts', () => {
  it('lists drafts for the project with status and severity chips, and opens one by route', async () => {
    const onNavigate = vi.fn()
    mount({ onNavigate })
    const row = await screen.findByTestId('draft-row')
    expect(api.listReviewDrafts).toHaveBeenCalledWith({ project: ROOT })
    expect(within(row).getByText('PR #12')).toBeTruthy()
    expect(within(row).getByText('1 blocking')).toBeTruthy()
    expect(within(row).getByText('1 nit')).toBeTruthy()
    await fireEvent.click(row)
    expect(onNavigate).toHaveBeenCalledWith('#projects/%2Fhome%2Fu%2Falpha/automations/review-bot?draft=d1')
  })

  it('groups findings by severity, links file and evidence', async () => {
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    const sections = screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))
    expect(sections).toEqual(['blocking findings', 'nit findings'])
    expect((await screen.findByRole('link', { name: 'src/b.go:40' })).getAttribute('href')).toBe('https://github.com/o/n/blob/abc123/src/b.go#L40')
    expect(screen.getByRole('link', { name: 'Evidence' }).getAttribute('href')).toBe('#chat/a9?node=step%3A7')
  })

  it('edits a finding inline and saves it with the draft id', async () => {
    ;(api.editReviewDraft as Mock).mockImplementation(async (_id: string, e: { findings: api.Finding[]; summary: string }) => draft({ ...e }))
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    await fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    const titles = screen.getAllByLabelText('Title') as HTMLInputElement[]
    await fireEvent.input(titles[0], { target: { value: 'Leaks a goroutine on error' } })
    await fireEvent.change(screen.getAllByLabelText('Severity')[1], { target: { value: 'should-fix' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save edits' }))
    await waitFor(() => expect(api.editReviewDraft).toHaveBeenCalled())
    const [id, body] = (api.editReviewDraft as Mock).mock.calls[0]
    expect(id).toBe('d1')
    expect(body.summary).toBe('Two things to look at.')
    expect(body.findings.find((f: api.Finding) => f.id === 'f2').title).toBe('Leaks a goroutine on error')
    expect(body.findings.find((f: api.Finding) => f.id === 'f1').severity).toBe('should-fix')
  })

  it('confirms before posting, then shows the review link', async () => {
    ;(api.postReviewDraft as Mock).mockResolvedValue(draft({ status: 'posted', reviewUrl: 'https://github.com/o/n/pull/12#pullrequestreview-1' }))
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    await fireEvent.click(screen.getByRole('button', { name: 'Post' }))
    expect(api.postReviewDraft).not.toHaveBeenCalled()
    await fireEvent.click(await screen.findByRole('button', { name: 'Post review' }))
    await waitFor(() => expect(api.postReviewDraft).toHaveBeenCalledWith('d1'))
    expect((await screen.findByRole('link', { name: 'View review' })).getAttribute('href')).toContain('pullrequestreview-1')
    expect(screen.queryByRole('button', { name: 'Post' })).toBeNull()
  })

  it('discards a draft', async () => {
    ;(api.discardReviewDraft as Mock).mockResolvedValue(undefined)
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    await fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    await waitFor(() => expect(api.discardReviewDraft).toHaveBeenCalledWith('d1'))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Discard' })).toBeNull())
  })

  it('sends the chosen findings to the author agent', async () => {
    ;(api.sendDraftToAuthor as Mock).mockResolvedValue({ sent: 1, skipped: 1 })
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    await fireEvent.click(screen.getByRole('button', { name: 'Send to author agent' }))
    await fireEvent.click(screen.getByLabelText('Send Naming'))
    await fireEvent.click(screen.getByRole('button', { name: /Send 1 to author/ }))
    await waitFor(() => expect(api.sendDraftToAuthor).toHaveBeenCalledWith('d1', ['f2']))
    expect(await screen.findByText('Sent 1 finding to the author agent (1 skipped).')).toBeTruthy()
  })

  it('says so when no agent owns the PR', async () => {
    ;(api.sendDraftToAuthor as Mock).mockRejectedValue(new api.APIError(404, { error: 'no Marshal agent owns this PR' }))
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    await fireEvent.click(screen.getByRole('button', { name: 'Send to author agent' }))
    await fireEvent.click(screen.getByRole('button', { name: /Send 2 to author/ }))
    expect(await screen.findByText('No Marshal agent owns this PR')).toBeTruthy()
  })

  it('offers only Discard on a failed draft', async () => {
    ;(api.listReviewDrafts as Mock).mockResolvedValue([draft({ status: 'failed', error: 'no structured result', findings: [] })])
    ;(api.discardReviewDraft as Mock).mockResolvedValue(undefined)
    mount({ draftId: 'd1' })
    await screen.findByTestId('draft-view')
    expect(screen.getByRole('alert').textContent).toContain('no structured result')
    expect(screen.queryByRole('button', { name: 'Post' })).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    await waitFor(() => expect(api.discardReviewDraft).toHaveBeenCalledWith('d1'))
  })

  it('refetches drafts when the refresh tick moves', async () => {
    const { rerender } = mount()
    await screen.findByTestId('draft-row')
    await rerender({ root: ROOT, onNavigate: vi.fn(), refreshTick: 1 })
    await waitFor(() => expect(api.listReviewDrafts).toHaveBeenCalledTimes(2))
  })
})
