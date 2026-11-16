import { describe, it, expect, vi } from 'vitest'
import { get } from 'svelte/store'
import type { FactoryItem } from './factory'
import { createFactoryStore } from './factoryStore'

/** One item with the wire shape's required fields; each test overrides what it asserts. */
function item(id: string, over: Partial<FactoryItem> = {}): FactoryItem {
  return {
    id,
    title: id,
    state: 'running',
    stage: '',
    repoId: 'repo',
    projectRoot: '/repo',
    hostId: 'host',
    branch: `factory/${id}`,
    baseSha: '',
    version: 1,
    draft: { requestId: id, title: id, plan: '', spec: '', config: {} as FactoryItem['draft']['config'] },
    approvals: null,
    grants: null,
    rounds: null,
    workers: null,
    prs: null,
    effects: null,
    decision: null,
    history: null,
    createdAt: '2026-01-01T00:00:00Z',
    approvedAt: '',
    updatedAt: '2026-01-01T00:00:00Z',
    ...over,
  }
}

/** A visibility seam the test flips, as the poller's `PollSeam.document` expects. */
function fakeDocument(isHidden: () => boolean) {
  const listeners = new Set<EventListenerOrEventListenerObject>()
  return {
    listeners,
    doc: {
      get hidden() {
        return isHidden()
      },
      addEventListener: (_: string, cb: EventListenerOrEventListenerObject) => {
        listeners.add(cb)
      },
      removeEventListener: (_: string, cb: EventListenerOrEventListenerObject) => {
        listeners.delete(cb)
      },
    },
  }
}

function fire(listeners: Set<EventListenerOrEventListenerObject>) {
  listeners.forEach((cb) => typeof cb === 'function' && cb(new Event('visibilitychange')))
}

describe('factory store loading', () => {
  it('initialLoadAndRefreshState', async () => {
    let finish!: (items: FactoryItem[]) => void
    const list = vi.fn(() => new Promise<FactoryItem[]>((resolve) => { finish = resolve }))
    const store = createFactoryStore({ list, now: () => 42 })

    const first = store.refresh()
    expect(get(store.state).loading).toBe(true)
    expect(get(store.state).refreshing).toBe(false)
    finish([item('a')])
    await first
    expect(get(store.state).items.map((i) => i.id)).toEqual(['a'])
    expect(get(store.state).loading).toBe(false)
    expect(get(store.state).refreshing).toBe(false)
    expect(get(store.state).error).toBeNull()
    expect(get(store.state).lastUpdated).toBe(42)

    // Past the first snapshot a read is a check, not a load: the list stays up.
    const second = store.refresh()
    expect(get(store.state).loading).toBe(false)
    expect(get(store.state).refreshing).toBe(true)
    expect(get(store.state).items.map((i) => i.id)).toEqual(['a'])
    finish([])
    await second
    expect(get(store.state).refreshing).toBe(false)
  })

  it('emptySnapshotIsAuthoritative', async () => {
    const store = createFactoryStore({ list: async () => [], now: () => 7 })
    store.upsert(item('a'))
    expect(get(store.state).items).toHaveLength(1)
    await store.refresh()
    expect(get(store.state).items).toEqual([])
    expect(get(store.state).lastUpdated).toBe(7)
  })

  it('failedRefreshKeepsItemsAndLastUpdated', async () => {
    const list = vi.fn().mockResolvedValueOnce([item('a')]).mockRejectedValueOnce(new Error('offline'))
    const store = createFactoryStore({ list, now: () => 100 })
    await store.refresh()

    await expect(store.refresh()).rejects.toThrow('offline')
    const state = get(store.state)
    expect(state.items.map((i) => i.id)).toEqual(['a'])
    expect(state.lastUpdated).toBe(100)
    expect(state.error).toBe('offline')
    expect(state.loading).toBe(false)
    expect(state.refreshing).toBe(false)
  })
})

