import { describe, expect, it, vi } from 'vitest'
import { get } from 'svelte/store'
import { applyDeltaTo, createFleetStore, describePending, sortAttentionFirst, toPendingPermission, toPendingQuestion, toRow, type AgentRow } from './fleet'
import { setToken, type AgentStatus } from './api'

const row = (x: Partial<AgentRow>): AgentRow => ({
  id: 'x',
  project: '/p',
  status: 'idle',
  updatedAt: '',
  name: 'n',
  mode: '',
  access: null,
  activity: '',
  contextPct: 0,
  changedFiles: 0,
  interrupted: false,
  ...x,
})

describe('sortAttentionFirst', () => {
  it('puts agents needing a human first', () => {
    const rows = [row({ id: 'idle' }), row({ id: 'a', status: 'awaiting-approval' })]
    expect(sortAttentionFirst(rows)[0].id).toBe('a')
  })

  it('ranks question alongside approval, ahead of error', () => {
    const rows = [row({ id: 'e', status: 'error' }), row({ id: 'q', status: 'awaiting-question' })]
    expect(sortAttentionFirst(rows).map((r) => r.id)).toEqual(['q', 'e'])
  })

  it('keeps running ahead of idle', () => {
    const rows = [row({ id: 'i', status: 'idle' }), row({ id: 'r', status: 'running' })]
    expect(sortAttentionFirst(rows).map((r) => r.id)).toEqual(['r', 'i'])
  })

  it('breaks ties by id so ordering is stable across refreshes', () => {
    const rows = [row({ id: 'b' }), row({ id: 'a' })]
    expect(sortAttentionFirst(rows).map((r) => r.id)).toEqual(['a', 'b'])
  })

  it('does not mutate its input', () => {
    const rows = [row({ id: 'idle' }), row({ id: 'a', status: 'awaiting-approval' })]
    sortAttentionFirst(rows)
    expect(rows[0].id).toBe('idle')
  })
})

describe('applyDeltaTo', () => {
  it('applies an activity delta to the addressed agent only', () => {
    const rows = [row({ id: 'a' }), row({ id: 'b' })]
    const got = applyDeltaTo(rows, { kind: 'activity', sessionId: 'a', activity: 'file.read' })
    expect(got.find((r) => r.id === 'a')?.activity).toBe('file.read')
    expect(got.find((r) => r.id === 'b')?.activity).toBe('')
  })

  it('applies telemetry fields', () => {
    const got = applyDeltaTo([row({ id: 'a' })], { kind: 'telemetry', sessionId: 'a', contextPct: 61, changedFiles: 4 })
    expect(got[0].contextPct).toBe(61)
    expect(got[0].changedFiles).toBe(4)
  })

  it('applies a full access report and keeps explicit revocations', () => {
    const got = applyDeltaTo([row({ id: 'a' })], {
      kind: 'mode',
      sessionId: 'a',
      access: { mode: 'act', auto: false, system_access: false, access_revision: 2 },
    })
    expect(got[0].mode).toBe('act')
    expect(got[0].access).toEqual({ mode: 'act', auto: false, system: false, revision: 2 })
  })

  it('ignores a stale or malformed mode report', () => {
    const current = row({ id: 'a', mode: 'act', access: { mode: 'act', auto: false, system: false, revision: 4 } })
    const stale = applyDeltaTo([current], {
      kind: 'mode',
      sessionId: 'a',
      access: { mode: 'act', auto: true, system: true, access_revision: 2 },
    })
    expect(stale[0]).toBe(current)
    const malformed = applyDeltaTo([current], {
      kind: 'mode',
      sessionId: 'a',
      access: { mode: 'auto', auto: true, system_access: true, access_revision: 9 },
    })
    expect(malformed[0]).toBe(current)
  })

  it('stores a gate record on the row', () => {
    const gate = { result: { ok: false, skipped: false, failedCommand: 'go test' }, at: '2026-10-04T00:00:00Z' }
    const got = applyDeltaTo([row({ id: 'a' }), row({ id: 'b' })], { kind: 'gate', sessionId: 'a', gate })
    expect(got[0].gate).toEqual(gate)
    expect(got[1].gate).toBeUndefined()
  })

  it('returns the original array when no agent matches', () => {
    const rows = [row({ id: 'a' })]
    expect(applyDeltaTo(rows, { kind: 'activity', sessionId: 'zzz', activity: 'x' })).toBe(rows)
  })

  it('drops every agent of a removed project', () => {
    const rows = [row({ id: 'a', project: '/gone' }), row({ id: 'b', project: '/stays' })]
    const got = applyDeltaTo(rows, { kind: 'project_removed', project: '/gone' })
    expect(got.map((r) => r.id)).toEqual(['b'])
  })

  // A pending delta carries only the kind; the payload needed to render a
  // decision comes from a snapshot refetch. The status still flips at once
  // so the card moves to the top without waiting for the round trip.
  it('flips status on a pending approval delta', () => {
    const got = applyDeltaTo([row({ id: 'a', status: 'running' })], {
      kind: 'pending',
      sessionId: 'a',
      pendingKind: 'approval',
    })
    expect(got[0].status).toBe('awaiting-approval')
  })

  it('flips status on a pending question delta', () => {
    const got = applyDeltaTo([row({ id: 'a', status: 'running' })], {
      kind: 'pending',
      sessionId: 'a',
      pendingKind: 'question',
    })
    expect(got[0].status).toBe('awaiting-question')
  })
})

