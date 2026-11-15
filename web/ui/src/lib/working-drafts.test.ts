import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  WORKING_DRAFT_DEBOUNCE_MS,
  WorkingDraftConflictError,
  WorkingDraftController,
  createMemoryWorkingDraftStore,
  emptyWorkingDraftContent,
  type WorkingDraft,
  type WorkingDraftContent,
  type WorkingDraftConvertRequest,
  type WorkingDraftCreateRequest,
  type WorkingDraftRemoveRequest,
  type WorkingDraftRequests,
  type WorkingDraftSaveRequest,
  type WorkingDraftStore,
  type WorkingDraftTimer,
} from './working-drafts'

const draftContent = (patch: Partial<WorkingDraftContent> = {}): WorkingDraftContent => ({
  ...emptyWorkingDraftContent(),
  ...patch,
})

/** The saved draft every conversion test reviews. */
const CONVERTED = draftContent({ title: 'Ready to convert', plan: 'Do it', repoId: 'repo', noRequiredCiAcknowledged: true })

/** One reviewed conversion of a saved draft. */
function review(body: WorkingDraftConvertRequest['factoryRequest'], version: number, patch: Partial<WorkingDraftConvertRequest> = {}): WorkingDraftConvertRequest {
  return { requestId: 'conv-1', expectedVersion: version, factoryRequest: body, settingsVersion: 'settings-v1', ...patch }
}