describe('factory store mutations', () => {
  it('upsertReplacesAndAddsItems', () => {
    const store = createFactoryStore({ list: async () => [] })
    store.upsert(item('a', { title: 'first' }))
    store.upsert(item('b'))
    store.upsert(item('a', { title: 'second' }))
    expect(get(store.state).items.map((i) => i.id)).toEqual(['a', 'b'])
    expect(store.itemById('a')?.title).toBe('second')
    expect(store.itemById('b')?.title).toBe('b')
  })

  it('staleListCannotOverwriteCommandUpsert', async () => {
    let finishList!: (items: FactoryItem[]) => void
    const list = vi.fn(() => new Promise<FactoryItem[]>((resolve) => { finishList = resolve }))
    const store = createFactoryStore({ list, now: () => 5000 })

    const reading = store.refresh()
    store.upsert(item('a', { state: 'approved' }))
    finishList([item('a', { state: 'draft' })])
    await reading

    // The list started before the command, so its version is the older one.
    expect(store.itemById('a')?.state).toBe('approved')
    expect(get(store.state).lastUpdated).toBeNull()
    expect(get(store.state).loading).toBe(false)
    expect(get(store.state).refreshing).toBe(false)
  })
})

describe('factory store single items', () => {
  it('staleSingleItemReadReturnsNewerCachedItem', async () => {
    let finishItem!: (item: FactoryItem) => void
    const readOne = vi.fn(() => new Promise<FactoryItem>((resolve) => { finishItem = resolve }))
    const store = createFactoryStore({ get: readOne, list: async () => [] })

    const reading = store.getItem('a')
    store.upsert(item('a', { state: 'approved' }))
    finishItem(item('a', { state: 'draft' }))

    await expect(reading).resolves.toMatchObject({ state: 'approved' })
    expect(store.itemById('a')?.state).toBe('approved')
  })

  it('staleItemReadCannotOverwriteNewerListSnapshot', async () => {
    let finishItem!: (item: FactoryItem) => void
    const readOne = vi.fn(() => new Promise<FactoryItem>((resolve) => { finishItem = resolve }))
    let finishList!: (items: FactoryItem[]) => void
    const list = vi.fn(() => new Promise<FactoryItem[]>((resolve) => { finishList = resolve }))
    const store = createFactoryStore({ list, get: readOne })

    // The item read is in flight when the list snapshot lands with a newer copy.
    const reading = store.getItem('a')
    const listing = store.refresh()
    finishList([item('a', { state: 'approved' })])
    await listing
    finishItem(item('a', { state: 'draft' }))

    // The item read started before the snapshot, so it keeps the newer copy.
    await expect(reading).resolves.toMatchObject({ state: 'approved' })
    expect(store.itemById('a')?.state).toBe('approved')
    expect(list).toHaveBeenCalledTimes(1)
    expect(readOne).toHaveBeenCalledTimes(1)
  })

  it('unrelatedUpsertDoesNotDiscardMissingItemRead', async () => {
    let finishItem!: (item: FactoryItem) => void
    const readOne = vi.fn(() => new Promise<FactoryItem>((resolve) => { finishItem = resolve }))
    const store = createFactoryStore({ get: readOne, list: async () => [] })

    const reading = store.getItem('b')
    // Another item's command result says nothing about the read of `b`.
    store.upsert(item('a'))
    finishItem(item('b', { title: 'B' }))

    await expect(reading).resolves.toMatchObject({ title: 'B' })
    expect(store.itemById('b')?.title).toBe('B')
  })

  it('cachedItemDoesNotFetch', async () => {
    const readOne = vi.fn(async () => item('a', { title: 'cached' }))
    const store = createFactoryStore({ get: readOne, list: async () => [] })
    store.upsert(item('a', { title: 'kept' }))

    await expect(store.getItem('a')).resolves.toMatchObject({ title: 'kept' })
    expect(readOne).not.toHaveBeenCalled()
  })

  it('missingItemFetchesOnce', async () => {
    const readOne = vi.fn(async () => item('a'))
    const store = createFactoryStore({ get: readOne, list: async () => [] })

    const [first, second] = await Promise.all([store.getItem('a'), store.getItem('a')])
    expect(readOne).toHaveBeenCalledTimes(1)
    expect(first.id).toBe('a')
    expect(second.id).toBe('a')
    // The adopted copy now answers from the cache.
    await store.getItem('a')
    expect(readOne).toHaveBeenCalledTimes(1)
  })

  it('itemErrorsPropagate', async () => {
    const readOne = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce(item('a'))
    const store = createFactoryStore({ get: readOne, list: async () => [] })

    await expect(store.getItem('a')).rejects.toThrow('boom')
    expect(store.itemById('a')).toBeUndefined()
    // A rejected read leaves nothing behind, so the next call retries.
    await expect(store.getItem('a')).resolves.toMatchObject({ id: 'a' })
    expect(readOne).toHaveBeenCalledTimes(2)
  })
})

