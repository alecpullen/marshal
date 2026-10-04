import { describe, expect, it } from 'vitest'
import { get } from 'svelte/store'
import { applyDeltaTo, createFleetStore, groupAgents, describePending, sortAttentionFirst, toPendingPermission, toPendingQuestion, toRow, type AgentRow } from './fleet'
import type { AgentStatus } from './api'

const row = (x: Partial<AgentRow>): AgentRow => ({
  id: 'x',
  project: '/p',
  status: 'idle',
  updatedAt: '',
  name: 'n',
  mode: 'edit',
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

  it('applies a mode change', () => {
    const got = applyDeltaTo([row({ id: 'a', mode: 'default' })], { kind: 'mode', sessionId: 'a', mode: 'auto' })
    expect(got[0].mode).toBe('auto')
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
    expect(got).toEqual({ toolCallId: 'tc1', toolName: 'shell.run', command: 'ls -la', diff: '--- a\n+++ b' })
  })

  it('omits absent optional fields rather than passing empty strings', () => {
    const got = toPendingPermission({ kind: 'approval', id: 'tc1', params: { toolName: 'file.read' } })
    expect(got.command).toBeUndefined()
    expect(got.diff).toBeUndefined()
  })

  it('survives missing params', () => {
    expect(toPendingPermission({ kind: 'approval', id: 'tc1' })).toEqual({ toolCallId: 'tc1', toolName: '' })
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


describe('groupAgents', () => {
  const mk = (o: Partial<AgentStatus> & { id: string }): AgentRow =>
    toRow({ project: '/p', status: 'idle', updatedAt: '2024-05-04T12:00:00Z', ...o })

  it('buckets agents by what they need', () => {
    const g = groupAgents([
      mk({ id: 'asks', status: 'awaiting-approval', pending: { kind: 'approval', id: 't' } }),
      mk({ id: 'busy', status: 'running' }),
      mk({ id: 'done', changedFiles: 3 }),
      mk({ id: 'shipped', changedFiles: 3, prUrl: 'https://x/pr/1' }),
      mk({ id: 'quiet' }),
      mk({ id: 'broken', status: 'error' }),
    ])
    expect(g.needsYou.map((a) => a.id)).toEqual(['asks'])
    expect(g.running.map((a) => a.id)).toEqual(['busy'])
    expect(g.ready.map((a) => a.id)).toEqual(['done'])
    expect(g.earlier.map((a) => a.id).sort()).toEqual(['broken', 'quiet', 'shipped'])
  })

  it('never lists a running agent that is waiting on you under Running', () => {
    const g = groupAgents([mk({ id: 'a', status: 'running', pending: { kind: 'question', id: 'q' } })])
    expect(g.running).toHaveLength(0)
    expect(g.needsYou).toHaveLength(1)
  })

  it('sorts by pending kind, then recency', () => {
    const g = groupAgents([
      mk({ id: 'q', pending: { kind: 'question', id: '1' }, updatedAt: '2024-05-04T13:00:00Z' }),
      mk({ id: 'old', pending: { kind: 'approval', id: '2' }, updatedAt: '2024-05-04T10:00:00Z' }),
      mk({ id: 'new', pending: { kind: 'approval', id: '3' }, updatedAt: '2024-05-04T12:00:00Z' }),
    ])
    expect(g.needsYou.map((a) => a.id)).toEqual(['new', 'old', 'q'])
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
    expect(applyDeltaTo(rows, { kind: 'watch', agentId: 'studio', event: { state: 'fired' } })).toBe(rows)
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'watch', agentId: 'studio' })
    actions.applyDelta({ kind: 'watch', agentId: 'a1' })
    expect(get(state).watchTick).toBe(2)
  })

  it('keeps budget state and queues reroute notices in the store', () => {
    const { state, actions } = createFleetStore()
    actions.applyDelta({ kind: 'budget', scope: 'daily', spentUsd: 3, capUsd: 5, action: 'block' })
    expect(get(state).budget).toEqual({ scope: 'daily', spentUsd: 3, capUsd: 5, action: 'block' })
    actions.applyDelta({ kind: 'reroute', id: 'r1', watch: 'w', role: 'reviewer', from: 'a', to: 'b' })
    actions.applyDelta({ kind: 'reroute', id: 'r1', watch: 'w', role: 'reviewer', from: 'a', to: 'b' })
    expect(get(state).notices).toHaveLength(1)
    actions.dismissNotice('r1')
    expect(get(state).notices).toHaveLength(0)
  })
})
