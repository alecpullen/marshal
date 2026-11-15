/**
 * Factory working drafts: the client half of the draft-recovery contract
 * (spec §3, `web/bridge`'s working-drafts routes).
 *
 * A draft is edited in a browser that can be closed, reloaded, opened twice,
 * or lose its network mid-keystroke. So the browser is the first place a
 * draft is written: every keystroke lands in local storage before it is sent,
 * a pending request keeps its request id and its exact bytes until it is
 * acknowledged, and the server's answer only advances the saved base when it
 * is the answer to the request that was actually in flight.
 *
 * Three rules explain most of the code below.
 *
 *  - **Local first.** `edit` writes the record before the debounce fires, so a
 *    tab that closes mid-debounce still has the text. A storage failure is a
 *    visible error, never a lost edit.
 *  - **One pending request.** At most one request is unacknowledged at a time,
 *    and it is stored with the record. A retry after a reconnect sends that
 *    request again — same id, same body — so the bridge's replay rules make it
 *    idempotent instead of an overwrite.
 *  - **Identity is the bridge's.** Local records are keyed by the installation
 *    identity the authenticated bridge reports, plus project, draft and the
 *    editor tab. A URL is not an identity: two bridges on the same host would
 *    otherwise share (and destroy) each other's drafts, and a tab id must never
 *    be assumed to survive a browser restart.
 *
 * Everything here is framework-agnostic: no Svelte, no direct IndexedDB use in
 * the controller. Storage is an injected adapter, and the clock, timer, ids and
 * network calls are injected too, so the tests drive it with fake timers and an
 * in-memory double.
 */
import type { FactoryCreate, FactoryItem } from './factory'

/** The draft's editable content. Mirrors Go's `WorkingDraftContent`. */
export interface WorkingDraftContent {
  title: string
  plan: string
  spec: string
  repoId: string
  targetBranchOverride: string
  workspaceOverride: string
  implementerOverride: string
  reviewerOverride: string
  reviewerInstructions: string
  maxRoundsOverride: string
  ciPollOverride: string
  ciWaitOverride: string
  checksMode: WorkingDraftChecksMode
  expectedChecksText: string
  native: boolean
  nativeChosen: boolean
  implementerAuto: boolean
  noRequiredCiAcknowledged: boolean
}

/** A blank raw override that is left blank means "follow", never a value. */
export type WorkingDraftChecksMode = 'inherit' | 'none' | 'custom'

/** The bridge's state word for a draft; every response reports one. */
export type WorkingDraftState = 'editing' | 'converting' | 'converted' | 'deleted'

/** One working draft as the bridge stores it. Mirrors Go's `WorkingDraft`. */
export interface WorkingDraft {
  id: string
  projectRoot: string
  version: number
  state: WorkingDraftState
  content: WorkingDraftContent
  updatedAt: string
  factoryItemId?: string
  error?: string
}

export interface WorkingDraftCreateRequest {
  projectRoot: string
  requestId: string
  content: WorkingDraftContent
}

export interface WorkingDraftSaveRequest {
  requestId: string
  expectedVersion: number
  content: WorkingDraftContent
}

export interface WorkingDraftRemoveRequest {
  requestId: string
  expectedVersion: number
}

/**
 * One review, frozen: the request id, the draft version it was reviewed at, the
 * exact body it will create, and the project settings version it was read
 * against. A retry sends these bytes again; a new review is a new request.
 *
 * `settingsVersion` is optional because the bridge treats an absent version as
 * "do not check it": a caller that did not read the settings is not claiming
 * they are current. The controller always sends the version it reviewed.
 */
export interface WorkingDraftConvertRequest {
  requestId: string
  expectedVersion: number
  factoryRequest: FactoryCreate
  settingsVersion?: string
}

export interface WorkingDraftConvertResult {
  draft: WorkingDraft
  item?: FactoryItem
}

export function emptyWorkingDraftContent(): WorkingDraftContent {
  return {
    title: '',
    plan: '',
    spec: '',
    repoId: '',
    targetBranchOverride: '',
    workspaceOverride: '',
    implementerOverride: '',
    reviewerOverride: '',
    reviewerInstructions: '',
    maxRoundsOverride: '',
    ciPollOverride: '',
    ciWaitOverride: '',
    checksMode: 'inherit',
    expectedChecksText: '',
    native: false,
    nativeChosen: false,
    implementerAuto: false,
    noRequiredCiAcknowledged: false,
  }
}

const CONTENT_FIELDS = [
  'title',
  'plan',
  'spec',
  'repoId',
  'targetBranchOverride',
  'workspaceOverride',
  'implementerOverride',
  'reviewerOverride',
  'reviewerInstructions',
  'maxRoundsOverride',
  'ciPollOverride',
  'ciWaitOverride',
  'checksMode',
  'expectedChecksText',
  'native',
  'nativeChosen',
  'implementerAuto',
  'noRequiredCiAcknowledged',
] as const satisfies readonly (keyof WorkingDraftContent)[]

/** Content equality over the draft's own fields, so key order cannot matter. */
export function sameWorkingDraftContent(a: WorkingDraftContent | null, b: WorkingDraftContent | null): boolean {
  if (a === b) return true
  if (!a || !b) return false
  return CONTENT_FIELDS.every((field) => a[field] === b[field])
}

const cloneContent = (content: WorkingDraftContent | null): WorkingDraftContent | null => (content ? { ...content } : null)
const cloneDraft = (draft: WorkingDraft): WorkingDraft => ({ ...draft, content: { ...draft.content } })

