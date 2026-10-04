import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getGate, getLastRequest, getNode, getStack, getStepDiffs, listFiles, readFile, runGate, setToken } from './api'

function reply(status: number, body?: unknown) {
  return vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    text: async () => (body === undefined ? '' : JSON.stringify(body)),
  })
}

describe('session and agent dock API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  const unsupported = (feature: string) => reply(501, { error: `${feature}_unsupported` })

  it('getStack passes the subagent', async () => {
    const f = reply(200, { rev: 1, roots: [], nodes: [] })
    vi.stubGlobal('fetch', f)
    await getStack('s 1', 3)
    expect(f.mock.calls[0][0]).toBe('/api/sessions/s%201/stack?subagent=3')
    await getStack('s 1')
    expect(f.mock.calls[1][0]).toBe('/api/sessions/s%201/stack')
  })

  it('getNode encodes the node id and maps 501', async () => {
    const f = reply(200, { node: {}, detail: {} })
    vi.stubGlobal('fetch', f)
    await getNode('s1', 'step:1/2', 4)
    expect(f.mock.calls[0][0]).toBe('/api/sessions/s1/nodes/step%3A1%2F2?subagent=4')
    expect(f.mock.calls[0][1].method).toBe('GET')
    vi.stubGlobal('fetch', unsupported('stack'))
    expect(await getNode('s1', 'x')).toBe('unsupported')
  })

  it.each([
    ['getLastRequest', () => getLastRequest('s1'), '/api/sessions/s1/last-request'],
    ['getStepDiffs', () => getStepDiffs('s1'), '/api/sessions/s1/step-diffs'],
    ['listFiles', () => listFiles('a1', 'src/x'), '/api/agents/a1/files?path=src%2Fx'],
    ['readFile', () => readFile('a1', 'src/x.go'), '/api/agents/a1/file?path=src%2Fx.go'],
  ])('%s hits the route and maps 501', async (_n, call, url) => {
    const f = reply(200, {})
    vi.stubGlobal('fetch', f)
    await call()
    expect(f.mock.calls[0][0]).toBe(url)
    expect(f.mock.calls[0][1].method).toBe('GET')
    vi.stubGlobal('fetch', unsupported('x'))
    expect(await call()).toBe('unsupported')
  })

  it('getGate resolves null on 204 and the record otherwise', async () => {
    vi.stubGlobal('fetch', reply(204))
    expect(await getGate('a1')).toBeNull()
    const rec = { result: { ok: true, skipped: false }, at: '2026-10-04T00:00:00Z' }
    vi.stubGlobal('fetch', reply(200, rec))
    expect(await getGate('a1')).toEqual(rec)
  })

  it('runGate posts to verify', async () => {
    const f = reply(200, { result: { ok: false, skipped: false }, at: 'x' })
    vi.stubGlobal('fetch', f)
    await runGate('a1')
    expect(f.mock.calls[0][0]).toBe('/api/agents/a1/verify')
    expect(f.mock.calls[0][1].method).toBe('POST')
  })
})
