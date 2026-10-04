import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, BudgetError, answerRun, errMessage, getRoster, getRun, listRuns, startRun, undoReroute, getCommitDraft, listReviewComments, postReviewComment, recentPrompts, resolveReviewComment, getGate, getLastRequest, getNode, getStack, getStepDiffs, listFiles, readFile, runGate, setToken } from './api'

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

describe('review and new-agent API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  it('getCommitDraft returns the message and maps 501', async () => {
    const f = reply(200, { message: 'fix: x' })
    vi.stubGlobal('fetch', f)
    expect(await getCommitDraft('a 1')).toBe('fix: x')
    expect(f.mock.calls[0][0]).toBe('/api/agents/a%201/commit-draft')
    vi.stubGlobal('fetch', reply(501, { error: 'commit_draft_unsupported' }))
    expect(await getCommitDraft('a1')).toBe('unsupported')
  })

  it('listReviewComments unwraps the comments', async () => {
    const f = reply(200, { comments: [{ id: 'c1' }] })
    vi.stubGlobal('fetch', f)
    expect(await listReviewComments('a1')).toEqual([{ id: 'c1' }])
    expect(f.mock.calls[0][0]).toBe('/api/agents/a1/review/comments')
    vi.stubGlobal('fetch', reply(200, {}))
    expect(await listReviewComments('a1')).toEqual([])
  })

  it('postReviewComment sends the body', async () => {
    const f = reply(200, { id: 'c1' })
    vi.stubGlobal('fetch', f)
    const c = { path: 'a.go', line: 3, side: 'new' as const, quote: 'x', body: 'why?' }
    await postReviewComment('a1', c)
    expect(f.mock.calls[0][0]).toBe('/api/agents/a1/review/comments')
    expect(f.mock.calls[0][1].method).toBe('POST')
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual(c)
  })

  it('resolveReviewComment posts to resolve', async () => {
    const f = reply(200, { status: 'resolved' })
    vi.stubGlobal('fetch', f)
    await resolveReviewComment('a1', 'c 1')
    expect(f.mock.calls[0][0]).toBe('/api/agents/a1/review/comments/c%201/resolve')
    expect(f.mock.calls[0][1].method).toBe('POST')
  })

  it('recentPrompts passes project and limit', async () => {
    const f = reply(200, { prompts: ['a', 'b'] })
    vi.stubGlobal('fetch', f)
    expect(await recentPrompts('/p q')).toEqual(['a', 'b'])
    expect(f.mock.calls[0][0]).toBe('/api/prompts/recent?project=%2Fp%20q&limit=20')
  })
})

describe('runs API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  it('listRuns returns [] for a null body', async () => {
    vi.stubGlobal('fetch', reply(200, null))
    expect(await listRuns()).toEqual([])
  })

  it('getRun encodes the id and maps 501 to unsupported', async () => {
    const f = reply(200, { kind: 'none' })
    vi.stubGlobal('fetch', f)
    await getRun('a/1')
    expect(f.mock.calls[0][0]).toBe('/api/runs/a%2F1')
    vi.stubGlobal('fetch', reply(501, { error: 'run_unsupported' }))
    expect(await getRun('a1')).toBe('unsupported')
  })

  it('startRun, answerRun, undoReroute and getRoster hit their routes', async () => {
    const f = reply(202, { agentId: 'a1' })
    vi.stubGlobal('fetch', f)
    await startRun({ project: '/p', kind: 'swarm', goal: 'g' })
    expect(f.mock.calls[0][0]).toBe('/api/runs')
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ project: '/p', kind: 'swarm', goal: 'g' })
    await answerRun('a1', 'yes')
    expect(f.mock.calls[1][0]).toBe('/api/runs/a1/answer')
    expect(JSON.parse(f.mock.calls[1][1].body)).toEqual({ answer: 'yes' })
    await undoReroute('r 1')
    expect(f.mock.calls[2][0]).toBe('/api/reroutes/r%201/undo')
    await getRoster('s1')
    expect(f.mock.calls[3][0]).toBe('/api/sessions/s1/roster')
  })

  it('maps a 429 budget_exceeded to BudgetError, but not other 429s', async () => {
    vi.stubGlobal('fetch', reply(429, { error: 'budget_exceeded', scope: 'agent' }))
    const err = await startRun({ kind: 'swarm', goal: 'g' }).catch((e) => e)
    expect(err).toBeInstanceOf(BudgetError)
    expect(err).toBeInstanceOf(APIError)
    expect(err.scope).toBe('agent')
    expect(errMessage(err)).toBe('Budget reached (agent)')
    vi.stubGlobal('fetch', reply(429, { error: 'slow down' }))
    const other = await startRun({ kind: 'swarm', goal: 'g' }).catch((e) => e)
    expect(other).not.toBeInstanceOf(BudgetError)
  })
})