describe('factory store polling', () => {
  it('activeAndIdleCadence', async () => {
    vi.useFakeTimers()
    let items = [item('a', { state: 'running' })]
    const list = vi.fn(async () => items)
    const store = createFactoryStore({ list, now: () => 1, pollSeam: { document: fakeDocument(() => false).doc } })
    store.start()

    await vi.advanceTimersByTimeAsync(0)
    expect(list).toHaveBeenCalledTimes(1)
    // Live work polls on the active cadence.
    await vi.advanceTimersByTimeAsync(2000)
    expect(list).toHaveBeenCalledTimes(2)

    items = [item('a', { state: 'closed' })]
    await vi.advanceTimersByTimeAsync(2000)
    expect(list).toHaveBeenCalledTimes(3)
    // Nothing needs a person or a worker now, so the idle cadence takes over.
    await vi.advanceTimersByTimeAsync(2000)
    expect(list).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(15000)
    expect(list).toHaveBeenCalledTimes(4)

    store.stop()
    await vi.advanceTimersByTimeAsync(60000)
    expect(list).toHaveBeenCalledTimes(4)
    vi.useRealTimers()
  })

  it('visibilityAndStopSuspendPolling', async () => {
    vi.useFakeTimers()
    let hidden = false
    const { doc, listeners } = fakeDocument(() => hidden)
    const list = vi.fn(async () => [] as FactoryItem[])
    const store = createFactoryStore({ list, now: () => 1, pollSeam: { document: doc } })

    store.start()
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(list).toHaveBeenCalledTimes(1)
    // A second start adds no second timer or listener.
    expect(listeners.size).toBe(1)

    hidden = true
    fire(listeners)
    await vi.advanceTimersByTimeAsync(30000)
    expect(list).toHaveBeenCalledTimes(1)

    hidden = false
    fire(listeners)
    await vi.advanceTimersByTimeAsync(0)
    expect(list).toHaveBeenCalledTimes(2)

    store.stop()
    store.stop()
    expect(listeners.size).toBe(0)
    await vi.advanceTimersByTimeAsync(60000)
    expect(list).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
  })

  it('concurrentRefreshCoalesces', async () => {
    let finish!: (items: FactoryItem[]) => void
    const list = vi.fn(() => new Promise<FactoryItem[]>((resolve) => { finish = resolve }))
    const store = createFactoryStore({ list, now: () => 9 })

    const first = store.refresh()
    const second = store.refresh()
    expect(list).toHaveBeenCalledTimes(1)

    finish([item('a')])
    await Promise.all([first, second])
    expect(list).toHaveBeenCalledTimes(1)
    expect(get(store.state).items.map((i) => i.id)).toEqual(['a'])

    // Once settled, the next refresh reads again.
    const third = store.refresh()
    expect(list).toHaveBeenCalledTimes(2)
    finish([])
    await third
  })
})
