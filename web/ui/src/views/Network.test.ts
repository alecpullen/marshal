import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Network from './Network.svelte'
import * as api from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return { ...actual, getNetworkHosts: vi.fn(), getNetworkRequests: vi.fn(), getNetworkAgents: vi.fn() }
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

const NOW = Date.UTC(2026, 9, 4, 10, 0, 0)
const hostRow = (o: Partial<api.NetHostRow>): api.NetHostRow => ({ host: 'x.com', rule: 'allowlisted', requests: 1, blocked: 0, bytesUp: 0, bytesDown: 0, lastSeen: NOW, decision: 'allow', ...o })

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getNetworkHosts as Mock).mockResolvedValue({
    processMode: false,
    rows: [
      hostRow({ host: 'api.github.com', rule: 'injected', injected: true, requests: 40, bytesUp: 2048, bytesDown: 5 * 1024 * 1024 }),
      hostRow({ host: 'registry.npmjs.org', rule: 'allowlisted', requests: 120 }),
      hostRow({ host: 'evil.example', rule: 'blocked', requests: 3, blocked: 3, decision: 'block' }),
    ],
  })
  ;(api.getNetworkRequests as Mock).mockResolvedValue([
    { at: NOW - 2000, agentId: 'a1', host: 'api.github.com', port: 443, decision: 'allow', injected: true, bytesUp: 10, bytesDown: 20, durationMs: 120 },
    { at: NOW - 1000, agentId: 'a2', host: 'evil.example', port: 443, decision: 'block', bytesUp: 0, bytesDown: 0, durationMs: 2 },
  ])
  ;(api.getNetworkAgents as Mock).mockResolvedValue([{ agentId: 'a1', hosts: 4, requests: 50, blocked: 0, bytesUp: 1, bytesDown: 2, lastSeen: NOW }])
})

const hostNames = () => screen.getAllByTestId('host-row').map((r) => within(r).getAllByRole('cell')[0].textContent)

describe('Network inspector', () => {
  it('renders the hosts table with rule tags and the injected badge', async () => {
    render(Network, { workspace: 'go-dev', onNavigate: vi.fn() })
    expect(await screen.findByText('api.github.com')).toBeTruthy()
    expect(api.getNetworkHosts).toHaveBeenCalledWith({ workspace: 'go-dev', agent: undefined })
    expect(screen.getByText('proxy can read')).toBeTruthy()
    expect(screen.getAllByText('injected').length).toBe(1)
    expect(screen.getByText('blocked')).toBeTruthy()
    expect(screen.getByText('allowlisted')).toBeTruthy()
    expect(screen.getByText('2.0 KB / 5.0 MB')).toBeTruthy()
    expect(screen.queryByTestId('process-banner')).toBeNull()
  })

  it('sorts by any column, and again to reverse', async () => {
    render(Network, { onNavigate: vi.fn() })
    await screen.findByText('api.github.com')
    // Default: requests, descending.
    expect(hostNames()).toEqual(['registry.npmjs.org', 'api.github.com', 'evil.example'])
    await fireEvent.click(screen.getByRole('button', { name: /^Host\b/ }))
    expect(hostNames()).toEqual(['api.github.com', 'evil.example', 'registry.npmjs.org'])
    await fireEvent.click(screen.getByRole('button', { name: /^Host\b/ }))
    expect(hostNames()).toEqual(['registry.npmjs.org', 'evil.example', 'api.github.com'])
    await fireEvent.click(screen.getByRole('button', { name: /^Bytes/ }))
    expect(hostNames()[0]).toBe('api.github.com')
  })

  it('shows the not-isolated banner in process mode', async () => {
    ;(api.getNetworkHosts as Mock).mockResolvedValue({ processMode: true, rows: [] })
    render(Network, { onNavigate: vi.fn() })
    expect((await screen.findByTestId('process-banner')).textContent).toBe('Not isolated: agents run as processes, so the proxy is advisory')
  })

  it('lists requests newest first and filters on host', async () => {
    render(Network, { agent: 'a1', onNavigate: vi.fn() })
    await fireEvent.click(screen.getByRole('button', { name: 'Requests' }))
    await screen.findAllByTestId('request-row')
    expect(api.getNetworkRequests).toHaveBeenCalledWith({ workspace: undefined, agent: 'a1' })
    expect(screen.getAllByTestId('request-row').map((r) => within(r).getAllByRole('cell')[1].textContent)).toEqual(['a2', 'a1'])
    await fireEvent.input(screen.getByLabelText('Filter by host'), { target: { value: 'GITHUB' } })
    expect(screen.getAllByTestId('request-row')).toHaveLength(1)
    expect(screen.getByText('injected', { selector: 'span' })).toBeTruthy()
  })

  it('shows per-agent totals linking to the session, but not on an agent-scoped page', async () => {
    const { unmount } = render(Network, { workspace: 'w', onNavigate: vi.fn() })
    await fireEvent.click(screen.getByRole('button', { name: 'By agent' }))
    const link = await screen.findByRole('link', { name: 'a1' })
    expect(link.getAttribute('href')).toBe('#chat/a1')
    expect(api.getNetworkAgents).toHaveBeenCalledWith({ workspace: 'w' })
    unmount()
    render(Network, { agent: 'a1', onNavigate: vi.fn() })
    expect(screen.queryByRole('button', { name: 'By agent' })).toBeNull()
  })

  it('refreshes every 10 seconds while open', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    render(Network, { onNavigate: vi.fn() })
    await waitFor(() => expect(api.getNetworkHosts).toHaveBeenCalledTimes(1))
    await vi.advanceTimersByTimeAsync(10_000)
    expect(api.getNetworkHosts).toHaveBeenCalledTimes(2)
  })

  it('shows a load failure', async () => {
    ;(api.getNetworkHosts as Mock).mockRejectedValue(new api.APIError(502, { error: 'bridge down' }))
    render(Network, { onNavigate: vi.fn() })
    expect((await screen.findByRole('alert')).textContent).toBe('bridge down')
  })
})
