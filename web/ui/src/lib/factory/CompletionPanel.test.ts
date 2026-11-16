import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import CompletionPanel from './CompletionPanel.svelte'
import type {
  FactoryAssessment, FactoryCompletion, FactoryItem, FactoryMergeBatch, FactoryMergeStep, FactoryPR, FactoryPRLink,
} from '../factory'

afterEach(cleanup)

/** One pull request as the bridge observed it; only the fields a surface reads. */
const pr = (n: number, over: Partial<FactoryPR> = {}): FactoryPR => ({
  number: n,
  url: `https://forge.example/pull/${n}`,
  nodeId: `node-${n}`,
  headRef: `feat/${n}`,
  baseRef: 'main',
  headSha: `head-${n}-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`,
  baseSha: 'base-sha',
  state: 'open',
  draft: false,
  ...over,
})

/** One link: a pull request plus the observation the bridge made of it. */
const link = (n: number, over: Partial<FactoryPRLink> = {}): FactoryPRLink => ({
  repoId: 'repo',
  pr: pr(n),
  origin: 'user',
  observedAt: '2026-05-10T12:00:00Z',
  fresh: true,
  ...over,
})

const step = (n: number, state: string, over: Partial<FactoryMergeStep> = {}): FactoryMergeStep => ({
  repoId: 'repo',
  number: n,
  nodeId: `node-${n}`,
  headSha: `head-${n}-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`,
  baseRef: 'main',
  strategy: 'merge',
  state,
  ...over,
})

const batch = (over: Partial<FactoryMergeBatch> = {}): FactoryMergeBatch => ({
  id: 'batch-1',
  requestId: 'req-1',
  state: 'active',
  completeAfter: false,
  steps: [],
  ...over,
})

const assessment = (over: Partial<FactoryAssessment> = {}): FactoryAssessment => ({
  status: 'available',
  summary: 'The work matches its criteria.',
  source: 'assessor',
  inputDigest: 'digest-1',
  observedAt: '2026-05-10T12:00:00Z',
  fresh: true,
  ...over,
})

/** A work item as the bridge reports one. Completion is absent unless a test sets it. */
const item = (over: Partial<FactoryItem> = {}): FactoryItem => ({
  id: 'x',
  title: 'Task',
  state: 'implementing',
  version: 3,
  approvals: null,
  rounds: [],
  grants: [],
  workers: [],
  effects: [],
  prs: [],
  history: [],
  ...over,
}) as unknown as FactoryItem

const completion = (over: Partial<FactoryCompletion> = {}): FactoryCompletion => ({ status: 'open', links: [], ...over })

/** The ordered lines the confirmation shows, in the order it shows them. */
const confirmLines = () => screen.getAllByTestId('completion-confirm-pr').map((row) => row.textContent?.replace(/\s+/g, ' ').trim())
/** The payload a panel handed back with a kind, as the caller would build an envelope from it. */
const sent = (run: ReturnType<typeof vi.fn>, call = -1) => run.mock.calls.at(call) as [string, Record<string, unknown>]
const selectable = async (n: number) => fireEvent.click(screen.getByLabelText(`Select PR #${n} for merging`))
const dialog = () => screen.getByTestId('completion-confirm')

