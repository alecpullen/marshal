import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import FactoryItem from './FactoryItem.svelte'
import * as api from '../lib/api'
import type { FactoryItem as Item } from '../lib/factory'
vi.mock('../lib/api', async (actual) => ({ ...await actual<typeof import('../lib/api')>(), getFactoryItem: vi.fn(), commandFactoryItem: vi.fn() }))
afterEach(cleanup)
const base = { id: 'x', title: 'Task', repoId: 'repo', state: 'draft', stage: '', version: 1, draft: { plan: 'Build', spec: '', config: { targetBranch: 'main', repoId: 'repo', maxRounds: 5, implementerRouting: {}, reviewerRouting: {}, expectedChecks: [] } }, rounds: [], grants: [], prs: [], workers: [], history: [] } as unknown as Item
it('requires explicit approval before a draft starts', async () => {
  vi.mocked(api.getFactoryItem).mockResolvedValue(base)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  expect(await screen.findByText('Review draft before starting')).toBeTruthy()
  expect(api.commandFactoryItem).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Approve & start' })).toBeTruthy()
})
it('shows failed CI before compact passes', async () => {
  const item = { ...base, state: 'needs_input', rounds: [{ ci: { complete: true, checks: [
    { key: { name: 'fail' }, conclusion: 'failure', logTail: 'trace' }, { key: { name: 'pass' }, conclusion: 'success' },
  ] } }] } as Item
  vi.mocked(api.getFactoryItem).mockResolvedValue(item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  await screen.findByText(/fail: failure/)
  expect(screen.getByText('1 passing checks')).toBeTruthy()
})
it('answers the current decision and refreshes on a stale version', async () => {
  const item = { ...base, state: 'needs_input', decision: { id: 'q1', kind: 'question', question: 'Which route?', reason: '' } } as Item
  vi.mocked(api.getFactoryItem).mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockRejectedValueOnce(new api.APIError(409, { error: 'stale version', currentVersion: 2 }))
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  await screen.findByText('Which route?')
  await import('@testing-library/svelte').then(async ({ fireEvent }) => {
    await fireEvent.input(screen.getByLabelText('Answer'), { target: { value: 'Use A' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
  })
  await screen.findByText(/Item changed. Review the refreshed version/)
  expect(vi.mocked(api.commandFactoryItem).mock.calls[0][1]).toMatchObject({ decisionId: 'q1', answer: 'Use A', expectedVersion: 1 })
})
it('requires a named worker and deliberate recovery attestation', async () => {
  vi.mocked(api.getFactoryItem).mockResolvedValue({ ...base, state: 'recovering', decision: { id: 'd', kind: 'recovery', question: 'Worker unknown' } } as Item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  await screen.findByText('Worker unknown')
  expect((screen.getByRole('button', { name: 'Record named-worker attestation' }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByLabelText('I confirm this named previous worker has stopped') as HTMLInputElement).checked).toBe(false)
})
it('labels a stopping worker as unconfirmed', async () => {
  vi.mocked(api.getFactoryItem).mockResolvedValue({ ...base, state: 'stopping' } as Item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  expect(await screen.findByText('Stop requested. Worker shutdown is not yet confirmed.')).toBeTruthy()
})
it('requires an explicit replacement plan and retry grant', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  vi.mocked(api.getFactoryItem).mockResolvedValue({ ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Allowance spent' }, grants: [{ additionalRounds: 5 }], rounds: Array(5).fill({}) } as Item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  await screen.findByText('Allowance spent')
  expect(screen.getByText(/5 rounds used · 5 granted total/)).toBeTruthy()
  await fireEvent.click(screen.getByLabelText('Change approved plan, routes or reviewer instructions'))
  expect(screen.getByLabelText('Replacement plan')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Grant rounds and retry' }) as HTMLButtonElement).disabled).toBe(true)
})
it('sends route changes in the replacement approval config', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'needs_input', decision: { id: 'd', kind: 'exhausted', question: 'Try again' },
    approvals: [{ plan: 'Approved plan', spec: 'Approved spec', config: { ...base.draft.config, reviewerInstructions: 'Prior', implementerRouting: { profile: 'old' }, reviewerRouting: { profile: 'old' } } }] } as unknown as Item
  vi.mocked(api.getFactoryItem).mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  await screen.findByText('Try again')
  await fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'Change route' } })
  await fireEvent.click(screen.getByLabelText('Change approved plan, routes or reviewer instructions'))
  expect((screen.getByLabelText('Replacement plan') as HTMLTextAreaElement).value).toBe('Approved plan')
  await fireEvent.input(screen.getByLabelText('New implementer profile (optional)'), { target: { value: 'new-impl' } })
  await fireEvent.input(screen.getByLabelText('New reviewer profile (optional)'), { target: { value: 'new-review' } })
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
it('requires exact last agent ID before recovery attestation', async () => {
  const { fireEvent } = await import('@testing-library/svelte')
  const item = { ...base, state: 'recovering', decision: { id: 'd', kind: 'recovery', question: 'Worker unknown' }, workers: [{ role: 'implementer', agentId: 'agent-2' }] } as Item
  vi.mocked(api.getFactoryItem).mockResolvedValue(item)
  vi.mocked(api.commandFactoryItem).mockResolvedValue(item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
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
  vi.mocked(api.getFactoryItem).mockResolvedValue(item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  await screen.findByText('high: Unsafe call')
  expect(screen.getByText('web/a.go:42')).toBeTruthy()
  expect(screen.getByText('Handle the error')).toBeTruthy()
  expect(screen.getByText('Reviewer question: Which API?')).toBeTruthy()
  expect(screen.getByText('Reviewer reason: Inputs missing')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'reviewer: agent/1' }).getAttribute('href')).toBe('#chat/agent%2F1')
})
it('distinguishes empty from unavailable CI evidence', async () => {
  vi.mocked(api.getFactoryItem).mockResolvedValue({ ...base, state: 'checking', rounds: [{ ci: { complete: false, checks: [] } }] } as unknown as Item)
  const view = render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  expect(await screen.findByText(/CI evidence unavailable or incomplete/)).toBeTruthy()
  view.unmount()
  vi.mocked(api.getFactoryItem).mockResolvedValue({ ...base, state: 'checking', rounds: [{ ci: { complete: true, checks: [] } }] } as unknown as Item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  expect(await screen.findByText('Observation complete; no checks reported.')).toBeTruthy()
})
it('previews material environment and permission settings before approval', async () => {
  vi.mocked(api.getFactoryItem).mockResolvedValue({ ...base, draft: { ...base.draft, config: { ...base.draft.config, native: false, workspace: 'go-dev', implementerAuto: true, reviewerInstructions: 'Check migration safety' } } } as Item)
  render(FactoryItem, { itemId: 'x', onNavigate: vi.fn() })
  expect(await screen.findByText(/Environment: go-dev · Implementer auto permission: on/)).toBeTruthy()
  expect(screen.getByText('Reviewer instructions: Check migration safety')).toBeTruthy()
})
