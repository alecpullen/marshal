import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import FactoryRevision from './FactoryRevision.svelte'
import type {
  FactoryApproval,
  FactoryConfig,
  FactoryItem,
  FactoryRevisionCommand,
  FactoryRevisionCommandKind,
  FactoryRevisionProposal,
} from './factory'

afterEach(cleanup)

/** The config a follow-up create request carries over; its contents are not this component's subject. */
const config = { repoId: 'repo', projectRoot: '/p', native: true, implementerAuto: false, noRequiredCiAcknowledged: false, targetBranch: 'main' } as unknown as FactoryConfig

const approval = (revision: number, plan: string, spec = ''): FactoryApproval => ({
  revision, plan, spec, planHash: `hash-${revision}`, config, approvedAt: `2026-01-0${revision}T00:00:00Z`,
})

const proposal = (over: Partial<FactoryRevisionProposal> = {}): FactoryRevisionProposal => ({
  id: 'p1',
  version: 1,
  baseRevision: 2,
  baseHash: 'hash-2',
  plan: 'New plan',
  spec: 'New spec',
  candidateHash: 'cand-1',
  createdBy: 'planner',
  autoAssess: true,
  status: 'proposed',
  assessmentState: 'not_requested',
  createdAt: '2026-01-03T00:00:00Z',
  updatedAt: '2026-01-03T00:00:00Z',
  ...over,
})

const baseItem = {
  id: 'x',
  title: 'Task',
  state: 'implementing',
  stage: '',
  version: 3,
  projectRoot: '/p',
  draft: { plan: 'Draft plan', spec: 'Draft spec', config },
  approvals: [approval(1, 'First plan'), approval(2, 'Approved plan', 'Approved spec')],
  rounds: [],
  grants: [],
  prs: [],
  workers: [],
  history: [],
} as unknown as FactoryItem

/** The bridge seam: `true` is accepted, `false` is refused and keeps the person's text. */
const run = vi.fn<(kind: FactoryRevisionCommandKind, revision: FactoryRevisionCommand) => Promise<boolean>>()

function mount(over: { item?: FactoryItem; autoAssess?: boolean; busy?: boolean } = {}) {
  return render(FactoryRevision, { item: baseItem, run, autoAssess: true, ...over })
}

beforeEach(() => {
  run.mockReset()
  run.mockResolvedValue(true)
})

