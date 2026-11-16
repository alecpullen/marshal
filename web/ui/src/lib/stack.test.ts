import { describe, expect, it, vi } from 'vitest'
import { get } from 'svelte/store'
import { APIError } from './api'
import { createStackStore, createSubagentControls, NO_SUBAGENT_CONTROLS, subagentControls, type StackPatch, type StackSnapshot, type WireNode } from './stack'

const node = (id: string, o: Partial<WireNode> = {}): WireNode => ({ id, kind: 'step', ...o })

const snapshot = (): StackSnapshot => ({
  rev: 5,
  roots: ['turn:1'],
  nodes: [node('turn:1', { kind: 'turn', children: ['step:1'] }), node('step:1', { parent: 'turn:1' })],
})

const patch = (o: Partial<StackPatch>): unknown => ({
  method: 'session/update',
  params: { sessionId: 's1', update: { kind: 'stack_patch', rev: 6, baseRev: 5, roots: ['turn:1'], ...o } },
})

const settle = () => new Promise((r) => setTimeout(r, 0))

async function ready(fetcher = vi.fn().mockResolvedValue(snapshot())) {
  const store = createStackStore('s1', { fetcher: fetcher })
  await store.load()
  return { store, fetcher }
}

describe('stack store retries', () => {
  it('retries a failed refetch on a growing delay, even with no new events', async () => {
    vi.useFakeTimers()
    try {
      const fetcher = vi
        .fn()
        .mockResolvedValueOnce(snapshot())
        .mockRejectedValueOnce(new Error('down'))
        .mockRejectedValueOnce(new Error('down'))
        .mockResolvedValue({ ...snapshot(), rev: 9 })
      const store = createStackStore('s1', { fetcher: fetcher })
      await store.load()
      store.onEvent({ type: 'replay_overflow' })
      await vi.advanceTimersByTimeAsync(0)
      expect(fetcher).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(1000)
      expect(fetcher).toHaveBeenCalledTimes(3)
      await vi.advanceTimersByTimeAsync(1999)
      expect(fetcher).toHaveBeenCalledTimes(3)
      await vi.advanceTimersByTimeAsync(1)
      expect(fetcher).toHaveBeenCalledTimes(4)
      expect(get(store).rev).toBe(9)
      store.destroy()
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not fetch once per patch while the endpoint is down', async () => {
    vi.useFakeTimers()
    try {
      const fetcher = vi.fn().mockRejectedValue(new Error('down'))
      const store = createStackStore('s1', { fetcher: fetcher })
      await store.load()
      for (let i = 0; i < 5; i++) store.onEvent(patch({ rev: 2 + i, baseRev: 1 + i }))
      await vi.advanceTimersByTimeAsync(0)
      expect(fetcher).toHaveBeenCalledTimes(1)
      store.destroy()
    } finally {
      vi.useRealTimers()
    }
  })

  it('stops retrying after destroy', async () => {
    vi.useFakeTimers()
    try {
      const fetcher = vi.fn().mockRejectedValue(new Error('down'))
      const store = createStackStore('s1', { fetcher: fetcher })
      await store.load()
      store.destroy()
      await vi.advanceTimersByTimeAsync(60000)
      expect(fetcher).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('stack store', () => {
  it('loads a snapshot', async () => {
    const { store, fetcher } = await ready()
    const s = get(store)
    expect(fetcher).toHaveBeenCalledWith('s1', undefined)
    expect(s.status).toBe('ready')
    expect(s.rev).toBe(5)
    expect([...s.nodes.keys()]).toEqual(['turn:1', 'step:1'])
  })

  it('applies a patch: a new child and its updated parent', async () => {
    const { store } = await ready()
    store.onEvent(
      patch({
        upsert: [
          node('turn:1', { kind: 'turn', children: ['step:1', 'step:2'] }),
          node('step:2', { parent: 'turn:1', step: { headline: 'Reading' } }),
        ],
      }),
    )
    const s = get(store)
    expect(s.rev).toBe(6)
    expect(s.nodes.get('turn:1')?.children).toEqual(['step:1', 'step:2'])
    expect(s.nodes.get('step:2')?.step?.headline).toBe('Reading')
  })

  it('ignores a stale patch', async () => {
    const { store, fetcher } = await ready()
    store.onEvent(patch({ rev: 5, baseRev: 4, upsert: [node('x')] }))
    expect(get(store).nodes.has('x')).toBe(false)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('refetches exactly once on a baseRev gap, even for a burst', async () => {
    const { store, fetcher } = await ready()
    store.onEvent(patch({ rev: 9, baseRev: 8 }))
    store.onEvent(patch({ rev: 10, baseRev: 9 }))
    await settle()
    // The first gap starts a fetch; the second folds into one follow-up.
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('refetches exactly once for a single gap patch', async () => {
    const { store, fetcher } = await ready()
    store.onEvent(patch({ rev: 9, baseRev: 8 }))
    await settle()
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('removes nodes', async () => {
    const { store } = await ready()
    store.onEvent(patch({ upsert: [node('turn:1', { kind: 'turn', children: [] })], remove: ['step:1'] }))
    expect(get(store).nodes.has('step:1')).toBe(false)
  })

  it('collects a node nothing reaches', async () => {
    const { store } = await ready()
    // step:1 is no longer listed as a child and was not removed explicitly.
    store.onEvent(patch({ upsert: [node('turn:1', { kind: 'turn', children: [] })] }))
    expect([...get(store).nodes.keys()]).toEqual(['turn:1'])
  })

  it('reports unsupported agents', async () => {
    const store = createStackStore('s1', { fetcher: vi.fn().mockResolvedValue('unsupported') })
    await store.load()
    expect(get(store).status).toBe('unsupported')
    store.onEvent(patch({}))
    expect(get(store).status).toBe('unsupported')
  })

  it('reports an error when the first fetch fails, and recovers on the next load', async () => {
    const fetcher = vi.fn().mockRejectedValueOnce(new Error('down')).mockResolvedValue(snapshot())
    const store = createStackStore('s1', { fetcher: fetcher })
    await store.load()
    expect(get(store).status).toBe('error')
    await store.load()
    expect(get(store).status).toBe('ready')
  })

  it('keeps its snapshot when a later fetch fails', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(snapshot()).mockRejectedValue(new Error('down'))
    const store = createStackStore('s1', { fetcher: fetcher })
    await store.load()
    await store.load()
    expect(get(store).status).toBe('ready')
  })

  it('refetches on telemetry and on replay overflow', async () => {
    const { store, fetcher } = await ready()
    store.onEvent({ method: 'session/update', params: { update: { kind: 'session_telemetry' } } })
    await settle()
    expect(fetcher).toHaveBeenCalledTimes(2)
    store.onEvent({ type: 'replay_overflow' })
    await settle()
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('does not apply a patch that arrives while the snapshot is loading', async () => {
    let resolve!: (s: StackSnapshot) => void
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise((r) => (resolve = r))).mockResolvedValue(snapshot())
    const store = createStackStore('s1', { fetcher: fetcher })
    const loading = store.load()
    store.onEvent(patch({ rev: 6, baseRev: 5, upsert: [node('x')] }))
    resolve(snapshot())
    await loading
    // The patch is not applied on top of a snapshot that may already hold
    // it; the store asks for one more snapshot instead.
    expect(get(store).nodes.has('x')).toBe(false)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })
})

describe('stack store subagent scoping', () => {
  const sub = (id: number) => patch({ subagentId: id, upsert: [node('turn:1', { kind: 'turn', children: ['step:1', 'step:9'] }), node('step:9', { parent: 'turn:1' })] })

  it('a parent store ignores a child patch', async () => {
    const { store, fetcher } = await ready()
    store.onEvent(sub(3))
    await settle()
    expect(get(store).nodes.has('step:9')).toBe(false)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('a child store applies its own patch and fetches its own transcript', async () => {
    const fetcher = vi.fn().mockResolvedValue(snapshot())
    const store = createStackStore('s1', { subagentId: 3, fetcher })
    await store.load()
    expect(fetcher).toHaveBeenCalledWith('s1', 3)
    store.onEvent(sub(3))
    expect(get(store).nodes.has('step:9')).toBe(true)
  })

  it('a child store ignores the parent patches', async () => {
    const store = createStackStore('s1', { subagentId: 3, fetcher: vi.fn().mockResolvedValue(snapshot()) })
    await store.load()
    store.onEvent(patch({ upsert: [node('turn:1', { kind: 'turn', children: ['step:1', 'step:9'] }), node('step:9', { parent: 'turn:1' })] }))
    expect(get(store).nodes.has('step:9')).toBe(false)
  })
})

describe('stack store refusals', () => {
  it('reports a 400 as an error without retrying', async () => {
    vi.useFakeTimers()
    try {
      const fetcher = vi.fn().mockRejectedValue(new APIError(400, { error: 'no separate transcript' }))
      const store = createStackStore('s1', { subagentId: 3, fetcher })
      await store.load()
      expect(get(store).status).toBe('error')
      await vi.advanceTimersByTimeAsync(60000)
      expect(fetcher).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('child controls', () => {
  it('re-sends one uncertain steer with the same request ID and never reports consumption', async () => {
    const post = vi
      .fn()
      .mockRejectedValueOnce(new APIError(502, { error: 'bridge: child stopped', code: 'connection_lost' }))
      .mockResolvedValueOnce({ requestId: 'req-fixed', status: 'accepted' })
    const controls = createSubagentControls('s1', 7, { post, newRequestId: () => 'req-fixed' })

    const out = await controls.steer('wrap up and report')

    // The uncertain attempt was retried, and the retry carried the same ID:
    // the session deduplicates on it, so the child never sees it twice.
    expect(post).toHaveBeenCalledTimes(2)
    const paths = post.mock.calls.map(([path]) => path)
    expect(paths).toEqual([
      '/api/sessions/s1/subagents/7/steer',
      '/api/sessions/s1/subagents/7/steer',
    ])
    const bodies = post.mock.calls.map(([, body]) => body as { requestId: string; message: string })
    expect(bodies[0]).toEqual(bodies[1])
    expect(bodies[0]).toEqual({ requestId: 'req-fixed', message: 'wrap up and report' })

    // Acceptance is not consumption: nothing in the outcome claims the child
    // read the instruction.
    expect(out).toEqual({ requestId: 'req-fixed', status: 'accepted' })
    expect(out).not.toHaveProperty('consumed')
    expect(out).not.toHaveProperty('delivered')
  })

  it('keeps the minted request ID when the bridge does not echo one', async () => {
    const post = vi.fn().mockResolvedValueOnce({ status: 'accepted' })
    const controls = createSubagentControls('s1', 4, { post, newRequestId: () => 'req-minted' })
    expect(await controls.steer('go')).toEqual({ requestId: 'req-minted', status: 'accepted' })
  })

  it('does not retry a definite refusal', async () => {
    const post = vi.fn().mockRejectedValue(new APIError(409, { error: 'already used', code: 'conflict' }))
    const controls = createSubagentControls('s1', 7, { post, newRequestId: () => 'req-1' })
    await expect(controls.steer('again')).rejects.toBeInstanceOf(APIError)
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('does not retry a 501 the session answers with', async () => {
    // 501 is the session saying it has no such control, not a lost answer:
    // asking twice more would only repeat the same refusal.
    const post = vi.fn().mockRejectedValue(new APIError(501, { error: 'steer is not supported', code: 'unsupported' }))
    const controls = createSubagentControls('s1', 7, { post, newRequestId: () => 'req-1' })
    await expect(controls.steer('go')).rejects.toBeInstanceOf(APIError)
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('still retries an unknown 5xx, which a retry can settle', async () => {
    const post = vi
      .fn()
      .mockRejectedValueOnce(new APIError(502, { error: 'bridge: connection lost', code: 'connection_lost' }))
      .mockResolvedValueOnce({ requestId: 'req-1', status: 'accepted' })
    const controls = createSubagentControls('s1', 7, { post, newRequestId: () => 'req-1' })
    expect(await controls.steer('go')).toEqual({ requestId: 'req-1', status: 'accepted' })
    expect(post).toHaveBeenCalledTimes(2)
  })

  it('reports a stop as the acknowledgement it is', async () => {
    const post = vi.fn().mockResolvedValueOnce({ cancelled: true })
    const controls = createSubagentControls('s1', 7, { post })
    expect(await controls.stop()).toEqual({ cancelled: true })
    expect(post).toHaveBeenCalledWith('/api/sessions/s1/subagents/7/cancel', {})
  })

  it('reads the session status capabilities and treats absent as unsupported', () => {
    expect(
      subagentControls({ subagentControls: { settings: false, steer: true, stop: true, observe: true } }),
    ).toEqual({ settings: false, steer: true, stop: true, observe: true })

    // Absent, malformed or partial is unavailable: support is never inferred.
    expect(subagentControls({})).toEqual(NO_SUBAGENT_CONTROLS)
    expect(subagentControls(null)).toEqual(NO_SUBAGENT_CONTROLS)
    expect(subagentControls({ subagentControls: { steer: true } })).toEqual({
      settings: false,
      steer: true,
      stop: false,
      observe: false,
    })
  })
})
