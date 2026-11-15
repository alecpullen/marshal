import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import type { ComponentProps } from 'svelte'
import WorkingDrafts from './WorkingDrafts.svelte'
import * as api from '../lib/api'
import {
  WorkingDraftConflictError,
  createMemoryWorkingDraftStore,
  emptyWorkingDraftContent,
  type WorkingDraft,
  type WorkingDraftContent,
  type WorkingDraftConvertRequest,
  type WorkingDraftCreateRequest,
  type WorkingDraftRecord,
  type WorkingDraftRemoveRequest,
  type WorkingDraftRequests,
  type WorkingDraftSaveRequest,
  type WorkingDraftTimer,
} from '../lib/working-drafts'

/*
  The bridge is mocked and the controller's storage, clock, timer and requests
  are all injected: nothing here touches the network, IndexedDB or a real
  credential.
*/
vi.mock('../lib/api', async (actual) => ({
  ...await actual<typeof import('../lib/api')>(),
  listRepos: vi.fn(),
  listWorkspaces: vi.fn(),
  getModels: vi.fn(),
  getProjectSettings: vi.fn(),
  getVersionedProjectSettings: vi.fn(),
  createFactoryItem: vi.fn(),
  getFactoryItem: vi.fn(),
  listWorkingDrafts: vi.fn(),
  getWorkingDraftInstallation: vi.fn(),
  createWorkingDraft: vi.fn(),
  getWorkingDraft: vi.fn(),
  saveWorkingDraft: vi.fn(),
  convertWorkingDraft: vi.fn(),
}))
afterEach(cleanup)

const ROOT = '/home/u/alpha'

const content = (patch: Partial<WorkingDraftContent> = {}): WorkingDraftContent => ({
  ...emptyWorkingDraftContent(),
  native: true,
  ...patch,
})

const draftOf = (
  id: string,
  version: number,
  body: WorkingDraftContent,
  state: WorkingDraft['state'] = 'editing',
  extra: Partial<WorkingDraft> = {},
): WorkingDraft => ({ id, projectRoot: ROOT, version, state, content: body, updatedAt: '2026-01-01T00:00:00Z', ...extra })

const recordOf = (patch: Partial<WorkingDraftRecord> = {}): WorkingDraftRecord => ({
  installationId: 'inst-1',
  projectRoot: ROOT,
  draftId: 'local-x1',
  editorId: 'other-tab',
  content: content({ title: 'Local copy' }),
  base: null,
  baseVersion: 0,
  server: null,
  state: 'editing',
  pending: null,
  conversion: null,
  updatedAt: 10,
  ...patch,
})

/** A controllable debounce: `run` fires whatever the editor has queued. */
function manualTimer(): { timer: WorkingDraftTimer; run: () => void } {
  const queue: (() => void)[] = []
  const timer: WorkingDraftTimer = {
    setTimeout: (job) => {
      queue.push(job)
      return queue.length
    },
    clearTimeout: (handle) => {
      queue[(handle as number) - 1] = () => {}
    },
  }
  const run = () => {
    for (const job of queue.splice(0)) job()
  }
  return { timer, run }
}

/** Run the debounce until `check` passes: mounting may queue one edit of its own. */
const until = async (run: () => void, check: () => void) => {
  await waitFor(() => {
    run()
    check()
  })
}

