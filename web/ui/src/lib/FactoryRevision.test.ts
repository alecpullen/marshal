import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
// @ts-expect-error -- vitest runs on node, and this project installs no node type package.
import { webcrypto } from 'node:crypto'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import FactoryRevision from './FactoryRevision.svelte'
import { factoryPlanHash } from './factory'
import type {
  FactoryApproval,
  FactoryConfig,
  FactoryItem,
  FactoryRevisionCommand,
  FactoryRevisionCommandKind,
  FactoryRevisionProposal,
} from './factory'

afterEach(cleanup)

/*
  jsdom's crypto has randomUUID but no SubtleCrypto, and the component hashes
  the candidate with the platform's SHA-256 exactly as a browser does. Install
  node's webcrypto once per file; the pinned digests below are the check.
*/
beforeEach(() => {
  if (!globalThis.crypto.subtle) {
    Object.defineProperty(globalThis.crypto, 'subtle', { value: webcrypto.subtle, configurable: true })
  }
})

/** The config a follow-up create request carries over; its contents are not this component's subject. */
const config = { repoId: 'repo', projectRoot: '/p', native: true, implementerAuto: false, noRequiredCiAcknowledged: false, targetBranch: 'main' } as unknown as FactoryConfig

const approval = (revision: number, plan: string, spec = '', planHash = `hash-${revision}`): FactoryApproval => ({
  revision, plan, spec, planHash, config, approvedAt: `2026-01-0${revision}T00:00:00Z`,
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

/** An assessment of exactly this candidate at exactly this base, so it may advise. */
const assessmentOf = (over: Partial<NonNullable<FactoryRevisionProposal['assessment']>> = {}) => ({
  classification: 'scope_change',
  reason: 'The migration needs a new column',
  recommendation: 'amend' as const,
  strategy: 'wait_safe',
  assessorId: 'assessor-1',
  candidateHash: 'cand-1',
  baseRevision: 2,
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

/** The item at `version`, as the bridge would report it after a command. */
const itemWith = (over: Partial<FactoryItem>): FactoryItem => ({ ...baseItem, ...over }) as FactoryItem

/*
  The bridge seam: it answers with the item it produced, or `null` when it
  refused. Every assertion here is about the command object handed to it, which
  is the thing the bridge validates.
*/
type RevisionCall = [FactoryRevisionCommandKind, FactoryRevisionCommand]
const run = vi.fn<(kind: FactoryRevisionCommandKind, revision: FactoryRevisionCommand) => Promise<FactoryItem | null>>()

function mount(over: { item?: FactoryItem; autoAssess?: boolean; autoAssessKnown?: boolean; busy?: boolean } = {}) {
  return render(FactoryRevision, { item: baseItem, run, autoAssess: true, ...over })
}

/** The recorded calls, as [kind, revision] pairs. */
const calls = (): RevisionCall[] => run.mock.calls as RevisionCall[]

beforeEach(() => {
  run.mockReset()
  run.mockResolvedValue(baseItem)
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
    // Approval is still offered: a planner is advice, not a gate. It waits for
    // the person's strategy, because an unassessed candidate has no advice to
    // take one from and the bridge refuses an authorization naming none.
    const approve = screen.getByRole('button', { name: 'Approve this revision' }) as HTMLButtonElement
    expect(approve.disabled).toBe(true)
    expect(screen.getByTestId('revision-strategy-needed')).toBeTruthy()

    const choices = screen.getByTestId('revision-delivery-choices')
    for (const label of ['Wait for a safe boundary', 'Interrupt the running step', 'Hold until the work finishes']) {
      const input = within(choices).getByLabelText(label) as HTMLInputElement
      expect(input.type).toBe('radio')
      // Every choice is reachable and reachable by keyboard: a real radio in a
      // labelled group, not a div pretending to be one.
      expect(input.disabled).toBe(false)
      expect(input.checked).toBe(false)
    }
    await fireEvent.click(within(choices).getByLabelText('Interrupt the running step'))
    await fireEvent.click(within(choices).getByRole('button', { name: 'Record delivery choice' }))

    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    const [kind, revision] = calls()[0]
    expect(kind).toBe('revision_delivery_choice')
    expect(revision).toEqual({ proposalId: 'p1', proposalVersion: 1, candidateHash: 'cand-1', strategy: 'interrupt' })
    // Choosing how to deliver it is not a delivery, and not an approval.
    expect(calls().some(([k]) => k === 'revision_authorize')).toBe(false)
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
    // Only `delivered` reads as delivered, and even then it is the runtime
    // accepting the input, never proof the agent acted on it.
    expect(screen.getByTestId('revision-receipt').textContent).toContain('The agent has this revision')
    expect(screen.getByTestId('revision-receipt-meaning').textContent).toContain('not proof the agent followed it')
  })

  it('proposeBindsTheExactBytesItSends', async () => {
    mount({ item: itemWith({ proposals: [] }) })

    await fireEvent.input(screen.getByLabelText('Proposed plan'), { target: { value: 'Amend the migration step' } })
    await fireEvent.input(screen.getByLabelText('Proposed specification'), { target: { value: 'spéc v2' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save proposal' }))

    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    const [kind, revision] = calls()[0]
    expect(kind).toBe('revision_propose')
    // The hash is the bridge's own digest of the bytes in the same payload: a
    // proposal whose candidateHash does not bind plan/spec is refused outright.
    expect(revision).toEqual({
      plan: 'Amend the migration step',
      spec: 'spéc v2',
      candidateHash: await factoryPlanHash('Amend the migration step', 'spéc v2'),
      baseRevision: 2,
      baseHash: 'hash-2',
    })
    // A literal digest, not just "the same helper": the algorithm is pinned in
    // factory.test.ts against the same values as the bridge's Go test.
    expect(revision.candidateHash).toBe('e11b59f55a68c5819da9812b7b728f988daf7dc2e776f4ff710b1393f7b1e725')
  })

  it('supersedesTheLiveProposalBeforeProposing', async () => {
    const live = proposal({ id: 'p1', version: 4, plan: 'Old candidate', candidateHash: 'cand-old' })
    // The supersede's answer is the item a new proposal must be written
    // against: a poll that moved the approved revision is only visible there.
    const superseded = itemWith({
      version: 4,
      approvals: [approval(1, 'First plan'), approval(2, 'Approved plan', 'Approved spec'), approval(3, 'Activated plan', 'Activated spec')],
      proposals: [proposal({ id: 'p1', version: 5, status: 'superseded', candidateHash: 'cand-old' })],
    })
    run.mockResolvedValueOnce(superseded).mockResolvedValueOnce(superseded)

    mount({ item: itemWith({ proposals: [live] }) })
    await fireEvent.input(screen.getByLabelText('Proposed plan'), { target: { value: 'Replacement plan' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save proposal' }))

    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(2))
    const [[firstKind, first], [secondKind, second]] = calls()
    // Supersede first, naming the proposal and the version reviewed, or the
    // bridge answers "supersede it explicitly" and nothing is saved.
    expect(firstKind).toBe('revision_supersede')
    expect(first).toEqual({ proposalId: 'p1', proposalVersion: 4, candidateHash: 'cand-old' })
    // Then the proposal, written against the item the first command returned.
    expect(secondKind).toBe('revision_propose')
    expect(second).toEqual({
      plan: 'Replacement plan',
      spec: 'New spec',
      candidateHash: await factoryPlanHash('Replacement plan', 'New spec'),
      baseRevision: 3,
      baseHash: 'hash-3',
    })
  })

  it('aRefusedSupersedeStopsTheSave', async () => {
    run.mockResolvedValueOnce(null)
    mount({ item: itemWith({ proposals: [proposal({ id: 'p1', version: 4 })] }) })

    await fireEvent.input(screen.getByLabelText('Proposed plan'), { target: { value: 'Replacement plan' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save proposal' }))

    await screen.findByTestId('revision-failed')
    // Proposing over a proposal the bridge still holds would be refused too, so
    // nothing is sent after a supersede it refused.
    expect(calls().map(([kind]) => kind)).toEqual(['revision_supersede'])
    expect((screen.getByLabelText('Proposed plan') as HTMLTextAreaElement).value).toBe('Replacement plan')
  })

  it('authorizeNamesTheCandidatesBaseRoutingAndStrategy', async () => {
    mount({
      item: itemWith({
        proposals: [
          proposal({
            id: 'p7',
            version: 3,
            baseRevision: 2,
            baseHash: 'hash-2',
            candidateHash: 'cand-7',
            status: 'awaiting_authorization',
            assessmentState: 'assessed',
            assessment: assessmentOf({ candidateHash: 'cand-7', baseRevision: 2, strategy: 'interrupt' }),
          }),
        ],
      }),
    })

    // The recorded advice seeds the strategy, so approving is one click.
    expect((screen.getByLabelText('Interrupt the running step') as HTMLInputElement).checked).toBe(true)
    await fireEvent.click(screen.getByRole('button', { name: 'Approve this revision' }))

    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    const [kind, revision] = calls()[0]
    expect(kind).toBe('revision_authorize')
    // Every field the bridge compares against the journal, from the proposal:
    // a missing or stale one is a 409, not an approval.
    expect(revision).toEqual({
      proposalId: 'p7',
      proposalVersion: 3,
      candidateHash: 'cand-7',
      baseRevision: 2,
      baseHash: 'hash-2',
      routing: 'amend',
      strategy: 'interrupt',
    })
  })

  it('aStaleAssessmentDoesNotChooseTheStrategy', async () => {
    mount({
      item: itemWith({
        proposals: [
          proposal({
            status: 'awaiting_authorization',
            assessmentState: 'assessed',
            // Advice for different bytes: it may be shown as history, but the
            // bridge will not take a strategy from it.
            assessment: assessmentOf({ candidateHash: 'cand-old', strategy: 'interrupt' }),
          }),
        ],
      }),
    })

    expect(screen.getByTestId('revision-assessment-stale')).toBeTruthy()
    expect((screen.getByLabelText('Interrupt the running step') as HTMLInputElement).checked).toBe(false)
    expect((screen.getByRole('button', { name: 'Approve this revision' }) as HTMLButtonElement).disabled).toBe(true)

    // The person chooses, and that choice is what the authorization carries.
    await fireEvent.click(screen.getByLabelText('Hold until the work finishes'))
    await fireEvent.click(screen.getByRole('button', { name: 'Approve this revision' }))

    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    expect(calls()[0][1].strategy).toBe('hold')
  })

  it('aFirstAssessmentRequestCarriesNoAttemptOfItsOwn', async () => {
    mount({ item: itemWith({ proposals: [proposal()] }) })

    // The bridge derives the intent from the proposal; an id of the client's
    // own names an attempt that does not exist and is refused.
    await fireEvent.click(screen.getByRole('button', { name: 'Request assessment' }))
    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    const [kind, revision] = calls()[0]
    expect(kind).toBe('revision_assess_request')
    expect(revision).toEqual({ proposalId: 'p1', proposalVersion: 1, candidateHash: 'cand-1' })
    expect('attemptId' in revision).toBe(false)
  })

  it('anUncertainAssessmentIsRetriedByNamingTheRecordedAttempt', async () => {
    mount({
      item: itemWith({
        proposals: [proposal({ assessmentState: 'uncertain', assessmentAttemptId: 'p1:assessment:1' })],
      }),
    })

    await fireEvent.click(screen.getByRole('button', { name: 'Retry the assessment' }))
    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    const [kind, revision] = calls()[0]
    // A lost attempt is retired by the retry kind, and only the attempt the
    // bridge holds is accepted as the thing being retired.
    expect(kind).toBe('revision_assess_retry')
    expect(revision).toEqual({
      proposalId: 'p1',
      proposalVersion: 1,
      candidateHash: 'cand-1',
      attemptId: 'p1:assessment:1',
    })
  })

  it('offersNoAssessmentControlTheBridgeWouldRefuse', () => {
    // "pending" is an intent already recorded; "assessed" has answered; both
    // are refused by request and by retry, so neither gets a button.
    for (const state of ['pending', 'assessed', 'failed']) {
      cleanup()
      mount({ item: itemWith({ proposals: [proposal({ assessmentState: state })] }) })
      expect(screen.queryByRole('button', { name: 'Request assessment' })).toBeNull()
      expect(screen.queryByRole('button', { name: 'Retry the assessment' })).toBeNull()
    }
    // A failed assessment is said to need a fresh proposal, which is the only
    // path the bridge has for one.
    expect(screen.getByTestId('revision-assessment-failed')).toBeTruthy()
  })

  it('manualFollowUpConfirmation', async () => {
    mount({ item: itemWith({ proposals: [proposal({ plan: 'Separate work', spec: 'Its own spec' })] }) })

    const confirm = screen.getByLabelText('I confirm this starts a separate work item instead of changing this one') as HTMLInputElement
    const button = screen.getByRole('button', { name: 'Start a follow-up item' }) as HTMLButtonElement
    // A follow-up is irreversible from this screen, so it is never one click.
    expect(confirm.checked).toBe(false)
    expect(button.disabled).toBe(true)
    expect(run).not.toHaveBeenCalled()
  })

  it('followUpAuthorizesThenConfirms', async () => {
    const routed = itemWith({
      proposals: [
        proposal({
          version: 2,
          plan: 'Separate work',
          spec: 'Its own spec',
          status: 'follow_up_pending',
          routing: 'follow_up',
          assessmentState: 'assessed',
          assessment: assessmentOf(),
        }),
      ],
    })
    run.mockResolvedValueOnce(routed).mockResolvedValueOnce(routed)

    mount({
      item: itemWith({
        proposals: [
          proposal({
            plan: 'Separate work',
            spec: 'Its own spec',
            status: 'awaiting_authorization',
            assessmentState: 'assessed',
            assessment: assessmentOf(),
          }),
        ],
      }),
    })

    await fireEvent.click(screen.getByLabelText('I confirm this starts a separate work item instead of changing this one'))
    await fireEvent.click(screen.getByRole('button', { name: 'Start a follow-up item' }))

    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(2))
    const [[firstKind, first], [secondKind, second]] = calls()
    // The authorization is what records the follow-up intent on the proposal;
    // without it the confirmation has nothing to confirm.
    expect(firstKind).toBe('revision_authorize')
    expect(first).toEqual({
      proposalId: 'p1',
      proposalVersion: 1,
      candidateHash: 'cand-1',
      baseRevision: 2,
      baseHash: 'hash-2',
      routing: 'follow_up',
      strategy: 'wait_safe',
    })
    // Then the confirmation, against the version that authorization returned.
    expect(secondKind).toBe('revision_follow_up_confirm')
    expect(second).toEqual({
      proposalId: 'p1',
      proposalVersion: 2,
      candidateHash: 'cand-1',
      followUp: {
        requestId: expect.any(String),
        title: 'Task (follow-up)',
        plan: 'Separate work',
        spec: 'Its own spec',
        config,
      },
    })
    // The confirmation routes nothing itself: the server ignores a routing on
    // it, and sending one would only describe the first command's effect.
    expect(second.routing).toBeUndefined()
    // A follow-up routes the change elsewhere; it does not amend this item.
    expect(calls().some(([k, r]) => k === 'revision_authorize' && r.routing !== 'follow_up')).toBe(false)
  })

  it('aRefusedAuthorizationStopsTheConfirmation', async () => {
    run.mockResolvedValueOnce(null)
    mount({
      item: itemWith({
        proposals: [
          proposal({
            status: 'awaiting_authorization',
            assessmentState: 'assessed',
            assessment: assessmentOf(),
          }),
        ],
      }),
    })

    await fireEvent.click(screen.getByLabelText('I confirm this starts a separate work item instead of changing this one'))
    await fireEvent.click(screen.getByRole('button', { name: 'Start a follow-up item' }))

    await screen.findByTestId('revision-failed')
    // Nothing is confirmed without the intent the refused authorization would
    // have recorded, so no second command is sent.
    expect(calls().map(([kind]) => kind)).toEqual(['revision_authorize'])
  })

  it('aLiveProposalCanBeRejectedOrSuperseded', async () => {
    mount({ item: itemWith({ proposals: [proposal()] }) })
    await fireEvent.click(screen.getByRole('button', { name: 'Reject this proposal' }))
    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toEqual(['revision_reject', { proposalId: 'p1', proposalVersion: 1, candidateHash: 'cand-1' }])

    cleanup()
    run.mockReset()
    run.mockResolvedValue(baseItem)
    mount({ item: itemWith({ proposals: [proposal()] }) })
    await fireEvent.click(screen.getByRole('button', { name: 'Supersede it with a new one' }))
    await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toEqual(['revision_supersede', { proposalId: 'p1', proposalVersion: 1, candidateHash: 'cand-1' }])
  })

  it('aPollDoesNotRevertAnUnsentStrategyChoice', async () => {
    const first = proposal({ id: 'p1', status: 'awaiting_authorization', assessmentState: 'assessed', assessment: assessmentOf() })
    const view = mount({ item: itemWith({ proposals: [first] }) })

    await fireEvent.click(screen.getByLabelText('Interrupt the running step'))
    expect((screen.getByLabelText('Interrupt the running step') as HTMLInputElement).checked).toBe(true)

    // A poll brings a newer assessment recommending something else. The choice
    // someone made and has not sent is theirs: it is seeded once per proposal.
    await view.rerender({
      item: itemWith({
        version: 4,
        proposals: [{ ...first, version: 2, assessment: assessmentOf({ strategy: 'hold', reason: 'A newer reading' }) }],
      }),
    })
    expect(screen.getByTestId('revision-reason').textContent).toBe('A newer reading')
    expect((screen.getByLabelText('Interrupt the running step') as HTMLInputElement).checked).toBe(true)
    expect((screen.getByLabelText('Wait for a safe boundary') as HTMLInputElement).checked).toBe(false)
  })

  it('aNewProposalDoesNotCarryAConfirmationAcross', async () => {
    const first = proposal({ id: 'p1', assessment: assessmentOf(), assessmentState: 'assessed' })
    const view = mount({ item: itemWith({ proposals: [first] }) })

    const confirm = () => screen.getByLabelText('I confirm this starts a separate work item instead of changing this one') as HTMLInputElement
    await fireEvent.click(confirm())
    expect(confirm().checked).toBe(true)

    await view.rerender({ item: itemWith({ proposals: [{ ...first, id: 'p2', version: 1 }] }) })
    // The confirmation named a different proposal, so it does not stand.
    expect(confirm().checked).toBe(false)
  })

  it('shows the assessment, its reasons and its routing recommendation', () => {
    mount({
      item: itemWith({
        proposals: [
          proposal({
            status: 'awaiting_authorization',
            assessmentState: 'assessed',
            pauseState: 'requested',
            assessment: assessmentOf(),
          }),
        ],
      }),
    })

    expect(screen.getByTestId('revision-classification').textContent).toBe('Scope change')
    expect(screen.getByTestId('revision-reason').textContent).toBe('The migration needs a new column')
    expect(screen.getByTestId('revision-recommendation').textContent).toBe('Amend the running work')
    expect(screen.getByTestId('revision-strategy').textContent).toBe('Wait for a safe boundary')
    expect(screen.getByTestId('revision-assessor').textContent).toBe('assessor-1')
    // Pause evidence is named as evidence, not as a confirmation.
    expect(screen.getByTestId('revision-pause').textContent).toContain('not confirmed stopped')
  })

  it('shows the effective automatic assessment and asks explicitly when it is off', () => {
    mount({ autoAssess: false, item: itemWith({ proposals: [proposal()] }) })
    expect(screen.getByTestId('revision-auto-assess').textContent).toContain('Automatic assessment: off')
    // Off means nothing is assessed behind the person's back, so the way to get
    // an assessment is the explicit request offered above.
    expect(screen.getByRole('button', { name: 'Request assessment' })).toBeTruthy()

    cleanup()
    mount({ autoAssess: true, item: itemWith({ proposals: [proposal()] }) })
    expect(screen.getByTestId('revision-auto-assess').textContent).toContain('Automatic assessment: on')
  })

  it('aFailedSettingsReadIsNotReportedAsOn', async () => {
    mount({ autoAssessKnown: false, item: itemWith({ proposals: [proposal()] }) })
    const note = screen.getByTestId('revision-auto-assess').textContent ?? ''
    // "On" would be a claim about a file nobody managed to read.
    expect(note).toContain('Could not read')
    expect(note).not.toContain('Automatic assessment: on')
    expect(note).not.toContain('Automatic assessment: off')
  })

  it('stacks the comparison on a phone and offers no dead controls without a proposal', () => {
    mount({ item: itemWith({ approvals: [], proposals: [] }) })
    // The screen opens without a planner, a proposal or an approval: it is an
    // editor, not a decision gate.
    expect(screen.getByTestId('factory-revision')).toBeTruthy()
    expect((screen.getByLabelText('Proposed plan') as HTMLTextAreaElement).value).toBe('Draft plan')
    expect(screen.getByTestId('revision-current').textContent).toContain('No approved revision yet')
    // Nothing is approvable or deliverable because nothing is proposed.
    expect(screen.queryByRole('button', { name: 'Approve this revision' })).toBeNull()
    expect(screen.queryByTestId('revision-delivery-choices')).toBeNull()
    expect(screen.queryByTestId('revision-retire')).toBeNull()
    // The two halves are one grid that stacks below the desktop breakpoint.
    const compare = screen.getByTestId('revision-current').parentElement as HTMLElement
    expect(compare.className).toContain('grid')
    expect(compare.className).toContain('lg:grid-cols-2')
    expect(screen.getByLabelText('Proposed plan').closest('section')?.getAttribute('aria-label')).toBe('Proposed revision')
  })
})
