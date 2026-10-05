import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Repos from './Repos.svelte'
import * as api from '../../lib/api.js'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, listRepos: vi.fn(), listCredentials: vi.fn(), registerRepo: vi.fn(), removeRepo: vi.fn() }
})

afterEach(cleanup)

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.listRepos as Mock).mockResolvedValue([{ id: 'marshal', url: 'https://github.com/a/marshal.git', branch: 'main', forge: 'github', credRef: 'gh', watch: true, watchLabel: 'agent' }])
  ;(api.listCredentials as Mock).mockResolvedValue([{ id: 'gh', kind: 'vault', set: true }])
  ;(api.registerRepo as Mock).mockResolvedValue({})
  ;(api.removeRepo as Mock).mockResolvedValue(undefined)
})

describe('Repos settings', () => {
  it('lists registered repos', async () => {
    render(Repos, { onToast: vi.fn() })
    const row = await screen.findByTestId('repo-row')
    expect(row.textContent).toContain('marshal')
    expect(row.textContent).toContain('github')
    expect(row.textContent).toContain('agent')
  })

  it('posts the registration form, leaving blank fields out', async () => {
    const onToast = vi.fn()
    render(Repos, { onToast })
    await screen.findByTestId('repo-row')
    await waitFor(() => expect((screen.getByLabelText('Credential') as HTMLSelectElement).options.length).toBe(2))
    await fireEvent.input(screen.getByLabelText('Repo id'), { target: { value: 'tea' } })
    await fireEvent.input(screen.getByLabelText('Repo URL'), { target: { value: 'https://gitea.example/o/r.git' } })
    await fireEvent.input(screen.getByLabelText('Branch'), { target: { value: 'trunk' } })
    await fireEvent.change(screen.getByLabelText('Forge'), { target: { value: 'gitea' } })
    await fireEvent.input(screen.getByLabelText('API base'), { target: { value: 'https://gitea.example/api/v1' } })
    await fireEvent.change(screen.getByLabelText('Credential'), { target: { value: 'gh' } })
    await fireEvent.click(screen.getByLabelText('Watch issues'))
    await fireEvent.input(screen.getByLabelText('Watch label'), { target: { value: 'bot' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Register repo' }))
    await waitFor(() =>
      expect(api.registerRepo).toHaveBeenCalledWith({ id: 'tea', url: 'https://gitea.example/o/r.git', branch: 'trunk', forge: 'gitea', apiBase: 'https://gitea.example/api/v1', credRef: 'gh', watch: true, watchLabel: 'bot' }),
    )
    expect(onToast).toHaveBeenCalledWith('Registered tea')
    // The form resets.
    expect((screen.getByLabelText('Repo id') as HTMLInputElement).value).toBe('')
  })

  it('sends just id, url and watch=false for a minimal repo', async () => {
    render(Repos, { onToast: vi.fn() })
    await screen.findByTestId('repo-row')
    await fireEvent.input(screen.getByLabelText('Repo id'), { target: { value: 'r' } })
    await fireEvent.input(screen.getByLabelText('Repo URL'), { target: { value: 'https://x/y.git' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Register repo' }))
    await waitFor(() => expect(api.registerRepo).toHaveBeenCalledWith({ id: 'r', url: 'https://x/y.git', watch: false }))
  })

  it("shows the bridge's reason when registration is refused", async () => {
    ;(api.registerRepo as Mock).mockRejectedValue(new api.APIError(400, { error: 'watch needs a forge' }))
    render(Repos, { onToast: vi.fn() })
    await screen.findByTestId('repo-row')
    await fireEvent.input(screen.getByLabelText('Repo id'), { target: { value: 'r' } })
    await fireEvent.input(screen.getByLabelText('Repo URL'), { target: { value: 'u' } })
    await fireEvent.click(screen.getByLabelText('Watch issues'))
    await fireEvent.click(screen.getByRole('button', { name: 'Register repo' }))
    expect((await screen.findByRole('alert')).textContent).toBe('watch needs a forge')
  })

  it('confirms before removing', async () => {
    render(Repos, { onToast: vi.fn() })
    const row = await screen.findByTestId('repo-row')
    await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))
    expect(api.removeRepo).not.toHaveBeenCalled()
    await fireEvent.click(within(row).getByRole('button', { name: 'Confirm remove' }))
    await waitFor(() => expect(api.removeRepo).toHaveBeenCalledWith('marshal'))
  })
})