/**
 * A request that has not been acknowledged yet, stored with the record so a
 * retry can send it again byte for byte.
 */
export interface PendingWorkingDraftRequest {
  requestId: string
  /** `create` until the bridge has assigned an id; the POST replays by id after that. */
  kind: 'create' | 'save'
  /** The exact bytes this request carries. A later edit must not rewrite them. */
  content: WorkingDraftContent
  expectedVersion: number
  enqueuedAt: number
}

/** What a local record is filed under: bridge installation, project, draft, editor. */
export interface WorkingDraftKey {
  installationId: string
  projectRoot: string
  /** The bridge's draft id, or a `local-…` key until it has one. */
  draftId: string
  /** The editor tab that wrote it. Never reused across tabs or restarts. */
  editorId: string
}

/**
 * A conversion this tab sent and has not had a conclusive answer to. It holds
 * the exact review — id, body, version and settings version — so a reload or a
 * lost answer retries that request instead of minting a second conversion.
 */
export interface PendingWorkingDraftConversion {
  requestId: string
  expectedVersion: number
  factoryRequest: FactoryCreate
  settingsVersion: string
  enqueuedAt: number
}

/** One device's record of one draft edited by one tab. */
export interface WorkingDraftRecord extends WorkingDraftKey {
  content: WorkingDraftContent
  /** The last server-acknowledged content, when one has arrived. */
  base: WorkingDraftContent | null
  baseVersion: number
  /** The server's newer state while a version conflict is unresolved. */
  server: WorkingDraft | null
  state: WorkingDraftState | 'unknown'
  pending: PendingWorkingDraftRequest | null
  /** The conversion this tab is holding until its outcome is established. */
  conversion?: PendingWorkingDraftConversion | null
  updatedAt: number
}

/**
 * The local-storage adapter. Every method is async and may reject: a rejection
 * from `write` is a quota or persistence failure, which the controller reports
 * instead of dropping the edit.
 */
export interface WorkingDraftStore {
  /** Every record this device holds for one bridge installation and project. */
  list(query: Pick<WorkingDraftKey, 'installationId' | 'projectRoot'>): Promise<WorkingDraftRecord[]>
  read(key: WorkingDraftKey): Promise<WorkingDraftRecord | null>
  write(record: WorkingDraftRecord): Promise<void>
  /** Re-key one record (a new project, or the bridge's id) in a single transaction. */
  migrate(key: WorkingDraftKey, next: Pick<WorkingDraftKey, 'projectRoot' | 'draftId'>): Promise<WorkingDraftRecord | null>
  remove(key: WorkingDraftKey): Promise<void>
}

/** The composite storage key: installation, project, draft, editor. */
export function workingDraftKeyString(key: WorkingDraftKey): string {
  return [key.installationId, key.projectRoot, key.draftId, key.editorId].join('\u001f')
}

const clonePending = (pending: PendingWorkingDraftRequest | null): PendingWorkingDraftRequest | null =>
  pending ? { ...pending, content: { ...pending.content } } : null

const cloneFactoryRequest = (request: FactoryCreate): FactoryCreate => ({ ...request, config: { ...request.config } })

const cloneConversion = (conversion: PendingWorkingDraftConversion | null): PendingWorkingDraftConversion | null =>
  conversion ? { ...conversion, factoryRequest: cloneFactoryRequest(conversion.factoryRequest) } : null

const cloneRecord = (record: WorkingDraftRecord): WorkingDraftRecord => ({
  ...record,
  content: { ...record.content },
  base: cloneContent(record.base),
  server: record.server ? cloneDraft(record.server) : null,
  pending: clonePending(record.pending),
  conversion: cloneConversion(record.conversion ?? null),
})

const scopeOf = (key: Pick<WorkingDraftKey, 'installationId' | 'projectRoot'>): string =>
  [key.installationId, key.projectRoot].join('\u001f')

/**
 * An in-memory `WorkingDraftStore`. It is the adapter's reference behaviour
 * (and what the unit tests use), so the IndexedDB one has nothing to decide.
 */
export function createMemoryWorkingDraftStore(initial: WorkingDraftRecord[] = []): WorkingDraftStore {
  const rows = new Map<string, WorkingDraftRecord>()
  for (const record of initial) rows.set(workingDraftKeyString(record), cloneRecord(record))
  return {
    async list(query) {
      const out: WorkingDraftRecord[] = []
      for (const record of rows.values()) {
        if (record.installationId === query.installationId && record.projectRoot === query.projectRoot) out.push(cloneRecord(record))
      }
      return out
    },
    async read(key) {
      const record = rows.get(workingDraftKeyString(key))
      return record ? cloneRecord(record) : null
    },
    async write(record) {
      rows.set(workingDraftKeyString(record), cloneRecord(record))
    },
    async migrate(key, next) {
      const from = workingDraftKeyString(key)
      const record = rows.get(from)
      if (!record) return null
      const moved: WorkingDraftRecord = { ...cloneRecord(record), projectRoot: next.projectRoot, draftId: next.draftId }
      rows.delete(from)
      rows.set(workingDraftKeyString(moved), moved)
      return cloneRecord(moved)
    },
    async remove(key) {
      rows.delete(workingDraftKeyString(key))
    },
  }
}