function fakeBridge(initial: WorkingDraft | null = null) {
  const calls = {
    create: [] as WorkingDraftCreateRequest[],
    save: [] as { id: string; req: WorkingDraftSaveRequest }[],
    get: [] as string[],
    convert: [] as { id: string; req: WorkingDraftConvertRequest }[],
    remove: [] as { id: string; req: WorkingDraftRemoveRequest }[],
  }
  const held = { draft: initial }
  const requests: WorkingDraftRequests = {
    installation: vi.fn(async () => 'inst-1'),
    create: vi.fn(async (req: WorkingDraftCreateRequest) => {
      calls.create.push(req)
      held.draft = draftOf('d-new', 1, req.content)
      return held.draft
    }),
    get: vi.fn(async (id: string) => {
      calls.get.push(id)
      return held.draft ?? draftOf(id, 1, content())
    }),
    save: vi.fn(async (id: string, req: WorkingDraftSaveRequest) => {
      calls.save.push({ id, req })
      held.draft = draftOf(id, req.expectedVersion + 1, req.content)
      return held.draft
    }),
    convert: vi.fn(async (id: string, req: WorkingDraftConvertRequest) => {
      calls.convert.push({ id, req })
      const body = held.draft ? held.draft.content : emptyWorkingDraftContent()
      const draft = draftOf(id, req.expectedVersion + 1, body, 'converted', { factoryItemId: 'item-1' })
      held.draft = draft
      return { draft }
    }),
    remove: vi.fn(async (id: string, req: WorkingDraftRemoveRequest) => {
      calls.remove.push({ id, req })
      held.draft = { ...(held.draft ?? draftOf(id, req.expectedVersion, content())), version: req.expectedVersion + 1, state: 'deleted' }
      return held.draft
    }),
  }
  return { requests, calls, held }
}

type DraftProps = Omit<ComponentProps<typeof WorkingDrafts>, 'onNavigate'> & { onNavigate?: (hash: string) => void }

const props = (patch: DraftProps = {}): ComponentProps<typeof WorkingDrafts> => ({
  onNavigate: vi.fn(),
  ...patch,
} as ComponentProps<typeof WorkingDrafts>)

const mount = (patch: DraftProps = {}) => render(WorkingDrafts, props(patch))

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listRepos).mockResolvedValue([{ id: 'repo', url: 'https://example.test', branch: 'main' }])
  vi.mocked(api.listWorkspaces).mockResolvedValue([{ source: 'studio', name: 'go-dev', usage: 0 }])
  vi.mocked(api.getModels).mockResolvedValue({ profiles: { careful: {}, fast: {} } } as never)
  vi.mocked(api.getProjectSettings).mockResolvedValue({ intake: {} })
  vi.mocked(api.getVersionedProjectSettings).mockResolvedValue({ settings: { intake: {}, routing: { profile: 'fast' } }, etag: 'settings-v1' })
  vi.mocked(api.listWorkingDrafts).mockResolvedValue([])
})

