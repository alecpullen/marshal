import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import RecoveryPanel from './RecoveryPanel.svelte'
import * as api from '../api'
import type { FactoryApproval, FactoryConfig, FactoryDecision, FactoryItem, FactoryWorker } from '../factory'

vi.mock('../api', async (actual) => ({ ...await actual<typeof import('../api')>(), getModels: vi.fn() }))

afterEach(cleanup)
beforeEach(() => {
  vi.mocked(api.getModels).mockReset()
  vi.mocked(api.getModels).mockResolvedValue({ profiles: {} } as never)
})

/** The configuration in force, as the bridge resolved one. */
const config = (over: Partial<FactoryConfig> = {}): FactoryConfig => ({
  repoId: 'repo', projectRoot: '/repo', hostId: 'local', targetBranch: 'main', workspace: '', native: true,
  implementerRouting: {}, reviewerRouting: {}, reviewerInstructions: 'Prior instructions',
  implementerAuto: false, expectedChecks: [], noRequiredCiAcknowledged: false, maxRounds: 5, ciPollSeconds: 30, ciWaitSeconds: 600,
  ...over,
}) as FactoryConfig

/** One recorded approval revision. */
const approval = (revision: number, plan: string, over: Partial<FactoryConfig> = {}): FactoryApproval =>
  ({ revision, plan, spec: `${plan} spec`, planHash: `hash-${revision}`, config: config(over), approvedAt: `2026-01-0${revision}T00:00:00Z` }) as FactoryApproval

/** A work item carrying the decision under test. */
const item = (over: Partial<FactoryItem> = {}): FactoryItem =>
  ({
    id: 'x', title: 'Task', state: 'needs_input', version: 1,
    draft: { requestId: 'r', title: 'Task', plan: 'Draft plan', spec: 'Draft spec', config: config() },
    approvals: null, rounds: [], grants: [], workers: [], prs: [], effects: [], history: [],
    ...over,
  }) as unknown as FactoryItem

const decision = (over: Partial<FactoryDecision> = {}): FactoryDecision =>
  ({ id: 'd', kind: 'exhausted', question: 'Allowance spent', reason: '', effectId: '', createdAt: '', ...over }) as FactoryDecision

const worker = (agentId: string): FactoryWorker => ({ agentId, sessionId: 's1', role: 'implementer', spawnEffectId: 'e1', parked: false }) as FactoryWorker

const optionValues = (label: string) =>
  [...(screen.getByLabelText(label) as HTMLSelectElement).options].map((option) => option.value)
const optionLabels = (label: string) =>
  [...(screen.getByLabelText(label) as HTMLSelectElement).options].map((option) => option.textContent)
/** The picker holds the real list once the models read has landed. */
const loaded = (label: string, profile: string) => waitFor(() => expect(optionValues(label)).toContain(profile))

