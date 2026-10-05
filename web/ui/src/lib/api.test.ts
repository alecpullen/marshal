import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, BudgetError, answerRun, errMessage, getCommitDraft, getRoster, getRun, listReviewComments, listRuns, postReviewComment, recentPrompts, resolveReviewComment, startRun, undoReroute, getGate, getLastRequest, getNode, getStack, getStepDiffs, listFiles, readFile, runGate, setToken, confirmPlugin, confirmSkill, createWatch, deleteMemory, discardPlugin, discardSkill, getBudgets, getModels, getUsage, listMemory, listPlugins, listSkills, listWatches, overrideBudget, previewSkill, probeProvider, removePlugin, removeSkill, scanPlugin, setBudgets, setMemoryConfidence, setPresets, setProviderKey, setProviders, setRouting, spawnAgent, stopWatch, createWorkspace, diffWorkspace, getWorkspace, listBuilds, listWorkspaces, patchWorkspace, publishWorkspace, rotateWorkspaceCA, saveWorkspaceDraft, setWorkspacePool, startBuild, deleteWorkspace, getNetworkHosts, getNetworkRequests, getNetworkAgents, postNetworkDecision, getSecretsStatus, listSecrets, putSecret, deleteSecret, listCredentials, putCredential, deleteCredential, listRepos, registerRepo, removeRepo, getProjectSettings, putProjectSettings, getProjectHealth, getNetworkPending, openTerminal, openWorkspaceShell, terminalInput, terminalResize, terminalRelease, closeTerminal, terminalEventsUrl, openPreview, listRecipes, getRecipe, saveRecipe, deleteRecipe, copyRecipe, runRecipe, listSchedules, saveSchedule, deleteSchedule, runSchedule, getNotifications, saveNotifications, testNotifications, createStatusLink, listStatusLinks, revokeStatusLink, memorySuggestions, promoteMemory } from './api'

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

describe('workspaces API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  const call = async (fn: () => Promise<unknown>, body: unknown = {}) => {
    const f = reply(200, body)
    vi.stubGlobal('fetch', f)
    await fn()
    const [url, init] = f.mock.calls[0]
    return { url, method: init.method, body: init.body ? JSON.parse(init.body) : undefined }
  }

  it('maps each function to its route and body', async () => {
    expect(await call(() => listWorkspaces(), [])).toMatchObject({ url: '/api/workspaces', method: 'GET' })
    expect(await call(() => createWorkspace('svc', 'starter:go-service'))).toMatchObject({ url: '/api/workspaces', method: 'POST', body: { name: 'svc', from: 'starter:go-service' } })
    expect(await call(() => getWorkspace('svc'))).toMatchObject({ url: '/api/workspaces/svc', method: 'GET' })
    expect(await call(() => getWorkspace('svc', 3))).toMatchObject({ url: '/api/workspaces/svc?version=3' })
    expect(await call(() => saveWorkspaceDraft('svc', 'x'))).toMatchObject({ url: '/api/workspaces/svc/draft', method: 'PUT', body: { source: 'x' } })
    expect(await call(() => patchWorkspace('svc', 3, { apt: ['git'] }))).toMatchObject({ url: '/api/workspaces/svc/patch', method: 'POST', body: { layer: 3, value: { apt: ['git'] } } })
    expect(await call(() => publishWorkspace('svc'))).toMatchObject({ url: '/api/workspaces/svc/publish', method: 'POST' })
    expect(await call(() => deleteWorkspace('svc'))).toMatchObject({ url: '/api/workspaces/svc', method: 'DELETE' })
    expect(await call(() => setWorkspacePool('svc', 2))).toMatchObject({ url: '/api/workspaces/svc/pool', method: 'PUT', body: { size: 2 } })
    expect(await call(() => rotateWorkspaceCA('svc'))).toMatchObject({ url: '/api/workspaces/svc/ca/rotate', method: 'POST' })
    expect(await call(() => listBuilds('svc'), { versions: [] })).toMatchObject({ url: '/api/workspaces/svc/builds', method: 'GET' })
    expect(await call(() => startBuild('svc'))).toMatchObject({ url: '/api/workspaces/svc/builds', method: 'POST' })
    expect(await call(() => startBuild('svc', 2))).toMatchObject({ body: { version: 2 } })
    expect(await call(() => getSecretsStatus())).toMatchObject({ url: '/api/secrets/status' })
    expect(await call(() => getNetworkHosts({ workspace: 'svc' }), { rows: [] })).toMatchObject({ url: '/api/network?view=hosts&workspace=svc' })
  })

  it('diffWorkspace accepts plain text or {diff}', async () => {
    vi.stubGlobal('fetch', reply(200, { diff: '@@ -1 +1 @@' }))
    expect(await diffWorkspace('svc', 1, 2)).toBe('@@ -1 +1 @@')
    const f = reply(200, '@@ text')
    vi.stubGlobal('fetch', f)
    await diffWorkspace('svc', 1, 2)
    expect(f.mock.calls[0][0]).toBe('/api/workspaces/svc/diff?a=1&b=2')
  })

  it('spawnAgent sends the workspace reference', async () => {
    const r = await call(() => spawnAgent({ project: '/p', workspace: 'svc@2' }), { agentId: 'a' })
    expect(r.body).toMatchObject({ workspace: 'svc@2' })
  })
})