it('listsMultipleProjectDrafts', async () => {
  vi.mocked(api.listWorkingDrafts).mockResolvedValue([
    draftOf('d1', 2, content({ title: 'Auth refactor' })),
    draftOf('d2', 5, content({ title: 'Ledger fix' }), 'converted', { factoryItemId: 'item-9' }),
  ])
  const storage = createMemoryWorkingDraftStore([recordOf({ draftId: 'local-x1', content: content({ title: 'Scratch idea' }) })])
  const bridge = fakeBridge()
  mount({ projectRoot: ROOT, storage, requests: bridge.requests })

  expect(await screen.findAllByTestId('draft-row')).toHaveLength(3)
  // A draft of the bridge's links to its own route, scoped to the project —
  // never to a project path inside the draft id.
  const auth = screen.getByRole('link', { name: 'Auth refactor' })
  expect(auth.getAttribute('href')).toBe('#factory/drafts/d1?project=%2Fhome%2Fu%2Falpha')
  expect(screen.getByRole('link', { name: 'Scratch idea' }).getAttribute('href')).toBe('#factory/drafts/local-x1?project=%2Fhome%2Fu%2Falpha')
  // A local-only draft says so, and a converted one keeps its item reachable.
  expect(screen.getByText('Only on this device')).toBeTruthy()
  expect(screen.getByText('Became factory work')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Factory item' }).getAttribute('href')).toBe('#factory/item-9')
  // Listing is a read: nothing is created or converted by opening the list.
  expect(bridge.calls.create).toHaveLength(0)
  expect(api.createFactoryItem).not.toHaveBeenCalled()
})

it('newDraftIsLocalUntilProjectSelected', async () => {
  const storage = createMemoryWorkingDraftStore()
  const bridge = fakeBridge()
  const { timer, run } = manualTimer()
  const view = mount({ projectRoot: '', newDraft: true, storage, requests: bridge.requests, timer })

  const title = await screen.findByLabelText('Title')
  await waitFor(() => expect(screen.getByTestId('draft-status').textContent).toContain('Only on this device'))
  await fireEvent.input(title, { target: { value: 'Draft one' } })

  // The text is on this device before it is anywhere else.
  await waitFor(async () => {
    const rows = await storage.list({ installationId: 'inst-1', projectRoot: '' })
    expect(rows.some((r) => r.content.title === 'Draft one')).toBe(true)
  })
  run()
  await new Promise((resolve) => setTimeout(resolve, 20))
  expect(bridge.calls.create).toHaveLength(0)

  // The project arrives with the route: the same draft is sent to it.
  await view.rerender(props({ projectRoot: ROOT, newDraft: true, storage, requests: bridge.requests, timer }))
  await until(run, () => expect(bridge.calls.create.length).toBeGreaterThan(0))
  expect(bridge.calls.create[0].projectRoot).toBe(ROOT)
  expect(bridge.calls.create[0].content.title).toBe('Draft one')
  await waitFor(() => expect(screen.getByTestId('draft-status').textContent).toContain('Saved'))
})

it('offersAStrandedLocalDraftToALaterSession', async () => {
  // Two repositories, so no repository is chosen on the draft's behalf: that
  // default is recorded as an edit of its own and would race the adoption.
  vi.mocked(api.listRepos).mockResolvedValue([
    { id: 'repo', url: 'https://example.test', branch: 'main' },
    { id: 'other', url: 'https://example.test/other', branch: 'main' },
  ])
  // What an earlier session typed on this page with no project chosen, plus a
  // record from another project that must never be offered here.
  const storage = createMemoryWorkingDraftStore([
    recordOf({ projectRoot: '', draftId: 'local-old', editorId: 'editor-a', content: content({ title: 'Typed before the reload' }) }),
    recordOf({ projectRoot: '/elsewhere', draftId: 'd9', editorId: 'editor-c', content: content({ title: 'Another project' }) }),
  ])
  const bridge = fakeBridge()
  const { timer } = manualTimer()
  mount({ projectRoot: '', newDraft: true, storage, requests: bridge.requests, timer, editorId: 'editor-b' })

  // The fresh controller cannot match the earlier session's editor or draft
  // id, but the record is still this page's scope, so a way back is offered.
  expect(await screen.findByTestId('draft-recovery')).toBeTruthy()
  expect(screen.getByText('Typed before the reload')).toBeTruthy()
  expect(screen.queryByText('Another project')).toBeNull()
  expect(screen.getAllByRole('button', { name: 'Restore this copy' })).toHaveLength(1)

  // Adopting it yields the original content in the editor.
  await fireEvent.click(screen.getByRole('button', { name: 'Restore this copy' }))
  await waitFor(() => expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Typed before the reload'))
})

it('restoresIncompleteFieldsAndInheritance', async () => {
  vi.mocked(api.getProjectSettings).mockResolvedValue({ intake: {}, workspace: 'go-dev', routing: { profile: 'fast' } })
  const bridge = fakeBridge(draftOf('d1', 3, content({ title: 'Partial draft', repoId: 'repo', native: false })))
  const storage = createMemoryWorkingDraftStore()
  const { timer } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage, requests: bridge.requests, timer })

  const title = (await screen.findByLabelText('Title')) as HTMLInputElement
  await waitFor(() => expect(title.value).toBe('Partial draft'))
  // The fields the draft never filled are empty, not invented.
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('')
  expect((screen.getByLabelText('Specification (optional)') as HTMLTextAreaElement).value).toBe('')
  expect((screen.getByLabelText('Target branch') as HTMLInputElement).value).toBe('')
  expect((screen.getByLabelText('Total rounds') as HTMLInputElement).value).toBe('')
  // Empty fields still follow the project, exactly as the manual form does.
  await waitFor(() => expect(screen.getByTestId('workspace-effective').textContent).toContain('Project agent default: go-dev'))
  expect(screen.getByTestId('implementer-effective').textContent).toContain('Project agent default: fast')
  expect(screen.getByTestId('reviewer-effective').textContent).toContain('Project agent default: fast')
  expect(screen.getByTestId('checks-effective').textContent).toContain('no required checks are inherited')
  // Restoring a draft is not an edit: nothing is sent on open.
  expect(bridge.calls.get).toContain('d1')
  expect(bridge.calls.create).toHaveLength(0)
  expect(bridge.calls.save).toHaveLength(0)
})