describe('toRow', () => {
  it('fills optional fields with defaults', () => {
    const got = toRow({ id: 'a', project: '/p', status: 'idle', updatedAt: '' } as AgentStatus)
    expect(got).toMatchObject({ name: '', mode: '', activity: '', contextPct: 0, changedFiles: 0, interrupted: false })
  })

  it('carries the pending payload through', () => {
    const got = toRow({
      id: 'a',
      project: '/p',
      status: 'awaiting-approval',
      updatedAt: '',
      pending: { kind: 'approval', id: 'tc1', params: { toolName: 'shell.run' } },
    } as AgentStatus)
    expect(got.pending?.id).toBe('tc1')
  })
})

describe('describePending', () => {
  it('prefers the command for a shell approval', () => {
    expect(
      describePending({ kind: 'approval', id: 'tc1', params: { toolName: 'shell.run', command: 'rm -rf build' } }),
    ).toBe('rm -rf build')
  })

  it('falls back to the tool name when there is no command', () => {
    expect(describePending({ kind: 'approval', id: 'tc1', params: { toolName: 'file.write' } })).toBe('file.write')
  })

  it('uses the first question text for a question', () => {
    expect(
      describePending({
        kind: 'question',
        id: 'q1',
        params: { questions: [{ question: 'Proceed with the migration?' }] },
      }),
    ).toBe('Proceed with the migration?')
  })

  it('degrades to a generic label when params are missing', () => {
    expect(describePending({ kind: 'approval', id: 'tc1' })).toBe('Approval required')
    expect(describePending({ kind: 'question', id: 'q1' })).toBe('Answer required')
  })

  it('never throws on an unexpected params shape', () => {
    expect(describePending({ kind: 'approval', id: 'tc1', params: { questions: 'not-an-array' } })).toBe(
      'Approval required',
    )
  })
})

describe('toPendingPermission', () => {
  it('maps the raw approval params onto the modal shape', () => {
    const got = toPendingPermission({
      kind: 'approval',
      id: 'tc1',
      params: { toolName: 'shell.run', command: 'ls -la', diff: '--- a\n+++ b' },
    })
    expect(got).toEqual({ toolCallId: 'tc1', toolName: 'shell.run', command: 'ls -la', diff: '--- a\n+++ b', exactOnly: true })
  })

  it('omits absent optional fields rather than passing empty strings', () => {
    const got = toPendingPermission({ kind: 'approval', id: 'tc1', params: { toolName: 'file.read' } })
    expect(got.command).toBeUndefined()
    expect(got.diff).toBeUndefined()
  })

  it('survives missing params', () => {
    expect(toPendingPermission({ kind: 'approval', id: 'tc1' })).toEqual({ toolCallId: 'tc1', toolName: '', exactOnly: true })
  })

  it('preserves typed permission scope', () => {
    const got = toPendingPermission({
      kind: 'approval',
      id: 'tc1',
      params: {
        toolName: 'mode.request',
        reason: 'need act',
        modeAccess: {
          requestId: 'r-1',
          sessionId: 's-1',
          reason: 'need act',
          current: { mode: 'ask', auto: false, system_access: false, access_revision: 2 },
          missingEditing: true,
          missingSystem: false,
        },
      },
    })
    // A typed capability bundle is not an exact-only approval, and its whole
    // presentation survives the adapter instead of being dropped.
    expect(got.exactOnly).toBe(false)
    expect(got.reason).toBe('need act')
    expect(got.modeAccess).toMatchObject({
      requestId: 'r-1',
      sessionId: 's-1',
      missingEditing: true,
      missingSystem: false,
      current: { mode: 'ask', auto: false, system_access: false, access_revision: 2 },
    })
  })
})

