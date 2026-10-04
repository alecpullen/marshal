import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Models from './Models.svelte'
import * as api from '../../lib/api.js'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, getModels: vi.fn(), getBudgets: vi.fn(), setPresets: vi.fn(), setRouting: vi.fn(), setBudgets: vi.fn() }
})

afterEach(cleanup)

const cfg = {
  providers: { groq: { type: 'openai_compatible', baseUrl: 'u', hasKey: true, keySource: 'env' } },
  presets: { big: { provider: 'groq', model: 'big-1', contextWindow: 128000 }, small: { provider: 'groq', model: 'small-1' } },
  profiles: {
    balanced: { implementer: { preset: 'big' } },
    cheap: { implementer: { preset: 'small' } },
  },
  customAgents: [],
  defaultProfile: 'balanced',
  activePreset: '',
  roles: ['implementer', 'reviewer'],
  budgets: { dailyUsd: 25, perAgentUsd: 5, onDailyCap: 'block', onAgentCap: 'pause' },
}

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getModels as Mock).mockResolvedValue(cfg)
  ;(api.getBudgets as Mock).mockResolvedValue({ budgets: cfg.budgets, spentTodayUsd: 3.5 })
  ;(api.setPresets as Mock).mockResolvedValue(undefined)
  ;(api.setRouting as Mock).mockResolvedValue(undefined)
  ;(api.setBudgets as Mock).mockResolvedValue(undefined)
})

describe('Models', () => {
  it('sends the whole profiles map with the changed cell', async () => {
    const onToast = vi.fn()
    render(Models, { onToast })
    const select = await screen.findByLabelText('reviewer preset')
    await fireEvent.change(select, { target: { value: 'small' } })
    await waitFor(() =>
      expect(api.setRouting).toHaveBeenCalledWith({
        profiles: {
          balanced: { implementer: { preset: 'big' }, reviewer: { preset: 'small' } },
          cheap: { implementer: { preset: 'small' } },
        },
      }),
    )
    await waitFor(() => expect(onToast).toHaveBeenCalledWith(expect.stringContaining('Applies to agents started from now')))
  })

  it('edits the other profile after switching, and sets the default', async () => {
    render(Models, { onToast: vi.fn() })
    await screen.findByLabelText('reviewer preset')
    await fireEvent.click(screen.getByText('cheap'))
    await fireEvent.click(screen.getByText('Set default'))
    await waitFor(() => expect(api.setRouting).toHaveBeenCalledWith({ defaultProfile: 'cheap' }))
  })

  it('saves an edited preset by name only', async () => {
    render(Models, { onToast: vi.fn() })
    const model = await screen.findByLabelText('Model for small')
    const row = model.closest('tr')!
    expect((within(row).getByText('Save') as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.input(model, { target: { value: 'small-2' } })
    await fireEvent.click(within(row).getByText('Save'))
    await waitFor(() => expect(api.setPresets).toHaveBeenCalledWith({ small: { provider: 'groq', model: 'small-2' } }))
  })

  it('shows today’s spend and saves budgets', async () => {
    const onToast = vi.fn()
    render(Models, { onToast })
    expect((await screen.findByTestId('budget-spend')).textContent).toContain('Spent today $3.50 of $25.00')
    await fireEvent.input(screen.getByLabelText('Daily cap'), { target: { value: '40' } })
    await fireEvent.change(screen.getByLabelText('Agent cap action'), { target: { value: 'warn' } })
    await fireEvent.click(screen.getByText('Save budgets'))
    await waitFor(() => expect(api.setBudgets).toHaveBeenCalledWith({ dailyUsd: 40, perAgentUsd: 5, onDailyCap: 'block', onAgentCap: 'warn' }))
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('Saved budgets. Applies to agents started from now'))
  })

  it('shows a failed save without losing the page', async () => {
    ;(api.setBudgets as Mock).mockRejectedValue(new api.APIError(400, { error: 'budget caps must not be negative' }))
    render(Models, { onToast: vi.fn() })
    await screen.findByTestId('budget-spend')
    await fireEvent.click(screen.getByText('Save budgets'))
    expect((await screen.findByRole('alert')).textContent).toContain('negative')
  })
})
