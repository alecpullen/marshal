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
  await fireEvent.click(screen.getByLabelText('Submit replacement plan for renewed approval'))
  expect(screen.getByLabelText('Replacement plan')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Grant rounds and retry' }) as HTMLButtonElement).disabled).toBe(true)
})
