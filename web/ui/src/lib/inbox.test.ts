import { describe, expect, it } from 'vitest'
import { buildInbox } from './inbox'
import { toRow } from './fleet'
import type { AgentStatus, PendingSubmission } from './api'

const mk = (o: Partial<AgentStatus> & { id: string }) =>
  toRow({ project: '/p', status: 'idle', updatedAt: '2024-05-04T12:00:00Z', ...o })
const sub = (id: string, createdAt: string): PendingSubmission => ({
  id,
  origin: 'mcp',
  title: id,
  repoId: 'r',
  createdAt,
  expiresAt: '2024-06-01T00:00:00Z',
})

describe('buildInbox', () => {
  const agents = [
    mk({ id: 'ask', pending: { kind: 'approval', id: 't' }, updatedAt: '2024-05-04T12:00:00Z' }),
    mk({ id: 'busy', status: 'running' }),
    mk({ id: 'done', changedFiles: 2 }),
    mk({ id: 'theirs', status: 'running', ownerId: 'bob' }),
  ]

  it('merges intake into Needs you, oldest first', () => {
    const inbox = buildInbox(agents, [sub('early', '2024-05-04T09:00:00Z'), sub('late', '2024-05-04T15:00:00Z')], false)
    expect(inbox.needsYou.map((i) => (i.kind === 'agent' ? i.agent.id : i.submission.id))).toEqual(['early', 'ask', 'late'])
    expect(inbox.ready.map((a) => a.id)).toEqual(['done'])
    expect(inbox.running.map((a) => a.id).sort()).toEqual(['busy', 'theirs'])
  })

  it('keeps only local or unowned items when mine', () => {
    const inbox = buildInbox(agents, [], true)
    expect(inbox.running.map((a) => a.id)).toEqual(['busy'])
  })

  it('counts ownerId "local" as mine', () => {
    const inbox = buildInbox([mk({ id: 'l', status: 'running', ownerId: 'local' })], [], true)
    expect(inbox.running).toHaveLength(1)
  })
})
