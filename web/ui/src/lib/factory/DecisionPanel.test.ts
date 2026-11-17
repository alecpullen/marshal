import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import DecisionPanel from './DecisionPanel.svelte'
import type { FactoryDecision, FactoryItem } from '../factory'

afterEach(cleanup)

/** A work item as the bridge reports one, carrying the decision under test. */
const item = (over: Partial<FactoryItem> = {}): FactoryItem =>
  ({
    id: 'x',
    title: 'Task',
    state: 'needs_input',
    version: 1,
    draft: { requestId: 'r', title: 'Task', plan: 'Draft plan', spec: 'Draft spec', config: { repoId: 'repo' } },
    rounds: [],
    grants: [],
    workers: [],
    ...over,
  }) as unknown as FactoryItem

const decision = (over: Partial<FactoryDecision> = {}): FactoryDecision =>
  ({ id: 'q1', kind: 'question', question: 'Which route?', reason: '', effectId: '', createdAt: '', ...over }) as FactoryDecision

describe('DecisionPanel', () => {
  it('answersCurrentDecision', async () => {
    const run = vi.fn()
    render(DecisionPanel, { item: item({ decision: decision() }), busy: false, run })
    await screen.findByText('Which route?')
    // Nothing is sent until an answer exists, and then it is the one command
    // the bridge executes for a question.
    expect((screen.getByRole('button', { name: 'Send answer' }) as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.input(screen.getByLabelText('Answer'), { target: { value: 'Use A' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
    expect(run).toHaveBeenCalledWith('answer', { answer: 'Use A' })
  })

  it('decisionInputSurvivesItemRefresh', async () => {
    const run = vi.fn()
    const first = item({ decision: decision() })
    const view = render(DecisionPanel, { item: first, busy: false, run })
    await screen.findByText('Which route?')
    await fireEvent.input(screen.getByLabelText('Answer'), { target: { value: 'Use A' } })
    // The same decision redrawn from a newer store record: the panel is not
    // keyed to the version, so the typed answer is still there.
    await view.rerender({ item: item({ version: 2, stage: 'refreshed', decision: decision() }) })
    expect((screen.getByLabelText('Answer') as HTMLTextAreaElement).value).toBe('Use A')
    expect(run).not.toHaveBeenCalled()
  })

  it('unknownDecisionHasNoUnsupportedAction', async () => {
    const run = vi.fn()
    render(DecisionPanel, {
      item: item({ decision: decision({ kind: 'mystery_kind', question: 'Something only the bridge knows', reason: 'No client action exists for this' }) }),
      busy: false,
      run,
    })
    // The question and the reason are stated, and nothing else is offered:
    // this client cannot execute an action it does not implement.
    await screen.findByText('Something only the bridge knows')
    expect(screen.getByText('No client action exists for this')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByText(/continue/i)).toBeNull()
    expect(run).not.toHaveBeenCalled()
  })
})
