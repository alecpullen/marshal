import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, BudgetError, answerRun, errMessage, getCommitDraft, getRoster, getRun, listReviewComments, listRuns, postReviewComment, recentPrompts, resolveReviewComment, startRun, undoReroute, getGate, getLastRequest, getNode, getStack, getStepDiffs, listFiles, readFile, runGate, setToken, confirmPlugin, confirmSkill, createWatch, deleteMemory, discardPlugin, discardSkill, getBudgets, getModels, getUsage, listMemory, listPlugins, listSkills, listWatches, overrideBudget, previewSkill, probeProvider, removePlugin, removeSkill, scanPlugin, setBudgets, setMemoryConfidence, setPresets, setProviderKey, setProviders, setRouting, spawnAgent, stopWatch } from './api'

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

describe('runs API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  it('listRuns returns [] for a null body', async () => {
    vi.stubGlobal('fetch', reply(200, null))
    expect(await listRuns()).toEqual([])
  })

  it('listRuns turns the bridge timestamp into milliseconds', async () => {
    vi.stubGlobal('fetch', reply(200, [{ agentId: 'a', project: '/p', run: { kind: 'none' }, at: '2026-10-04T00:00:00Z', error: 'boom' }, { agentId: 'b', project: '/p', run: { kind: 'none' }, at: '0001-01-01T00:00:00Z' }]))
    const rows = await listRuns()
    expect(rows[0].at).toBe(Date.parse('2026-10-04T00:00:00Z'))
    expect(rows[0].error).toBe('boom')
    expect(typeof rows[1].at).toBe('number')
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

describe('control API (library, models, usage, budgets, watches)', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  const body = (f: ReturnType<typeof reply>, i = 0) => JSON.parse(f.mock.calls[i][1].body)

  it.each([
    ['listSkills', () => listSkills('project', '/w/a b'), 'GET', '/api/library/skills?scope=project&project=%2Fw%2Fa+b'],
    ['listSkills global', () => listSkills('global'), 'GET', '/api/library/skills?scope=global'],
    ['previewSkill', () => previewSkill('github.com/x/y'), 'POST', '/api/library/skills/preview'],
    ['confirmSkill', () => confirmSkill('tok', 'global'), 'POST', '/api/library/skills/confirm'],
    ['discardSkill', () => discardSkill('tok'), 'POST', '/api/library/skills/discard'],
    ['removeSkill', () => removeSkill('my skill', 'global'), 'DELETE', '/api/library/skills/my%20skill?scope=global'],
    ['listPlugins', () => listPlugins('global'), 'GET', '/api/library/plugins?scope=global'],
    ['scanPlugin', () => scanPlugin('github.com/x/p', 'v1'), 'POST', '/api/library/plugins/scan'],
    ['confirmPlugin', () => confirmPlugin('s', 'project', '/w/a'), 'POST', '/api/library/plugins/confirm'],
    ['discardPlugin', () => discardPlugin('s'), 'POST', '/api/library/plugins/discard'],
    ['removePlugin', () => removePlugin('p', 'project', '/w/a'), 'DELETE', '/api/library/plugins/p?scope=project&project=%2Fw%2Fa'],
    ['listMemory', () => listMemory('/w/a'), 'GET', '/api/library/memory?project=%2Fw%2Fa'],
    ['deleteMemory', () => deleteMemory(7, '/w/a'), 'DELETE', '/api/library/memory/7?project=%2Fw%2Fa'],
    ['setMemoryConfidence', () => setMemoryConfidence(7, '/w/a', 'stale'), 'POST', '/api/library/memory/7/confidence?project=%2Fw%2Fa'],
    ['getModels', () => getModels(), 'GET', '/api/models'],
    ['setProviders', () => setProviders({ x: null }), 'PUT', '/api/models/providers'],
    ['setProviderKey', () => setProviderKey('my p', 'k'), 'PUT', '/api/models/providers/my%20p/key'],
    ['setPresets', () => setPresets({ a: null }), 'PUT', '/api/models/presets'],
    ['setRouting', () => setRouting({ defaultProfile: 'd' }), 'PUT', '/api/models/routing'],
    ['probeProvider', () => probeProvider('p'), 'POST', '/api/models/probe'],
    ['getBudgets', () => getBudgets(), 'GET', '/api/budgets'],
    ['setBudgets', () => setBudgets({ dailyUsd: 1, perAgentUsd: 1, onDailyCap: 'warn', onAgentCap: 'warn' }), 'PUT', '/api/budgets'],
    ['overrideBudget', () => overrideBudget('a 1'), 'POST', '/api/agents/a%201/budget/override'],
    ['getUsage', () => getUsage('7d', 'model'), 'GET', '/api/usage?range=7d&by=model'],
    ['listWatches', () => listWatches(), 'GET', '/api/watches'],
    ['createWatch', () => createWatch({ spec: { name: 'w', kind: 'command', command: 'x', mode: 'once', intervalMs: 5000 } }), 'POST', '/api/watches'],
    ['stopWatch', () => stopWatch('studio', 'w 1'), 'DELETE', '/api/watches/studio/w%201'],
  ])('%s uses the route and method', async (_n, call, method, url) => {
    const f = reply(200, {})
    vi.stubGlobal('fetch', f)
    await call()
    expect(f.mock.calls[0][0]).toBe(url)
    expect(f.mock.calls[0][1].method).toBe(method)
  })

  it('stages a preview and scan on the scope that will own them', async () => {
    const f = reply(200, {})
    vi.stubGlobal('fetch', f)
    await previewSkill('src', 'project', '/w/a')
    expect(body(f)).toEqual({ source: 'src', scope: 'project', project: '/w/a' })
    await scanPlugin('src', 'v1', 'global')
    expect(body(f, 1)).toEqual({ source: 'src', scope: 'global', ref: 'v1' })
  })

  it('returns the report from a budget save', async () => {
    vi.stubGlobal('fetch', reply(200, { budgets: { dailyUsd: 7, perAgentUsd: 1, onDailyCap: 'warn', onAgentCap: 'warn' }, daily: { day: 'd', spentUsd: 0, blocked: false }, agents: [], loaded: true }))
    const r = await setBudgets({ dailyUsd: 1e9, perAgentUsd: 1, onDailyCap: 'warn', onAgentCap: 'warn' })
    expect(r.budgets.dailyUsd).toBe(7)
  })

  it('reads the bridge budget report', async () => {
    vi.stubGlobal('fetch', reply(200, { budgets: { dailyUsd: 5, perAgentUsd: 1, onDailyCap: 'block', onAgentCap: 'pause' }, daily: { day: 'd', spentUsd: 2, blocked: true }, agents: [{ agentId: 'a', spentUsd: 1, paused: true, overridden: false }], loaded: true }))
    const b = await getBudgets()
    expect(b.daily.blocked).toBe(true)
    expect(b.agents[0].paused).toBe(true)
    expect(b.loaded).toBe(true)
  })

  it('sends the install token, scope and project', async () => {
    const f = reply(200, {})
    vi.stubGlobal('fetch', f)
    await confirmSkill('tok', 'project', '/w/a')
    expect(body(f)).toEqual({ stagingToken: 'tok', scope: 'project', project: '/w/a' })
    await confirmPlugin('s1', 'global')
    expect(body(f, 1)).toEqual({ scanToken: 's1', scope: 'global' })
  })

  it('unwraps list results and defaults to empty', async () => {
    vi.stubGlobal('fetch', reply(200, { skills: [{ name: 'a' }] }))
    expect(await listSkills('global')).toEqual([{ name: 'a' }])
    vi.stubGlobal('fetch', reply(200, {}))
    expect(await listMemory('/w')).toEqual([])
    expect(await listPlugins('global')).toEqual([])
    vi.stubGlobal('fetch', reply(200, null))
    expect(await listWatches()).toEqual([])
  })

  it.each([
    ['listSkills', () => listSkills('project', '/w')],
    ['previewSkill', () => previewSkill('x')],
    ['listPlugins', () => listPlugins('project', '/w')],
    ['scanPlugin', () => scanPlugin('x')],
    ['listMemory', () => listMemory('/w')],
  ])('%s maps 501 to unsupported', async (_n, call) => {
    vi.stubGlobal('fetch', reply(501, { error: 'project_library_unsupported' }))
    expect(await call()).toBe('unsupported')
  })

  it('sends only the named provider and the key body', async () => {
    const f = reply(200, {})
    vi.stubGlobal('fetch', f)
    await setProviders({ groq: { type: 'openai_compatible', baseUrl: 'u' }, old: null })
    expect(body(f)).toEqual({ providers: { groq: { type: 'openai_compatible', baseUrl: 'u' }, old: null } })
    await setProviderKey('groq', 'secret')
    expect(body(f, 1)).toEqual({ key: 'secret' })
  })

  it('passes a spawn routing choice through', async () => {
    const f = reply(200, { agentId: 'a' })
    vi.stubGlobal('fetch', f)
    await spawnAgent({ project: '/w', routing: { profile: 'fast' } })
    expect(body(f).routing).toEqual({ profile: 'fast' })
  })

  it('turns a budget 429 into a BudgetError', async () => {
    vi.stubGlobal('fetch', reply(429, { error: 'budget_exceeded', scope: 'daily' }))
    await expect(spawnAgent({ project: '/w' })).rejects.toBeInstanceOf(BudgetError)
  })
})
