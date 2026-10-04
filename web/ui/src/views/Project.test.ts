import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Project from './Project.svelte'
import * as api from '../lib/api.js'
import { toRow } from '../lib/fleet'
import { healthChecks, latestAgent, parseWorkspaceRef } from '../lib/project/model'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    getProjectSettings: vi.fn(),
    putProjectSettings: vi.fn(),
    getProjectHealth: vi.fn(),
    getWorkspacePolicy: vi.fn(),
    listWorkspaces: vi.fn(),
    listRepos: vi.fn(),
    listClients: vi.fn(),
    getModels: vi.fn(),
  }
})

afterEach(cleanup)

const ROOT = '/home/u/alpha'
const settings: api.ProjectSettings = { workspace: 'go-dev', mode: 'edit', routing: { profile: 'fast', overrides: { reviewer: 'big' } }, intake: { repoId: 'r1', labels: ['agent'], clients: ['c1'] } }
const healthy: api.ProjectHealth = { gateRunnable: 'yes', mirrorFresh: [{ repoId: 'r1', present: true, ageSeconds: 120, head: 'main' }], orphanWorktrees: [], trust: 'trusted', workspaceResolves: { ref: 'go-dev', resolves: true, built: true } }

const mount = (over: Record<string, unknown> = {}) => render(Project, { root: ROOT, agents: [], onNavigate: vi.fn(), ...over })

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getProjectSettings as Mock).mockResolvedValue(settings)
  ;(api.putProjectSettings as Mock).mockImplementation(async (_r: string, s: api.ProjectSettings) => s)
  ;(api.getProjectHealth as Mock).mockResolvedValue(healthy)
  ;(api.getWorkspacePolicy as Mock).mockResolvedValue({ mode: 'edit', allow: ['go test ./...'] })
  ;(api.listWorkspaces as Mock).mockResolvedValue([{ source: 'studio', name: 'go-dev' }, { source: 'studio', name: 'node' }, { source: 'repo', name: 'ci', project: ROOT }])
  ;(api.listRepos as Mock).mockResolvedValue([{ id: 'r1', url: 'u' }, { id: 'r2', url: 'u2' }])
  ;(api.listClients as Mock).mockResolvedValue([{ id: 'c1', name: 'Cursor' }, { id: 'c2', name: 'Zed' }])
  ;(api.getModels as Mock).mockResolvedValue({ profiles: { fast: {}, careful: {} } })
})

