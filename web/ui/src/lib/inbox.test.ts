import { describe, expect, it } from 'vitest'
import { mineOnly } from './inbox'
import type { Inbox, InboxItem } from './contract'
import type { PendingSubmission } from './api'

const sub = (id: string, createdAt: string): PendingSubmission => ({
  id,
  auto: null,
  clientId: 'c1',
  mode: 'act',
  origin: 'mcp',
  plan: '',
  prompt: '',
  ref: '',
  title: id,
  repoId: 'r',
  createdAt,
  expiresAt: '2024-06-01T00:00:00Z',
})

const inbox = (o: { groups?: Partial<Inbox['groups']>; queue?: InboxItem[] } = {}): Inbox => ({
  groups: { needsYou: [], running: [], ready: [], earlier: [], ...(o.groups ?? {}) },
  queue: o.queue ?? [],
})

const ownerOf = (owners: Record<string, string | undefined>) => (id: string) => owners[id]

describe('mineOnly', () => {
  it('keeps local or unowned agent IDs and drops another owner’s, in the bridge’s order', () => {
    const got = mineOnly(
      inbox({ groups: { running: ['busy', 'theirs', 'mine'], ready: ['theirs'] } }),
      ownerOf({ busy: undefined, theirs: 'bob', mine: 'local' }),
    )
    expect(got.groups.running).toEqual(['busy', 'mine'])
    expect(got.groups.ready).toEqual([])
  })

  it('drops an agent ID from every group, keeping each list’s order', () => {
    const got = mineOnly(
      inbox({ groups: { needsYou: ['a', 'b'], running: ['a', 'b'], ready: ['a', 'b'], earlier: ['a', 'b'] } }),
      ownerOf({ a: 'bob', b: undefined }),
    )
    expect(got.groups).toEqual({ needsYou: ['b'], running: ['b'], ready: ['b'], earlier: ['b'] })
  })

  it('filters the queue’s intake items by the submission’s owner', () => {
    const theirs = { ...sub('t', '2024-05-04T09:00:00Z'), ownerId: 'bob' }
    const got = mineOnly(
      inbox({ queue: [{ kind: 'intake', submission: theirs, at: theirs.createdAt }, { kind: 'intake', submission: sub('m', '2024-05-04T10:00:00Z'), at: '2024-05-04T10:00:00Z' }] }),
      ownerOf({}),
    )
    expect(got.queue.map((i) => i.submission?.id)).toEqual(['m'])
  })

  it('filters the queue’s agent items by the agent’s owner', () => {
    const got = mineOnly(
      inbox({ queue: [{ kind: 'agent', agentId: 'theirs', at: '2024-05-04T09:00:00Z' }, { kind: 'agent', agentId: 'busy', at: '2024-05-04T10:00:00Z' }] }),
      ownerOf({ theirs: 'bob' }),
    )
    expect(got.queue.map((i) => i.agentId)).toEqual(['busy'])
  })

  it('counts ownerId "local" as mine', () => {
    const got = mineOnly(
      inbox({ groups: { running: ['l'] }, queue: [{ kind: 'intake', submission: { ...sub('i', '2024-05-04T09:00:00Z'), ownerId: 'local' }, at: '2024-05-04T09:00:00Z' }] }),
      ownerOf({ l: 'local' }),
    )
    expect(got.groups.running).toEqual(['l'])
    expect(got.queue).toHaveLength(1)
  })
})