/** The handoff body a conversion submits for `CONVERTED`. */
const handoff = (requestId = 'conv-1'): WorkingDraftConvertRequest['factoryRequest'] => ({
  requestId,
  title: CONVERTED.title,
  plan: CONVERTED.plan,
  spec: '',
  config: { repoId: 'repo', projectRoot: '/p', native: true, implementerAuto: false, noRequiredCiAcknowledged: true },
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
    convert: [] as { id: string; req: WorkingDraftConvertRequest }[],
    remove: [] as { id: string; req: WorkingDraftRemoveRequest }[],
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
    convert: vi.fn(async (id: string, req: WorkingDraftConvertRequest) => {
      calls.convert.push({ id, req })
      // The bridge converts the draft it holds, so the content is the draft's.
      const content = held.draft ? held.draft.content : draftContent()
      const draft = { ...acknowledged(id, req.expectedVersion + 1, content, 'converted'), factoryItemId: 'item-1' }
      held.draft = draft
      return { draft }
    }),
    remove: vi.fn(async (id: string, req: WorkingDraftRemoveRequest) => {
      calls.remove.push({ id, req })
      // The tombstone keeps the content and moves the version on, exactly as
      // the bridge's delete does.
      held.draft = acknowledged(id, req.expectedVersion + 1, held.draft ? held.draft.content : draftContent(), 'deleted')
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

  it('a confirmed refusal frees the request for the next correction', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = makeController(bridge, storage, 'tab-1')
    await tab.load('/p', null)

    const first = draftContent({ title: 'first' })
    await tab.edit(first)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(tab.view()).toMatchObject({ draftId: 'd1', baseVersion: 1, saved: true })

    // The bridge refuses the next save outright: the body is over its limit.
    const online = bridge.requests.save
    const refused = vi.fn(async (_id: string, _req: WorkingDraftSaveRequest) => {
      throw Object.assign(new Error('plan and specification exceed 1 MiB'), { status: 400 })
    })
    bridge.requests.save = refused

    const huge = draftContent({ title: 'first', plan: 'far too long' })
    await tab.edit(huge)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(refused).toHaveBeenCalledTimes(1)
    const refusedId = refused.mock.calls[0][1].requestId

    // The refusal is a visible error, not "offline": the bridge answered, the
    // content is kept, and no frozen request holds the draft hostage.
    const refusedView = tab.view()
    expect(refusedView).toMatchObject({ offline: false, error: 'plan and specification exceed 1 MiB', pending: true, saved: false })
    expect(refusedView.local).toEqual(huge)
    const [record] = await recordsFor(storage)
    expect(record.pending).toBeNull()

    // The user shortens the plan: the correction goes out under a fresh id.
    bridge.requests.save = online
    const corrected = draftContent({ title: 'first', plan: 'shorter now' })
    await tab.edit(corrected)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.save).toHaveLength(1)
    expect(bridge.calls.save[0].req.requestId).not.toBe(refusedId)
    expect(bridge.calls.save[0].req.content).toEqual(corrected)
    expect(tab.view()).toMatchObject({ saved: true, error: null, offline: false })
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

  it('a later session can recover a draft stranded with no project', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    // A draft from another project must never be offered to this one.
    await storage.write({
      installationId: 'inst-1',
      projectRoot: '/elsewhere',
      draftId: 'd9',
      editorId: 'editor-c',
      content: draftContent({ title: 'Another project' }),
      base: null,
      baseVersion: 0,
      server: null,
      state: 'editing',
      pending: null,
      updatedAt: 900,
    })

    const { controller: first } = makeController(bridge, storage, 'editor-a')
    await first.load(null)
    const typed = draftContent({ title: 'Typed with no project', plan: 'Still here' })
    await first.edit(typed)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 2)
    // No project was chosen, so nothing was ever sent: the content exists only
    // on this device, under the first session's own keys.
    expect(bridge.calls.create).toHaveLength(0)
    first.destroy()

    // A reload is a fresh controller: a new editor tab and a new local draft
    // key, neither of which the earlier record matches.
    const { controller: reloaded } = makeController(bridge, storage, 'editor-b')
    const restored = await reloaded.load(null)
    expect(restored.local).toBeNull()
    // The record is still this page's scope, so it is offered rather than
    // stranded, and nothing from another project is.
    expect(restored.recoverable.map((r) => r.editorId)).toEqual(['editor-a'])
    expect(restored.recoverable[0]).toMatchObject({ content: typed, pending: false })

    // Adopting it yields the original content, through the existing path.
    await reloaded.adopt('editor-a')
    expect(reloaded.view()).toMatchObject({ local: typed, pending: true })
    reloaded.destroy()
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

describe('working draft conversion', () => {
  afterEach(() => vi.useRealTimers())

  /** A tab with `CONVERTED` saved at version 1. */
  async function savedTab(bridge: ReturnType<typeof fakeBridge>, storage: WorkingDraftStore, editorId = 'tab-1') {
    const made = makeController(bridge, storage, editorId)
    await made.controller.load('/p', null)
    await made.controller.edit(CONVERTED)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(made.controller.view()).toMatchObject({ baseVersion: 1, saved: true })
    return made
  }

  it('convertedDraftCannotBeAutosaved', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = await savedTab(bridge, storage)

    const converted = await tab.convert(review(handoff(), 1))
    expect(converted).toMatchObject({ state: 'converted', factoryItemId: 'item-1', pending: false, error: null })
    expect(bridge.calls.convert).toHaveLength(1)
    expect(bridge.calls.convert[0].req).toMatchObject({ requestId: 'conv-1', expectedVersion: 1, settingsVersion: 'settings-v1' })
    expect(bridge.calls.convert[0].req.factoryRequest.title).toBe(CONVERTED.title)

    // A converted draft is read-only: later typing stays here and is never
    // written over the item the draft became.
    const after = draftContent({ ...CONVERTED, plan: 'Do it, then more' })
    await tab.edit(after)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 3)
    await tab.flush()
    expect(bridge.calls.save).toHaveLength(0)
    expect(bridge.calls.create).toHaveLength(1)
    expect(tab.view()).toMatchObject({ state: 'converted', local: after, pending: true })

    // And converting it again creates nothing at all.
    const again = await tab.convert(review(handoff('conv-2'), tab.view().baseVersion, { requestId: 'conv-2' }))
    expect(again.error).toMatch(/already a factory item/i)
    expect(bridge.calls.convert).toHaveLength(1)
  })

  it('lostConversionResponseReconciles', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = await savedTab(bridge, storage)

    // The bridge took the conversion; this tab never saw the answer.
    bridge.requests.convert = vi.fn(async () => {
      throw new Error('socket closed')
    })
    const lost = await tab.convert(review(handoff(), 1))
    expect(lost).toMatchObject({ state: 'editing', offline: true, error: 'socket closed' })
    // The review is frozen on this device rather than replaced.
    expect(lost.conversion).toMatchObject({ requestId: 'conv-1', expectedVersion: 1, settingsVersion: 'settings-v1' })
    const [record] = await recordsFor(storage)
    expect(record.conversion).toMatchObject({ requestId: 'conv-1', factoryRequest: { title: CONVERTED.title } })

    // A reload reconciles: the draft is converted, so nothing is sent again.
    bridge.held.draft = { ...acknowledged('d1', 2, CONVERTED, 'converted'), factoryItemId: 'item-1' }
    const convert = vi.fn(async () => {
      throw new Error('a reconciled conversion must not be retried')
    })
    bridge.requests.convert = convert
    const { controller: reopened } = makeController(bridge, storage, 'tab-1')
    const reconciled = await reopened.load('/p', 'd1')
    expect(reconciled).toMatchObject({ state: 'converted', factoryItemId: 'item-1', conversion: null, offline: false })
    expect(convert).not.toHaveBeenCalled()
    expect((await recordsFor(storage)).map((r) => r.conversion)).toEqual([null])
  })

  it('simultaneousConvertReturnsOneItem', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tabA } = await savedTab(bridge, storage, 'tab-a')

    // A second tab opened the same saved version.
    const { controller: tabB } = makeController(bridge, storage, 'tab-b')
    expect(await tabB.load('/p', 'd1')).toMatchObject({ baseVersion: 1, saved: true })

    const first = await tabA.convert(review(handoff(), 1))
    expect(first).toMatchObject({ state: 'converted', factoryItemId: 'item-1' })

    // The other tab's conversion arrives after the draft is factory work: the
    // bridge reports its current state, and no second item is created.
    bridge.requests.convert = vi.fn(async (id: string, req: WorkingDraftConvertRequest) => {
      bridge.calls.convert.push({ id, req })
      throw new WorkingDraftConflictError('draft is converted', {
        ...acknowledged(id, 2, CONVERTED, 'converted'),
        factoryItemId: 'item-1',
      })
    })
    const second = await tabB.convert(review(handoff('conv-2'), tabB.view().baseVersion, { requestId: 'conv-2' }))
    expect(second).toMatchObject({ state: 'converted', factoryItemId: 'item-1', conflict: false, pending: false })
    expect(second.factoryItemId).toBe(first.factoryItemId)
    expect(bridge.calls.convert.map((c) => c.req.requestId)).toEqual(['conv-1', 'conv-2'])

    // Neither tab can autosave over the item afterwards.
    const edited = draftContent({ ...CONVERTED, title: 'Ready to convert, edited' })
    await tabA.edit(edited)
    await tabB.edit(edited)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 2)
    expect(bridge.calls.save).toHaveLength(0)
  })

  it('validationErrorKeepsWorkingDraft', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab } = await savedTab(bridge, storage)

    // A confirmed refusal: the bridge created nothing and said why.
    bridge.requests.convert = vi.fn(async () => {
      throw Object.assign(new Error('title is too long'), { status: 400 })
    })
    const refused = await tab.convert(review(handoff(), 1))
    expect(refused).toMatchObject({ state: 'editing', error: 'title is too long', pending: false, conversion: null })
    expect(refused.local).toEqual(CONVERTED)
    expect(refused.base).toEqual(CONVERTED)
    expect((await recordsFor(storage)).map((r) => r.conversion)).toEqual([null])

    // The draft is still editable, and it still autosaves.
    const shortened = draftContent({ ...CONVERTED, title: 'Short title' })
    await tab.edit(shortened)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(bridge.calls.save).toHaveLength(1)
    expect(tab.view().saved).toBe(true)

    // Nothing was left holding the refused request: the next attempt is new.
    bridge.requests.convert = vi.fn(async (id: string, req: WorkingDraftConvertRequest) => {
      bridge.calls.convert.push({ id, req })
      return { draft: { ...acknowledged(id, req.expectedVersion + 1, shortened, 'converted'), factoryItemId: 'item-2' } }
    })
    const converted = await tab.convert(review(handoff('conv-2'), tab.view().baseVersion, { requestId: 'conv-2' }))
    expect(converted).toMatchObject({ state: 'converted', factoryItemId: 'item-2', error: null })
    expect(bridge.calls.convert.map((c) => c.req.requestId)).toEqual(['conv-2'])
  })
})