describe('Project overview', () => {
  it('loads the saved settings into the defaults and intake cards', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('Mode') as HTMLSelectElement).value).toBe('edit'))
    expect(api.getProjectSettings).toHaveBeenCalledWith(ROOT)
    await waitFor(() => expect((screen.getByLabelText('Workspace') as HTMLSelectElement).value).toBe('go-dev'))
    await waitFor(() => expect([...(screen.getByLabelText('Workspace') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'go-dev', 'node', 'repo:ci']))
    expect((screen.getByLabelText('Model profile') as HTMLSelectElement).value).toBe('fast')
    expect((screen.getByLabelText('Intake repo') as HTMLSelectElement).value).toBe('r1')
    expect(screen.getByText('agent')).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: 'Cursor' }) as HTMLInputElement).checked).toBe(true)
    expect((screen.getByRole('checkbox', { name: 'Zed' }) as HTMLInputElement).checked).toBe(false)
  })

  it('saves defaults with the right body, keeping intake and routing overrides', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('Workspace') as HTMLSelectElement).options.length).toBe(4))
    await fireEvent.change(screen.getByLabelText('Workspace'), { target: { value: 'repo:ci' } })
    await fireEvent.change(screen.getByLabelText('Model profile'), { target: { value: 'careful' } })
    await fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'auto' } })
    await fireEvent.change(screen.getByLabelText('Isolation'), { target: { value: 'isolated' } })
    await fireEvent.change(screen.getByLabelText('Ship target'), { target: { value: 'patch' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putProjectSettings).toHaveBeenCalled())
    expect(api.putProjectSettings).toHaveBeenCalledWith(ROOT, {
      workspace: 'repo:ci',
      routing: { profile: 'careful', overrides: { reviewer: 'big' } },
      mode: 'auto',
      isolated: true,
      shipTarget: 'patch',
      intake: { repoId: 'r1', labels: ['agent'], clients: ['c1'] },
    })
    expect((await screen.findByRole('status')).textContent).toBe('Defaults saved')
  })

  it('leaves unset defaults out of the body', async () => {
    ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {} })
    mount()
    await waitFor(() => expect(api.getProjectSettings).toHaveBeenCalled())
    await fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(api.putProjectSettings).toHaveBeenCalled())
    const body = (api.putProjectSettings as Mock).mock.calls[0][1]
    // What goes on the wire: nothing but the intake.
    expect(JSON.parse(JSON.stringify(body))).toEqual({ intake: {} })
  })

  it('saves intake: repo, labels as chips, allowed clients', async () => {
    mount()
    await waitFor(() => expect((screen.getByLabelText('Intake repo') as HTMLSelectElement).value).toBe('r1'))
    await waitFor(() => expect(screen.getByRole('checkbox', { name: 'Zed' })).toBeTruthy())
    await fireEvent.input(screen.getByLabelText('New label'), { target: { value: 'bug' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    // One label per repo: adding replaces the saved one.
    expect(screen.queryByText('agent')).toBeNull()
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Zed' }))
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Cursor' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Save intake' }))
    await waitFor(() => expect(api.putProjectSettings).toHaveBeenCalled())
    expect((api.putProjectSettings as Mock).mock.calls[0][1].intake).toEqual({ repoId: 'r1', labels: ['bug'], clients: ['c2'] })
  })

  it('shows a refused save, such as an unregistered repo, and keeps the form', async () => {
    ;(api.putProjectSettings as Mock).mockRejectedValue(new api.APIError(400, { error: 'intake.repoId "r9" is not a registered repo' }))
    mount()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save intake' })).toBeTruthy())
    await fireEvent.click(screen.getByRole('button', { name: 'Save intake' }))
    expect((await screen.findByRole('alert')).textContent).toContain('is not a registered repo')
  })

  it('renders the health checks with a dot each, and Refresh fetches again', async () => {
    ;(api.getProjectHealth as Mock).mockResolvedValueOnce({ ...healthy, gateRunnable: 'no', orphanWorktrees: ['wt-1'], mirrorFresh: [{ repoId: 'r1', present: false }] })
    mount()
    const rows = await screen.findAllByTestId('health-check')
    const dotOf = (label: string) => within(rows.find((r) => r.textContent?.includes(label))!).getByRole('img').getAttribute('aria-label')
    expect(dotOf('Verify gate')).toBe('err')
    expect(dotOf('Mirror r1')).toBe('err')
    expect(dotOf('Orphan worktrees')).toBe('warn')
    expect(dotOf('Folder trust')).toBe('ok')
    expect(dotOf('Default workspace')).toBe('ok')
    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(api.getProjectHealth).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(within(screen.getAllByTestId('health-check').find((r) => r.textContent?.includes('Verify gate'))!).getByRole('img').getAttribute('aria-label')).toBe('ok'))
  })

  it('shows the default workspace policy read-only with a designer link', async () => {
    const onNavigate = vi.fn()
    mount({ onNavigate })
    const card = await screen.findByTestId('policy-card')
    await waitFor(() => expect(card.textContent).toContain('go test ./...'))
    expect(api.getWorkspacePolicy).toHaveBeenCalledWith('go-dev', undefined)
    expect(within(card).queryByRole('textbox')).toBeNull()
    await fireEvent.click(within(card).getByRole('link', { name: 'Edit in designer' }))
    expect(onNavigate).toHaveBeenCalledWith('#workspaces/go-dev/edit')
  })

  it('says so when the workspace is a repo template or none is set', async () => {
    ;(api.getProjectSettings as Mock).mockResolvedValue({ workspace: 'repo:ci', intake: {} })
    const { unmount } = mount()
    await waitFor(() => expect(screen.getByTestId('policy-card').textContent).toContain('comes from the repo'))
    expect(api.getWorkspacePolicy).not.toHaveBeenCalled()
    expect(screen.queryByRole('link', { name: 'Edit in designer' })).toBeNull()
    unmount()
    ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {} })
    mount()
    await waitFor(() => expect(screen.getByTestId('policy-card').textContent).toContain('No default workspace'))
  })
})

