import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Schedules from './Schedules.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api')>()
  return { ...actual, listSchedules: vi.fn(), listRecipes: vi.fn(), saveSchedule: vi.fn(), deleteSchedule: vi.fn(), runSchedule: vi.fn() }
})

const projects = [{ root: '/work/alpha', available: true }] as api.ProjectStatus[]
const recipes = [{ name: 'update-deps', title: 'Update dependencies', kind: 'prompt', prompt: 'Update {{ecosystem}}', inputs: [{ name: 'ecosystem', label: 'Ecosystem', required: true }] }]
const sched = { id: 's1', name: 'Nightly deps', recipe: 'update-deps', project: '/work/alpha', cron: '0 9 * * 1-5', enabled: true, lastResult: 'succeeded' }

beforeEach(() => {
  ;(api.listSchedules as Mock).mockResolvedValue([sched])
  ;(api.listRecipes as Mock).mockResolvedValue(recipes)
  ;(api.saveSchedule as Mock).mockImplementation(async (s) => ({ id: 's2', ...s }))
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('Schedules', () => {
  it('lists schedules with a readable cron and last result', async () => {
    render(Schedules, { projects, onNavigate: vi.fn() })
    const row = await screen.findByTestId('schedule-row')
    expect(row.textContent).toContain('Nightly deps')
    expect(row.textContent).toContain('Weekdays at 09:00')
    expect(row.textContent).toContain('succeeded')
  })

  it('the enabled toggle saves the flipped state', async () => {
    render(Schedules, { projects, onNavigate: vi.fn() })
    await fireEvent.click(await screen.findByRole('switch', { name: 'Enabled: Nightly deps' }))
    await waitFor(() => expect(api.saveSchedule).toHaveBeenCalledWith(expect.objectContaining({ id: 's1', enabled: false })))
  })

  it('the form previews the next runs and posts the schedule', async () => {
    render(Schedules, { projects, onNavigate: vi.fn() })
    await screen.findByTestId('schedule-row')
    await fireEvent.click(screen.getByRole('button', { name: 'New schedule' }))
    expect(screen.getByTestId('cron-preview').textContent).toMatch(/Daily at 09:00\. Next: .+UTC · .+UTC · .+UTC/)
    await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Morning' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Weekdays at 09:00' }))
    await fireEvent.input(screen.getByLabelText(/Ecosystem/), { target: { value: 'go' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() =>
      expect(api.saveSchedule).toHaveBeenCalledWith({ name: 'Morning', recipe: 'update-deps', project: '/work/alpha', inputs: { ecosystem: 'go' }, cron: '0 9 * * 1-5', enabled: true }),
    )
  })

  it('rejects an invalid cron and missing required inputs', async () => {
    render(Schedules, { projects, onNavigate: vi.fn() })
    await screen.findByTestId('schedule-row')
    await fireEvent.click(screen.getByRole('button', { name: 'New schedule' }))
    await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'x' } })
    await fireEvent.input(screen.getByLabelText('Cron (UTC)'), { target: { value: 'nope' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    expect((await screen.findByRole('alert')).textContent).toContain('cron')
    expect(api.saveSchedule).not.toHaveBeenCalled()
  })

  it('Run now opens the agent it started', async () => {
    ;(api.runSchedule as Mock).mockResolvedValue({ agentId: 'a5' })
    const onNavigate = vi.fn()
    render(Schedules, { projects, onNavigate })
    await fireEvent.click(await screen.findByRole('button', { name: 'Run now' }))
    await waitFor(() => expect(onNavigate).toHaveBeenCalledWith('#chat/a5'))
  })
})
