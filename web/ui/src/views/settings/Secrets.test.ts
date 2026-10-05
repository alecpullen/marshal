import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Secrets from './Secrets.svelte'
import * as api from '../../lib/api.js'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, getSecretsStatus: vi.fn(), listSecrets: vi.fn(), putSecret: vi.fn(), deleteSecret: vi.fn() }
})

afterEach(cleanup)

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getSecretsStatus as Mock).mockResolvedValue({ backend: 'local', healthy: true })
  ;(api.listSecrets as Mock).mockResolvedValue(['vault:git/github', 'vault:providers/openai'])
  ;(api.putSecret as Mock).mockResolvedValue(undefined)
  ;(api.deleteSecret as Mock).mockResolvedValue(undefined)
})

describe('Secrets settings', () => {
  it('shows the backend and its health, and refs without any value', async () => {
    render(Secrets, { onToast: vi.fn() })
    const rows = await screen.findAllByTestId('secret-row')
    expect(rows.map((r) => within(r).getByText(/^vault:/).textContent)).toEqual(['vault:git/github', 'vault:providers/openai'])
    const status = screen.getByTestId('secrets-status')
    expect(status.textContent).toContain('local')
    expect(status.textContent).toContain('healthy')
    expect(screen.getAllByText('value hidden')).toHaveLength(2)
  })

  it('sets a secret, normalising the ref, and never renders the value afterwards', async () => {
    const onToast = vi.fn()
    const { container } = render(Secrets, { onToast })
    await screen.findAllByTestId('secret-row')
    await fireEvent.input(screen.getByLabelText('Secret ref'), { target: { value: 'git/gitea' } })
    await fireEvent.input(screen.getByLabelText('Secret value'), { target: { value: 'hunter2-s3cret' } })
    await fireEvent.click(within(screen.getByRole('form', { name: 'Set a secret' })).getByRole('button', { name: 'Set' }))
    await waitFor(() => expect(api.putSecret).toHaveBeenCalledWith('vault:git/gitea', 'hunter2-s3cret'))
    await waitFor(() => expect((screen.getByLabelText('Secret value') as HTMLInputElement).value).toBe(''))
    expect(container.textContent).not.toContain('hunter2-s3cret')
    expect(container.innerHTML).not.toContain('hunter2-s3cret')
    expect(onToast).toHaveBeenCalledWith('Stored vault:git/gitea')
  })

  it('clears the value even when the save fails, and shows why', async () => {
    ;(api.putSecret as Mock).mockRejectedValue(new api.APIError(502, { error: 'bao unreachable' }))
    render(Secrets, { onToast: vi.fn() })
    await screen.findAllByTestId('secret-row')
    await fireEvent.input(screen.getByLabelText('Secret ref'), { target: { value: 'vault:x' } })
    await fireEvent.input(screen.getByLabelText('Secret value'), { target: { value: 'v' } })
    await fireEvent.click(within(screen.getByRole('form', { name: 'Set a secret' })).getByRole('button', { name: 'Set' }))
    expect((await screen.findByRole('alert')).textContent).toBe('bao unreachable')
    expect((screen.getByLabelText('Secret value') as HTMLInputElement).value).toBe('')
    expect((screen.getByLabelText('Secret ref') as HTMLInputElement).value).toBe('vault:x')
  })

  it('overwrites an existing ref from its row', async () => {
    render(Secrets, { onToast: vi.fn() })
    const rows = await screen.findAllByTestId('secret-row')
    await fireEvent.click(within(rows[0]).getByRole('button', { name: 'Set' }))
    await fireEvent.input(screen.getByLabelText('New value for vault:git/github'), { target: { value: 'new-one' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.putSecret).toHaveBeenCalledWith('vault:git/github', 'new-one'))
    expect(screen.queryByLabelText('New value for vault:git/github')).toBeNull()
  })

  it('confirms before deleting', async () => {
    render(Secrets, { onToast: vi.fn() })
    const rows = await screen.findAllByTestId('secret-row')
    await fireEvent.click(within(rows[1]).getByRole('button', { name: 'Delete' }))
    expect(api.deleteSecret).not.toHaveBeenCalled()
    await fireEvent.click(within(rows[1]).getByRole('button', { name: 'Confirm delete' }))
    await waitFor(() => expect(api.deleteSecret).toHaveBeenCalledWith('vault:providers/openai'))
  })

  it('filters by prefix', async () => {
    render(Secrets, { onToast: vi.fn() })
    await screen.findAllByTestId('secret-row')
    await fireEvent.input(screen.getByLabelText('Filter by prefix'), { target: { value: 'git/' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Filter' }))
    await waitFor(() => expect(api.listSecrets).toHaveBeenLastCalledWith('git/'))
  })

  it('disables Set on the env backend and says how to enable it', async () => {
    ;(api.getSecretsStatus as Mock).mockResolvedValue({ backend: 'env', healthy: true })
    render(Secrets, { onToast: vi.fn() })
    await screen.findAllByTestId('secret-row')
    expect((await screen.findByTestId('env-note')).textContent).toBe('Configure the local or OpenBao backend to store secrets')
    expect((within(screen.getByRole('form', { name: 'Set a secret' })).getByRole('button', { name: 'Set' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByLabelText('Secret value') as HTMLInputElement).disabled).toBe(true)
    for (const r of screen.getAllByTestId('secret-row')) expect((within(r).getByRole('button', { name: 'Set' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
