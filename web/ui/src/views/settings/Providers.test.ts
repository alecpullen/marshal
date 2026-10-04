import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Providers from './Providers.svelte'
import * as api from '../../lib/api.js'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, getModels: vi.fn(), setProviders: vi.fn(), setProviderKey: vi.fn(), probeProvider: vi.fn() }
})

afterEach(cleanup)

const models = () => ({
  providers: {
    groq: { type: 'openai_compatible', baseUrl: 'https://api.groq.com/openai/v1', hasKey: false, keySource: 'none' },
    local: { type: 'ollama', baseUrl: 'http://localhost:11434', hasKey: true, keySource: 'env', apiKeyEnv: 'OLLAMA_KEY' },
  },
})

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getModels as Mock).mockResolvedValue(models())
  ;(api.setProviders as Mock).mockResolvedValue(undefined)
  ;(api.setProviderKey as Mock).mockResolvedValue(undefined)
})

describe('Providers', () => {
  it('shows each provider with its key state', async () => {
    render(Providers, { onToast: vi.fn() })
    const cards = await screen.findAllByTestId('provider-card')
    expect(cards).toHaveLength(2)
    expect(within(cards[0]).getByText('no key')).toBeTruthy()
    expect(within(cards[1]).getByText('key from OLLAMA_KEY')).toBeTruthy()
  })

  it('probes a provider and shows the model count or the error', async () => {
    ;(api.probeProvider as Mock).mockResolvedValueOnce({ models: [{ id: 'a' }, { id: 'b' }] }).mockResolvedValueOnce({ models: [], error: 'connection refused' })
    render(Providers, { onToast: vi.fn() })
    const cards = await screen.findAllByTestId('provider-card')
    await fireEvent.click(within(cards[0]).getByText('Probe'))
    expect(await within(cards[0]).findByText('2 models')).toBeTruthy()
    expect(api.probeProvider).toHaveBeenCalledWith('groq')
    await fireEvent.click(within(cards[1]).getByText('Probe'))
    expect(await within(cards[1]).findByText('connection refused')).toBeTruthy()
  })

  it('adds a provider from a template, sending only that provider', async () => {
    render(Providers, { onToast: vi.fn() })
    await screen.findAllByTestId('provider-card')
    await fireEvent.click(screen.getByText('Add provider'))
    await fireEvent.change(screen.getByLabelText('Template'), { target: { value: 'anthropic' } })
    expect((screen.getByLabelText('Base URL') as HTMLInputElement).value).toBe('https://api.anthropic.com')
    expect((screen.getByLabelText('Provider name') as HTMLInputElement).value).toBe('anthropic')
    await fireEvent.click(screen.getByText('Save provider'))
    await waitFor(() =>
      expect(api.setProviders).toHaveBeenCalledWith({
        anthropic: { type: 'anthropic', baseUrl: 'https://api.anthropic.com', template: 'anthropic', toolCalling: true },
      }),
    )
  })

  it('suffixes a template name that is already taken', async () => {
    render(Providers, { onToast: vi.fn() })
    await screen.findAllByTestId('provider-card')
    await fireEvent.click(screen.getByText('Add provider'))
    await fireEvent.change(screen.getByLabelText('Template'), { target: { value: 'groq' } })
    expect((screen.getByLabelText('Provider name') as HTMLInputElement).value).toBe('groq-2')
  })

  it('clears the key from the page once it is saved', async () => {
    const { container } = render(Providers, { onToast: vi.fn() })
    const cards = await screen.findAllByTestId('provider-card')
    await fireEvent.click(within(cards[0]).getByText('Set key'))
    const input = screen.getByLabelText('API key for groq') as HTMLInputElement
    expect(input.type).toBe('password')
    await fireEvent.input(input, { target: { value: 'sk-secret-123' } })
    await fireEvent.click(screen.getByText('Save key'))
    await waitFor(() => expect(api.setProviderKey).toHaveBeenCalledWith('groq', 'sk-secret-123'))
    await waitFor(() => expect(screen.queryByLabelText('API key for groq')).toBeNull())
    expect(container.innerHTML).not.toContain('sk-secret-123')
    expect(document.body.innerHTML).not.toContain('sk-secret-123')
  })

  it('removes a provider after confirmation', async () => {
    render(Providers, { onToast: vi.fn() })
    const cards = await screen.findAllByTestId('provider-card')
    await fireEvent.click(within(cards[0]).getByText('Remove'))
    expect(api.setProviders).not.toHaveBeenCalled()
    await fireEvent.click(within(cards[0]).getByText('Confirm remove'))
    await waitFor(() => expect(api.setProviders).toHaveBeenCalledWith({ groq: null }))
  })
})
