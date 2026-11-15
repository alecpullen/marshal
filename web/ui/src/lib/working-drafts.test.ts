import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  WORKING_DRAFT_DEBOUNCE_MS,
  WorkingDraftConflictError,
  WorkingDraftController,
  createMemoryWorkingDraftStore,
  emptyWorkingDraftContent,
  type WorkingDraft,
  type WorkingDraftContent,
  type WorkingDraftCreateRequest,
  type WorkingDraftRequests,
  type WorkingDraftSaveRequest,
  type WorkingDraftStore,
  type WorkingDraftTimer,
} from './working-drafts'

const draftContent = (patch: Partial<WorkingDraftContent> = {}): WorkingDraftContent => ({
  ...emptyWorkingDraftContent(),
  ...patch,
})

const acknowledged = (
  id: string,
  version: number,
  content: WorkingDraftContent,
  state: WorkingDraft['state'] = 'editing',
): WorkingDraft => ({ id, projectRoot: '/p', version, state, content, updatedAt: '2026-01-01T00:00:00Z' })

/**
 * The bridge double. It keeps the draft it would answer a read with, and
 * records what each call carried, so a test can assert the request identity
 * rather than just "something was sent".
 */
function fakeBridge() {
  const calls = {
    create: [] as WorkingDraftCreateRequest[],
    save: [] as { id: string; req: WorkingDraftSaveRequest }[],
    get: [] as string[],
  }
  const held: { draft: WorkingDraft | null } = { draft: null }
  const requests: WorkingDraftRequests = {
    installation: vi.fn(async () => 'inst-1'),
    create: vi.fn(async (req: WorkingDraftCreateRequest) => {
      calls.create.push(req)
      held.draft = acknowledged('d1', 1, req.content)
      return held.draft
    }),
    get: vi.fn(async (id: string) => {
      calls.get.push(id)
      return held.draft ?? acknowledged(id, 0, draftContent())
    }),
    save: vi.fn(async (id: string, req: WorkingDraftSaveRequest) => {
      calls.save.push({ id, req })
      held.draft = acknowledged(id, req.expectedVersion + 1, req.content)
      return held.draft
    }),
  }
  return { requests, calls, held }
}

/** Records the delay it was asked for, then uses vitest's fake clock. */
function fakeTimer() {
  const delays: number[] = []
  const timer: WorkingDraftTimer = {
    setTimeout: (run, ms) => {
      delays.push(ms)
      return setTimeout(run, ms)
    },
    clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
  }
  return { timer, delays }
}

function makeController(bridge: ReturnType<typeof fakeBridge>, storage: WorkingDraftStore, editorId: string) {
  const { timer, delays } = fakeTimer()
  let ids = 0
  const controller = new WorkingDraftController({
    storage,
    requests: bridge.requests,
    editorId,
    installationId: () => 'inst-1',
    now: () => 1000,
    timer,
    newId: () => `${editorId}-${++ids}`,
  })
  return { controller, delays }
}

const recordsFor = (storage: WorkingDraftStore, projectRoot = '/p') => storage.list({ installationId: 'inst-1', projectRoot })