describe('CompletionPanel', () => {
  it('reads business status apart from the execution state', () => {
    render(CompletionPanel, { item: item({ state: 'stopped', completion: completion({ status: 'complete' }) }), busy: false, run: vi.fn() })
    // Done is the person's decision; what the run is doing is its own fact.
    expect(screen.getByTestId('completion-status').textContent).toContain('Complete')
    expect(screen.getByTestId('completion-execution').textContent).toBe('stopped')
    // The goal is the item's own title.
    expect(screen.getByTestId('completion-goal').textContent).toBe('Task')
    // No approval records any criteria, so the panel says they are unknown
    // rather than presenting the working draft as agreed.
    expect(screen.getByTestId('completion-criteria').textContent).toContain('Unknown')
  })

  it('states criteria from the newest approval', () => {
    const approvals = [
      { revision: 1, plan: 'First', spec: 'Old criteria', planHash: 'h1', config: {}, approvedAt: '' },
      { revision: 2, plan: 'Second', spec: 'The suite passes and the migration is reversible', planHash: 'h2', config: {}, approvedAt: '' },
    ] as unknown as FactoryItem['approvals']
    render(CompletionPanel, { item: item({ approvals }), busy: false, run: vi.fn() })
    expect(screen.getByTestId('completion-criteria').textContent).toContain('The suite passes and the migration is reversible')
    expect(screen.getByTestId('completion-criteria').textContent).not.toContain('Old criteria')
  })

  it('completes with pull requests still open, and says what that leaves behind', async () => {
    const run = vi.fn()
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12), link(13)] }) }),
      busy: false,
      run,
    })
    // Open PRs are stated, never a reason to disable the person's decision.
    expect(screen.getByTestId('completion-decision').textContent).toContain('2 linked pull requests are still open')
    const button = screen.getByRole('button', { name: 'Complete work item' }) as HTMLButtonElement
    expect(button.disabled).toBe(false)
    await fireEvent.click(button)
    // Only the kind travels; no merge is implied and no version is invented.
    expect(run).toHaveBeenCalledWith('complete')
    expect(run.mock.calls.at(-1)?.[1]).toBeUndefined()
  })

  it('reopens without starting any work', async () => {
    const run = vi.fn()
    render(CompletionPanel, { item: item({ completion: completion({ status: 'complete' }) }), busy: false, run })
    // A complete item offers reopening, not a second completion.
    expect(screen.queryByRole('button', { name: 'Complete work item' })).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Reopen work item' }))
    expect(run).toHaveBeenCalledTimes(1)
    expect(run).toHaveBeenCalledWith('reopen')
    // Reopening resumes nothing: no merge, no stop, no assessment rides along.
    expect(sent(run)[0]).toBe('reopen')
    expect(sent(run)[1]).toBeUndefined()
  })

  it('links a pull request by number or URL and refreshes evidence', async () => {
    const run = vi.fn()
    render(CompletionPanel, { item: item({ completion: completion() }), busy: false, run })
    const input = screen.getByLabelText('Pull request number or URL')
    const linkButton = screen.getByRole('button', { name: 'Link PR' }) as HTMLButtonElement
    // Nothing typed, nothing to link.
    expect(linkButton.disabled).toBe(true)
    await fireEvent.input(input, { target: { value: ' 12 ' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Link PR' }))
    expect(run).toHaveBeenCalledWith('link_pr', { completion: { link: '12' } })
    await fireEvent.input(input, { target: { value: 'https://forge.example/owner/repo/pull/13' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Link PR' }))
    expect(run).toHaveBeenLastCalledWith('link_pr', { completion: { link: 'https://forge.example/owner/repo/pull/13' } })
    // Evidence is re-read on request; the panel never claims it is current.
    await fireEvent.click(screen.getByRole('button', { name: 'Refresh evidence' }))
    expect(run).toHaveBeenCalledWith('refresh_pr_evidence')
  })

  it('asks for an assessment and shows a stale one as stale', async () => {
    const run = vi.fn()
    render(CompletionPanel, {
      item: item({ completion: completion({ assessment: assessment({ fresh: false }) }) }),
      busy: false,
      run,
    })
    // An assessment whose inputs moved keeps its summary and loses its freshness.
    expect(screen.getByTestId('completion-assessment').textContent).toContain('stale')
    expect(screen.getByTestId('completion-assessment').textContent).toContain('The work matches its criteria.')
    expect(screen.getByTestId('completion-decision').textContent).toContain('has since moved')
    await fireEvent.click(screen.getByRole('button', { name: 'Assess completion' }))
    expect(run).toHaveBeenCalledWith('assess_completion')
  })

  it('orders the selection and confirms the exact revisions and strategy', async () => {
    const run = vi.fn()
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12), link(13)] }) }),
      busy: false,
      run,
    })
    await selectable(12)
    await selectable(13)
    // Two checkboxes, plus the two ordering controls, are all keyboard reachable.
    await fireEvent.click(screen.getByRole('button', { name: 'Move PR #13 up' }))
    const order = screen.getAllByTestId('completion-order-item').map((row) => row.textContent?.replace(/\s+/g, ' ').trim())
    expect(order[0]).toContain('1. PR #13')
    expect(order[1]).toContain('2. PR #12')
    await fireEvent.change(screen.getByLabelText('Strategy'), { target: { value: 'squash' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Merge selected' }))

    // The confirmation names the action, the ordered pull requests, their
    // exact heads and base branches, and the strategy.
    const confirm = dialog()
    expect(within(confirm).getByText(/merge in this order/)).toBeTruthy()
    expect(confirm.textContent).toContain('squash')
    expect(confirmLines()).toEqual([
      '1. PR #13 — head head-13-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa → base main',
      '2. PR #12 — head head-12-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa → base main',
    ])
    // Focus is moved into the confirmation, and Back leaves without sending.
    await waitFor(() => expect(document.activeElement).toBe(confirm))
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(screen.queryByTestId('completion-confirm')).toBeNull()
    expect(run).not.toHaveBeenCalled()

    // Confirming sends the reviewed identities of exactly what was displayed.
    await fireEvent.click(screen.getByRole('button', { name: 'Merge selected' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm merge' }))
    const [kind, input] = sent(run)
    expect(kind).toBe('merge_pr')
    expect(input.completion).toEqual({
      prs: [
        { repoId: 'repo', number: 13, nodeId: 'node-13', headSha: 'head-13-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', baseRef: 'main' },
        { repoId: 'repo', number: 12, nodeId: 'node-12', headSha: 'head-12-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', baseRef: 'main' },
      ],
      strategy: 'squash',
      completeAfter: false,
    })
  })

  it('leaves the confirmation on Escape', async () => {
    const run = vi.fn()
    render(CompletionPanel, { item: item({ completion: completion({ links: [link(12)] }) }), busy: false, run })
    await selectable(12)
    await fireEvent.click(screen.getByRole('button', { name: 'Merge selected and complete' }))
    await fireEvent.keyDown(dialog(), { key: 'Escape' })
    expect(screen.queryByTestId('completion-confirm')).toBeNull()
    expect(run).not.toHaveBeenCalled()
  })

  it('requires a continue or stop choice while the run is active', async () => {
    const run = vi.fn()
    const running = item({
      state: 'implementing',
      workers: [{ agentId: 'a1', sessionId: 's1', role: 'implementer', spawnEffectId: 'e1', parked: false }],
      effects: [],
      completion: completion({ links: [link(12)] }),
    })
    render(CompletionPanel, { item: running, busy: false, run })
    const merge = screen.getByRole('button', { name: 'Merge selected' }) as HTMLButtonElement
    await selectable(12)
    // The bridge refuses a batch that leaves the run undecided, so no merge
    // can be authorized before the person answers.
    expect(screen.getByTestId('completion-run-choice')).toBeTruthy()
    expect(merge.disabled).toBe(true)
    await fireEvent.click(screen.getByLabelText('Request stop'))
    expect(merge.disabled).toBe(false)
    await fireEvent.click(merge)
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm merge' }))
    // The choice travels with the batch it belongs to.
    expect(sent(run)[1].completion).toMatchObject({ runChoice: 'request_stop', completeAfter: false })
  })

  it('needs no run choice when the item owns no run', async () => {
    render(CompletionPanel, {
      item: item({ state: 'stopped', workers: [{ agentId: 'a1', sessionId: 's1', role: 'implementer', spawnEffectId: 'e1', parked: true }], completion: completion({ links: [link(12)] }) }),
      busy: false,
      run: vi.fn(),
    })
    // A parked worker and a terminal state own no run, so nothing is asked.
    expect(screen.queryByTestId('completion-run-choice')).toBeNull()
  })

  it('blocks a merge on stale evidence and offers the refresh instead', async () => {
    const run = vi.fn()
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12, { fresh: false })] }) }),
      busy: false,
      run,
    })
    // The link keeps its snapshot, and says it no longer describes the PR.
    expect(screen.getByTestId('completion-link').textContent).toContain('stale')
    await selectable(12)
    expect(screen.getByTestId('completion-stale-block').textContent).toContain('Refresh evidence before merging')
    for (const name of ['Merge selected', 'Merge selected and complete']) {
      expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled).toBe(true)
    }
    // Completing is still the person's call; only merging needs current evidence.
    expect((screen.getByRole('button', { name: 'Complete work item' }) as HTMLButtonElement).disabled).toBe(false)
    await fireEvent.click(screen.getAllByRole('button', { name: /Refresh evidence/ })[0])
    expect(run).toHaveBeenCalledWith('refresh_pr_evidence')
  })

  it('only offers open pull requests for a merge', () => {
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12), link(13, { pr: pr(13, { state: 'merged' }) })] }) }),
      busy: false,
      run: vi.fn(),
    })
    // A merged or closed pull request stays readable but cannot be reviewed again.
    expect(screen.getByLabelText('Select PR #12 for merging')).toBeTruthy()
    expect(screen.queryByLabelText('Select PR #13 for merging')).toBeNull()
    expect(screen.getAllByTestId('completion-link')).toHaveLength(2)
  })

  it('renders a partial batch, retries only what is left, and completes with open PRs', async () => {
    const run = vi.fn()
    const failed = batch({
      state: 'stopped',
      completeAfter: true,
      steps: [step(12, 'confirmed'), step(13, 'failed', { result: 'the forge refused the merge' }), step(14, 'unattempted')],
    })
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12), link(13), link(14)], batch: failed }) }),
      busy: false,
      run,
    })
    const steps = screen.getAllByTestId('completion-batch-step')
    // Confirmed, failed and unattempted are distinguishable, and a failed
    // entry carries the receipt's own words.
    expect(steps.map((row) => row.getAttribute('data-state'))).toEqual(['confirmed', 'failed', 'unattempted'])
    expect(steps[1].textContent).toContain('the forge refused the merge')

    // Retrying names only what the batch never confirmed, keeping its intent.
    await fireEvent.click(screen.getByRole('button', { name: 'Retry remaining' }))
    expect(confirmLines()).toEqual([
      '1. PR #13 — head head-13-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa → base main',
      '2. PR #14 — head head-14-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa → base main',
    ])
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm merge and complete' }))
    const [kind, input] = sent(run)
    expect(kind).toBe('merge_and_complete')
    expect(input.completion).toMatchObject({ completeAfter: true })
    expect((input.completion as { prs: { number: number }[] }).prs.map((entry) => entry.number)).toEqual([13, 14])

    // Completing with the pull requests still open is offered beside the retry.
    await fireEvent.click(screen.getByRole('button', { name: 'Complete with open PRs' }))
    expect(run).toHaveBeenLastCalledWith('complete')
  })

  it('offers no recovery while the batch is still being driven', () => {
    render(CompletionPanel, {
      item: item({
        completion: completion({
          links: [link(12), link(13)],
          batch: batch({
            state: 'active',
            completeAfter: true,
            steps: [step(12, 'dispatched'), step(13, 'unattempted')],
          }),
        }),
      }),
      busy: false,
      run: vi.fn(),
    })
    // The entries stay readable while the bridge drives them, but a merge
    // command for an active batch is refused as a conflict, so nothing here
    // pretends the remainder can be authorized.
    expect(screen.getAllByTestId('completion-batch-step').map((row) => row.getAttribute('data-state'))).toEqual(['dispatched', 'unattempted'])
    expect(screen.queryByTestId('completion-batch-recovery')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry remaining' })).toBeNull()
  })

  it('never retries an uncertain merge and says why', async () => {
    const run = vi.fn()
    const uncertain = batch({
      state: 'stopped',
      completeAfter: false,
      steps: [step(12, 'confirmed'), step(13, 'uncertain'), step(14, 'unattempted')],
    })
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12), link(13), link(14)], batch: uncertain }) }),
      busy: false,
      run,
    })
    expect(screen.getAllByTestId('completion-batch-step').map((row) => row.getAttribute('data-state'))).toEqual(['confirmed', 'uncertain', 'unattempted'])
    expect(screen.getByTestId('completion-uncertain').textContent).toContain('never sent a second time')
    await fireEvent.click(screen.getByRole('button', { name: 'Retry remaining' }))
    // The uncertain entry's outcome has to be read again, so it is not re-sent.
    expect(confirmLines()).toEqual(['1. PR #14 — head head-14-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa → base main'])
  })

  it('keeps the stop outcome apart from the completion status', () => {
    render(CompletionPanel, {
      item: item({ completion: completion({ status: 'open', stopState: 'failed' }) }),
      busy: false,
      run: vi.fn(),
    })
    // A failed stop says something about the run, never that the work is done.
    expect(screen.getByTestId('completion-stop-state').textContent).toContain('failed')
    expect(screen.getByTestId('completion-status').textContent).toContain('Open')
  })

  it('disables every action while a command is in flight', () => {
    render(CompletionPanel, {
      item: item({ completion: completion({ links: [link(12)], assessment: assessment() }) }),
      busy: true,
      run: vi.fn(),
    })
    const names = ['Assess completion', 'Link PR', 'Refresh evidence', 'Complete work item']
    for (const name of names) expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled).toBe(true)
  })
})
