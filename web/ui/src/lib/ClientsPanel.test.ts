import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import ClientsPanel from './ClientsPanel.svelte'
import * as api from './api.js'

/*
  Quota badges are the only visible signal that a client is restricted,
  and the bridge defaults an absent field to unlimited — so the form must
  drop 0/empty fields from the payload rather than send them as zeros.
*/
vi.mock('./api.js', async (importActual) => {
  const actual = await importActual<typeof import('./api.js')>()
  return {
    ...actual,
    listClients: vi.fn(),
    createClient: vi.fn(),
    deleteClient: vi.fn(),
    listProjects: vi.fn(),
    getFactoryGrant: vi.fn(),
    putFactoryGrant: vi.fn(),
  }
})

function client(over: Partial<api.MCPClient> = {}): api.MCPClient {
  return {
    id: 'c1',
    name: 'claude-code',
    autonomous: false,
    maxConcurrent: 0,
    maxPerDay: 0,
    allowedRepos: [],
    ownerId: 'o1',
    createdAt: '2025-01-01T00:00:00Z',
    ...over,
  }
}

/** Registered projects carry the opaque factory ID a grant names, never the path. */
const projects: api.ProjectStatus[] = [
  { root: '/home/u/alpha', available: true, trust: 'trusted', isolation: 'available', mcpProjectId: 'p1' },
  { root: '/home/u/beta', available: true, trust: 'trusted', isolation: 'available', mcpProjectId: 'p2' },
]

const zeroGrant = (projectId = 'p1'): api.MCPFactoryProjectGrant => api.emptyFactoryGrant(projectId)

/** Choose a pair, which is what starts the one grant read. */
async function openGrant(clientId = 'c1', projectId = 'p1') {
  ;(api.listClients as Mock).mockResolvedValue([client()])
  render(ClientsPanel)
  const user = userEvent.setup()
  // The clients and projects arrive from the bridge, so the options appear a
  // tick after the selects do.
  const clientSelect = (await screen.findByLabelText('Client')) as HTMLSelectElement
  await vi.waitFor(() => expect([...clientSelect.options].map((o) => o.value)).toContain(clientId))
  await user.selectOptions(clientSelect, clientId)
  const projectSelect = screen.getByLabelText('Project') as HTMLSelectElement
  await vi.waitFor(() => expect([...projectSelect.options].map((o) => o.value)).toContain(projectId))
  await user.selectOptions(projectSelect, projectId)
  return user
}

function seedCreate() {
  ;(api.createClient as Mock).mockResolvedValue({
    id: 'c1',
    name: 'x',
    token: 'tok-1',
    autonomous: false,
  })
}

afterEach(cleanup)

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.listProjects as Mock).mockResolvedValue(projects)
})