describe('toPendingQuestion', () => {
  it('maps questions with options and flags', () => {
    const got = toPendingQuestion('s1', {
      kind: 'question',
      id: 'q1',
      params: {
        questions: [
          { question: 'Which?', options: [{ label: 'A', value: 'a' }], multi: true, allowOther: true },
        ],
      },
    })
    expect(got).toEqual({
      questionId: 'q1',
      sessionId: 's1',
      questions: [{ question: 'Which?', options: [{ label: 'A', value: 'a' }], multi: true, allowOther: true }],
    })
  })

  it('normalises bare string options into label/value pairs', () => {
    const got = toPendingQuestion('s1', {
      kind: 'question',
      id: 'q1',
      params: { questions: [{ question: 'Pick', options: ['yes', 'no'] }] },
    })
    expect(got.questions[0].options).toEqual([
      { label: 'yes', value: 'yes' },
      { label: 'no', value: 'no' },
    ])
  })

  it('yields an empty question list when params are unusable', () => {
    expect(toPendingQuestion('s1', { kind: 'question', id: 'q1', params: { questions: 'nope' } }).questions).toEqual([])
  })
})


describe('run, budget and reroute deltas', () => {
  const run = { kind: 'sdd' as const, sdd: { totalTasks: 2, doneTasks: 1 } as never }

  it('stores a run delta on the addressed row', () => {
    const got = applyDeltaTo([row({ id: 'a' }), row({ id: 'b' })], { kind: 'run', sessionId: 'a', run, at: 42 })
    expect(got[0].run).toEqual(run)
    expect(got[0].runAt).toBe(42)
    expect(got[1].run).toBeUndefined()
  })

  it('leaves rows alone for budget and reroute', () => {
    const rows = [row({ id: 'a' })]
    expect(applyDeltaTo(rows, { kind: 'budget', scope: 'daily', spentUsd: 1, capUsd: 2, action: 'warn' })).toBe(rows)
    expect(applyDeltaTo(rows, { kind: 'reroute', id: 'r', watch: 'w', role: 'reviewer', from: 'a', to: 'b' })).toBe(rows)
  })

  it('counts watch deltas in the store and leaves rows alone', () => {
    const rows = [row({ id: 'a' })]
    expect(applyDeltaTo(rows, { kind: 'watch', sessionId: 'studio', event: { State: 'fired' } })).toBe(rows)
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'watch', sessionId: 'studio' })
    actions.applyDelta({ kind: 'watch', sessionId: 'a1' })
    expect(get(state).watchTick).toBe(2)
  })

  it('records automation deltas without touching rows', () => {
    const rows = [row({ id: 'a' })]
    expect(applyDeltaTo(rows, { kind: 'automation', type: 'review_draft', id: 'd1' })).toBe(rows)
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'automation', type: 'review_draft', id: 'd1' })
    actions.applyDelta({ kind: 'automation', type: 'ci_result', id: 'c1', status: 'fixed' })
    expect(get(state).automationTick).toBe(2)
    expect(get(state).automations.map((e) => [e.type, e.id, e.status])).toEqual([['review_draft', 'd1', undefined], ['ci_result', 'c1', 'fixed']])
  })

  it('keeps budget state and queues reroute notices in the store', () => {
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'budget', scope: 'daily', spentUsd: 3, capUsd: 5, action: 'block' })
    expect(get(state).budget).toEqual({ scope: 'daily', spentUsd: 3, capUsd: 5, action: 'block' })
    expect(get(state).budgetTick).toBe(1)
    actions.applyDelta({ kind: 'reroute', id: 'r1', watch: 'w', role: 'reviewer', from: 'a', to: 'b' })
    actions.applyDelta({ kind: 'reroute', id: 'r1', watch: 'w', role: 'reviewer', from: 'a', to: 'b' })
    expect(get(state).notices).toHaveLength(1)
    actions.dismissNotice('r1')
    expect(get(state).notices).toHaveLength(0)
  })
})