describe('working draft autosave', () => {
  afterEach(() => vi.useRealTimers())

  it('autosaves and reopens across contexts', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tabA, delays } = makeController(bridge, storage, 'tab-a')
    await tabA.load('/p', null)

    const first = draftContent({ title: 'Ship the thing' })
    await tabA.edit(first)

    // The edit is on the device before the debounce, and nothing was sent yet.
    expect(delays).toEqual([WORKING_DRAFT_DEBOUNCE_MS])
    expect((await recordsFor(storage)).map((r) => r.content)).toEqual([first])
    expect(bridge.calls.create).toHaveLength(0)
    expect(tabA.view()).toMatchObject({ pending: true, saving: false, saved: false })

    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.create).toHaveLength(1)
    expect(bridge.calls.create[0]).toMatchObject({ projectRoot: '/p', content: first })
    expect(tabA.view()).toMatchObject({ draftId: 'd1', baseVersion: 1, pending: false, saved: true })

    // The bridge named the draft, so the record moved with it.
    expect((await recordsFor(storage)).map((r) => r.draftId)).toEqual(['d1'])

    // An edit made while the bridge is unreachable stays on the device.
    const online = bridge.requests.save
    bridge.requests.save = vi.fn(async () => {
      throw new Error('network down')
    })
    const second = draftContent({ title: 'Ship the thing, carefully' })
    await tabA.edit(second)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(tabA.view()).toMatchObject({ offline: true, pending: true, saved: false })
    expect((await recordsFor(storage)).map((r) => r.content)).toEqual([second])

    // The tab closes. Reopening it restores its own copy and the unsent request.
    const { controller: reopenedTab } = makeController(bridge, storage, 'tab-a')
    const restored = await reopenedTab.load('/p', 'd1')
    expect(restored.local).toEqual(second)
    expect(restored.base).toEqual(first)
    expect(restored.pending).toBe(true)
    expect(restored.recoverable).toEqual([])
    reopenedTab.destroy()

    // A different tab (a new browser context) is offered the copy instead of
    // silently overwriting it.
    const { controller: otherTab } = makeController(bridge, storage, 'tab-b')
    const opened = await otherTab.load('/p', 'd1')
    expect(opened.local).toEqual(first)
    expect(opened.recoverable.map((r) => r.editorId)).toEqual(['tab-a'])
    expect(opened.recoverable[0]).toMatchObject({ content: second, pending: true })

    bridge.requests.save = online
    await otherTab.adopt('tab-a')
    expect(otherTab.view()).toMatchObject({ local: second, pending: true, saved: false })
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.save).toHaveLength(1)
    expect(bridge.calls.save[0].req).toMatchObject({ expectedVersion: 1, content: second })
    expect(otherTab.view()).toMatchObject({ baseVersion: 2, saved: true })
  })

  it('does not mark a newer edit saved', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    await tab.load('/p', null)

    let release!: (draft: WorkingDraft) => void
    bridge.requests.create = vi.fn(
      () =>
        new Promise<WorkingDraft>((resolve) => {
          release = resolve
        }),
    )

    const first = draftContent({ title: 'first' })
    const second = draftContent({ title: 'first, then more' })
    await tab.edit(first)
    const flushed = tab.flush()
    await vi.advanceTimersByTimeAsync(0)
    expect(tab.view().saving).toBe(true)

    // The user keeps typing while the first request is in flight.
    await tab.edit(second)
    release(acknowledged('d1', 1, first))
    await flushed

    // The ack is for `first`, so `second` is still unsaved — and still here.
    expect(tab.view()).toMatchObject({ base: first, baseVersion: 1, local: second, pending: true, saved: false })

    // It goes out on its own, against the version the ack established.
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.save).toHaveLength(1)
    expect(bridge.calls.save[0].req).toMatchObject({ expectedVersion: 1, content: second })
    expect(tab.view()).toMatchObject({ baseVersion: 2, saved: true })
  })

  it('retries the identical pending request', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    await tab.load('/p', null)

    const online = bridge.requests.create
    // Only what actually reached the bridge is logged in `calls`; this double
    // records the attempts it refused itself.
    const offline = vi.fn(async (_req: WorkingDraftCreateRequest) => {
      throw new Error('network down')
    })
    bridge.requests.create = offline

    const first = draftContent({ title: 'first' })
    await tab.edit(first)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(offline).toHaveBeenCalledTimes(1)
    expect(tab.view()).toMatchObject({ offline: true, pending: true, saved: false })

    // A newer edit does not rewrite the outstanding request.
    const second = draftContent({ title: 'second' })
    await tab.edit(second)
    await tab.flush()
    expect(offline).toHaveBeenCalledTimes(2)
    expect(offline.mock.calls[1][0]).toEqual(offline.mock.calls[0][0])

    // On reconnect the outstanding request is replayed as-is …
    bridge.requests.create = online
    await tab.flush()
    expect(bridge.calls.create).toHaveLength(1)
    expect(bridge.calls.create[0].requestId).toBe(offline.mock.calls[0][0].requestId)
    expect(bridge.calls.create[0].content).toEqual(first)

    // … and only then does the newer content go out, under a new id.
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.save).toHaveLength(1)
    expect(bridge.calls.save[0].req.requestId).not.toBe(bridge.calls.create[0].requestId)
    expect(bridge.calls.save[0].req.content).toEqual(second)
    expect(tab.view().saved).toBe(true)
  })

  it('offline edits remain local', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    await tab.load('/p', null)

    const online = bridge.requests.create
    bridge.requests.create = vi.fn(async () => {
      throw new Error('offline')
    })
    const typed = draftContent({ title: 'typed on a train', plan: 'still here' })
    await tab.edit(typed)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)

    expect(tab.view()).toMatchObject({ offline: true, error: 'offline', local: typed, pending: true, saved: false })
    const [record] = await recordsFor(storage)
    expect(record.content).toEqual(typed)
    expect(record.pending).toMatchObject({ kind: 'create', content: typed })

    // Reconnecting sends what was kept, not a fresh blank draft.
    bridge.requests.create = online
    await tab.flush()
    expect(bridge.calls.create[0].content).toEqual(typed)
    expect(tab.view()).toMatchObject({ offline: false, error: null, saved: true })
  })

  it('conflict keeps all versions', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    await tab.load('/p', null)

    const saved = draftContent({ title: 'saved' })
    await tab.edit(saved)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(tab.view().saved).toBe(true)

    // Another device saved version 3 while this tab was open.
    const theirs = draftContent({ title: 'theirs', plan: 'their plan' })
    bridge.requests.save = vi.fn(async () => {
      throw new WorkingDraftConflictError('stale version', acknowledged('d1', 3, theirs))
    })
    const mine = draftContent({ title: 'mine', plan: 'my plan' })
    await tab.edit(mine)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)

    const conflicted = tab.view()
    expect(conflicted).toMatchObject({ conflict: true, saved: false, pending: true, baseVersion: 1 })
    expect(conflicted.base).toEqual(saved)
    expect(conflicted.local).toEqual(mine)
    expect(conflicted.server).toEqual(theirs)
    // All three survive in the record, so a reload still offers the choice.
    const [record] = await recordsFor(storage)
    expect(record.base).toEqual(saved)
    expect(record.content).toEqual(mine)
    expect(record.server).toMatchObject({ version: 3, content: theirs })

    // Keeping the local copy rebases onto the server's version and resends.
    bridge.requests.save = vi.fn(async (id: string, req: WorkingDraftSaveRequest) => {
      bridge.calls.save.push({ id, req })
      return acknowledged(id, req.expectedVersion + 1, req.content)
    })
    await tab.keepLocal()
    expect(bridge.calls.save[0].req).toMatchObject({ expectedVersion: 3, content: mine })
    expect(tab.view()).toMatchObject({ conflict: false, baseVersion: 4, saved: true })

    // And taking the server's copy drops only the local edit.
    bridge.requests.save = vi.fn(async () => {
      throw new WorkingDraftConflictError('stale version', acknowledged('d1', 9, theirs))
    })
    await tab.edit(draftContent({ title: 'again' }))
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(tab.view().conflict).toBe(true)
    await tab.discardLocal()
    expect(tab.view()).toMatchObject({ conflict: false, server: null, local: theirs, base: theirs, baseVersion: 9, pending: false })
  })

  it('quota failure is visible', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const backing = createMemoryWorkingDraftStore()
    const refusing: WorkingDraftStore = {
      ...backing,
      write: async () => {
        throw new Error('QuotaExceededError: the quota has been exceeded')
      },
    }
    const { controller: tab } = makeController(bridge, refusing, 'tab-1')
    await tab.load('/p', null)

    const typed = draftContent({ title: 'too big to keep' })
    await tab.edit(typed)

    // The failure is reported instead of the edit disappearing.
    expect(tab.view().error).toMatch(/could not keep this draft in this browser/i)
    expect(tab.view().error).toMatch(/quota/i)
    expect(tab.view()).toMatchObject({ local: typed, pending: true, saved: false })
    expect(await recordsFor(backing)).toEqual([])

    // The content still reaches the bridge, and the failure stays visible.
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.create[0].content).toEqual(typed)
    expect(tab.view()).toMatchObject({ base: typed, saved: true })
    expect(tab.view().error).toMatch(/could not keep this draft in this browser/i)
  })

  it('stays local only before project selection', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    const unassigned = await tab.load(null)
    expect(unassigned).toMatchObject({ localOnly: true, projectRoot: null })

    const typed = draftContent({ title: 'no project yet' })
    await tab.edit(typed)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 2)
    expect(bridge.calls.create).toHaveLength(0)
    expect(bridge.calls.save).toHaveLength(0)

    // Kept on the device, with no project, until one is chosen.
    expect((await recordsFor(storage, '')).map((r) => r.content)).toEqual([typed])
    expect(tab.view().pending).toBe(true)

    const assigned = await tab.assignProject('/p')
    expect(assigned.localOnly).toBe(false)
    expect(bridge.calls.create).toHaveLength(1)
    expect(bridge.calls.create[0]).toMatchObject({ projectRoot: '/p', content: typed })
    expect(await recordsFor(storage, '')).toEqual([])
    expect((await recordsFor(storage)).map((r) => [r.projectRoot, r.draftId])).toEqual([['/p', 'd1']])
    expect(tab.view().saved).toBe(true)
  })

  it('migrates the local key atomically', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const migrate = vi.fn(storage.migrate)
    const watched: WorkingDraftStore = { ...storage, migrate }
    const { controller: tab } = makeController(bridge, watched, 'tab-1')
    await tab.load('/p', null)

    const typed = draftContent({ title: 'named later' })
    await tab.edit(typed)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)

    expect(migrate).toHaveBeenCalledTimes(1)
    const [from, to] = migrate.mock.calls[0]
    expect(from).toMatchObject({ installationId: 'inst-1', projectRoot: '/p', editorId: 'tab-1' })
    expect(from.draftId).toMatch(/^local-/)
    expect(to).toEqual({ projectRoot: '/p', draftId: 'd1' })

    // Exactly one record, under the new key: the old one is gone, not copied.
    const records = await recordsFor(storage)
    expect(records.map((r) => [r.draftId, r.content])).toEqual([['d1', typed]])
    expect(await storage.read({ installationId: 'inst-1', projectRoot: '/p', draftId: from.draftId, editorId: 'tab-1' })).toBeNull()

    // A refused move leaves the content under the old key and says so.
    const untouched = createMemoryWorkingDraftStore()
    const stuck: WorkingDraftStore = {
      ...untouched,
      migrate: async () => {
        throw new Error('transaction aborted')
      },
    }
    const { controller: other } = makeController(fakeBridge(), stuck, 'tab-2')
    await other.load('/p', null)
    const kept = draftContent({ title: 'still on this device' })
    await other.edit(kept)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)

    expect(other.view().error).toMatch(/could not keep this draft in this browser/i)
    const survivors = await recordsFor(untouched)
    expect(survivors).toHaveLength(1)
    expect(survivors[0].draftId).toMatch(/^local-/)
    expect(survivors[0].content).toEqual(kept)
  })

  it('does not resurrect deleted or stale drafts', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    await tab.load('/p', null)

    let release!: (draft: WorkingDraft) => void
    bridge.requests.create = vi.fn(
      () =>
        new Promise<WorkingDraft>((resolve) => {
          release = resolve
        }),
    )
    const typed = draftContent({ title: 'work in progress' })
    await tab.edit(typed)
    const flushed = tab.flush()
    await vi.advanceTimersByTimeAsync(0)

    // The bridge answers that the draft was deleted elsewhere.
    release(acknowledged('d1', 1, typed, 'deleted'))
    await flushed

    expect(tab.view()).toMatchObject({ state: 'deleted', saved: false, local: typed, base: null })
    // The local copy is the only one left, and nothing re-creates the draft.
    expect((await recordsFor(storage)).map((r) => [r.state, r.content])).toEqual([['deleted', typed]])
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 3)
    expect(bridge.calls.create).toHaveLength(0)
    expect(bridge.calls.save).toHaveLength(0)

    // A reply for a request the user already discarded is stale as well.
    const second = fakeBridge()
    const { controller: other } = makeController(second, createMemoryWorkingDraftStore(), 'tab-2')
    await other.load('/p', null)
    await other.edit(draftContent({ title: 'v1' }))
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(other.view()).toMatchObject({ baseVersion: 1, saved: true })

    let releaseSave!: (draft: WorkingDraft) => void
    second.requests.save = vi.fn(
      () =>
        new Promise<WorkingDraft>((resolve) => {
          releaseSave = resolve
        }),
    )
    await other.edit(draftContent({ title: 'v2' }))
    const pendingSave = other.flush()
    await vi.advanceTimersByTimeAsync(0)
    await other.discardLocal()
    releaseSave(acknowledged('d1', 2, draftContent({ title: 'v2' })))
    await pendingSave

    expect(other.view()).toMatchObject({ local: draftContent({ title: 'v1' }), baseVersion: 1, saved: true, pending: false })
  })
})
