import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Watches from './Watches.svelte'
import * as api from '../lib/api.js'
import type { AgentRow } from '../lib/fleet'
import type { WatchInfo } from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return { ...actual, listWatches: vi.fn(), createWatch: vi.fn(), stopWatch: vi.fn(), getModels: vi.fn() }
})

afterEach(cleanup)

const agent = (id: string, name: string) => ({ id, name, project: '/w', status: 'running', updatedAt: '', mode: 'edit', activity: '', contextPct: 0, changedFiles: 0, interrupted: false }) as AgentRow

const now = Date.now()
const samples = (vals: [number, boolean][]) => vals.map(([value, tripped], i) => ({ at: now - (vals.length - i) * 600_000, value, tripped }))

const studio: WatchInfo = {
  agentId: 'studio',
  id: 'w1',
  name: 'cost-spike',
  kind: 'command',
  state: 'watching',
  condition: 'json spend > 5',
  mode: 'repeat',
  intervalMs: 60000,
  fireCount: 0,
  lastSample: '{"spend":3}',
  createdAt: now,
  samples: samples([[1, false], [3, false], [6, true]]),
  onTrip: { reroute: { role: 'reviewer', preset: 'small' } },
}
const owned: WatchInfo = {
  agentId: 'a1',
  id: 'w2',
  name: 'build-done',
  kind: 'job',
  state: 'fired',
  condition: 'exit_code 0',
  mode: 'once',
  intervalMs: 5000,
  fireCount: 1,
  lastSample: 'ok',
  createdAt: now,
  samples: samples([[1, true]]),
}

const cfg = { roles: ['implementer', 'reviewer'], presets: { big: {}, small: {} } }

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.listWatches as Mock).mockResolvedValue([studio, owned])
  ;(api.createWatch as Mock).mockResolvedValue({ id: 'new' })
  ;(api.stopWatch as Mock).mockResolvedValue(undefined)
  ;(api.getModels as Mock).mockResolvedValue(cfg)
})

const mount = (extra: Record<string, unknown> = {}) => render(Watches, { agents: [agent('a1', 'Alpha')], onNavigate: vi.fn(), ...extra })