describe('network, secrets, repos and project settings API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  it.each([
    ['hosts', () => getNetworkHosts({ workspace: 'go dev', agent: 'a1' }), '/api/network?view=hosts&workspace=go+dev&agent=a1'],
    ['hosts unscoped', () => getNetworkHosts(), '/api/network?view=hosts'],
    ['requests', () => getNetworkRequests({ agent: 'a1' }), '/api/network?view=requests&agent=a1'],
    ['agents', () => getNetworkAgents({ workspace: 'w' }), '/api/network?view=agents&workspace=w'],
  ])('%s', async (_n, call, url) => {
    const f = reply(200, null)
    vi.stubGlobal('fetch', f)
    await call()
    expect(f.mock.calls[0][0]).toBe(url)
    expect(f.mock.calls[0][1].method).toBe('GET')
  })

  it('getNetworkHosts reads processMode and tolerates an empty body', async () => {
    vi.stubGlobal('fetch', reply(200, { processMode: true, rows: [{ host: 'a.com' }] }))
    expect(await getNetworkHosts()).toEqual({ processMode: true, rows: [{ host: 'a.com' }] })
    vi.stubGlobal('fetch', reply(200, null))
    expect(await getNetworkHosts()).toEqual({ processMode: false, rows: [] })
  })

  it('getNetworkPending unwraps the list and scopes by agent', async () => {
    const f = reply(200, { pending: [{ kind: 'network_block', sessionId: 'a1', agentId: 'a1', host: 'h', at: 1 }] })
    vi.stubGlobal('fetch', f)
    expect(await getNetworkPending('a 1')).toHaveLength(1)
    expect(f.mock.calls[0][0]).toBe('/api/network/pending?agent=a+1')
    vi.stubGlobal('fetch', reply(200, null))
    expect(await getNetworkPending()).toEqual([])
  })

  it('postNetworkDecision posts the body', async () => {
    const f = reply(200, { ok: true })
    vi.stubGlobal('fetch', f)
    await postNetworkDecision('a1', 'api.x.com', 'allow-agent')
    expect(f.mock.calls[0][0]).toBe('/api/network/decisions')
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ agentId: 'a1', host: 'api.x.com', decision: 'allow-agent' })
  })

  it('secrets: status, list, put and delete keep ref slashes', async () => {
    const f = reply(200, { refs: ['vault:git/github'] })
    vi.stubGlobal('fetch', f)
    expect(await listSecrets('git/')).toEqual(['vault:git/github'])
    expect(f.mock.calls[0][0]).toBe('/api/secrets?prefix=git%2F')
    await getSecretsStatus()
    expect(f.mock.calls[1][0]).toBe('/api/secrets/status')
    await putSecret('vault:git/github', 's3cret')
    expect(f.mock.calls[2][0]).toBe('/api/secrets/git/github')
    expect(f.mock.calls[2][1].method).toBe('PUT')
    expect(JSON.parse(f.mock.calls[2][1].body)).toEqual({ value: 's3cret' })
    await deleteSecret('providers/openai')
    expect(f.mock.calls[3][0]).toBe('/api/secrets/providers/openai')
    expect(f.mock.calls[3][1].method).toBe('DELETE')
  })

  it('credentials and repos', async () => {
    const f = reply(200, [])
    vi.stubGlobal('fetch', f)
    await listCredentials()
    await putCredential({ id: 'gh', kind: 'vault', ref: 'vault:git/github' })
    await deleteCredential('a/b')
    await listRepos()
    await registerRepo({ id: 'r', url: 'https://x/y.git', forge: 'github' })
    await removeRepo('r')
    const calls = f.mock.calls.map((c) => [c[1].method, c[0]])
    expect(calls).toEqual([
      ['GET', '/api/credentials'],
      ['POST', '/api/credentials'],
      ['DELETE', '/api/credentials/a%2Fb'],
      ['GET', '/api/repos'],
      ['POST', '/api/repos'],
      ['DELETE', '/api/repos/r'],
    ])
    expect(JSON.parse(f.mock.calls[1][1].body)).toEqual({ id: 'gh', kind: 'vault', ref: 'vault:git/github' })
  })

  it('project settings and health take the root as a query', async () => {
    const f = reply(200, {})
    vi.stubGlobal('fetch', f)
    expect((await getProjectSettings('/p q')).intake).toEqual({})
    expect(f.mock.calls[0][0]).toBe('/api/projects/settings?root=%2Fp+q')
    await putProjectSettings('/p', { mode: 'edit', intake: { labels: ['a'] } })
    expect(f.mock.calls[1][0]).toBe('/api/projects/settings?root=%2Fp')
    expect(f.mock.calls[1][1].method).toBe('PUT')
    expect(JSON.parse(f.mock.calls[1][1].body)).toEqual({ mode: 'edit', intake: { labels: ['a'] } })
    await getProjectHealth('/p')
    expect(f.mock.calls[2][0]).toBe('/api/projects/health?root=%2Fp')
  })
})