it('showsSavingAndOffline', async () => {
  // Two repositories, so no repository is chosen on the draft's behalf and the
  // only send is the one this test makes.
  vi.mocked(api.listRepos).mockResolvedValue([
    { id: 'repo', url: 'https://example.test', branch: 'main' },
    { id: 'other', url: 'https://example.test/other', branch: 'main' },
  ])
  let fail: (e: unknown) => void = () => {}
  const attempts: WorkingDraftCreateRequest[] = []
  const requests: WorkingDraftRequests = {
    installation: async () => 'inst-1',
    create: vi.fn((req: WorkingDraftCreateRequest) => {
      attempts.push(req)
      return new Promise<WorkingDraft>((_resolve, reject) => {
        fail = reject
      })
    }),
    get: vi.fn(async (id: string) => draftOf(id, 1, content())),
    save: vi.fn(async (id: string, req: WorkingDraftSaveRequest) => draftOf(id, req.expectedVersion + 1, req.content)),
    convert: vi.fn(async (id: string, req: WorkingDraftConvertRequest) => ({
      draft: draftOf(id, req.expectedVersion + 1, content(), 'converted'),
    })),
    remove: vi.fn(async (id: string, req: WorkingDraftRemoveRequest) => draftOf(id, req.expectedVersion + 1, content(), 'deleted')),
  }
  const { timer, run } = manualTimer()
  mount({ projectRoot: ROOT, newDraft: true, storage: createMemoryWorkingDraftStore(), requests, timer })

  await fireEvent.input(await screen.findByLabelText('Title'), { target: { value: 'Task' } })
  await until(run, () => expect(screen.getByTestId('draft-status').textContent).toContain('Saving'))
  expect(requests.create).toHaveBeenCalledTimes(1)

  fail(new Error('network down'))
  await waitFor(() => expect(screen.getByTestId('draft-status').textContent).toContain('Offline'))
  // The text is still there, and the retry is the same request, not a new one.
  expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Task')
  const first = attempts[0]
  await fireEvent.click(screen.getByRole('button', { name: 'Retry now' }))
  await waitFor(() => expect(attempts.length).toBeGreaterThan(1))
  expect(new Set(attempts.map((a) => a.requestId)).size).toBe(1)
  expect(attempts[1].content.title).toBe('Task')
  expect(first.content.title).toBe('Task')
})

