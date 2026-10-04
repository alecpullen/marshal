import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Credentials from './Credentials.svelte'
import * as api from '../../lib/api.js'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, listCredentials: vi.fn(), putCredential: vi.fn(), deleteCredential: vi.fn() }
})

afterEach(cleanup)

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.listCredentials as Mock).mockResolvedValue([
    { id: 'gh', kind: 'vault', ref: 'vault:git/github', user: 'x-access-token', set: true },
    { id: 'tok', kind: 'pat', envVar: 'GITHUB_TOKEN', set: false },
    { id: 'key', kind: 'ssh', keyPath: '/home/u/.ssh/id', set: true },
  ])
  ;(api.putCredential as Mock).mockResolvedValue({})
  ;(api.deleteCredential as Mock).mockResolvedValue(undefined)
})

const openForm = async () => {
  await screen.findAllByTestId('credential-row')
  await fireEvent.click(screen.getByRole('button', { name: 'Add credential' }))
}

describe('Credentials settings', () => {
  it('lists id, kind, ref or env var, user and set state', async () => {
    render(Credentials, { onToast: vi.fn() })
    const rows = await screen.findAllByTestId('credential-row')
    expect(rows.map((r) => within(r).getAllByRole('cell').map((c) => c.textContent?.trim()).slice(0, 5))).toEqual([
      ['gh', 'vault', 'vault:git/github', 'x-access-token', 'set'],
      ['tok', 'pat', 'GITHUB_TOKEN', '—', 'not set'],
      ['key', 'ssh', '/home/u/.ssh/id', '—', 'set'],
    ])
  })

  it('shows the fields of the chosen kind', async () => {
    render(Credentials, { onToast: vi.fn() })
    await openForm()
    expect(screen.getByLabelText('Environment variable')).toBeTruthy() // pat is the default
    expect(screen.queryByLabelText('Key path')).toBeNull()
    await fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'ssh' } })
    expect(screen.getByLabelText('Key path')).toBeTruthy()
    expect(screen.queryByLabelText('Environment variable')).toBeNull()
    await fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'vault' } })
    expect(screen.getByLabelText('Secret ref')).toBeTruthy()
    expect(screen.getByLabelText('User')).toBeTruthy()
    await fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'none' } })
    expect(screen.queryByLabelText('User')).toBeNull()
    expect(screen.queryByLabelText('Secret ref')).toBeNull()
  })

  it('posts only the fields of the kind, with the vault: prefix on a ref', async () => {
    render(Credentials, { onToast: vi.fn() })
    await openForm()
    await fireEvent.input(screen.getByLabelText('Credential id'), { target: { value: 'gitea' } })
    await fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'vault' } })
    await fireEvent.input(screen.getByLabelText('Secret ref'), { target: { value: 'git/gitea' } })
    await fireEvent.input(screen.getByLabelText('User'), { target: { value: 'bot' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save credential' }))
    await waitFor(() => expect(api.putCredential).toHaveBeenCalledWith({ id: 'gitea', kind: 'vault', ref: 'vault:git/gitea', user: 'bot' }))
  })

  it('a pat sends its env var and nothing from other kinds', async () => {
    render(Credentials, { onToast: vi.fn() })
    await openForm()
    await fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'ssh' } })
    await fireEvent.input(screen.getByLabelText('Key path'), { target: { value: '/k' } })
    await fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'pat' } })
    await fireEvent.input(screen.getByLabelText('Credential id'), { target: { value: 't2' } })
    await fireEvent.input(screen.getByLabelText('Environment variable'), { target: { value: 'TOK' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save credential' }))
    await waitFor(() => expect(api.putCredential).toHaveBeenCalledWith({ id: 't2', kind: 'pat', envVar: 'TOK' }))
  })

  it("shows the server's refusal when a repo uses the credential", async () => {
    ;(api.deleteCredential as Mock).mockRejectedValue(new api.APIError(409, { error: 'credential "gh" is used by repo "marshal"' }))
    render(Credentials, { onToast: vi.fn() })
    const rows = await screen.findAllByTestId('credential-row')
    await fireEvent.click(within(rows[0]).getByRole('button', { name: 'Delete' }))
    await fireEvent.click(within(rows[0]).getByRole('button', { name: 'Confirm delete' }))
    expect((await screen.findByRole('alert')).textContent).toBe('credential "gh" is used by repo "marshal"')
    expect(api.deleteCredential).toHaveBeenCalledWith('gh')
  })
})
