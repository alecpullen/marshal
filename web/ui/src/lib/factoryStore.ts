import { get, writable, type Readable } from 'svelte/store'
import { errMessage, getFactoryItem, listFactoryItems } from './api'
import { createFactoryPoller, type FactoryItem, type PollSeam } from './factory'
import { isFactoryActive } from './studio/model'

/**
 * The shell-level factory list: one store behind every factory view, so the
 * views stop polling for themselves. It keeps three things honest that a
 * per-view poller cannot:
 *
 *  - a command's own result is never overwritten by a list read that started
 *    before it (a global mutation revision), nor by a single-item read that
 *    started before it (a per-ID revision);
 *  - `loading` means "no authoritative snapshot yet" and `refreshing` means
 *    "we have one and are checking again", so a refresh never blanks a list;
 *  - a failed read keeps the items and `lastUpdated` it had, surfaces the
 *    error, and still rejects so the poller's backoff keeps working.
 *
 * The wire shapes and the poller come from `lib/factory`; this file owns no
 * transport, no event stream and no view-specific filtering.
 */
export interface FactoryState {
  items: FactoryItem[]
  loading: boolean
  refreshing: boolean
  error: string | null
  lastUpdated: number | null
}
export interface FactoryStore {
  state: Readable<FactoryState>
  start(): void
  stop(): void
  refresh(): Promise<void>
  getItem(id: string): Promise<FactoryItem>
  upsert(item: FactoryItem): void
  itemById(id: string): FactoryItem | undefined
}
export interface FactoryStoreOptions {
  list?: () => Promise<FactoryItem[]>
  get?: (id: string) => Promise<FactoryItem>
  now?: () => number
  pollSeam?: PollSeam
}

export function createFactoryStore(options: FactoryStoreOptions = {}): FactoryStore {
  const list = options.list ?? listFactoryItems
  const readOne = options.get ?? getFactoryItem
  const now = options.now ?? Date.now
  const state = writable<FactoryState>({ items: [], loading: false, refreshing: false, error: null, lastUpdated: null })

  /** Bumped by every command upsert; a list read started before the bump is stale. */
  let revision = 0
  /** Per-item command revisions, so one item's upsert cannot discard another item's read. */
  const itemRevisions = new Map<string, number>()
  /** The single in-flight list read, and the in-flight read per missing item. */
  let listRead: Promise<void> | null = null
  const itemReads = new Map<string, Promise<FactoryItem>>()
  /** Whether an accepted list snapshot has landed; until then a read is a first load. */
  let snapshot = false

  function itemById(id: string): FactoryItem | undefined {
    return get(state).items.find((i) => i.id === id)
  }

  /**
   * Replace an item in place or append it. A read that populates the cache and
   * a command's own result both land here, so the list never shows a duplicate.
   */
  function apply(item: FactoryItem) {
    state.update((s) => {
      const at = s.items.findIndex((i) => i.id === item.id)
      if (at < 0) return { ...s, items: [...s.items, item] }
      return { ...s, items: s.items.map((i, j) => (j === at ? item : i)) }
    })
  }

  async function readList(): Promise<void> {
    const startedAt = revision
    state.update((s) => ({ ...s, loading: !snapshot, refreshing: snapshot, error: null }))
    let items: FactoryItem[]
    try {
      items = await list()
    } catch (e) {
      // The items and their timestamp stand: a failed check says nothing about
      // what the last good snapshot held.
      state.update((s) => ({ ...s, loading: false, refreshing: false, error: errMessage(e) }))
      throw e
    }
    if (revision !== startedAt) {
      // A command landed while this read was in flight. Its result is the newer
      // truth; the next poll tick reconciles, so nothing is recorded from here.
      state.update((s) => ({ ...s, loading: false, refreshing: false }))
      return
    }
    snapshot = true
    state.set({ items, loading: false, refreshing: false, error: null, lastUpdated: now() })
  }

  /** One list read at a time: concurrent callers share the in-flight one. */
  function refresh(): Promise<void> {
    if (listRead) return listRead
    const read = readList().finally(() => {
      listRead = null
    })
    listRead = read
    return read
  }

  function getItem(id: string): Promise<FactoryItem> {
    const cached = itemById(id)
    if (cached) return Promise.resolve(cached)
    const inFlight = itemReads.get(id)
    if (inFlight) return inFlight
    const startedAt = itemRevisions.get(id) ?? 0
    const read = readOne(id)
      .then((item) => {
        if ((itemRevisions.get(id) ?? 0) !== startedAt) {
          // An upsert of this item landed first; its copy is the newer one.
          const current = itemById(id)
          if (current) return current
        }
        // A snapshot read is not a command mutation: adopting it must not
        // invalidate another read that is already in flight.
        apply(item)
        return item
      })
      .finally(() => {
        itemReads.delete(id)
      })
    itemReads.set(id, read)
    return read
  }

  function upsert(item: FactoryItem) {
    revision++
    itemRevisions.set(item.id, (itemRevisions.get(item.id) ?? 0) + 1)
    apply(item)
  }

  // The store records the error itself, so the poller's hook is only the
  // rejection it needs to keep counting failures and backing off.
  const poller = createFactoryPoller(
    refresh,
    () => get(state).items.some(isFactoryActive),
    () => {},
    options.pollSeam,
  )

  return {
    state,
    // The poller's own start/stop guards make these idempotent: a second start
    // never doubles the timer, and a stop after a stop has nothing to release.
    start: () => poller.start(),
    stop: () => poller.stop(),
    refresh,
    getItem,
    upsert,
    itemById,
  }
}