describe('network_block decisions', () => {
  const block = (host: string, agentId = 'a1', at = 1) => ({ kind: 'network_block' as const, sessionId: agentId, agentId, host, workspace: 'go', at })

  it('collects one decision per (agent, host), a later delta replacing the earlier', () => {
    const { state, actions } = createFleetStore()
    actions.applyDelta(block('a.com', 'a1', 1))
    actions.applyDelta(block('b.com', 'a1', 2))
    actions.applyDelta(block('a.com', 'a2', 3))
    actions.applyDelta({ ...block('a.com', 'a1', 9), workspace: 'node' })
    const d = get(state).decisions
    expect(d.map((x) => `${x.agentId}/${x.host}`)).toEqual(['a1/b.com', 'a2/a.com', 'a1/a.com'])
    expect(d.find((x) => x.agentId === 'a1' && x.host === 'a.com')?.workspace).toBe('node')
  })

  it('leaves agent rows alone', () => {
    const rows = [row({ id: 'a1' })]
    expect(applyDeltaTo(rows, block('a.com'))).toBe(rows)
  })

  it('decideNetwork posts, then removes the item; a failed post keeps it', async () => {
    const { state, actions } = createFleetStore()
    actions.applyDelta(block('a.com'))
    actions.applyDelta(block('b.com'))
    const fetchMock = vi.fn().mockResolvedValueOnce({ ok: false, status: 500, text: async () => '{"error":"x"}' }).mockResolvedValue({ ok: true, status: 200, text: async () => '{"ok":true}' })
    vi.stubGlobal('fetch', fetchMock)
    setToken('t')
    await expect(actions.decideNetwork('a1', 'a.com', 'block')).rejects.toBeTruthy()
    expect(get(state).decisions).toHaveLength(2)
    await actions.decideNetwork('a1', 'a.com', 'allow-agent')
    expect(get(state).decisions.map((x) => x.host)).toEqual(['b.com'])
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ agentId: 'a1', host: 'a.com', decision: 'allow-agent' })
    vi.unstubAllGlobals()
  })
})

describe('telemetry deltas', () => {
  it('keeps the last telemetry per agent and still updates the row', () => {
    const { state, actions } = createFleetStore()
    state.update((s) => ({ ...s, agents: [row({ id: 'a1' })] }))
    actions.applyDelta({ kind: 'telemetry', sessionId: 'a1', contextPct: 42, changedFiles: 3 })
    actions.applyDelta({ kind: 'telemetry', sessionId: 'a1', contextPct: 55, changedFiles: 4, toolStats: [{ name: 'x', calls: 1, errors: 1, slowestMs: 5 }], rules: ['no-network'] })
    const s = get(state)
    expect(s.telemetry.a1).toMatchObject({ contextPct: 55, changedFiles: 4, rules: ['no-network'] })
    expect(s.agents[0].contextPct).toBe(55)
  })
})

describe('stale decisions', () => {
  it('refresh restores pending prompts from the bridge, merged with live ones', async () => {
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'network_block', sessionId: 'a1', agentId: 'a1', host: 'live.com', at: 5 })
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async (u: string) => ({
      ok: true,
      status: 200,
      text: async () =>
        u === '/api/agents' ? JSON.stringify([{ id: 'a1', project: '/p', status: 'idle', updatedAt: '' }, { id: 'a2', project: '/p', status: 'idle', updatedAt: '' }])
        : u === '/api/network/pending' ? JSON.stringify({ pending: [{ kind: 'network_block', sessionId: 'a2', agentId: 'a2', host: 'old.com', workspace: 'w', at: 1 }] })
        : '[]',
    })))
    setToken('t')
    await actions.refresh()
    expect(get(state).decisions.map((d) => `${d.agentId}/${d.host}`)).toEqual(['a2/old.com', 'a1/live.com'])
    vi.unstubAllGlobals()
  })

  it('refresh still works when the pending route is missing', async () => {
    const { state, actions } = createFleetStore()
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async (u: string) => u === '/api/network/pending' ? { ok: false, status: 404, text: async () => '' } : { ok: true, status: 200, text: async () => '[]' }))
    setToken('t')
    await actions.refresh()
    expect(get(state).error).toBeNull()
    vi.unstubAllGlobals()
  })

  it('refresh drops decisions of agents that are gone', async () => {
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'network_block', sessionId: 'gone', agentId: 'gone', host: 'a.com', at: 1 })
    actions.applyDelta({ kind: 'network_block', sessionId: 'a1', agentId: 'a1', host: 'b.com', at: 2 })
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async (u: string) => ({ ok: true, status: 200, text: async () => (u === '/api/agents' ? JSON.stringify([{ id: 'a1', project: '/p', status: 'idle', updatedAt: '' }]) : '[]') })))
    setToken('t')
    await actions.refresh()
    expect(get(state).decisions.map((d) => d.agentId)).toEqual(['a1'])
    vi.unstubAllGlobals()
  })
})

