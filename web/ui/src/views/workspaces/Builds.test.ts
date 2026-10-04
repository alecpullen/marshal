import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Builds from './Builds.svelte'
import * as api from '../../lib/api.js'
import * as sse from '../../lib/sse'

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return { ...actual, listBuilds: vi.fn(), startBuild: vi.fn() }
})
vi.mock('../../lib/sse', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/sse')>()
  return { ...actual, connectBuildLog: vi.fn() }
})

afterEach(cleanup)

type Handlers = { onLine: (l: string, at?: number) => void; onDone: (s: string) => void }
let streams: { n: number; h: Handlers; stop: ReturnType<typeof vi.fn> }[] = []

beforeEach(() => {
  streams = []
  vi.clearAllMocks()
  vi.mocked(sse.connectBuildLog).mockImplementation(((_name: string, n: number, h: Handlers) => {
    const stop = vi.fn()
    streams.push({ n, h, stop })
    return stop
  }) as unknown as typeof sse.connectBuildLog)
  vi.mocked(api.listBuilds).mockResolvedValue({
    versions: [
      { n: 1, at: 0, buildStatus: 'ok', buildMs: 61000, sizeBytes: 300 * 1024 * 1024, imageTag: 'marshal-ws/svc:v1' },
      { n: 2, at: 0, buildStatus: 'building' },
    ],
  })
})

describe('Builds', () => {
  it('lists versions with status, duration, size and tag, newest first', async () => {
    render(Builds, { name: 'svc', onNavigate: vi.fn() })
    const rows = await screen.findAllByTestId('build-row')
    expect(rows.map((r) => r.querySelector('.font-mono')?.textContent)).toEqual(['v2', 'v1'])
    expect(rows[1].textContent).toContain('61 s')
    expect(rows[1].textContent).toContain('300 MB')
    expect(rows[1].textContent).toContain('marshal-ws/svc:v1')
  })

  it('opens the latest build log and appends lines as they arrive', async () => {
    render(Builds, { name: 'svc', onNavigate: vi.fn() })
    await waitFor(() => expect(streams.map((s) => s.n)).toEqual([2]))
    streams[0].h.onLine('cached l1')
    streams[0].h.onLine('step 2/4')
    await waitFor(() => expect(screen.getByTestId('build-log').textContent).toContain('step 2/4'))
    const lines = screen.getByTestId('build-log').querySelectorAll('span')
    expect(lines[0].hasAttribute('data-cached')).toBe(true)
    expect(lines[1].hasAttribute('data-cached')).toBe(false)
  })

  it('done updates the status', async () => {
    render(Builds, { name: 'svc', onNavigate: vi.fn() })
    await waitFor(() => expect(streams).toHaveLength(1))
    expect(screen.getAllByText('building').length).toBeGreaterThan(0)
    streams[0].h.onDone('ok')
    await waitFor(() => expect(screen.queryAllByText('building')).toHaveLength(0))
    expect(api.listBuilds).toHaveBeenCalledTimes(2)
  })

  it('selecting another version closes the stream and opens that build\'s replay', async () => {
    render(Builds, { name: 'svc', onNavigate: vi.fn() })
    await waitFor(() => expect(streams).toHaveLength(1))
    await fireEvent.click((await screen.findAllByTestId('build-row'))[1])
    await waitFor(() => expect(streams.map((s) => s.n)).toEqual([2, 1]))
    expect(streams[0].stop).toHaveBeenCalled()
  })

  it('a failure highlights the last 20 lines and offers Rebuild', async () => {
    vi.mocked(api.startBuild).mockResolvedValue({ version: 2 })
    render(Builds, { name: 'svc', onNavigate: vi.fn() })
    await waitFor(() => expect(streams).toHaveLength(1))
    for (let i = 1; i <= 25; i++) streams[0].h.onLine(`line ${i}`)
    streams[0].h.onDone('failed')
    const rebuild = await screen.findByText('Rebuild')
    const tail = screen.getByTestId('build-log').querySelectorAll('[data-tail]')
    expect(tail).toHaveLength(20)
    expect(tail[0].textContent).toBe('line 6')
    await fireEvent.click(rebuild)
    await waitFor(() => expect(api.startBuild).toHaveBeenCalledWith('svc', 2))
    // The same build reconnects.
    await waitFor(() => expect(streams.map((s) => s.n)).toEqual([2, 2]))
  })
})
