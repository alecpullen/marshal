import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import CIFixer from './CIFixer.svelte'
import * as api from '../../lib/api.js'
import { toRow } from '../../lib/fleet'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, getProjectSettings: vi.fn(), putProjectSettings: vi.fn(), listRepos: vi.fn(), listSecrets: vi.fn(), listCIHistory: vi.fn() }
})

afterEach(cleanup)

const ROOT = '/home/u/alpha'
const hist = (o: Partial<api.CIHistory>): api.CIHistory => ({ id: 'c1', repoId: 'r1', sha: '0123456789abcdef', check: 'go test', status: 'fixed', reason: 'fixed the nil check', costUsd: 0.42, ...o })

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {}, automations: { reviewBot: { enabled: true, repoId: 'r1', skipDrafts: true, autoPost: false, holdSeverities: [] } } })
  ;(api.putProjectSettings as Mock).mockImplementation(async (_r: string, s: api.ProjectSettings) => s)
  ;(api.listRepos as Mock).mockResolvedValue([{ id: 'r1', url: 'u' }])
  ;(api.listSecrets as Mock).mockResolvedValue([])
  ;(api.listCIHistory as Mock).mockResolvedValue([
    hist({ id: 'c1', agentId: 'a1', prUrl: 'https://github.com/o/n/pull/5' }),
    hist({ id: 'c2', status: "didn't reproduce", reason: 'passes on retry', costUsd: 0.1 }),
    hist({ id: 'c3', status: 'gave up: forbidden change', reason: 'skips a test', agentId: 'gone' }),
  ])
})

const mount = (over: Record<string, unknown> = {}) => render(CIFixer, { root: ROOT, agents: [toRow({ id: 'a1', project: ROOT, status: 'idle', updatedAt: '' })], onNavigate: vi.fn(), ...over })

describe('CI fixer', () => {
  it('saves nested automations.ciFixer and keeps the review bot settings', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('CI repo') as HTMLSelectElement).options.length).toBe(2))
    await fireEvent.click(screen.getByLabelText('Try to fix failing checks'))
    await fireEvent.change(screen.getByLabelText('CI repo'), { target: { value: 'r1' } })
    await fireEvent.input(screen.getByLabelText('Branches'), { target: { value: 'main, release' } })
    await fireEvent.input(screen.getByLabelText('Max minutes'), { target: { value: '15' } })
    await fireEvent.input(screen.getByLabelText('Max USD'), { target: { value: '3' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    await waitFor(() => expect(api.putProjectSettings).toHaveBeenCalled())
    const body = (api.putProjectSettings as Mock).mock.calls[0][1]
    expect(body.automations.reviewBot.repoId).toBe('r1')
    expect(body.automations.ciFixer).toEqual({ enabled: true, repoId: 'r1', branches: ['main', 'release'], maxMinutes: 15, maxUsd: 3, push: false, pushBranches: [] })
  })

  it('shows push branches and a warning only when push is on', async () => {
    mount()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save settings' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByLabelText('Push branches')).toBeNull()
    await fireEvent.click(screen.getByLabelText('Push fixes to the branch instead of opening a PR'))
    expect(screen.getByLabelText('Push branches')).toBeTruthy()
    expect(screen.getByRole('note').textContent).toContain('skips human review')
  })

  it('renders a tag per status with its tone', async () => {
    mount()
    const rows = await screen.findAllByTestId('ci-row')
    expect(rows).toHaveLength(3)
    expect(within(rows[0]).getByText('fixed').className).toContain('text-ok')
    expect(within(rows[1]).getByText("didn't reproduce").className).toContain('text-muted')
    expect(within(rows[2]).getByText('gave up: forbidden change').className).toContain('text-err')
    expect(within(rows[0]).getByText('01234567')).toBeTruthy()
    expect(within(rows[0]).getByText('$0.42')).toBeTruthy()
    expect(within(rows[0]).getByRole('link', { name: 'PR' }).getAttribute('href')).toBe('https://github.com/o/n/pull/5')
    expect(api.listCIHistory).toHaveBeenCalledWith({ project: ROOT })
  })

  it('opens the agent session when it still exists, and not otherwise', async () => {
    const onNavigate = vi.fn()
    mount({ onNavigate })
    const rows = await screen.findAllByTestId('ci-row')
    await fireEvent.click(rows[2])
    await fireEvent.click(rows[1])
    expect(onNavigate).not.toHaveBeenCalled()
    await fireEvent.click(rows[0])
    expect(onNavigate).toHaveBeenCalledWith('#chat/a1')
  })
})