const DRAFT_DB = 'marshal-working-drafts'
const DRAFT_DB_VERSION = 1
const DRAFT_STORE = 'drafts'
const DRAFT_SCOPE_INDEX = 'by-scope'

/** One stored row: the composite key, the scope index value, and the record. */
interface StoredRow {
  key: string
  scope: string
  record: WorkingDraftRecord
}

/**
 * The production adapter: IndexedDB, opened lazily so a browser without it
 * fails on first use rather than at import. The database is only ever an
 * implementation of `WorkingDraftStore`; the controller cannot see it.
 */
export function createIndexedDbWorkingDraftStore(factory?: () => IDBFactory | undefined): WorkingDraftStore {
  let opening: Promise<IDBDatabase> | null = null

  const openDb = (): Promise<IDBDatabase> => {
    if (opening) return opening
    opening = new Promise<IDBDatabase>((resolve, reject) => {
      const idb = factory ? factory() : typeof indexedDB === 'undefined' ? undefined : indexedDB
      if (!idb) {
        reject(new Error('This browser has no IndexedDB, so drafts cannot be kept locally.'))
        return
      }
      const req = idb.open(DRAFT_DB, DRAFT_DB_VERSION)
      req.onupgradeneeded = () => {
        const db = req.result
        if (!db.objectStoreNames.contains(DRAFT_STORE)) {
          const store = db.createObjectStore(DRAFT_STORE, { keyPath: 'key' })
          store.createIndex(DRAFT_SCOPE_INDEX, 'scope', { unique: false })
        }
      }
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => reject(req.error ?? new Error('Could not open local draft storage.'))
      req.onblocked = () => reject(new Error('Local draft storage is blocked by another tab.'))
    })
    // A rejected open must not be cached: the next call tries again.
    opening.catch(() => {
      opening = null
    })
    return opening
  }

  /** One transaction, resolving on `complete` so a quota abort is a rejection. */
  async function transact<T>(mode: IDBTransactionMode, run: (store: IDBObjectStore, done: (value: T) => void) => void): Promise<T> {
    const db = await openDb()
    return new Promise<T>((resolve, reject) => {
      const tx = db.transaction(DRAFT_STORE, mode)
      let value: T = undefined as T
      tx.oncomplete = () => resolve(value)
      tx.onabort = () => reject(tx.error ?? new Error('Local draft storage refused the write.'))
      tx.onerror = () => reject(tx.error ?? new Error('Local draft storage failed.'))
      run(tx.objectStore(DRAFT_STORE), (next) => {
        value = next
      })
    })
  }

  return {
    async list(query) {
      const rows = await transact<StoredRow[]>('readonly', (store, done) => {
        const req = store.index(DRAFT_SCOPE_INDEX).getAll(scopeOf(query))
        req.onsuccess = () => done((req.result as StoredRow[] | undefined) ?? [])
      })
      return rows.map((row) => row.record)
    },
    async read(key) {
      const row = await transact<StoredRow | undefined>('readonly', (store, done) => {
        const req = store.get(workingDraftKeyString(key))
        req.onsuccess = () => done(req.result as StoredRow | undefined)
      })
      return row ? row.record : null
    },
    async write(record) {
      const target = workingDraftKeyString(record)
      await transact<void>('readwrite', (store) => {
        store.put({ key: target, scope: scopeOf(record), record } satisfies StoredRow)
      })
    },
    async migrate(key, next) {
      const from = workingDraftKeyString(key)
      const toKey: WorkingDraftKey = { ...key, projectRoot: next.projectRoot, draftId: next.draftId }
      const to = workingDraftKeyString(toKey)
      return transact<WorkingDraftRecord | null>('readwrite', (store, done) => {
        const req = store.get(from)
        req.onsuccess = () => {
          const row = req.result as StoredRow | undefined
          if (!row) {
            done(null)
            return
          }
          const moved: WorkingDraftRecord = { ...row.record, projectRoot: toKey.projectRoot, draftId: toKey.draftId }
          // Both halves of the move are in this transaction: a reader sees the
          // record under the old key or the new one, never under both or neither.
          store.delete(from)
          store.put({ key: to, scope: scopeOf(moved), record: moved } satisfies StoredRow)
          done(moved)
        }
      })
    },
    async remove(key) {
      const target = workingDraftKeyString(key)
      await transact<void>('readwrite', (store) => {
        store.delete(target)
      })
    },
  }
}

/** The network calls the controller makes. Tests inject fakes; production injects `api`. */
export interface WorkingDraftRequests {
  /** The authenticated bridge's stable installation identity. */
  installation(): Promise<string>
  create(req: WorkingDraftCreateRequest): Promise<WorkingDraft>
  get(id: string): Promise<WorkingDraft>
  save(id: string, req: WorkingDraftSaveRequest): Promise<WorkingDraft>
  convert(id: string, req: WorkingDraftConvertRequest): Promise<WorkingDraftConvertResult>
}

/**
 * A `PUT` refused because the draft moved on. `current` is the server's state
 * at the refusal, so the user can see both versions instead of guessing.
 */
export class WorkingDraftConflictError extends Error {
  current: WorkingDraft | null
  constructor(message: string, current: WorkingDraft | null = null) {
    super(message)
    this.name = 'WorkingDraftConflictError'
    this.current = current
  }
}

export interface WorkingDraftTimer {
  setTimeout(run: () => void, ms: number): unknown
  clearTimeout(handle: unknown): void
}