describe('W5 ops API', () => {
  beforeEach(() => setToken('t'))
  afterEach(() => vi.unstubAllGlobals())

  const call = async (fn: () => Promise<unknown>, body: unknown = {}) => {
    const f = reply(200, body)
    vi.stubGlobal('fetch', f)
    await fn()
    const [url, init] = f.mock.calls[0]
    return { url, method: init.method, body: init.body ? JSON.parse(init.body) : undefined }
  }

  it('terminals use the agent route and the workspace shell route', async () => {
    expect(await call(() => openTerminal('a1', { cols: 80, rows: 24 }), { terminalId: 't' })).toMatchObject({ url: '/api/agents/a1/terminal', method: 'POST', body: { cols: 80, rows: 24 } })
    expect(await call(() => openWorkspaceShell('svc', { cols: 80, rows: 24 }), { terminalId: 't' })).toMatchObject({ url: '/api/workspaces/svc/shell', method: 'POST' })
    expect(await call(() => terminalResize({ agentId: 'a1' }, 't1', { cols: 100, rows: 30 }))).toMatchObject({ url: '/api/agents/a1/terminal/t1/resize', body: { cols: 100, rows: 30 } })
    expect(await call(() => terminalResize({ workspace: 'svc' }, 't1', { cols: 1, rows: 2 }))).toMatchObject({ url: '/api/workspaces/svc/shell/t1/resize' })
    expect(await call(() => terminalRelease('a1', 't1'))).toMatchObject({ url: '/api/agents/a1/terminal/t1/release', method: 'POST' })
    expect(await call(() => closeTerminal({ agentId: 'a1' }, 't1'))).toMatchObject({ url: '/api/agents/a1/terminal/t1', method: 'DELETE' })
    expect(await call(() => closeTerminal({ workspace: 'svc' }, 't1'))).toMatchObject({ url: '/api/workspaces/svc/shell/t1', method: 'DELETE' })
    expect(terminalEventsUrl({ agentId: 'a1' }, 't1')).toBe('/api/agents/a1/terminal/t1/events')
    expect(terminalEventsUrl({ workspace: 'svc' }, 't1')).toBe('/api/workspaces/svc/shell/t1/events')
  })

  it('terminal input is base64 of the UTF-8 bytes', async () => {
    const r = await call(() => terminalInput({ agentId: 'a1' }, 't1', 'ls é\r'))
    expect(r).toMatchObject({ url: '/api/agents/a1/terminal/t1/input', method: 'POST' })
    expect(r.body.data).toBe(btoa(String.fromCharCode(...new TextEncoder().encode('ls é\r'))))
  })

  it('openPreview posts the port', async () => {
    expect(await call(() => openPreview('a1', 3000), { url: 'http://x' })).toMatchObject({ url: '/api/agents/a1/preview/3000', method: 'POST' })
  })

  it('recipes', async () => {
    expect(await call(() => listRecipes(), [])).toMatchObject({ url: '/api/recipes', method: 'GET' })
    expect(await call(() => getRecipe('fix bug'))).toMatchObject({ url: '/api/recipes/fix%20bug' })
    expect(await call(() => saveRecipe({ name: 'r', kind: 'prompt', prompt: 'p' }))).toMatchObject({ url: '/api/recipes/r', method: 'PUT', body: { name: 'r', prompt: 'p' } })
    expect(await call(() => deleteRecipe('r'))).toMatchObject({ url: '/api/recipes/r', method: 'DELETE' })
    expect(await call(() => copyRecipe('r', 'r2'))).toMatchObject({ url: '/api/recipes/r/copy', method: 'POST', body: { name: 'r2' } })
    expect(await call(() => runRecipe('r', { project: '/p', inputs: { a: '1' } }), { agentId: 'a' })).toMatchObject({ url: '/api/recipes/r/run', method: 'POST', body: { project: '/p', inputs: { a: '1' } } })
  })

  it('schedules create without an id and replace with one', async () => {
    const s = { name: 'n', recipe: 'r', cron: '0 9 * * *', enabled: true }
    expect(await call(() => listSchedules(), [])).toMatchObject({ url: '/api/schedules', method: 'GET' })
    expect(await call(() => saveSchedule(s))).toMatchObject({ url: '/api/schedules', method: 'POST', body: s })
    expect(await call(() => saveSchedule({ ...s, id: 'x1' }))).toMatchObject({ url: '/api/schedules/x1', method: 'PUT' })
    expect(await call(() => deleteSchedule('x1'))).toMatchObject({ url: '/api/schedules/x1', method: 'DELETE' })
    expect(await call(() => runSchedule('x1'))).toMatchObject({ url: '/api/schedules/x1/run', method: 'POST' })
  })

  it('notifications default to no webhooks', async () => {
    vi.stubGlobal('fetch', reply(200, null))
    expect(await getNotifications()).toEqual({ webhooks: [] })
    const w = { webhooks: [{ url: 'https://h', events: ['budget'] }] }
    expect(await call(() => saveNotifications(w), w)).toMatchObject({ url: '/api/notifications', method: 'PUT', body: w })
    expect(await call(() => testNotifications())).toMatchObject({ url: '/api/notifications/test', method: 'POST' })
  })

  it('status links', async () => {
    expect(await call(() => createStatusLink('a1', 24), { id: 'l', url: '/s/tok' })).toMatchObject({ url: '/api/status-links', method: 'POST', body: { agentId: 'a1', ttlHours: 24 } })
    expect(await call(() => listStatusLinks(), [])).toMatchObject({ url: '/api/status-links', method: 'GET' })
    expect(await call(() => revokeStatusLink('l'))).toMatchObject({ url: '/api/status-links/l', method: 'DELETE' })
  })

  it('memory scopes, suggestions and promotion', async () => {
    expect(await call(() => listMemory('/p', 'global'), { entries: [] })).toMatchObject({ url: '/api/library/memory?project=%2Fp&scope=global' })
    expect(await call(() => memorySuggestions('/p'), { suggestions: [] })).toMatchObject({ url: '/api/library/memory/suggestions?project=%2Fp', method: 'GET' })
    expect(await call(() => promoteMemory(7, '/p', 'global'))).toMatchObject({ url: '/api/library/memory/7/promote?project=%2Fp', method: 'POST', body: { scope: 'global' } })
    expect(await call(() => promoteMemory(7, '/p', 'workspace', 'svc'))).toMatchObject({ body: { scope: 'workspace', scopeKey: 'svc' } })
  })

})