it('refusedSaveIsAVisibleErrorNotOffline', async () => {
  // Two repositories, so no repository is chosen on the draft's behalf and the
  // only sends are the ones this test makes.
  vi.mocked(api.listRepos).mockResolvedValue([
    { id: 'repo', url: 'https://example.test', branch: 'main' },
    { id: 'other', url: 'https://example.test/other', branch: 'main' },
  ])
  const attempts: WorkingDraftCreateRequest[] = []
  const requests: WorkingDraftRequests = {
    installation: async () => 'inst-1',
    create: vi.fn((req: WorkingDraftCreateRequest) => {
      attempts.push(req)
      return attempts.length === 1
        ? Promise.reject(Object.assign(new Error('plan and specification exceed 1 MiB'), { status: 400 }))
        : Promise.resolve(draftOf('d-new', 1, req.content))
    }),
    get: vi.fn(async (id: string) => draftOf(id, 1, content())),
    save: vi.fn(async (id: string, req: WorkingDraftSaveRequest) => draftOf(id, req.expectedVersion + 1, req.content)),
    convert: vi.fn(async (id: string, req: WorkingDraftConvertRequest) => ({
      draft: draftOf(id, req.expectedVersion + 1, content(), 'converted'),
    })),
    remove: vi.fn(async (id: string, req: WorkingDraftRemoveRequest) => draftOf(id, req.expectedVersion + 1, content(), 'deleted')),
  }
  const { timer, run } = manualTimer()
  mount({ projectRoot: ROOT, newDraft: true, storage: createMemoryWorkingDraftStore(), requests, timer })

  await fireEvent.input(await screen.findByLabelText('Title'), { target: { value: 'Too big' } })
  run()
  // A confirmed refusal is the bridge answering, not the network failing: the
  // strip reports the refusal itself, and no offline retry button is offered.
  await waitFor(() => expect(screen.getByTestId('draft-status').textContent).toContain('Could not save: plan and specification exceed 1 MiB'))
  expect(screen.getByTestId('draft-status').textContent).not.toContain('Offline')
  expect(screen.queryByRole('button', { name: 'Retry now' })).toBeNull()
  expect(attempts).toHaveLength(1)

  // The user shortens the plan: the correction goes out under a fresh request
  // id, not as a byte-for-byte replay of the refused body.
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Small' } })
  run()
  await waitFor(() => expect(attempts).toHaveLength(2))
  expect(attempts[1].content.title).toBe('Small')
  expect(attempts[1].requestId).not.toBe(attempts[0].requestId)
  await waitFor(() => expect(screen.getByTestId('draft-status').textContent).toContain('Saved'))
})

it('unavailableOptionsRemainVisible', async () => {
  const bridge = fakeBridge(draftOf('d1', 2, content({
    title: 'Kept options',
    native: false,
    repoId: 'gone-repo',
    workspaceOverride: 'gone-ws',
    implementerOverride: 'gone-impl',
    reviewerOverride: 'gone-rev',
  })))
  const { timer } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage: createMemoryWorkingDraftStore(), requests: bridge.requests, timer })

  expect(await screen.findByLabelText('Title')).toBeTruthy()
  // A saved value the project no longer offers stays selectable and labelled.
  expect(await screen.findByRole('option', { name: 'gone-repo (unavailable)' })).toBeTruthy()
  expect(screen.getByRole('option', { name: 'gone-ws (unavailable)' })).toBeTruthy()
  expect(screen.getByRole('option', { name: 'gone-impl (unavailable)' })).toBeTruthy()
  expect(screen.getByRole('option', { name: 'gone-rev (unavailable)' })).toBeTruthy()
  // The available options are still there, and nothing was rewritten to them.
  expect(await screen.findByRole('option', { name: 'repo' })).toBeTruthy()
  expect(await screen.findByRole('option', { name: 'go-dev' })).toBeTruthy()
  expect((screen.getByLabelText('Repository') as HTMLSelectElement).value).toBe('gone-repo')
  expect((screen.getByLabelText('Workspace') as HTMLSelectElement).value).toBe('gone-ws')
  expect((screen.getByLabelText('Implementer model profile') as HTMLSelectElement).value).toBe('gone-impl')
  expect((screen.getByLabelText('Reviewer model profile') as HTMLSelectElement).value).toBe('gone-rev')
  // Merely opening the draft sends nothing.
  expect(bridge.calls.save).toHaveLength(0)
})