describe('FactoryRevision', () => {
  it('oldAndNewEvidenceAreDistinguished', () => {
    mount({ item: { ...baseItem, proposals: [proposal({ plan: 'Changed plan', spec: 'Changed spec' })] } as FactoryItem })

    // The approved revision is shown as its own record...
    expect(screen.getByTestId('revision-current-plan').textContent).toContain('Approved plan')
    expect(screen.getByTestId('revision-current-spec').textContent).toContain('Approved spec')
    // ...and says what it is evidence for, which is that revision only.
    expect(screen.getByTestId('revision-current-evidence').textContent).toContain('covers revision 2 only')

    // The candidate is editable and explicitly outside that evidence.
    expect((screen.getByLabelText('Proposed plan') as HTMLTextAreaElement).value).toBe('Changed plan')
    expect((screen.getByLabelText('Proposed specification') as HTMLTextAreaElement).value).toBe('Changed spec')
    expect(screen.getByTestId('revision-coverage').textContent).toContain("not covered by revision 2's evidence")

    // The approved plan is never presented as the proposal.
    const proposed = screen.getByTestId('revision-proposed')
    expect(proposed.textContent).not.toContain('Approved plan')
    expect(screen.getByTestId('revision-current').textContent).not.toContain('Changed plan')
  })

  it('plannerUnavailableShowsPendingAndChoices', async () => {
    mount({
      item: {
        ...baseItem,
        proposals: [proposal({ status: 'awaiting_authorization', assessmentState: 'unavailable' })],
      } as FactoryItem,
    })

    // No assessor answered, and the screen says so rather than showing a blank.
    expect(screen.getByText('Planner unavailable')).toBeTruthy()
    expect(screen.getByTestId('revision-assessment-none').textContent).toContain('could not answer')
    // Approval is still offered: a planner is advice, not a gate.
    expect((screen.getByRole('button', { name: 'Approve this revision' }) as HTMLButtonElement).disabled).toBe(false)

    const choices = screen.getByTestId('revision-delivery-choices')
    for (const label of ['Wait for a safe boundary', 'Interrupt the running step', 'Hold until the work finishes']) {
      const input = within(choices).getByLabelText(label) as HTMLInputElement
      expect(input.type).toBe('radio')
      // Every choice is reachable and reachable by keyboard: a real radio in a
      // labelled group, not a div pretending to be one.
      expect(input.disabled).toBe(false)
    }
    await fireEvent.click(within(choices).getByLabelText('Interrupt the running step'))
    await fireEvent.click(within(choices).getByRole('button', { name: 'Record delivery choice' }))

    expect(run).toHaveBeenCalledTimes(1)
    expect(run).toHaveBeenCalledWith(
      'revision_delivery_choice',
      expect.objectContaining({ proposalId: 'p1', proposalVersion: 1, strategy: 'interrupt', candidateHash: 'cand-1' }),
    )
    // Choosing how to deliver it is not a delivery, and not an approval.
    expect(run.mock.calls.some(([kind]) => kind === 'revision_authorize')).toBe(false)
  })

  it('receiptSentIsNotDelivered', () => {
    const receiptOf = (state: string) => ({
      strategy: 'wait_safe',
      state,
      sessionId: 'sess-secret',
      turnId: 'turn-secret',
      requestId: 'request-secret',
      candidateHash: 'cand-1',
    })
    mount({
      item: {
        ...baseItem,
        proposals: [proposal({ status: 'authorized', delivery: receiptOf('accepted') as never })],
      } as FactoryItem,
    })

    // A receipt about the request is exactly that: the headline and the body
    // both say the agent has not been given anything on this evidence.
    const receipt = screen.getByTestId('revision-receipt')
    expect(receipt.textContent).toContain('Accepted — not delivered')
    expect(receipt.textContent).toContain('receipt, not a delivery')
    expect(receipt.textContent).not.toContain('The agent has this revision')
    // Nothing internal about the delivery is shown: no session, turn or request id.
    expect(receipt.textContent).not.toContain('sess-secret')
    expect(receipt.textContent).not.toContain('turn-secret')
    expect(receipt.textContent).not.toContain('request-secret')

    cleanup()
    mount({
      item: {
        ...baseItem,
        proposals: [proposal({ status: 'activated', delivery: receiptOf('delivered') as never })],
      } as FactoryItem,
    })
    // Only `delivered` reads as delivered.
    expect(screen.getByTestId('revision-receipt').textContent).toContain('The agent has this revision')
  })

  it('staleProposalRetainsText', async () => {
    run.mockResolvedValue(false)
    mount({ item: { ...baseItem, proposals: [proposal()] } as FactoryItem })

    await fireEvent.input(screen.getByLabelText('Proposed plan'), { target: { value: 'Amend the migration step' } })
    await fireEvent.input(screen.getByLabelText('Proposed specification'), { target: { value: 'Migrate after the deploy' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save proposal' }))

    expect(run).toHaveBeenCalledWith(
      'revision_propose',
      expect.objectContaining({ plan: 'Amend the migration step', spec: 'Migrate after the deploy', baseRevision: 2, baseHash: 'hash-2' }),
    )
    // The refusal is reported, and costs nobody their text.
    await screen.findByTestId('revision-failed')
    expect((screen.getByLabelText('Proposed plan') as HTMLTextAreaElement).value).toBe('Amend the migration step')
    expect((screen.getByLabelText('Proposed specification') as HTMLTextAreaElement).value).toBe('Migrate after the deploy')
    // Nothing was retried or auto-approved on the way out.
    expect(run).toHaveBeenCalledTimes(1)
  })

  it('manualFollowUpConfirmation', async () => {
    mount({ item: { ...baseItem, proposals: [proposal({ plan: 'Separate work', spec: 'Its own spec' })] } as FactoryItem })

    const confirm = screen.getByLabelText('I confirm this starts a separate work item instead of changing this one') as HTMLInputElement
    const button = screen.getByRole('button', { name: 'Start a follow-up item' }) as HTMLButtonElement
    // A follow-up is irreversible from this screen, so it is never one click.
    expect(confirm.checked).toBe(false)
    expect(button.disabled).toBe(true)
    expect(run).not.toHaveBeenCalled()

    await fireEvent.click(confirm)
    expect(button.disabled).toBe(false)
    await fireEvent.click(button)

    expect(run).toHaveBeenCalledTimes(1)
    const [kind, revision] = run.mock.calls[0]
    expect(kind).toBe('revision_follow_up_confirm')
    expect(revision.routing).toBe('follow_up')
    expect(revision.proposalId).toBe('p1')
    expect(revision.followUp?.plan).toBe('Separate work')
    expect(revision.followUp?.spec).toBe('Its own spec')
    expect(revision.followUp?.requestId).toBeTruthy()
    // A follow-up routes the change elsewhere; it does not amend this item.
    expect(run.mock.calls.some(([k]) => k === 'revision_authorize')).toBe(false)
    expect(run.mock.calls.some(([k]) => k === 'revision_delivery_choice')).toBe(false)
  })

  it('shows the assessment, its reasons and its routing recommendation', () => {
    mount({
      item: {
        ...baseItem,
        proposals: [
          proposal({
            status: 'awaiting_authorization',
            assessmentState: 'assessed',
            pauseState: 'requested',
            assessment: {
              classification: 'scope_change',
              reason: 'The migration needs a new column',
              recommendation: 'amend',
              strategy: 'wait_safe',
              assessorId: 'assessor-1',
              candidateHash: 'cand-1',
              baseRevision: 2,
            },
          }),
        ],
      } as FactoryItem,
    })

    expect(screen.getByTestId('revision-classification').textContent).toBe('Scope change')
    expect(screen.getByTestId('revision-reason').textContent).toBe('The migration needs a new column')
    expect(screen.getByTestId('revision-recommendation').textContent).toBe('Amend the running work')
    expect(screen.getByTestId('revision-strategy').textContent).toBe('Wait for a safe boundary')
    expect(screen.getByTestId('revision-assessor').textContent).toBe('assessor-1')
    // Pause evidence is named as evidence, not as a confirmation.
    expect(screen.getByTestId('revision-pause').textContent).toContain('not confirmed stopped')
  })

  it('shows the effective automatic assessment and asks explicitly when it is off', async () => {
    mount({ autoAssess: false, item: { ...baseItem, proposals: [proposal()] } as FactoryItem })
    expect(screen.getByTestId('revision-auto-assess').textContent).toContain('Automatic assessment: off')
    // Off means nothing is assessed behind the person's back, so the way to get
    // an assessment is an explicit request carrying its own attempt id.
    await fireEvent.click(screen.getByRole('button', { name: 'Request assessment' }))
    const [kind, revision] = run.mock.calls[0]
    expect(kind).toBe('revision_assess_request')
    expect(revision.proposalId).toBe('p1')
    expect(revision.attemptId).toBeTruthy()

    cleanup()
    mount({ autoAssess: true, item: { ...baseItem, proposals: [proposal()] } as FactoryItem })
    expect(screen.getByTestId('revision-auto-assess').textContent).toContain('Automatic assessment: on')
  })

  it('stacks the comparison on a phone and offers no dead controls without a proposal', () => {
    mount({ item: { ...baseItem, approvals: [], proposals: [] } as FactoryItem })
    // The screen opens without a planner, a proposal or an approval: it is an
    // editor, not a decision gate.
    expect(screen.getByTestId('factory-revision')).toBeTruthy()
    expect((screen.getByLabelText('Proposed plan') as HTMLTextAreaElement).value).toBe('Draft plan')
    expect(screen.getByTestId('revision-current').textContent).toContain('No approved revision yet')
    // Nothing is approvable or deliverable because nothing is proposed.
    expect(screen.queryByRole('button', { name: 'Approve this revision' })).toBeNull()
    expect(screen.queryByTestId('revision-delivery-choices')).toBeNull()
    // The two halves are one grid that stacks below the desktop breakpoint.
    const compare = screen.getByTestId('revision-current').parentElement as HTMLElement
    expect(compare.className).toContain('grid')
    expect(compare.className).toContain('lg:grid-cols-2')
    expect(screen.getByLabelText('Proposed plan').closest('section')?.getAttribute('aria-label')).toBe('Proposed revision')
  })
})