describe('working draft removal', () => {
  afterEach(() => vi.useRealTimers())

  /** A tab with one saved draft: id 'd1', version 1, titled 'No longer needed'. */
  async function savedTab(bridge: ReturnType<typeof fakeBridge>, storage: WorkingDraftStore, editorId = 'tab-1') {
    const made = makeController(bridge, storage, editorId)
    const typed = draftContent({ title: 'No longer needed', plan: 'Kept anyway' })
    await made.controller.load('/p', null)
    await made.controller.edit(typed)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS)
    expect(made.controller.view()).toMatchObject({ draftId: 'd1', baseVersion: 1, saved: true })
    return { ...made, typed }
  }

  it('removes with a frozen request and keeps the local copy', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab, typed } = await savedTab(bridge, storage)

    const removed = await tab.remove()
    // The frozen request reached the bridge with the version it expected.
    expect(bridge.calls.remove).toHaveLength(1)
    expect(bridge.calls.remove[0]).toMatchObject({ id: 'd1' })
    expect(bridge.calls.remove[0].req).toMatchObject({ requestId: expect.any(String), expectedVersion: 1 })
    // The tombstone is the draft's state, and the local copy is its record.
    expect(removed).toMatchObject({ state: 'deleted', conflict: false, error: null, offline: false, removal: null })
    expect(removed.local).toEqual(typed)
    expect(removed.base).toEqual(typed)
    const [record] = await recordsFor(storage)
    expect([record.state, record.content, record.removal]).toEqual(['deleted', typed, null])

    // The tombstone ends autosave: later typing stays here and is never sent.
    const after = draftContent({ ...typed, title: 'Typed after the removal' })
    await tab.edit(after)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 3)
    await tab.flush()
    expect(bridge.calls.save).toHaveLength(0)
    expect(bridge.calls.remove).toHaveLength(1)
    expect((await recordsFor(storage))[0].content).toEqual(after)
  })

  it('an unknown outcome replays the same request', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab, typed } = await savedTab(bridge, storage)
    const serving = bridge.requests.remove

    const attempts: WorkingDraftRemoveRequest[] = []
    bridge.requests.remove = vi.fn(async (_id: string, req: WorkingDraftRemoveRequest): Promise<WorkingDraft | null> => {
      attempts.push(req)
      // First the answer is lost; then the bridge answers with a 5xx.
      if (attempts.length === 1) throw new Error('network down')
      throw Object.assign(new Error('gateway exploded'), { status: 502 })
    })

    const lost = await tab.remove()
    expect(lost).toMatchObject({ state: 'editing', offline: true, error: 'network down' })
    expect(lost.local).toEqual(typed)
    expect(attempts).toHaveLength(1)
    // The request is frozen on this device before it is sent.
    const [record] = await recordsFor(storage)
    expect(record.removal).toMatchObject({ requestId: attempts[0].requestId, expectedVersion: 1 })

    // A retry sends the same id and version, not a new request.
    await tab.remove()
    expect(attempts).toHaveLength(2)
    expect(attempts[1].requestId).toBe(attempts[0].requestId)
    expect(attempts[1].expectedVersion).toBe(attempts[0].expectedVersion)

    // While the outcome is open, an edit stays local: no save races the removal.
    const edited = draftContent({ ...typed, title: 'Edited while the removal is open' })
    await tab.edit(edited)
    await vi.advanceTimersByTimeAsync(WORKING_DRAFT_DEBOUNCE_MS * 3)
    expect(bridge.calls.save).toHaveLength(0)
    // The reconnect hook replays the removal too, still under the same id.
    await tab.flush()
    expect(attempts).toHaveLength(3)
    expect(attempts[2].requestId).toBe(attempts[0].requestId)

    // The bridge comes back: the replayed request deletes, and the copy stays.
    bridge.requests.remove = serving
    await tab.flush()
    expect(bridge.calls.remove).toHaveLength(1)
    expect(bridge.calls.remove[0].req.requestId).toBe(attempts[0].requestId)
    expect(bridge.calls.remove[0].req.expectedVersion).toBe(1)
    expect(tab.view()).toMatchObject({ state: 'deleted', offline: false, error: null })
    expect(tab.view().local).toEqual(edited)
  })

  it('a stale version refreshes instead of deleting', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab, typed } = await savedTab(bridge, storage)

    const theirs = draftContent({ title: 'theirs', plan: 'their plan' })
    const attempts: WorkingDraftRemoveRequest[] = []
    bridge.requests.remove = vi.fn(async (_id: string, req: WorkingDraftRemoveRequest): Promise<WorkingDraft | null> => {
      attempts.push(req)
      throw new WorkingDraftConflictError('stale version', acknowledged('d1', 4, theirs))
    })

    const conflicted = await tab.remove()
    expect(attempts).toHaveLength(1)
    // Nothing was deleted: the bridge's current draft replaces the removal.
    expect(conflicted).toMatchObject({ state: 'editing', conflict: true, error: null, removal: null })
    expect(conflicted.server).toEqual(theirs)
    expect(conflicted.local).toEqual(typed)
    const [record] = await recordsFor(storage)
    expect([record.state, record.removal]).toEqual(['editing', null])

    // Resolving the conflict sends against the version the bridge reported.
    bridge.requests.save = vi.fn(async (id: string, req: WorkingDraftSaveRequest) => {
      bridge.calls.save.push({ id, req })
      return acknowledged(id, req.expectedVersion + 1, req.content)
    })
    await tab.keepLocal()
    expect(bridge.calls.save[0].req).toMatchObject({ expectedVersion: 4, content: typed })
    expect(tab.view()).toMatchObject({ conflict: false, saved: true })
    // The removal was never re-sent behind the user's back.
    expect(attempts).toHaveLength(1)
  })

  it('a reply already saying deleted settles the removal', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab, typed } = await savedTab(bridge, storage)

    bridge.requests.remove = vi.fn(async (_id: string, _req: WorkingDraftRemoveRequest): Promise<WorkingDraft | null> => {
      // The bridge already holds the tombstone, and its refusal says so.
      throw new WorkingDraftConflictError('draft is already deleted', acknowledged('d1', 2, typed, 'deleted'))
    })
    const done = await tab.remove()
    expect(done).toMatchObject({ state: 'deleted', conflict: false, error: null, offline: false, removal: null })
    expect(done.local).toEqual(typed)
  })

  it('a confirmed refusal is a visible error that frees the request', async () => {
    vi.useFakeTimers()
    const bridge = fakeBridge()
    const storage = createMemoryWorkingDraftStore()
    const { controller: tab, typed } = await savedTab(bridge, storage)

    // The project is no longer registered, so the bridge has no such draft.
    const refused = vi.fn(async (_id: string, _req: WorkingDraftRemoveRequest): Promise<WorkingDraft | null> => {
      throw Object.assign(new Error('working draft not found: project is not registered'), { status: 404 })
    })
    bridge.requests.remove = refused

    const answered = await tab.remove()
    expect(answered).toMatchObject({ state: 'editing', offline: false, conflict: false })
    expect(answered.error).toBe('working draft not found: project is not registered')
    expect(answered.local).toEqual(typed)
    expect(answered.removal).toBeNull()
    // The refusal frees the frozen request: the next attempt is a new one.
    const fresh = await tab.remove()
    expect(refused).toHaveBeenCalledTimes(2)
    expect(vi.mocked(refused).mock.calls[1][1].requestId).not.toBe(vi.mocked(refused).mock.calls[0][1].requestId)
    expect(fresh.local).toEqual(typed)
  })
})