it('deletedRemoteOffersCopy', async () => {
  const bridge = fakeBridge(draftOf('d1', 4, content({ title: 'Server copy' }), 'deleted'))
  const storage = createMemoryWorkingDraftStore([
    recordOf({
      draftId: 'd1',
      editorId: 'tab-1',
      content: content({ title: 'Rescued work', plan: 'Do the thing' }),
      baseVersion: 3,
    }),
  ])
  const { timer, run } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage, requests: bridge.requests, timer, editorId: 'tab-1' })

  expect(await screen.findByTestId('draft-deleted')).toBeTruthy()
  // The unsent local content is retained on screen, not replaced by the tombstone.
  expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Rescued work')
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('Do the thing')

  await fireEvent.click(screen.getByRole('button', { name: 'Copy to new working draft' }))
  await waitFor(() => expect(screen.getByTestId('copy-notice')).toBeTruthy())
  await until(run, () => expect(bridge.calls.create.length).toBeGreaterThan(0))
  // The copy is a new working draft — not a factory item, and not a save over
  // the draft the bridge deleted.
  expect(bridge.calls.save).toHaveLength(0)
  expect(new Set(bridge.calls.create.map((c) => c.requestId)).size).toBe(1)
  expect(bridge.calls.create[0].content).toMatchObject({ title: 'Rescued work', plan: 'Do the thing' })
  expect(api.createFactoryItem).not.toHaveBeenCalled()
  // The editor now holds the copy, so the deleted notice is gone.
  expect(screen.queryByTestId('draft-deleted')).toBeNull()
  const rows = await storage.list({ installationId: 'inst-1', projectRoot: ROOT })
  expect(rows.some((r) => r.draftId !== 'd1' && r.content.title === 'Rescued work')).toBe(true)
})

const convertible = (patch: Partial<WorkingDraftContent> = {}): WorkingDraftContent =>
  content({ title: 'Ready to convert', plan: 'Do it', repoId: 'repo', noRequiredCiAcknowledged: true, ...patch })

it('conversionRequiresSavedReview', async () => {
  const bridge = fakeBridge(draftOf('d1', 2, convertible()))
  const storage = createMemoryWorkingDraftStore()
  const { timer, run } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage, requests: bridge.requests, timer, editorId: 'tab-1' })

  const title = (await screen.findByLabelText('Title')) as HTMLInputElement
  await waitFor(() => expect(title.value).toBe('Ready to convert'))
  const convertButton = () => screen.getByRole('button', { name: 'Convert to factory draft' }) as HTMLButtonElement
  await waitFor(() => expect(convertButton().disabled).toBe(false))

  // An unsent edit is not reviewable: the conversion submits a saved version.
  await fireEvent.input(title, { target: { value: 'Ready to convert now' } })
  await waitFor(() => expect(convertButton().disabled).toBe(true))
  expect(screen.getByTestId('convert-blocked').textContent).toContain('Save the draft first')
  await until(run, () => expect(bridge.calls.save.length).toBeGreaterThan(0))
  await waitFor(() => expect(convertButton().disabled).toBe(false))

  // The review shows the saved content, the resolved configuration, and the
  // exact settings version the conversion is bound to.
  await fireEvent.click(convertButton())
  const review = await screen.findByTestId('convert-review')
  expect(review.textContent).toContain('Ready to convert now')
  await waitFor(() => expect(screen.getByTestId('convert-settings-version').textContent).toBe('settings-v1'))
  expect(screen.getByTestId('convert-branch').textContent).toContain('Repository branch: main')
  expect(screen.getByTestId('convert-repo').textContent).toBe('repo')
  // Opening a review sends nothing on its own.
  expect(bridge.calls.convert).toHaveLength(0)

  await fireEvent.click(screen.getByRole('button', { name: 'Convert and create the item' }))
  await waitFor(() => expect(bridge.calls.convert).toHaveLength(1))
  const sent = bridge.calls.convert[0].req
  // The reviewed version and settings version are what a retry replays.
  expect(sent.expectedVersion).toBe(3)
  expect(sent.settingsVersion).toBe('settings-v1')
  expect(sent.factoryRequest.title).toBe('Ready to convert now')
  expect(sent.factoryRequest.requestId).toBe(sent.requestId)
})

