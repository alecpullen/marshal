import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import Factory from './Factory.svelte'
import * as api from '../lib/api'
vi.mock('../lib/api', async (actual) => ({ ...await actual<typeof import('../lib/api')>(), listFactoryItems: vi.fn() }))
afterEach(cleanup)
it('shows an empty factory distinctly from unavailable data', async () => {
  vi.mocked(api.listFactoryItems).mockResolvedValueOnce([])
  render(Factory, { onNavigate: vi.fn() })
  expect(await screen.findByText('No factory work items yet.')).toBeTruthy()
})
