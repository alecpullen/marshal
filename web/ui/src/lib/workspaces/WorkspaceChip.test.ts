import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import WorkspaceChip from './WorkspaceChip.svelte'
import * as api from '../api.js'
import type { WSDoc, WorkspaceListItem } from '../api.js'

vi.mock('../api.js', async (importActual) => {
  const actual = await importActual<typeof import('../api.js')>()
  return { ...actual, listWorkspaces: vi.fn(), getProjectSettings: vi.fn(), getProjectHealth: vi.fn() }
})

afterEach(cleanup)

const doc = (tc: string[]) => ({ workspace: { name: '', base: 'debian:12', toolchains: tc, extends: '' }, packages: { apt: [], go: [], npm: [], pip: [] } }) as unknown as WSDoc
const ok = [{ n: 1, at: 0, buildStatus: 'ok' as const }]
const items: WorkspaceListItem[] = [
  { source: 'studio', name: 'node-app', published: 1, usage: 0, versions: ok, doc: doc(['node@22']) },
  { source: 'studio', name: 'go-service', published: 1, pool: 2, usage: 1, versions: ok, doc: doc(['go@1.23']) },
  { source: 'studio', name: 'fresh', published: 1, usage: 0, versions: [{ n: 1, at: 0, buildStatus: 'pending' }], doc: doc([]) },
  { source: 'repo', name: 'ci', project: '/p', usage: 0, doc: doc(['go@1.23']) },
  { source: 'repo', name: 'other', project: '/elsewhere', usage: 0 },
]

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listWorkspaces).mockResolvedValue(items)
  vi.mocked(api.getProjectSettings).mockResolvedValue({ workspace: 'go-service' } as never)
  vi.mocked(api.getProjectHealth).mockResolvedValue({ verify: { build: '', test: '' } } as never)
})

const options = () => [...(screen.getByLabelText('Workspace') as HTMLSelectElement).options]

describe('WorkspaceChip', () => {
  it('lists the project default first, marked, and drops other projects\' repo templates', async () => {
    render(WorkspaceChip, { project: '/p', value: '', onChange: vi.fn() })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(options().map((o) => o.value)).toEqual(['', 'go-service', 'node-app', 'fresh', 'repo:ci'])
    expect(options()[1].textContent).toContain('default')
    expect(options()[1].textContent).toContain('2 warm')
  })

  it('preselects the default', async () => {
    const onChange = vi.fn()
    render(WorkspaceChip, { project: '/p', value: '', onChange })
    await waitFor(() => expect(onChange).toHaveBeenCalledWith('go-service'))
  })

  it('does not override a choice already made', async () => {
    const onChange = vi.fn()
    render(WorkspaceChip, { project: '/p', value: 'node-app', onChange })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('disables an unbuilt workspace with "Build first"', async () => {
    render(WorkspaceChip, { project: '/p', value: '', onChange: vi.fn() })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    const fresh = options().find((o) => o.value === 'fresh')!
    expect(fresh.disabled).toBe(true)
    expect(fresh.textContent).toContain('Build first')
  })

  it('does not preselect an unbuilt default', async () => {
    vi.mocked(api.getProjectSettings).mockResolvedValue({ workspace: 'fresh' } as never)
    const onChange = vi.fn()
    render(WorkspaceChip, { project: '/p', value: '', onChange })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('reports the chosen reference', async () => {
    const onChange = vi.fn()
    render(WorkspaceChip, { project: '/p', value: '', onChange })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    await fireEvent.change(screen.getByLabelText('Workspace'), { target: { value: 'repo:ci' } })
    expect(onChange).toHaveBeenLastCalledWith('repo:ci')
  })

  it('warns when the workspace cannot run the project gate', async () => {
    vi.mocked(api.getProjectHealth).mockResolvedValue({ verify: { build: 'go build ./...', test: 'go test ./...' } } as never)
    render(WorkspaceChip, { project: '/p', value: 'node-app', onChange: vi.fn() })
    expect((await screen.findByTestId('gate-warning')).textContent).toContain('Verify gate may be skipped')
  })

  it('is quiet when the gate is runnable or unknown', async () => {
    vi.mocked(api.getProjectHealth).mockResolvedValue({ verify: { build: 'go build ./...', test: 'go test ./...' } } as never)
    const { rerender } = render(WorkspaceChip, { project: '/p', value: 'go-service', onChange: vi.fn() })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(screen.queryByTestId('gate-warning')).toBeNull()
    // An empty gate has nothing to run, so there is nothing to warn about.
    vi.mocked(api.getProjectHealth).mockResolvedValue({ verify: { build: '', test: '' } } as never)
    cleanup()
    render(WorkspaceChip, { project: '/p', value: 'node-app', onChange: vi.fn() })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(screen.queryByTestId('gate-warning')).toBeNull()
  })

  it('does not offer None when a project default exists', async () => {
    render(WorkspaceChip, { project: '/p', value: '', onChange: vi.fn() })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(options()[0].disabled).toBe(true)
    expect(options()[0].textContent).toContain('Choose a workspace')
    expect(options().some((o) => o.textContent?.includes('None'))).toBe(false)
  })

  it('offers None when there is no project default', async () => {
    vi.mocked(api.getProjectSettings).mockResolvedValue({} as never)
    render(WorkspaceChip, { project: '/p', value: '', onChange: vi.fn() })
    await waitFor(() => expect(options().length).toBeGreaterThan(1))
    expect(options()[0].textContent).toContain('None')
    expect(screen.queryByTestId('default-warning')).toBeNull()
  })

  it('warns that an unbuilt default will fail the spawn instead of falling back silently', async () => {
    vi.mocked(api.getProjectSettings).mockResolvedValue({ workspace: 'fresh' } as never)
    render(WorkspaceChip, { project: '/p', value: '', onChange: vi.fn() })
    expect((await screen.findByTestId('default-warning')).textContent).toContain('not built')
  })

  it('marks and preselects a pinned default, sending the pinned reference', async () => {
    vi.mocked(api.getProjectSettings).mockResolvedValue({ workspace: 'go-service@1' } as never)
    const onChange = vi.fn()
    render(WorkspaceChip, { project: '/p', value: '', onChange })
    await waitFor(() => expect(onChange).toHaveBeenCalledWith('go-service@1'))
    const o = options().find((x) => x.value === 'go-service@1')!
    expect(o.textContent).toContain('default')
    expect(screen.queryByTestId('default-warning')).toBeNull()
  })

  it('judges a pinned default by that version\'s build, not the latest', async () => {
    vi.mocked(api.listWorkspaces).mockResolvedValue([{ source: 'studio', name: 'go-service', published: 2, usage: 0, versions: [{ n: 1, at: 0, buildStatus: 'failed' }, { n: 2, at: 0, buildStatus: 'ok' }] }])
    vi.mocked(api.getProjectSettings).mockResolvedValue({ workspace: 'go-service@1' } as never)
    const onChange = vi.fn()
    render(WorkspaceChip, { project: '/p', value: '', onChange })
    expect((await screen.findByTestId('default-warning')).textContent).toContain('go-service@1')
    expect(onChange).not.toHaveBeenCalled()
  })
})