it('settingsChangeRequiresNewReview', async () => {
  const draft = convertible({ title: 'Reviewed draft' })
  const bridge = fakeBridge(draftOf('d1', 2, draft))
  vi.mocked(api.getVersionedProjectSettings)
    .mockResolvedValueOnce({ settings: { intake: {}, routing: { profile: 'fast' } }, etag: 'settings-v1' })
    .mockResolvedValueOnce({ settings: { intake: {}, routing: { profile: 'careful' } }, etag: 'settings-v2' })

  // The bridge refuses: the draft did not move, so the reviewed settings did.
  const convert = vi.fn(async (id: string, req: WorkingDraftConvertRequest) => {
    throw new WorkingDraftConflictError(`project settings changed at ${req.requestId}; review required`, draftOf(id, 2, draft))
  })
  const requests: WorkingDraftRequests = { ...bridge.requests, convert }
  const storage = createMemoryWorkingDraftStore()
  const { timer } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage, requests, timer, editorId: 'tab-1' })

  const convertButton = () => screen.getByRole('button', { name: 'Convert to factory draft' }) as HTMLButtonElement
  await waitFor(() => expect(convertButton().disabled).toBe(false))
  await fireEvent.click(convertButton())
  await waitFor(() => expect(screen.getByTestId('convert-settings-version').textContent).toBe('settings-v1'))

  await fireEvent.click(screen.getByRole('button', { name: 'Convert and create the item' }))
  // The refusal is reported, the settings are read again, and nothing is
  // resubmitted on the user's behalf.
  await waitFor(() => expect(screen.getByTestId('convert-notice')).toBeTruthy())
  expect(screen.getByTestId('convert-notice').textContent).toContain('settings changed')
  await waitFor(() => expect(screen.getByTestId('convert-settings-version').textContent).toBe('settings-v2'))
  expect(convert).toHaveBeenCalledTimes(1)
  expect(api.getVersionedProjectSettings).toHaveBeenCalledTimes(2)
  // The new configuration is what the second review shows.
  expect(screen.getByTestId('convert-review').textContent).toContain('Project agent default: careful')

  // A second confirmation is what sends again, against the settings it shows.
  await fireEvent.click(screen.getByRole('button', { name: 'Convert and create the item' }))
  await waitFor(() => expect(convert).toHaveBeenCalledTimes(2))
  expect(vi.mocked(convert).mock.calls[1][1].settingsVersion).toBe('settings-v2')
})

