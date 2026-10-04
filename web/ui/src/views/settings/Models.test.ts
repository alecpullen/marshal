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
  ;(api.getBudgets as Mock).mockResolvedValue({ budgets: cfg.budgets, daily: { day: '2026-10-04', spentUsd: 3.5, blocked: false }, agents: [], loaded: true })
  ;(api.setPresets as Mock).mockResolvedValue(undefined)
  ;(api.setRouting as Mock).mockResolvedValue(undefined)
  ;(api.setBudgets as Mock).mockImplementation(async (b: unknown) => ({ budgets: b, daily: { day: 'd', spentUsd: 3.5, blocked: false }, agents: [], loaded: true }))
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

  it('disables Save and explains when the bridge has not loaded the caps', async () => {
    ;(api.getBudgets as Mock).mockResolvedValue({ budgets: { dailyUsd: 0, perAgentUsd: 0, onDailyCap: 'warn', onAgentCap: 'warn' }, daily: { day: '', spentUsd: 0, blocked: false }, agents: [], loaded: false })
    render(Models, { onToast: vi.fn() })
    expect(await screen.findByTestId('budgets-unavailable')).toBeTruthy()
    expect((screen.getByText('Save budgets') as HTMLButtonElement).disabled).toBe(true)
    // The form shows the stored caps from config/get, never zeroes.
    await waitFor(() => expect((screen.getByLabelText('Daily cap') as HTMLInputElement).value).toBe('25'))
    expect(api.setBudgets).not.toHaveBeenCalled()
  })

  it('adopts the values the bridge stored after a save', async () => {
    ;(api.setBudgets as Mock).mockResolvedValue({ budgets: { dailyUsd: 10, perAgentUsd: 5, onDailyCap: 'block', onAgentCap: 'pause' }, daily: { day: 'd', spentUsd: 3.5, blocked: false }, agents: [], loaded: true })
    ;(api.getBudgets as Mock).mockResolvedValueOnce({ budgets: cfg.budgets, daily: { day: 'd', spentUsd: 3.5, blocked: false }, agents: [], loaded: true })
    render(Models, { onToast: vi.fn() })
    await screen.findByTestId('budget-spend')
    ;(api.getBudgets as Mock).mockResolvedValue({ budgets: { dailyUsd: 10, perAgentUsd: 5, onDailyCap: 'block', onAgentCap: 'pause' }, daily: { day: 'd', spentUsd: 3.5, blocked: false }, agents: [], loaded: true })
    await fireEvent.input(screen.getByLabelText('Daily cap'), { target: { value: '1000000' } })
    await fireEvent.click(screen.getByText('Save budgets'))
    await waitFor(() => expect((screen.getByLabelText('Daily cap') as HTMLInputElement).value).toBe('10'))
  })

  it('refetches spend on a budget delta without resetting the form', async () => {
    const { rerender } = render(Models, { onToast: vi.fn(), budgetTick: 0 })
    await screen.findByTestId('budget-spend')
    await fireEvent.input(screen.getByLabelText('Daily cap'), { target: { value: '99' } })
    ;(api.getBudgets as Mock).mockResolvedValue({ budgets: cfg.budgets, daily: { day: 'd', spentUsd: 20, blocked: true }, agents: [], loaded: true })
    await rerender({ onToast: vi.fn(), budgetTick: 1 })
    await waitFor(() => expect(screen.getByTestId('budget-spend').textContent).toContain('$20.00'))
    expect((screen.getByLabelText('Daily cap') as HTMLInputElement).value).toBe('99')
  })

  it('shows a failed save without losing the page', async () => {
    ;(api.setBudgets as Mock).mockRejectedValue(new api.APIError(400, { error: 'budget caps must not be negative' }))
    render(Models, { onToast: vi.fn() })
    await screen.findByTestId('budget-spend')
    await fireEvent.click(screen.getByText('Save budgets'))
    expect((await screen.findByRole('alert')).textContent).toContain('negative')
  })
})