describe('Project session sheet tab', () => {
  const rows = [
    toRow({ id: 'old', project: ROOT, status: 'idle', updatedAt: '2026-10-01T00:00:00Z', name: 'old-agent' }),
    toRow({ id: 'new', project: ROOT, status: 'running', updatedAt: '2026-10-04T00:00:00Z', name: 'new-agent', contextPct: 10, changedFiles: 1 }),
    toRow({ id: 'other', project: '/home/u/beta', status: 'running', updatedAt: '2026-10-05T00:00:00Z', name: 'other-agent' }),
  ]

  it("shows the project's most recent agent's telemetry, read-only", async () => {
    const telemetry = { new: { contextPct: 64, changedFiles: 3, toolStats: [{ name: 'shell.run', calls: 7, errors: 1, slowestMs: 950 }], rules: ['no-network'], at: 1 } }
    mount({ agents: rows, telemetry })
    await fireEvent.click(screen.getByRole('tab', { name: 'Session sheet' }))
    const sheet = screen.getByTestId('session-sheet')
    expect(sheet.textContent).toContain('new-agent')
    expect(screen.getByTestId('context-pct').textContent).toBe('64% used')
    expect(screen.getByTestId('changed-files').textContent).toBe('3 files')
    expect(sheet.textContent).toContain('shell.run')
    expect(sheet.textContent).toContain('(1 failed)')
    expect(sheet.textContent).toContain('slowest 950 ms')
    expect(sheet.textContent).toContain('no-network')
    expect(within(sheet).queryByRole('button')).toBeNull()
  })

  it('falls back to the row counts when no telemetry delta arrived, and says what is not reported', async () => {
    mount({ agents: rows })
    await fireEvent.click(screen.getByRole('tab', { name: 'Session sheet' }))
    expect(screen.getByTestId('context-pct').textContent).toBe('10% used')
    expect(screen.getByTestId('changed-files').textContent).toBe('1 file')
    expect(screen.getAllByText('Not reported yet.')).toHaveLength(2)
  })

  it('says so when the project has no agent', async () => {
    mount({ agents: [rows[2]] })
    await fireEvent.click(screen.getByRole('tab', { name: 'Session sheet' }))
    expect(screen.getByText('No agent has run in this project yet.')).toBeTruthy()
  })
})

describe('project model helpers', () => {
  it('parses workspace refs', () => {
    expect(parseWorkspaceRef('go-dev')).toEqual({ source: 'studio', name: 'go-dev' })
    expect(parseWorkspaceRef('go-dev@3')).toEqual({ source: 'studio', name: 'go-dev', version: 3 })
    expect(parseWorkspaceRef('repo:ci')).toEqual({ source: 'repo', name: 'ci' })
  })
  it('picks the latest agent of a project', () => {
    const a = toRow({ id: 'a', project: '/p', status: 'idle', updatedAt: '2026-01-01T00:00:00Z' })
    const b = toRow({ id: 'b', project: '/p', status: 'idle', updatedAt: '2026-01-02T00:00:00Z' })
    expect(latestAgent([a, b], '/p')?.id).toBe('b')
    expect(latestAgent([a, b], '/q')).toBeUndefined()
  })
  it('grades mirror age and unbuilt workspaces', () => {
    const checks = healthChecks({ gateRunnable: 'unknown', mirrorFresh: [{ repoId: 'r', present: true, ageSeconds: 7200 }], orphanWorktrees: [], trust: 'untrusted', workspaceResolves: { ref: 'w', resolves: true, built: false, error: 'not built' } })
    const dot = (id: string) => checks.find((c) => c.id === id)?.dot
    expect(dot('gate')).toBe('unknown')
    expect(dot('mirror:r')).toBe('warn')
    expect(dot('trust')).toBe('warn')
    expect(dot('workspace')).toBe('warn')
  })
})