/** A copy another tab (or a closed browser) left behind, offered for recovery. */
export interface WorkingDraftRecoverable {
  editorId: string
  content: WorkingDraftContent
  baseVersion: number
  pending: boolean
  updatedAt: number
}

/** What a view of the controller observes. */
export interface WorkingDraftView {
  /** This tab's identity: the fourth part of every local key it writes. */
  editorId: string
  installationId: string | null
  projectRoot: string | null
  /** The bridge's draft id, or null while the draft exists only here. */
  draftId: string | null
  /** The last server-acknowledged content. */
  base: WorkingDraftContent | null
  baseVersion: number
  /** What the editor shows. */
  local: WorkingDraftContent | null
  /** The server's content while a version conflict is unresolved. */
  server: WorkingDraftContent | null
  /** Local content is ahead of the saved base. */
  pending: boolean
  /** The local content is the saved one: nothing outstanding, server still editing. */
  saved: boolean
  /** A request is in flight. */
  saving: boolean
  /** The last send failed for a transport reason; the same request is retried. */
  offline: boolean
  /** The server has a newer version; `base`, `local` and `server` are all kept. */
  conflict: boolean
  state: WorkingDraftState | 'unknown'
  /** A visible failure: local persistence, or the last request's message. */
  error: string | null
  /** No project has been chosen yet, so nothing can be sent. */
  localOnly: boolean
  /** The factory item this draft became, once the bridge reports one. */
  factoryItemId: string | null
  /**
   * The conversion this tab holds until its outcome is established: the exact
   * request a reload or a lost answer replays.
   */
  conversion: PendingWorkingDraftConversion | null
  /** Other editors' copies of this draft that can be adopted. */
  recoverable: WorkingDraftRecoverable[]
}

export interface WorkingDraftControllerOptions {
  storage: WorkingDraftStore
  requests: WorkingDraftRequests
  /** The bridge installation identity. Defaults to `requests.installation()`. */
  installationId?: () => string | Promise<string>
  /** This tab's identity. Defaults to a fresh random id (never a URL or a route). */
  editorId?: string
  now?: () => number
  timer?: WorkingDraftTimer
  newId?: () => string
}

/** The autosave debounce. Fixed: the brief and the spec both name 500 ms. */
export const WORKING_DRAFT_DEBOUNCE_MS = 500

/** A draft the bridge has not named yet is keyed with this prefix. */
const LOCAL_DRAFT_PREFIX = 'local-'

