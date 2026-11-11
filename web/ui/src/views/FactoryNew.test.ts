import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import FactoryNew from './FactoryNew.svelte'
import * as api from '../lib/api'
vi.mock('../lib/api', async (actual) => ({ ...await actual<typeof import('../lib/api')>(), listRepos: vi.fn(), listWorkspaces: vi.fn(), getModels: vi.fn(), createFactoryItem: vi.fn() }))
afterEach(cleanup)
it('retains form input after a network error', async () => {
  vi.mocked(api.listRepos).mockResolvedValue([{ id: 'repo', url: 'https://example.test' }])
  vi.mocked(api.listWorkspaces).mockResolvedValue([])
  vi.mocked(api.getModels).mockResolvedValue({ profiles: { chosen: {} } } as never)
  vi.mocked(api.createFactoryItem).mockRejectedValue(new Error('network down'))
  render(FactoryNew, { onNavigate: vi.fn() })
  await screen.findByRole('option', { name: 'repo' })
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Task' } })
  await fireEvent.input(screen.getByLabelText('Plan'), { target: { value: 'Build it' } })
  await fireEvent.change(screen.getByLabelText('Implementer model profile'), { target: { value: 'chosen' } })
  await fireEvent.change(screen.getByLabelText('Reviewer model profile'), { target: { value: 'chosen' } })
  await fireEvent.click(screen.getByLabelText('This repository has no required CI'))
  await fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
  await screen.findByText(/network down/)
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('Build it')
})