describe('Watches table', () => {
  it('shows owner, kind, condition, trip action and state per watch', async () => {
    mount()
    const rows = await screen.findAllByTestId('watch-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('Studio')).toBeTruthy()
    expect(within(rows[0]).getByText('reroute reviewer → small')).toBeTruthy()
    expect(within(rows[0]).getByText('json spend > 5')).toBeTruthy()
    expect(within(rows[0]).getByRole('img', { name: 'Last 24 hours' })).toBeTruthy()
    expect(within(rows[1]).getByText('Alpha')).toBeTruthy()
    expect(within(rows[1]).getByText('notify')).toBeTruthy()
    expect(within(rows[1]).getByText('fired')).toBeTruthy()
  })

  it('refetches when a watch delta arrives', async () => {
    const { rerender } = mount({ tick: 0 })
    await screen.findAllByTestId('watch-row')
    expect(api.listWatches).toHaveBeenCalledTimes(1)
    await rerender({ agents: [], tick: 1, onNavigate: vi.fn() })
    await waitFor(() => expect(api.listWatches).toHaveBeenCalledTimes(2))
  })
})

describe('Watch detail', () => {
  it('opens on click with a threshold line, trip marks and the last sample', async () => {
    mount()
    await fireEvent.click((await screen.findAllByTestId('watch-row'))[0])
    const d = await screen.findByTestId('watch-detail')
    expect(within(d).getByTestId('threshold-line')).toBeTruthy()
    expect(within(d).getAllByTestId('trip-mark')).toHaveLength(1)
    expect(within(d).getByTestId('last-sample').textContent).toBe('{"spend":3}')
  })

  it('links an agent-owned watch to its agent', async () => {
    const onNavigate = vi.fn()
    mount({ onNavigate })
    await fireEvent.click((await screen.findAllByTestId('watch-row'))[1])
    const d = await screen.findByTestId('watch-detail')
    await fireEvent.click(within(d).getByText('Alpha'))
    expect(onNavigate).toHaveBeenCalledWith('#chat/a1')
  })

  it('stops a watch on its owner', async () => {
    mount()
    await fireEvent.click((await screen.findAllByTestId('watch-row'))[0])
    const d = await screen.findByTestId('watch-detail')
    await fireEvent.click(within(d).getByText('Stop'))
    await waitFor(() => expect(api.stopWatch).toHaveBeenCalledWith('studio', 'w1'))
    await waitFor(() => expect(screen.queryByTestId('watch-detail')).toBeNull())
  })
})

describe('New watch form', () => {
  const open = async () => {
    mount()
    await screen.findAllByTestId('watch-row')
    await fireEvent.click(screen.getByText('New watch'))
    await screen.findByTestId('watch-form')
  }
  const fill = async () => {
    await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'disk-full' } })
    await fireEvent.input(screen.getByLabelText('Command'), { target: { value: 'df --output=pcent /' } })
  }

  it('posts a Studio watch with a reroute rule', async () => {
    await open()
    await fill()
    await fireEvent.input(screen.getByLabelText('Condition'), { target: { value: 'json pct > 90' } })
    await fireEvent.input(screen.getByLabelText('Interval'), { target: { value: '10' } })
    await fireEvent.click(screen.getByLabelText('Reroute a role when this trips'))
    await fireEvent.change(await screen.findByLabelText('Role'), { target: { value: 'reviewer' } })
    await fireEvent.change(screen.getByLabelText('Preset'), { target: { value: 'small' } })
    await fireEvent.click(screen.getByText('Start watch'))
    await waitFor(() =>
      expect(api.createWatch).toHaveBeenCalledWith({
        spec: { name: 'disk-full', kind: 'command', command: 'df --output=pcent /', condition: 'json pct > 90', mode: 'once', intervalMs: 10000, notify: true, resume: false },
        onTrip: { reroute: { role: 'reviewer', preset: 'small' } },
      }),
    )
  })

  it('offers reroute to Studio only, and sends the agent id without onTrip otherwise', async () => {
    await open()
    expect(screen.getByTestId('reroute-fields')).toBeTruthy()
    await fireEvent.click(screen.getByLabelText('Reroute a role when this trips'))
    await fireEvent.change(screen.getByLabelText('Owner'), { target: { value: 'a1' } })
    expect(screen.queryByTestId('reroute-fields')).toBeNull()
    await fill()
    await fireEvent.click(screen.getByText('Start watch'))
    await waitFor(() => expect(api.createWatch).toHaveBeenCalled())
    const req = (api.createWatch as Mock).mock.calls[0][0]
    expect(req.agentId).toBe('a1')
    expect(req.onTrip).toBeUndefined()
  })

  it('needs a role and preset once reroute is on, and an interval of at least 2s', async () => {
    await open()
    await fill()
    const submit = screen.getByText('Start watch') as HTMLButtonElement
    expect(submit.disabled).toBe(false)
    await fireEvent.click(screen.getByLabelText('Reroute a role when this trips'))
    expect(submit.disabled).toBe(true)
    await fireEvent.click(screen.getByLabelText('Reroute a role when this trips'))
    await fireEvent.input(screen.getByLabelText('Interval'), { target: { value: '1' } })
    expect(submit.disabled).toBe(true)
  })

  it('shows the agent’s limit error inline', async () => {
    ;(api.createWatch as Mock).mockRejectedValue(new api.APIError(400, { error: 'watch limit reached (8)' }))
    await open()
    await fill()
    await fireEvent.click(screen.getByText('Start watch'))
    expect((await screen.findByRole('alert')).textContent).toBe('watch limit reached (8)')
    expect(screen.getByTestId('watch-form')).toBeTruthy()
  })
})