function randomId(): string {
  const c = typeof globalThis === 'undefined' ? undefined : globalThis.crypto
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

function describeError(e: unknown): string {
  if (e instanceof Error) return e.message
  return typeof e === 'string' ? e : String(e)
}

/**
 * The identity of a review: everything but its request id. A re-confirmed
 * review has the same signature and therefore replays the frozen request
 * instead of minting a new one. The request id inside the body is dropped for
 * the same reason the bridge drops it: it is never the caller's to claim.
 */
function conversionSignature(review: {
  expectedVersion: number
  settingsVersion?: string
  factoryRequest: FactoryCreate
}): string {
  return JSON.stringify({
    expectedVersion: review.expectedVersion,
    settingsVersion: review.settingsVersion ?? '',
    factoryRequest: { ...review.factoryRequest, requestId: '' },
  })
}

/**
 * A refusal the bridge confirmed with a status: it decided before creating
 * anything, so the draft is editable again and a new review is a new request. A
 * failure with no status is transport trouble, whose outcome nothing here can
 * establish — that request has to be replayed, not replaced.
 */
function isConfirmedRefusal(e: unknown): boolean {
  const status = (e as { status?: unknown } | null | undefined)?.status
  return typeof status === 'number' && status >= 400 && status < 409
}

const defaultTimer: WorkingDraftTimer = {
  setTimeout: (run, ms) => setTimeout(run, ms) as unknown,
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
}

/**
 * One draft, edited by one tab, saved to one bridge.
 *
 * Construct one per draft; `load` binds it to a project and draft, `edit`
 * records keystrokes, and `flush` retries whatever is outstanding (the
 * reconnect hook). The view is readable at any time and observable through
 * `subscribe`.
 */
export class WorkingDraftController {
  private readonly storage: WorkingDraftStore
  private readonly requests: WorkingDraftRequests
  private readonly installation: () => string | Promise<string>
  private readonly now: () => number
  private readonly timer: WorkingDraftTimer
  private readonly newId: () => string
  readonly editorId: string

  private readonly localDraftKey: string
  /** The key this tab's record is currently filed under; migration moves it. */
  private stored: Pick<WorkingDraftKey, 'projectRoot' | 'draftId'>
  private hasRecord = false

  private installationId: string | null = null
  private projectRoot: string | null = null
  private draftId: string | null = null
  private base: WorkingDraftContent | null = null
  private baseVersion = 0
  private local: WorkingDraftContent | null = null
  private serverDraft: WorkingDraft | null = null
  private draftState: WorkingDraftState | 'unknown' = 'unknown'
  private pendingRequest: PendingWorkingDraftRequest | null = null
  private pendingConversion: PendingWorkingDraftConversion | null = null
  private factoryItemId: string | null = null
  private saving = false
  private offline = false
  private conflict = false
  private error: string | null = null
  private recoverable: WorkingDraftRecoverable[] = []

  private timerHandle: unknown
  private localQueue: Promise<void> = Promise.resolve()
  private sendQueue: Promise<void> = Promise.resolve()
  private readonly listeners = new Set<(view: WorkingDraftView) => void>()

  constructor(opts: WorkingDraftControllerOptions) {
    this.storage = opts.storage
    this.requests = opts.requests
    this.now = opts.now ?? (() => Date.now())
    this.timer = opts.timer ?? defaultTimer
    this.newId = opts.newId ?? randomId
    this.editorId = opts.editorId ?? this.newId()
    const provider = opts.installationId
    this.installation = provider ? provider : () => this.requests.installation()
    this.localDraftKey = LOCAL_DRAFT_PREFIX + this.newId()
    this.stored = { projectRoot: '', draftId: this.localDraftKey }
  }

  // ---- reads -------------------------------------------------------------

  view(): WorkingDraftView {
    const pending = this.pendingRequest !== null || (this.local !== null && this.isAhead())
    return {
      editorId: this.editorId,
      installationId: this.installationId,
      projectRoot: this.projectRoot,
      draftId: this.draftId,
      base: this.base,
      baseVersion: this.baseVersion,
      local: this.local,
      server: this.serverDraft ? this.serverDraft.content : null,
      pending,
      saved: !pending && this.base !== null && this.draftState === 'editing',
      saving: this.saving,
      offline: this.offline,
      conflict: this.conflict,
      state: this.draftState,
      error: this.error,
      localOnly: this.projectRoot === null,
      factoryItemId: this.factoryItemId,
      conversion: cloneConversion(this.pendingConversion),
      recoverable: this.recoverable,
    }
  }

  /** Observe the view; the listener is called with the current one immediately. */
  subscribe(listener: (view: WorkingDraftView) => void): () => void {
    this.listeners.add(listener)
    listener(this.view())
    return () => {
      this.listeners.delete(listener)
    }
  }

  // ---- lifecycle ---------------------------------------------------------

  /**
   * Bind this controller to a project and draft and restore what this device
   * holds: this tab's own record, plus any copy another editor left behind
   * (offered through `recoverable`, never applied on the user's behalf).
   *
   * `projectRoot` is null for a draft that has no project yet: it stays local
   * until `assignProject` is called.
   */
  async load(projectRoot: string | null, draftId: string | null = null): Promise<WorkingDraftView> {
    this.projectRoot = projectRoot
    this.draftId = draftId
    this.stored = { projectRoot: projectRoot ?? '', draftId: draftId ?? this.localDraftKey }
    this.offline = false
    this.error = null
    await this.ensureInstallation()
    await this.restore()
    await this.refresh()
    // A refresh can reconcile a conversion (or clear the request it froze), so
    // the record is written back rather than left holding a finished request.
    await this.persist()
    this.emit()
    return this.view()
  }

  /**
   * Record an edit. It reaches local storage before this resolves and before
   * any request carries it; the send itself is debounced by 500 ms.
   */
  async edit(content: WorkingDraftContent): Promise<void> {
    this.local = { ...content }
    await this.persist()
    this.schedule()
    this.emit()
  }

  /** Send whatever is outstanding now. The reconnect hook; safe to call twice. */
  async flush(): Promise<WorkingDraftView> {
    this.clearTimer()
    await this.enqueueSend(() => this.send())
    return this.view()
  }

  /** Choose the project for a locally-created draft, then let it be sent. */
  async assignProject(projectRoot: string): Promise<WorkingDraftView> {
    if (this.projectRoot === projectRoot) return this.view()
    this.projectRoot = projectRoot
    await this.persist()
    await this.flush()
    return this.view()
  }

  /** Take another editor's copy as this tab's content. Its record is left alone. */
  async adopt(editorId: string): Promise<WorkingDraftView> {
    const copy = this.recoverable.find((r) => r.editorId === editorId)
    if (!copy) return this.view()
    this.local = { ...copy.content }
    this.conflict = false
    this.serverDraft = null
    this.error = null
    await this.persist()
    this.schedule()
    this.emit()
    return this.view()
  }

  /** Drop this tab's unsent edits and go back to the server's content. */
  async discardLocal(): Promise<WorkingDraftView> {
    this.clearTimer()
    const target = this.serverDraft ? this.serverDraft.content : this.base
    if (this.serverDraft) {
      this.base = { ...this.serverDraft.content }
      this.baseVersion = this.serverDraft.version
      if (this.serverDraft.id) this.draftId = this.serverDraft.id
    }
    this.local = cloneContent(target)
    this.serverDraft = null
    this.conflict = false
    this.offline = false
    this.error = null
    this.pendingRequest = null
    await this.persist()
    this.emit()
    return this.view()
  }

  /**
   * Keep the local content over the server's newer version: rebase onto the
   * version the conflict reported and send it again.
   */
  async keepLocal(): Promise<WorkingDraftView> {
    if (this.serverDraft) {
      this.base = { ...this.serverDraft.content }
      this.baseVersion = this.serverDraft.version
      if (this.serverDraft.id) this.draftId = this.serverDraft.id
    }
    this.serverDraft = null
    this.conflict = false
    this.error = null
    if (this.local === null) this.local = cloneContent(this.base)
    await this.persist()
    await this.flush()
    return this.view()
  }

  /**
   * Convert the reviewed draft into one factory item.
   *
   * The review is frozen before anything is sent: its request id, its exact
   * body, the draft version and the settings version it was read against all go
   * into this tab's record, so a reload or a lost answer replays that request
   * rather than minting a second conversion. Only a refusal the bridge confirms
   * (nothing was created) frees the id for a new attempt.
   */
  async convert(review: WorkingDraftConvertRequest): Promise<WorkingDraftView> {
    const refusal = this.conversionRefusal(review)
    if (refusal) {
      this.error = refusal
      this.emit()
      return this.view()
    }
    const held = this.pendingConversion
    // The same review re-confirmed is the same request, id included.
    const frozen: PendingWorkingDraftConversion =
      held && conversionSignature(held) === conversionSignature(review)
        ? held
        : {
            requestId: review.requestId,
            expectedVersion: review.expectedVersion,
            factoryRequest: cloneFactoryRequest(review.factoryRequest),
            settingsVersion: review.settingsVersion ?? '',
            enqueuedAt: this.now(),
          }
    return this.sendConversion(frozen)
  }

  /** Replay this tab's frozen conversion: the same request, byte for byte. */
  async retryConversion(): Promise<WorkingDraftView> {
    const held = this.pendingConversion
    if (!held) {
      this.error = 'There is no conversion request to retry.'
      this.emit()
      return this.view()
    }
    return this.sendConversion(held)
  }

  /**
   * Why this review cannot be converted, or '' when it can. A conversion is of
   * one saved version of one draft: an unsent edit, a version the review was not
   * read at, or a draft that is already factory work all refuse here.
   */
  private conversionRefusal(review: WorkingDraftConvertRequest): string {
    if (this.projectRoot === null) return 'This draft has no project, so it cannot become factory work.'
    if (!this.draftId || this.draftId.startsWith(LOCAL_DRAFT_PREFIX)) return 'Save this draft before converting it.'
    if (this.draftState === 'deleted') return 'This draft was deleted, so it cannot be converted.'
    if (this.draftState === 'converted') return 'This draft is already a factory item.'
    if (this.pendingRequest !== null || this.isAhead()) return 'Save the draft before converting: the review submits the saved version.'
    if (review.expectedVersion !== this.baseVersion) return 'This draft changed since the review. Review the saved version again.'
    return ''
  }

  private async sendConversion(frozen: PendingWorkingDraftConversion): Promise<WorkingDraftView> {
    this.pendingConversion = frozen
    this.error = null
    this.offline = false
    // The frozen request is on this device before it is sent, so a tab that
    // dies in flight carries the same id and body into its next attempt.
    await this.persist()
    this.saving = true
    this.emit()
    try {
      const result = await this.requests.convert(this.draftId as string, {
        requestId: frozen.requestId,
        expectedVersion: frozen.expectedVersion,
        factoryRequest: cloneFactoryRequest(frozen.factoryRequest),
        settingsVersion: frozen.settingsVersion,
      })
      this.applyConversion(frozen, result)
    } catch (e) {
      if (e instanceof WorkingDraftConflictError) this.applyConversionConflict(frozen, e.current)
      else if (isConfirmedRefusal(e)) {
        // The bridge decided before it created anything: the draft is editable
        // again, and a fresh review is a new request.
        this.pendingConversion = null
        this.error = describeError(e)
      } else {
        // Nothing establishes what happened, so the request stays frozen and is
        // replayed as it is.
        this.offline = true
        this.error = describeError(e)
      }
    } finally {
      this.saving = false
    }
    await this.persist()
    this.emit()
    return this.view()
  }

  private applyConversion(frozen: PendingWorkingDraftConversion, result: WorkingDraftConvertResult): void {
    // An answer to a request that is no longer the frozen one says nothing
    // about what this tab holds.
    if (this.pendingConversion !== frozen) return
    this.pendingConversion = null
    const draft = result.draft
    this.offline = false
    this.error = null
    this.conflict = false
    this.serverDraft = null
    this.draftState = draft.state
    this.factoryItemId = draft.factoryItemId ?? result.item?.id ?? null
    if (draft.id) this.draftId = draft.id
    if (draft.state === 'deleted') return
    // The converted content is the base: anything typed after it is ahead of
    // it, and is what "new working draft from these edits" carries.
    this.base = { ...draft.content }
    this.baseVersion = draft.version
  }

  private applyConversionConflict(frozen: PendingWorkingDraftConversion, current: WorkingDraft | null): void {
    if (current && current.state === 'converting') {
      // The bridge holds the conversion but could not establish its outcome.
      // The frozen request is kept, so the retry is that request, never a new
      // one, and the draft is not presented as saved or as failed.
      this.draftState = 'converting'
      this.pendingConversion = frozen
      this.error = 'The conversion was sent but its outcome is uncertain. Retrying sends the same request.'
      return
    }
    this.pendingConversion = null
    if (current && current.state === 'converted') {
      // Someone else's conversion of this draft won the race: adopt the outcome
      // rather than offering a choice this tab can no longer act on.
      this.draftState = 'converted'
      if (current.factoryItemId) this.factoryItemId = current.factoryItemId
      this.pendingRequest = null
      this.serverDraft = null
      this.conflict = false
      this.error = null
      this.base = { ...current.content }
      this.baseVersion = current.version
      this.clearTimer()
      return
    }
    if (current && current.state === 'editing' && current.version === frozen.expectedVersion) {
      // Nothing about the draft moved, so the reviewed settings did: the review
      // is stale and needs re-confirming against the settings it now has.
      this.error = 'The project settings changed. Review the conversion again.'
      return
    }
    // The draft itself moved on: show it beside the local content, exactly as a
    // save conflict does.
    this.error = null
    this.conflict = true
    if (current) {
      this.serverDraft = current
      this.draftState = current.state
    }
    this.clearTimer()
  }

  /** Stop the debounce timer and forget listeners. In-flight requests are left to finish. */
  destroy(): void {
    this.clearTimer()
    this.listeners.clear()
  }

  // ---- persistence -------------------------------------------------------

  private keyTarget(): Pick<WorkingDraftKey, 'projectRoot' | 'draftId'> {
    return { projectRoot: this.projectRoot ?? '', draftId: this.draftId ?? this.localDraftKey }
  }

  private recordKey(key: Pick<WorkingDraftKey, 'projectRoot' | 'draftId'>): WorkingDraftKey {
    return { installationId: this.installationId ?? '', projectRoot: key.projectRoot, draftId: key.draftId, editorId: this.editorId }
  }

  /** True when the local content differs from the last acknowledged base. */
  private isAhead(): boolean {
    if (this.local === null) return false
    if (this.base === null) return true
    return !sameWorkingDraftContent(this.base, this.local)
  }

  /**
   * Write this tab's record, then move it to its current key if the key
   * changed (a project was chosen, or the bridge named the draft). The write
   * comes first so a failed move can never lose the content: it is already
   * under the old key, and the next attempt tries the move again.
   */
  private persist(): Promise<boolean> {
    return this.enqueueLocal(() => this.writeRecord())
  }

  private async writeRecord(): Promise<boolean> {
    if (!this.installationId) return false
    if (this.local === null) {
      if (!this.hasRecord) return true
      try {
        await this.storage.remove(this.recordKey(this.stored))
        this.hasRecord = false
        return true
      } catch (e) {
        this.reportPersistenceFailure(e)
        return false
      }
    }
    const record: WorkingDraftRecord = {
      ...this.recordKey(this.stored),
      content: { ...this.local },
      base: cloneContent(this.base),
      baseVersion: this.baseVersion,
      server: this.serverDraft ? cloneDraft(this.serverDraft) : null,
      state: this.draftState,
      pending: clonePending(this.pendingRequest),
      conversion: cloneConversion(this.pendingConversion),
      updatedAt: this.now(),
    }
    try {
      await this.storage.write(record)
      this.hasRecord = true
    } catch (e) {
      this.reportPersistenceFailure(e)
      return false
    }
    const target = this.keyTarget()
    if (target.projectRoot !== this.stored.projectRoot || target.draftId !== this.stored.draftId) {
      try {
        await this.storage.migrate(this.recordKey(this.stored), target)
        this.stored = target
      } catch (e) {
        this.reportPersistenceFailure(e)
        return false
      }
    }
    return true
  }

  private reportPersistenceFailure(e: unknown): void {
    // Never a silent loss: the content is still in memory, and the user is told
    // the browser would not keep it.
    this.error = `Could not keep this draft in this browser: ${describeError(e)}`
    this.emit()
  }

  private async ensureInstallation(): Promise<void> {
    if (this.installationId) return
    try {
      const id = await this.installation()
      if (id) this.installationId = id
      else this.error = 'The bridge returned no installation identity, so drafts cannot be kept locally.'
    } catch (e) {
      this.error = `Could not identify this browser to the bridge: ${describeError(e)}`
    }
    this.emit()
  }

  private async restore(): Promise<void> {
    if (!this.installationId) return
    let records: WorkingDraftRecord[]
    try {
      records = await this.storage.list({ installationId: this.installationId, projectRoot: this.stored.projectRoot })
    } catch (e) {
      this.reportPersistenceFailure(e)
      return
    }
    const key = this.stored
    const isThisDraft = (r: WorkingDraftRecord): boolean => r.draftId === key.draftId && r.projectRoot === key.projectRoot
    this.recoverable = records
      .filter((r) => r.editorId !== this.editorId && isThisDraft(r))
      .map((r) => ({ editorId: r.editorId, content: { ...r.content }, baseVersion: r.baseVersion, pending: r.pending !== null, updatedAt: r.updatedAt }))
      .sort((a, b) => b.updatedAt - a.updatedAt)
    const mine = records.find((r) => r.editorId === this.editorId && isThisDraft(r))
    if (!mine) return
    this.base = cloneContent(mine.base)
    this.baseVersion = mine.baseVersion
    this.local = { ...mine.content }
    this.serverDraft = mine.server ? cloneDraft(mine.server) : null
    this.conflict = mine.server !== null
    this.draftState = mine.state
    this.pendingRequest = clonePending(mine.pending)
    this.pendingConversion = cloneConversion(mine.conversion ?? null)
    this.hasRecord = true
    // A tab that closed with a request in flight left it here; send it again,
    // with its own id and bytes, once the debounce allows.
    if (this.pendingRequest) this.schedule()
  }

  /** Reconcile with the bridge: another device may have moved the draft on. */
  private async refresh(): Promise<void> {
    const id = this.draftId
    if (!id || id.startsWith(LOCAL_DRAFT_PREFIX)) return
    let draft: WorkingDraft
    try {
      draft = await this.requests.get(id)
    } catch (e) {
      this.offline = true
      if (!this.error) this.error = describeError(e)
      return
    }
    this.draftState = draft.state
    if (draft.factoryItemId) this.factoryItemId = draft.factoryItemId
    if (draft.state === 'deleted') {
      // A tombstone: keep whatever is local for recovery, send nothing.
      this.pendingRequest = null
      this.pendingConversion = null
      this.clearTimer()
      return
    }
    if (draft.state === 'converting' || draft.state === 'converted') {
      // The conversion's outcome is the draft's, not this tab's. A converted
      // draft settles the frozen request; a still-converting one keeps it, so
      // the retry that finishes it is the same request.
      if (draft.state === 'converted') this.pendingConversion = null
      this.clearTimer()
      this.conflict = false
      this.serverDraft = null
      this.base = { ...draft.content }
      this.baseVersion = draft.version
      if (this.local === null) this.local = { ...draft.content }
      return
    }
    if (this.local === null) {
      this.local = { ...draft.content }
      this.base = { ...draft.content }
      this.baseVersion = draft.version
      return
    }
    const ahead = this.isAhead()
    if (draft.version <= this.baseVersion) return
    if (ahead) {
      this.conflict = true
      this.serverDraft = draft
      this.pendingRequest = null
      this.clearTimer()
      return
    }
    this.base = { ...draft.content }
    this.baseVersion = draft.version
    this.local = { ...draft.content }
  }

  // ---- sending -----------------------------------------------------------

  private schedule(): void {
    this.clearTimer()
    if (this.conflict || this.draftState === 'deleted') return
    // A draft the bridge is converting, or has converted, must never be saved:
    // the item it became is not this draft. Local edits stay here for the
    // "new working draft from these edits" path.
    if (this.draftState === 'converting' || this.draftState === 'converted') return
    this.timerHandle = this.timer.setTimeout(() => {
      this.timerHandle = undefined
      void this.flush()
    }, WORKING_DRAFT_DEBOUNCE_MS)
  }

  private clearTimer(): void {
    if (this.timerHandle === undefined) return
    this.timer.clearTimeout(this.timerHandle)
    this.timerHandle = undefined
  }

  private async send(): Promise<void> {
    // A draft with no project cannot be sent; it stays local until one is chosen.
    if (this.projectRoot === null) return
    // The user has to choose between the versions first.
    if (this.conflict) return
    // The bridge recorded a tombstone; sending would re-create the draft the
    // user deleted. The local copy is kept for recovery instead.
    if (this.draftState === 'deleted') return
    // A conversion is the draft's last save: once it is in flight or done,
    // autosave must never write over it.
    if (this.draftState === 'converting' || this.draftState === 'converted') return
    this.error = null
    this.offline = false
    if (!this.pendingRequest) {
      if (!this.isAhead()) return
      this.pendingRequest = {
        requestId: this.newId(),
        kind: this.draftId ? 'save' : 'create',
        content: { ...(this.local as WorkingDraftContent) },
        expectedVersion: this.draftId ? this.baseVersion : 0,
        enqueuedAt: this.now(),
      }
      // The pending request is stored before it is sent: a tab that dies
      // mid-flight replays exactly these bytes under this id.
      await this.persist()
      this.emit()
    }
    const sent = this.pendingRequest
    if (!sent) return
    this.saving = true
    this.emit()
    try {
      const draft =
        sent.kind === 'create'
          ? await this.requests.create({ projectRoot: this.projectRoot, requestId: sent.requestId, content: { ...sent.content } })
          : await this.requests.save(this.draftId as string, {
              requestId: sent.requestId,
              expectedVersion: sent.expectedVersion,
              content: { ...sent.content },
            })
      this.applyAck(sent, draft)
    } catch (e) {
      if (e instanceof WorkingDraftConflictError) this.applyConflict(e.current)
      else {
        // Transport or server trouble: keep the pending request untouched so
        // the retry is the same request, not a new one.
        this.offline = true
        this.error = describeError(e)
      }
    } finally {
      this.saving = false
    }
    await this.persist()
    this.emit()
    if (!this.offline && !this.conflict && this.pendingRequest === null && this.isAhead()) this.schedule()
  }

  private applyAck(sent: PendingWorkingDraftRequest, draft: WorkingDraft): void {
    // A reply for a request that is no longer the pending one is stale: the
    // user discarded it, or a delete or a reload superseded it. Adopting it
    // would mark content saved that nobody has on the server.
    if (this.pendingRequest !== sent) return
    this.pendingRequest = null
    this.draftState = draft.state
    if (draft.state === 'deleted') {
      // A tombstone is not a save. Keep base and local content: the copy here
      // is the only record of the work left, and it can still be recovered.
      return
    }
    if (draft.id) this.draftId = draft.id
    this.base = { ...draft.content }
    this.baseVersion = draft.version
  }

  private applyConflict(current: WorkingDraft | null): void {
    this.conflict = true
    this.pendingRequest = null
    this.clearTimer()
    if (current) {
      this.serverDraft = current
      this.draftState = current.state
    }
    this.error = null
  }

  // ---- serialization -----------------------------------------------------

  private enqueueLocal<T>(run: () => Promise<T>): Promise<T> {
    const next = this.localQueue.then(run, run)
    this.localQueue = next.then(
      () => undefined,
      () => undefined,
    )
    return next
  }

  private enqueueSend<T>(run: () => Promise<T>): Promise<T> {
    const next = this.sendQueue.then(run, run)
    this.sendQueue = next.then(
      () => undefined,
      () => undefined,
    )
    return next
  }

  private emit(): void {
    const view = this.view()
    for (const listener of [...this.listeners]) listener(view)
  }
}

export function createWorkingDraftController(opts: WorkingDraftControllerOptions): WorkingDraftController {
  return new WorkingDraftController(opts)
}