describe('RecoveryPanel', () => {
  it('retryRequiresReasonAndAllowance', async () => {
    const run = vi.fn()
    render(RecoveryPanel, { item: item({ decision: decision() }), busy: false, run })
    await screen.findByText('Allowance spent')
    const button = screen.getByRole('button', { name: 'Grant rounds and retry' }) as HTMLButtonElement
    // No reason and no allowance, no grant.
    expect(button.disabled).toBe(true)
    await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Resume the work' } })
    expect(button.disabled).toBe(false)
    // Reason present but the allowance withdrawn: still nothing to submit.
    await fireEvent.input(screen.getByLabelText('Additional rounds'), { target: { value: '0' } })
    expect(button.disabled).toBe(true)
    await fireEvent.input(screen.getByLabelText('Additional rounds'), { target: { value: '2' } })
    await fireEvent.click(button)
    // Only the reason and the allowance travel; nothing else is fabricated.
    expect(run).toHaveBeenCalledWith('retry', { reason: 'Resume the work', additionalRounds: 2 })
  })

  it('routeOnlyRetryOmitsReplacement', async () => {
    vi.mocked(api.getModels).mockResolvedValue({ profiles: { steady: {}, frontier: {} } } as never)
    const run = vi.fn()
    render(RecoveryPanel, { item: item({ decision: decision() }), busy: false, run })
    await screen.findByText('Allowance spent')
    await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change route' } })
    await loaded('Implementer profile', 'steady')
    await fireEvent.change(screen.getByLabelText('Implementer profile'), { target: { value: 'steady' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Grant rounds and retry' }))
    // A route-only retry names only the route that changed; no replacement
    // approval document is invented for it.
    const [, input] = vi.mocked(run).mock.calls.at(-1) as [string, Record<string, unknown>]
    expect(input).toMatchObject({ reason: 'Change route', additionalRounds: 1, implementerRouting: { profile: 'steady' } })
    expect(input.reviewerRouting).toBeUndefined()
    expect(input.replacement).toBeUndefined()
  })

  it('replacementRetryUsesLatestApproval', async () => {
    const run = vi.fn()
    render(RecoveryPanel, {
      item: item({
        decision: decision(),
        approvals: [
          approval(1, 'First plan', { reviewerInstructions: 'First instructions', implementerRouting: { profile: 'old' } }),
          approval(2, 'Second plan', { reviewerInstructions: 'Second instructions', implementerRouting: { profile: 'old' } }),
        ],
      }),
      busy: false,
      run,
    })
    await screen.findByText('Allowance spent')
    await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change the plan' } })
    await fireEvent.click(screen.getByLabelText('Change approved plan or reviewer instructions'))
    // The replacement starts from the newest approval, never the older draft.
    expect((screen.getByLabelText('Replacement plan') as HTMLTextAreaElement).value).toBe('Second plan')
    expect((screen.getByLabelText('Replacement specification') as HTMLTextAreaElement).value).toBe('Second plan spec')
    expect((screen.getByLabelText('Reviewer instructions') as HTMLTextAreaElement).value).toBe('Second instructions')
    await fireEvent.click(screen.getByRole('button', { name: 'Grant rounds and retry' }))
    const [, input] = vi.mocked(run).mock.calls.at(-1) as [string, Record<string, unknown>]
    const replacement = input.replacement as { plan: string; spec: string; requestId: string; config: FactoryConfig }
    expect(replacement.plan).toBe('Second plan')
    expect(replacement.spec).toBe('Second plan spec')
    expect(replacement.requestId).toBeTruthy()
    expect(replacement.config.reviewerInstructions).toBe('Second instructions')
    expect(replacement.config.implementerRouting).toEqual({ profile: 'old' })
    // A replacement is a new document, so no separate route input accompanies it.
    expect(input.implementerRouting).toBeUndefined()
  })

  it('retryInputSurvivesItemRefresh', async () => {
    vi.mocked(api.getModels).mockResolvedValue({ profiles: { steady: {} } } as never)
    const run = vi.fn()
    const view = render(RecoveryPanel, { item: item({ decision: decision() }), busy: false, run })
    await screen.findByText('Allowance spent')
    await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change route' } })
    await fireEvent.input(screen.getByLabelText('Additional rounds'), { target: { value: '3' } })
    await loaded('Implementer profile', 'steady')
    await fireEvent.change(screen.getByLabelText('Implementer profile'), { target: { value: 'steady' } })
    await fireEvent.click(screen.getByLabelText('Change approved plan or reviewer instructions'))
    await fireEvent.input(screen.getByLabelText('Reviewer instructions'), { target: { value: 'Review the retry carefully' } })
    // Same decision, newer store record: every control keeps what it held.
    await view.rerender({ item: item({ version: 4, stage: 'refreshed', decision: decision() }) })
    expect((screen.getByLabelText('Reason') as HTMLTextAreaElement).value).toBe('Change route')
    expect((screen.getByLabelText('Additional rounds') as HTMLInputElement).value).toBe('3')
    expect((screen.getByLabelText('Implementer profile') as HTMLSelectElement).value).toBe('steady')
    expect((screen.getByLabelText('Reviewer instructions') as HTMLTextAreaElement).value).toBe('Review the retry carefully')
    expect(run).not.toHaveBeenCalled()
  })

  it('loadsRealProfileChoices', async () => {
    vi.mocked(api.getModels).mockResolvedValue({ profiles: { careful: {}, fast: {}, strict: {} } } as never)
    render(RecoveryPanel, { item: item({ decision: decision() }), busy: false, run: vi.fn() })
    await screen.findByText('Allowance spent')
    await loaded('Implementer profile', 'careful')
    // The routes offered are the real profiles the models API reports.
    expect(optionValues('Implementer profile')).toEqual(['', 'careful', 'fast', 'strict'])
    expect(optionLabels('Implementer profile')).toEqual(['Keep the approved route', 'careful', 'fast', 'strict'])
    expect(optionValues('Reviewer profile')).toEqual(['', 'careful', 'fast', 'strict'])
    expect(api.getModels).toHaveBeenCalledTimes(1)
  })

  it('retainsUnavailableCurrentProfile', async () => {
    vi.mocked(api.getModels).mockResolvedValue({ profiles: { careful: {} } } as never)
    render(RecoveryPanel, {
      item: item({ decision: decision(), approvals: [approval(1, 'Plan', { implementerRouting: { profile: 'retired' } })] }),
      busy: false,
      run: vi.fn(),
    })
    await screen.findByText('Allowance spent')
    await loaded('Implementer profile', 'careful')
    // A routed profile the list no longer offers stays visible and selected,
    // so touching the picker cannot silently replace it.
    expect(optionLabels('Implementer profile')).toContain('retired (unavailable)')
    expect((screen.getByLabelText('Implementer profile') as HTMLSelectElement).value).toBe('retired')
  })

  it('refreshesEvidence', async () => {
    const run = vi.fn()
    render(RecoveryPanel, {
      item: item({ state: 'recovering', decision: decision({ kind: 'recovery', question: 'Worker unknown' }), workers: [worker('agent-2')] }),
      busy: false,
      run,
    })
    await screen.findByText('Last worker agent ID: agent-2')
    await fireEvent.click(screen.getByRole('button', { name: 'Refresh evidence' }))
    // Evidence is re-read before anyone attests to a worker's state.
    expect(run).toHaveBeenCalledWith('reconcile')
    expect(run).not.toHaveBeenCalledWith('recover', expect.anything())
  })

  it('requiresExactNamedWorkerAttestation', async () => {
    const run = vi.fn()
    render(RecoveryPanel, {
      item: item({ state: 'recovering', decision: decision({ kind: 'recovery', question: 'Worker unknown' }), workers: [worker('agent-2')] }),
      busy: false,
      run,
    })
    await screen.findByText('Last worker agent ID: agent-2')
    const button = screen.getByRole('button', { name: 'Record named-worker attestation' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    await fireEvent.input(screen.getByLabelText('Type the exact last worker agent ID'), { target: { value: 'agent-1' } })
    await fireEvent.input(screen.getByLabelText('How you confirmed it'), { target: { value: 'Process stopped' } })
    await fireEvent.click(screen.getByLabelText('I confirm this named previous worker has stopped'))
    // A near miss is not a match, and an unconfirmed worker is never attested.
    expect(button.disabled).toBe(true)
    await fireEvent.input(screen.getByLabelText('Type the exact last worker agent ID'), { target: { value: 'agent-2' } })
    expect(button.disabled).toBe(false)
    await fireEvent.click(button)
    expect(run).toHaveBeenCalledWith('recover', { confirmPreviousWorkerStopped: true, reason: 'agent-2: Process stopped' })
    // Attestation needs no profile list, so it never asks for one.
    expect(api.getModels).not.toHaveBeenCalled()
  })

  it('noCrossProviderContinueOption', async () => {
    render(RecoveryPanel, { item: item({ decision: decision() }), busy: false, run: vi.fn() })
    await screen.findByText('Allowance spent')
    // The bridge cannot resume an interrupted conversation across providers, so
    // the panel states the fresh round instead of offering it.
    expect(screen.getByText("A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.")).toBeTruthy()
    expect(screen.queryByText(/continue the conversation/i)).toBeNull()
    expect(screen.queryByRole('button', { name: /continue/i })).toBeNull()
    expect(screen.queryByRole('radio')).toBeNull()
    // The only actions are the ones the API executes.
    expect(screen.getAllByRole('button').map((button) => button.textContent?.trim())).toEqual(['Grant rounds and retry'])
    expect(within(screen.getByTestId('factory-recovery')).queryByText(/conversation/i)).toBeNull()
  })
})