describe('hold deltas', () => {
  it('mark the agent held and clear it on hand back', () => {
    const rows = [row({ id: 'a' }), row({ id: 'b' })]
    const held = applyDeltaTo(rows, { kind: 'hold', sessionId: 'a', held: true, by: 'terminal' })
    expect(held[0]).toMatchObject({ held: true, heldBy: 'terminal' })
    expect(held[1].held).toBeUndefined()
    const back = applyDeltaTo(held, { kind: 'hold', sessionId: 'a', held: false, by: 'terminal' })
    expect(back[0]).toMatchObject({ held: false, heldBy: undefined })
  })
})

describe('held on load', () => {
  it('toRow keeps held from the agent list and treats a missing field as not held', () => {
    expect(toRow({ id: 'a', project: '/p', status: 'idle', updatedAt: '', held: true }).held).toBe(true)
    expect(toRow({ id: 'b', project: '/p', status: 'idle', updatedAt: '' }).held ?? false).toBe(false)
  })
})

describe('the inbox in the store', () => {
  const inbox = (o: Partial<{ needsYou: string[]; running: string[]; ready: string[]; earlier: string[] }> = {}) => ({
    groups: { needsYou: [], running: [], ready: [], earlier: [], ...o },
    queue: [],
  })

  it('starts empty and takes an inbox delta whole, leaving the rows alone', () => {
    const { state, actions } = createFleetStore()
    expect(get(state).inbox).toEqual({ groups: { needsYou: [], running: [], ready: [], earlier: [] }, queue: [] })
    const rows = [row({ id: 'a' })]
    state.update((s) => ({ ...s, agents: rows }))
    const next = inbox({ running: ['a'] })
    actions.applyDelta({ kind: 'inbox', inbox: next })
    expect(get(state).inbox).toEqual(next)
    expect(get(state).agents).toBe(rows)
  })

  it('refresh fetches the inbox with the agents and the projects', async () => {
    const { state, actions } = createFleetStore()
    const next = inbox({ needsYou: ['a1'] })
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async (u: string) => ({
      ok: true,
      status: 200,
      text: async () =>
        u === '/api/agents' ? JSON.stringify([{ id: 'a1', project: '/p', status: 'idle', updatedAt: '' }])
        : u === '/api/inbox' ? JSON.stringify(next)
        : '[]',
    })))
    setToken('t')
    await actions.refresh()
    expect(get(state).inbox).toEqual(next)
    expect(get(state).agents.map((a) => a.id)).toEqual(['a1'])
    vi.unstubAllGlobals()
  })

  it('refetches the agents when the inbox names a row it lacks, at most once a second', async () => {
    const { state, actions } = createFleetStore()
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify([{ id: 'new', project: '/p', status: 'running', updatedAt: '' }]),
    })
    vi.stubGlobal('fetch', fetchMock)
    setToken('t')

    actions.applyDelta({ kind: 'inbox', inbox: inbox({ running: ['new'] }) })
    await vi.waitFor(() => expect(get(state).agents.map((a) => a.id)).toEqual(['new']))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/agents')

    // Same second: the second unknown ID rides on the refetch already made.
    actions.applyDelta({ kind: 'inbox', inbox: inbox({ running: ['new', 'other'] }) })
    await Promise.resolve()
    await Promise.resolve()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    vi.unstubAllGlobals()
  })

  it('does not refetch when every ID the inbox names has a row', async () => {
    const { state, actions } = createFleetStore()
    state.update((s) => ({ ...s, agents: [row({ id: 'a' })] }))
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    actions.applyDelta({ kind: 'inbox', inbox: inbox({ running: ['a'] }) })
    await Promise.resolve()
    expect(fetchMock).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })
})

