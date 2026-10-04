import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Gallery from './Gallery.svelte'
import * as api from '../../lib/api.js'
import { APIError, type WSDoc, type WorkspaceListItem } from '../../lib/api.js'
import type { AgentRow } from '../../lib/fleet'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, listWorkspaces: vi.fn(), createWorkspace: vi.fn(), listProjects: vi.fn() }
})

afterEach(cleanup)

const doc = (over: Partial<WSDoc['workspace']> = {}): WSDoc => ({
  workspace: { name: 'x', base: 'debian:12', toolchains: ['go@1.23'], extends: '', ...over },
  packages: { apt: ['git', 'make'], go: [], npm: [], pip: ['uv'] },
  mounts: [], files: {}, secretsEnv: {}, inject: {},
  network: { mode: 'allowlist', egress: [] }, resources: { cpu: 0, memory: '', disk: '', timeout: '' },
  policy: { mode: '', allow: [] }, setup: { run: '' },
})

const items: WorkspaceListItem[] = [
  { source: 'studio', name: 'go-service', published: 2, pool: 2, usage: 3, draftChanges: true, doc: doc(), versions: [{ n: 1, at: 0, buildStatus: 'ok' }, { n: 2, at: 0, buildStatus: 'failed' }] },
  { source: 'repo', name: 'ci', project: '/home/u/app', usage: 0, doc: doc({ base: 'ubuntu' }) },
]
const agents = [{ id: 'a1', name: 'builder', project: '/p', status: 'idle', updatedAt: '' }] as AgentRow[]

beforeEach(() => {
  vi.mocked(api.listWorkspaces).mockResolvedValue(items)
  vi.mocked(api.listProjects).mockResolvedValue([{ root: '/home/u/app', available: true }] as api.ProjectStatus[])
  vi.mocked(api.createWorkspace).mockResolvedValue({ name: 'new-one' })
})

describe('Gallery', () => {
  it('renders cards with source, version, draft, pool, chips, usage and build status', async () => {
    render(Gallery, { agents, onNavigate: vi.fn() })
    const cards = await screen.findAllByTestId('ws-card')
    expect(cards).toHaveLength(2)
    const studio = cards[0]
    expect(studio.textContent).toContain('go-service')
    expect(studio.textContent).toContain('Studio')
    expect(studio.textContent).toContain('v2')
    expect(studio.textContent).toContain('draft changes')
    expect(studio.textContent).toContain('2 warm')
    expect(studio.textContent).toContain('go@1.23')
    expect(studio.textContent).toContain('3 packages')
    expect(studio.textContent).toContain('3 agents')
    expect(studio.querySelector('[data-testid="build-dot"]')?.getAttribute('data-status')).toBe('failed')
    expect(cards[1].textContent).toContain('Repo · app')
  })

  it('a Studio card opens the designer; a repo card does not navigate', async () => {
    const nav = vi.fn()
    render(Gallery, { agents, onNavigate: nav })
    const cards = await screen.findAllByTestId('ws-card')
    await fireEvent.click(cards[1])
    expect(nav).not.toHaveBeenCalled()
    await fireEvent.click(cards[0])
    expect(nav).toHaveBeenCalledWith('#workspaces/go-service/edit')
  })

  async function openCreate(name = 'new-one') {
    const nav = vi.fn()
    render(Gallery, { agents, onNavigate: nav })
    await screen.findAllByTestId('ws-card')
    await fireEvent.click(screen.getByText('New workspace'))
    await fireEvent.input(await screen.findByLabelText('Workspace name'), { target: { value: name } })
    return nav
  }

  it('blank posts from=blank and opens the designer', async () => {
    const nav = await openCreate()
    await fireEvent.click(screen.getByText('Create'))
    await waitFor(() => expect(api.createWorkspace).toHaveBeenCalledWith('new-one', 'blank'))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('#workspaces/new-one/edit'))
  })

  it('a starter posts starter:<id>', async () => {
    await openCreate()
    await fireEvent.click(screen.getByLabelText('A starter'))
    await fireEvent.click(screen.getByText('Node app'))
    await fireEvent.click(screen.getByText('Create'))
    await waitFor(() => expect(api.createWorkspace).toHaveBeenCalledWith('new-one', 'starter:node-app'))
  })

  it('devcontainer posts devcontainer:<root> once a project is picked', async () => {
    await openCreate()
    await fireEvent.click(screen.getByLabelText('Import devcontainer.json'))
    expect((screen.getByText('Create') as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.change(await screen.findByLabelText('Project'), { target: { value: '/home/u/app' } })
    await fireEvent.click(screen.getByText('Create'))
    await waitFor(() => expect(api.createWorkspace).toHaveBeenCalledWith('new-one', 'devcontainer:/home/u/app'))
  })

  it('snapshot posts snapshot:<agentId>', async () => {
    await openCreate()
    await fireEvent.click(screen.getByLabelText('Snapshot a running agent'))
    await fireEvent.change(await screen.findByLabelText('Agent'), { target: { value: 'a1' } })
    await fireEvent.click(screen.getByText('Create'))
    await waitFor(() => expect(api.createWorkspace).toHaveBeenCalledWith('new-one', 'snapshot:a1'))
  })

  it('shows the bridge refusal inline and stays open', async () => {
    vi.mocked(api.createWorkspace).mockRejectedValue(new APIError(400, { error: 'devcontainer.json uses build; only image is supported' }))
    const nav = await openCreate()
    await fireEvent.click(screen.getByText('Create'))
    expect((await screen.findByRole('alert')).textContent).toContain('only image is supported')
    expect(nav).not.toHaveBeenCalled()
  })

  it('rejects an invalid name before posting', async () => {
    await openCreate('Bad Name')
    expect((screen.getByText('Create') as HTMLButtonElement).disabled).toBe(true)
  })
})
