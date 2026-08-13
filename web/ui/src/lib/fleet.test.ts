import { describe, expect, it } from 'vitest'
import { applyDeltaTo, describePending, sortAttentionFirst, toPendingPermission, toPendingQuestion, toRow, type AgentRow } from './fleet'
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