describe('ClientsPanel', () => {
  it('renders quota badges for a restricted client', async () => {
    ;(api.listClients as Mock).mockResolvedValue([
      client({ maxConcurrent: 3, maxPerDay: 10, allowedRepos: ['a/b', 'c/d'] }),
    ])

    render(ClientsPanel)

    expect(await screen.findByText('max 3 concurrent')).toBeTruthy()
    expect(screen.getByText('max 10/day')).toBeTruthy()
    expect(screen.getByText('2 repos')).toBeTruthy()
  })

  it('sends the entered quotas to createClient', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.type(screen.getByPlaceholderText('Max concurrent'), '3')
    await user.type(screen.getByPlaceholderText('Max per day'), '10')
    await user.type(
      screen.getByPlaceholderText('Allowed repos (owner/repo, comma-separated)'),
      'a/b, c/d',
    )
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledWith({
      name: 'x',
      autonomous: false,
      maxConcurrent: 3,
      maxPerDay: 10,
      allowedRepos: ['a/b', 'c/d'],
    })
  })

  it('omits maxConcurrent when 0 is typed, even though the input allows it', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.type(screen.getByPlaceholderText('Max concurrent'), '0')
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledTimes(1)
    const payload = (api.createClient as Mock).mock.calls[0][0] as Record<string, unknown>
    // 0 means unlimited to the bridge; sending it would silently cap the
    // client at zero, so the key must be absent rather than falsy.
    expect(payload).not.toHaveProperty('maxConcurrent')
  })

  it('omits maxConcurrent when a negative value is typed', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.type(screen.getByPlaceholderText('Max concurrent'), '-5')
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledTimes(1)
    const payload = (api.createClient as Mock).mock.calls[0][0] as Record<string, unknown>
    // The min attribute does not stop typing; the payload builder must.
    expect(payload).not.toHaveProperty('maxConcurrent')
  })

  it('omits quota fields when only a name is given', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledTimes(1)
    const payload = (api.createClient as Mock).mock.calls[0][0] as Record<string, unknown>
    expect(payload).toEqual({ name: 'x', autonomous: false })
    expect(payload).not.toHaveProperty('maxConcurrent')
    expect(payload).not.toHaveProperty('maxPerDay')
    expect(payload).not.toHaveProperty('allowedRepos')
  })

  it('trims and drops empty segments from the repos input', async () => {
    ;(api.listClients as Mock).mockResolvedValue([])
    seedCreate()

    render(ClientsPanel)

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Client name (e.g. claude-code)'), 'x')
    await user.type(
      screen.getByPlaceholderText('Allowed repos (owner/repo, comma-separated)'),
      'a/b, , c/d',
    )
    await user.click(screen.getByRole('button', { name: 'Create client' }))

    expect(api.createClient).toHaveBeenCalledWith({
      name: 'x',
      autonomous: false,
      allowedRepos: ['a/b', 'c/d'],
    })
  })

  it('existingClientStartsWithNoGrant', async () => {
    ;(api.getFactoryGrant as Mock).mockResolvedValue({
      clientId: 'c1',
      projectId: 'p1',
      grant: zeroGrant(),
      version: 'v0',
    })

    await openGrant()

    // A never-granted pair is the zero grant, not an error: nothing is checked
    // and no approval authority exists to begin with.
    expect(await screen.findByRole('button', { name: 'Save authority' })).toBeTruthy()
    for (const label of ['Read work items', 'Create drafts', 'Edit drafts', 'Answer questions', 'Approve and queue']) {
      expect((screen.getByLabelText(label) as HTMLInputElement).checked).toBe(false)
    }
    expect((screen.getByLabelText('Approval authority') as HTMLSelectElement).value).toBe('none')
    expect(screen.queryByText(/unknown to the bridge/)).toBeNull()
    expect(api.getFactoryGrant).toHaveBeenCalledWith('c1', 'p1')
    expect(api.getFactoryGrant).toHaveBeenCalledTimes(1)
  })

  it('editsProjectScopedFactoryGrant', async () => {
    ;(api.getFactoryGrant as Mock).mockResolvedValue({
      clientId: 'c1',
      projectId: 'p2',
      grant: zeroGrant('p2'),
      version: 'v0',
    })
    ;(api.putFactoryGrant as Mock).mockImplementation(
      async (clientId: string, projectId: string, grant: api.MCPFactoryProjectGrant, version: string) => ({
        clientId,
        projectId,
        grant,
        version: `${version}+`,
      }),
    )

    const user = await openGrant('c1', 'p2')
    await screen.findByRole('button', { name: 'Save authority' })

    await user.click(screen.getByLabelText('Read work items'))
    await user.click(screen.getByLabelText('Edit drafts'))
    // Approving is only offered alongside an authority, so pick one first.
    expect((screen.getByLabelText('Approve and queue') as HTMLInputElement).disabled).toBe(true)
    await user.selectOptions(screen.getByLabelText('Approval authority'), 'standing')
    await user.click(screen.getByLabelText('Approve and queue'))
    await user.click(screen.getByRole('button', { name: 'Save authority' }))

    // The write names the opaque project ID and the version it was edited
    // from; the untouched capabilities are sent as false, not omitted.
    expect(api.putFactoryGrant).toHaveBeenCalledWith(
      'c1',
      'p2',
      {
        projectId: 'p2',
        read: true,
        createDraft: false,
        editDraft: true,
        answerQuestion: false,
        approveAndQueue: true,
        approvalAuthority: 'standing',
      },
      'v0',
    )
    // The version follows the write, so the next save is not refused as stale.
    expect(await screen.findByText('Version v0+')).toBeTruthy()
  })

  it('grantConflictRetainsInput', async () => {
    ;(api.getFactoryGrant as Mock).mockResolvedValue({
      clientId: 'c1',
      projectId: 'p1',
      grant: zeroGrant(),
      version: 'v0',
    })
    // Another editor saved first; the refusal carries the value that won.
    const winner: api.FactoryGrantState = {
      clientId: 'c1',
      projectId: 'p1',
      grant: { ...zeroGrant(), read: true, approvalAuthority: 'user_directed' },
      version: 'v2',
    }
    ;(api.putFactoryGrant as Mock).mockRejectedValue(new api.FactoryGrantConflictError(winner))

    const user = await openGrant()
    await screen.findByRole('button', { name: 'Save authority' })
    await user.click(screen.getByLabelText('Answer questions'))
    await user.click(screen.getByRole('button', { name: 'Save authority' }))

    const conflict = await screen.findByTestId('grant-conflict')
    expect(conflict.textContent).toContain('v2')
    expect(conflict.textContent).toContain('user_directed')
    // The unsaved edit survives the refusal…
    expect((screen.getByLabelText('Answer questions') as HTMLInputElement).checked).toBe(true)
    // …and nothing was re-read behind the user's back: only the explicit
    // reload below asks for the saved value.
    expect(api.getFactoryGrant).toHaveBeenCalledTimes(1)

    await user.click(screen.getByRole('button', { name: 'Reload saved authority' }))
    await vi.waitFor(() => expect(api.getFactoryGrant).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect((screen.getByLabelText('Answer questions') as HTMLInputElement).checked).toBe(false))
    expect(screen.queryByTestId('grant-conflict')).toBeNull()
  })

  it('bareConflictIsReportedNotSilent', async () => {
    ;(api.getFactoryGrant as Mock).mockResolvedValue({
      clientId: 'c1',
      projectId: 'p1',
      grant: zeroGrant(),
      version: 'v0',
    })
    // A 409 the client cannot read: the bridge reported no winning state, so
    // there is no box to show. The refusal still has to be visible, or the
    // operator reads silence as a successful save.
    ;(api.putFactoryGrant as Mock).mockRejectedValue(new api.FactoryGrantConflictError(null))

    const user = await openGrant()
    await screen.findByRole('button', { name: 'Save authority' })
    await user.click(screen.getByLabelText('Read work items'))
    await user.click(screen.getByRole('button', { name: 'Save authority' }))

    expect(await screen.findByText(/did not report what it stored/)).toBeTruthy()
    expect(screen.queryByTestId('grant-conflict')).toBeNull()
    // The unsaved edit is still there, and nothing was re-read.
    expect((screen.getByLabelText('Read work items') as HTMLInputElement).checked).toBe(true)
    expect(api.getFactoryGrant).toHaveBeenCalledTimes(1)
  })

  it('unauthorizedGrantIsNotEditable', async () => {
    // The route answers one 404 for an unknown client and an unknown project
    // alike, so neither can become an oracle for which IDs exist.
    ;(api.getFactoryGrant as Mock).mockRejectedValue(new api.APIError(404, { error: 'unknown client or project' }))

    await openGrant()

    expect(await screen.findByText(/unknown to the bridge/)).toBeTruthy()
    // Nothing is editable without the version the bridge would have shown.
    expect(screen.queryByRole('button', { name: 'Save authority' })).toBeNull()
    expect(screen.queryByLabelText('Read work items')).toBeNull()
    expect(api.putFactoryGrant).not.toHaveBeenCalled()
  })
})