it('removesADraftOnlyAfterConfirmation', async () => {
  const bridge = fakeBridge(draftOf('d1', 2, content({ title: 'Not needed after all', plan: 'The plan' })))
  const { timer } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage: createMemoryWorkingDraftStore(), requests: bridge.requests, timer, editorId: 'tab-1' })

  await screen.findByLabelText('Title')
  // Removal is asked about first: opening the question sends nothing, and
  // keeping the draft sends nothing either.
  await fireEvent.click(screen.getByRole('button', { name: 'Remove this working draft' }))
  expect(bridge.calls.remove).toHaveLength(0)
  await fireEvent.click(screen.getByRole('button', { name: 'Keep this draft' }))
  expect(bridge.calls.remove).toHaveLength(0)

  await fireEvent.click(screen.getByRole('button', { name: 'Remove this working draft' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Remove it' }))
  await waitFor(() => expect(bridge.calls.remove).toHaveLength(1))
  // The frozen request went out against the version the editor had.
  expect(bridge.calls.remove[0]).toMatchObject({ id: 'd1' })
  expect(bridge.calls.remove[0].req).toMatchObject({ requestId: expect.any(String), expectedVersion: 2 })

  // The tombstone shows, and the local copy stays readable and recoverable.
  await waitFor(() => expect(screen.getByTestId('draft-deleted')).toBeTruthy())
  expect(screen.getByTestId('draft-status').textContent).toContain('Deleted')
  expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Not needed after all')
  expect((screen.getByLabelText('Plan') as HTMLTextAreaElement).value).toBe('The plan')
  expect(screen.getByRole('button', { name: 'Copy to new working draft' })).toBeTruthy()
  // The control is gone: the bridge already holds the tombstone.
  expect(screen.queryByRole('button', { name: 'Remove this working draft' })).toBeNull()
})

it('aStaleRemovalRefreshesTheDraftInsteadOfDeleting', async () => {
  const mine = content({ title: 'Mine' })
  const bridge = fakeBridge(draftOf('d1', 2, mine))
  const theirs = content({ title: 'Theirs', plan: 'Their plan' })
  const remove = vi.fn(async (_id: string, _req: WorkingDraftRemoveRequest): Promise<WorkingDraft | null> => {
    throw new WorkingDraftConflictError('stale version', draftOf('d1', 5, theirs))
  })
  const requests: WorkingDraftRequests = { ...bridge.requests, remove }
  const { timer } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage: createMemoryWorkingDraftStore(), requests, timer, editorId: 'tab-1' })

  await screen.findByLabelText('Title')
  await fireEvent.click(screen.getByRole('button', { name: 'Remove this working draft' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Remove it' }))

  // Nothing was deleted: the bridge's current draft is what shows instead.
  await waitFor(() => expect(screen.getByTestId('draft-conflict')).toBeTruthy())
  expect(screen.getByTestId('local-title').textContent).toBe('Mine')
  expect(screen.getByTestId('server-title').textContent).toBe('Theirs')
  expect(remove).toHaveBeenCalledTimes(1)
  expect(screen.queryByTestId('draft-deleted')).toBeNull()
  // The control is gone while the conflict is open.
  expect(screen.queryByRole('button', { name: 'Remove this working draft' })).toBeNull()
})

it('aConflictWithNoCurrentDraftOffersAReload', async () => {
  const mine = content({ title: 'Mine' })
  let reads = 0
  const requests: WorkingDraftRequests = {
    installation: async () => 'inst-1',
    create: vi.fn(async (req: WorkingDraftCreateRequest) => draftOf('d-new', 1, req.content)),
    get: vi.fn(async (id: string) => {
      reads++
      if (reads > 1) throw Object.assign(new Error('working draft not found: project is not registered'), { status: 404 })
      return draftOf(id, 2, mine)
    }),
    save: vi.fn(async (_id: string, _req: WorkingDraftSaveRequest) => {
      throw new WorkingDraftConflictError('expected version 2, have 7', null)
    }),
    convert: vi.fn(async (id: string, req: WorkingDraftConvertRequest) => ({
      draft: draftOf(id, req.expectedVersion + 1, content(), 'converted'),
    })),
    remove: vi.fn(async (id: string, req: WorkingDraftRemoveRequest) => draftOf(id, req.expectedVersion + 1, content(), 'deleted')),
  }
  const { timer, run } = manualTimer()
  mount({ projectRoot: ROOT, draftId: 'd1', storage: createMemoryWorkingDraftStore(), requests, timer, editorId: 'tab-1' })

  // An edit meets a 409 that carries no current draft to compare against.
  const title = (await screen.findByLabelText('Title')) as HTMLInputElement
  await fireEvent.input(title, { target: { value: 'Mine, edited' } })
  run()
  await waitFor(() => expect(screen.getByTestId('conflict-unreadable')).toBeTruthy())
  expect(screen.getByTestId('draft-status').textContent).toContain('changed elsewhere')
  // The editor keeps the local text, and sends are blocked rather than guessed.
  expect(title.value).toBe('Mine, edited')

  // Reloading asks the bridge again: it no longer has the draft, so the
  // conflict clears and the editor is usable again, local text intact.
  await fireEvent.click(screen.getByRole('button', { name: 'Reload from the bridge' }))
  await waitFor(() => expect(screen.queryByTestId('conflict-unreadable')).toBeNull())
  expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Mine, edited')
  expect(screen.getByTestId('draft-status').textContent).not.toContain('changed elsewhere')
  expect(screen.getByTestId('draft-status').textContent).toContain('Offline')
})
