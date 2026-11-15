<script lang="ts">
  import { onDestroy } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import {
    convertWorkingDraft,
    createWorkingDraft,
    errMessage,
    getWorkingDraft,
    getWorkingDraftInstallation,
    getVersionedProjectSettings,
    listRepos,
    listWorkingDrafts,
    saveWorkingDraft,
    type ProjectSettings,
    type RepoRow,
  } from '../lib/api'
  import { formatFactoryRoute, formatProjectRoute, formatWorkingDraftsRoute } from '../lib/routes'
  import FactoryNew, { describeEffective, factoryRequestOf, resolveFactory } from './FactoryNew.svelte'
  import WorkingDraftConflict from '../lib/WorkingDraftConflict.svelte'
  import {
    createIndexedDbWorkingDraftStore,
    createWorkingDraftController,
    sameWorkingDraftContent,
    type WorkingDraft,
    type WorkingDraftContent,
    type WorkingDraftController,
    type WorkingDraftConvertRequest,
    type WorkingDraftRecord,
    type WorkingDraftRequests,
    type WorkingDraftStore,
    type WorkingDraftTimer,
    type WorkingDraftView,
  } from '../lib/working-drafts'

  /**
   * Working drafts: the project's list, and one draft's editor.
   *
   * The list is what this device and the bridge know between them. The editor
   * is the handoff form itself — `FactoryNew` with a controller — so the
   * fields, the inheritance rules and the checks-mode distinction are the same
   * code the manual handoff uses. Everything around it here is about state the
   * user has to be able to see: saved, saving, offline, conflicting, failed,
   * deleted and converted.
   *
   * The controller is injected, not built into the component, so the storage
   * adapter and the bridge calls are testable and a browser without IndexedDB
   * fails visibly instead of silently losing edits.
   */
  let {
    projectRoot = '',
    draftId = null,
    newDraft = false,
    onNavigate,
    storage,
    requests,
    installationId,
    now,
    timer,
    editorId,
  }: {
    projectRoot?: string
    draftId?: string | null
    newDraft?: boolean
    onNavigate: (hash: string) => void
    storage?: WorkingDraftStore
    requests?: WorkingDraftRequests
    installationId?: () => string | Promise<string>
    now?: () => number
    timer?: WorkingDraftTimer
    editorId?: string
  } = $props()

  /** The bridge calls a draft controller makes. Tests inject their own. */
  const apiRequests: WorkingDraftRequests = {
    installation: getWorkingDraftInstallation,
    create: createWorkingDraft,
    get: getWorkingDraft,
    save: saveWorkingDraft,
    convert: convertWorkingDraft,
  }
  // Only constructed when the caller did not supply one; the adapter opens its
  // database lazily, so this costs nothing until a draft is actually kept.
  const store: WorkingDraftStore = storage ?? createIndexedDbWorkingDraftStore()
  const storeFor = (): WorkingDraftStore => store

  let controller: WorkingDraftController | null = null
  let unsubscribe: (() => void) | null = null
  let view = $state<WorkingDraftView | null>(null)
  let rows = $state<WorkingDraft[]>([])
  let locals = $state<WorkingDraftRecord[]>([])
  let loading = $state(true)
  let listError = $state('')
  /** True while the editor is a copy of a draft the bridge no longer has. */
  let copied = $state(false)

  const open = $derived(draftId ?? null)
  const editing = $derived(!!open || newDraft)
  /** The bridge's record for the open draft, when there is one. */
  const remote = $derived(rows.find((r) => r.id === view?.draftId) ?? null)

  function makeController(): WorkingDraftController {
    return createWorkingDraftController({
      storage: storeFor(),
      requests: requests ?? apiRequests,
      installationId,
      now,
      timer,
      editorId,
    })
  }

  /** The bridge's drafts for the project, and this device's unsent ones. */
  async function loadList(root: string, installation: string | null) {
    listError = ''
    if (!root) {
      rows = []
      locals = []
      return
    }
    try {
      rows = await listWorkingDrafts(root)
    } catch (e) {
      rows = []
      listError = errMessage(e)
    }
    if (!installation) {
      locals = []
      return
    }
    try {
      locals = await storeFor().list({ installationId: installation, projectRoot: root })
    } catch (e) {
      locals = []
      listError = errMessage(e)
    }
  }

  /**
   * Reuse the controller when only the project scope changed: a draft made
   * before a project was chosen keeps its content and is sent the moment one
   * arrives. Anything else is a different draft and gets a fresh controller.
   */
  let bound = { draftId: null as string | null, creating: false, projectRoot: '' }

  $effect(() => {
    const root = projectRoot
    const openId = draftId ?? null
    const creating = newDraft
    if (controller && root !== bound.projectRoot && openId === bound.draftId && creating === bound.creating) {
      bound = { ...bound, projectRoot: root }
      void controller.assignProject(root)
      return
    }
    if (controller && openId === bound.draftId && creating === bound.creating) return

    unsubscribe?.()
    controller?.destroy()
    const next = makeController()
    controller = next
    bound = { draftId: openId, creating, projectRoot: root }
    unsubscribe = next.subscribe((snapshot) => { view = snapshot })
    loading = true
    void (async () => {
      try {
        if (openId || creating) {
          await next.load(root || null, openId)
          await loadList(root, next.view().installationId)
        } else {
          const listed = await next.load(root || null, null)
          await loadList(root, listed.installationId)
        }
      } catch (e) {
        listError = errMessage(e)
      } finally {
        loading = false
      }
    })()
  })

  onDestroy(() => {
    unsubscribe?.()
    controller?.destroy()
  })

  // A draft that has not reached the bridge is only lost on purpose: the
  // browser's own unload warning covers reload and close.
  $effect(() => {
    const pending = view?.pending ?? false
    const offline = view?.offline ?? false
    if (!pending && !offline) return
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  })

  // ---- the editor ---------------------------------------------------------

  /**
   * Save the merged content. It becomes this editor's local content and is
   * sent against the version the newest conflict reported, so a merge is never
   * a blind overwrite of a version nobody saw.
   */
  async function resolve(combined: WorkingDraftContent) {
    if (!controller) return
    await controller.edit(combined)
    await controller.keepLocal()
  }
  function keepLocal() {
    void controller?.keepLocal()
  }
  function discardLocal() {
    void controller?.discardLocal()
  }
  function restoreCopy(id: string) {
    void controller?.adopt(id)
  }

  /**
   * A deleted draft's local content is all that is left of the work. Copying it
   * makes a new draft on this device — never a factory item — and sends it
   * once it has a project.
   */
  async function copyToNewDraft() {
    const content = view?.local
    if (!controller || !content) return
    unsubscribe?.()
    controller.destroy()
    const next = makeController()
    controller = next
    bound = { draftId: null, creating: true, projectRoot }
    unsubscribe = next.subscribe((snapshot) => { view = snapshot })
    copied = true
    await next.load(projectRoot || null, null)
    await next.edit({ ...content })
  }

  // ---- converting ---------------------------------------------------------

  /**
   * One conversion, reviewed before it is sent. The panel shows the exact saved
   * draft, the configuration the project settings resolve it to, and the
   * settings version the conversion is bound to; confirming sends all three.
   * Nothing is sent while the draft has unsaved edits or the settings read is
   * stale, and nothing is resubmitted on the user's behalf after a refusal.
   */
  let reviewOpen = $state(false)
  let reviewContent = $state<WorkingDraftContent | null>(null)
  let reviewSettings = $state<ProjectSettings | null>(null)
  let reviewRepos = $state<RepoRow[]>([])
  let reviewEtag = $state('')
  let reviewError = $state('')
  let reviewNotice = $state('')
  let reviewBusy = $state(false)

  /** A request id for one confirmation. The controller keeps it while frozen. */
  function nextRequestId(): string {
    const c = typeof globalThis === 'undefined' ? undefined : globalThis.crypto
    if (c && typeof c.randomUUID === 'function') return c.randomUUID()
    return `convert-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
  }

  /** Read the settings version and repositories this review would be bound to. */
  async function loadReview(): Promise<void> {
    reviewError = ''
    reviewEtag = ''
    if (!projectRoot) {
      reviewSettings = null
      reviewRepos = []
      return
    }
    try {
      const [versioned, repos] = await Promise.all([getVersionedProjectSettings(projectRoot), listRepos()])
      reviewSettings = versioned.settings
      reviewRepos = repos
      reviewEtag = versioned.etag
    } catch (e) {
      reviewSettings = null
      reviewRepos = []
      reviewError = `Could not read the project settings: ${errMessage(e)}`
    }
  }

  function openReview(): void {
    const snapshot = view?.local
    if (!snapshot) return
    reviewContent = { ...snapshot }
    reviewOpen = true
    reviewedVersion = -1
    reviewError = ''
    reviewNotice = ''
  }

  function closeReview(): void {
    reviewOpen = false
    reviewContent = null
    reviewError = ''
    reviewNotice = ''
  }

  // A settings version is only valid beside the saved version it was read at: a
  // save underneath the open panel refetches before the confirm is enabled.
  let reviewedVersion = -1
  $effect(() => {
    if (!reviewOpen) return
    const version = view?.baseVersion ?? -1
    if (version === reviewedVersion) return
    reviewedVersion = version
    void loadReview()
  })

  const reviewResolution = $derived(
    reviewContent ? resolveFactory({ content: reviewContent, projectRoot, settings: reviewSettings, repos: reviewRepos }) : null,
  )
  /** Why this reviewed content could not become an item, if it could not. */
  const reviewProblem = $derived.by((): string => {
    if (!reviewContent) return ''
    try {
      factoryRequestOf({ content: reviewContent, projectRoot, settings: reviewSettings, repos: reviewRepos, requestId: 'review' })
      return ''
    } catch (e) {
      return errMessage(e)
    }
  })
  /** What stops this draft being converted right now, or '' when nothing does. */
  const convertBlocked = $derived.by((): string => {
    const v = view
    if (!v) return 'Loading the draft…'
    if (v.localOnly) return 'This draft has no project yet, so it cannot become factory work.'
    if (!v.draftId) return 'This draft is only on this device. It has to be saved once before it can be reviewed.'
    if (v.pending) return 'Save the draft first: the review converts the saved version.'
    if (v.state === 'converted') return 'This draft is already a factory item.'
    if (v.state === 'deleted') return 'This draft was deleted, so it cannot be converted.'
    if (reviewContent && !sameWorkingDraftContent(reviewContent, v.base)) return 'The saved draft changed since this review. Review it again.'
    return ''
  })

  async function confirmConvert(): Promise<void> {
    if (!controller || !view || !reviewContent || reviewBusy) return
    const reviewedEtag = reviewEtag
    reviewBusy = true
    reviewError = ''
    reviewNotice = ''
    let request: WorkingDraftConvertRequest
    try {
      const requestId = nextRequestId()
      request = {
        requestId,
        expectedVersion: view.baseVersion,
        factoryRequest: factoryRequestOf({ content: { ...reviewContent }, projectRoot, settings: reviewSettings, repos: reviewRepos, requestId }),
        settingsVersion: reviewedEtag,
      }
    } catch (e) {
      reviewError = errMessage(e)
      reviewBusy = false
      return
    }
    const next = await controller.convert(request)
    reviewBusy = false
    if (next.state === 'converted') {
      closeReview()
      void loadList(projectRoot, next.installationId)
      return
    }
    // A frozen conversion keeps the panel closed behind the retry card.
    if (next.state === 'converting') return
    // Nothing was created under this review. Read the settings again and ask for
    // a fresh confirmation rather than resubmitting on the user's behalf.
    await loadReview()
    if (reviewedEtag && reviewEtag && reviewedEtag !== reviewEtag) {
      reviewNotice = 'Project settings changed — review the conversion again.'
      return
    }
    reviewError = next.error ?? 'The conversion did not start. Review it again.'
  }

  async function retryConvert(): Promise<void> {
    if (!controller || reviewBusy) return
    reviewBusy = true
    const next = await controller.retryConversion()
    reviewBusy = false
    if (next.state === 'converted') {
      closeReview()
      void loadList(projectRoot, next.installationId)
    }
  }

  // ---- what the user is told ----------------------------------------------

  /**
   * Exactly one state is reported, most urgent first. The list of them is the
   * contract: a draft is saved, saving, offline, conflicted or failed, and a
   * draft with no project is only on this device.
   */
  const status = $derived.by((): { text: string; tone: string } => {
    const v = view
    if (!v) return { text: '', tone: 'text-muted' }
    // A conversion is not a save: its state is the draft's, and it is reported
    // before anything about the local copy.
    if (v.state === 'converting') return { text: 'Conversion sent — outcome uncertain, retry to confirm it', tone: 'text-warn' }
    if (v.state === 'converted') return { text: 'Converted — this draft is read-only', tone: 'text-ok' }
    if (v.conflict) return { text: 'This draft changed elsewhere — resolve the conflict below', tone: 'text-warn' }
    // A transport failure is its own state: the request is kept and retried
    // byte for byte, so "offline" says more than the message it came with.
    if (v.offline) return { text: 'Offline — kept on this device, will retry', tone: 'text-warn' }
    if (v.error) return { text: `Could not save: ${v.error}`, tone: 'text-err' }
    if (v.saving) return { text: 'Saving…', tone: 'text-muted' }
    if (v.localOnly) return { text: 'Only on this device — open this draft from a project to save it', tone: 'text-warn' }
    if (v.pending) return { text: 'Pending — saved on this device', tone: 'text-muted' }
    if (v.saved) return { text: 'Saved', tone: 'text-ok' }
    return { text: 'Pending — saved on this device', tone: 'text-muted' }
  })

  /** The factory item this draft became, from whichever side knows it. */
  const convertedItemId = $derived(view?.factoryItemId ?? remote?.factoryItemId ?? null)

  interface ListRow { key: string; title: string; state: string; detail: string; href: string; itemId?: string }

  /** The bridge's drafts, then the ones only this device holds. */
  const listRows = $derived.by((): ListRow[] => {
    const served = new Set(rows.map((r) => r.id))
    const out: ListRow[] = rows.map((r) => ({
      key: `server-${r.id}`,
      title: r.content.title.trim() || 'Untitled draft',
      state: r.state,
      detail: r.factoryItemId ? 'Became factory work' : `Updated ${r.updatedAt}`,
      href: formatWorkingDraftsRoute({ draftId: r.id }, projectRoot),
      itemId: r.factoryItemId,
    }))
    for (const l of locals) {
      if (served.has(l.draftId)) continue
      out.push({
        key: `local-${l.draftId}-${l.editorId}`,
        title: l.content.title.trim() || 'Untitled draft',
        state: 'local',
        detail: 'Only on this device',
        href: formatWorkingDraftsRoute({ draftId: l.draftId }, projectRoot),
      })
    }
    return out
  })

  const listHref = $derived(formatWorkingDraftsRoute({}, projectRoot))
  const newHref = $derived(formatWorkingDraftsRoute({ create: true }, projectRoot))
  const go = (hash: string) => (e: MouseEvent) => { e.preventDefault(); onNavigate(hash) }
</script>

<div class="mx-auto flex min-w-0 max-w-4xl flex-col gap-4 p-3 sm:p-6">
  <div class="flex min-w-0 flex-wrap items-center justify-between gap-2">
    <div class="flex min-w-0 flex-wrap gap-3">
      <a class="text-sm text-accent underline" href="#factory" onclick={go('#factory')}>Factory</a>
      <a class="text-sm text-accent underline" href={listHref} onclick={go(listHref)}>Working drafts</a>
    </div>
    {#if projectRoot}
      <a class="text-sm text-accent underline" href={formatProjectRoute(projectRoot)} target="_blank" rel="noopener noreferrer" aria-label="Open project settings in a new tab">Project settings</a>
    {/if}
  </div>

  {#if listError}
    <Card class="border-attention text-sm" role="alert">{listError}</Card>
  {/if}
  {#if copied}
    <Card class="text-sm" role="status" data-testid="copy-notice">This is a new draft with your copy of the deleted draft's content. The factory item was not changed.</Card>
  {/if}

  {#if !editing}
    <header class="flex min-w-0 flex-wrap items-center justify-between gap-2">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold">Working drafts</h1>
        <p class="min-w-0 break-words text-sm text-muted">
          {#if projectRoot}Saved as you type, for {projectRoot}.{:else}Open working drafts from a project to save them there.{/if}
        </p>
      </div>
      <Button onclick={() => onNavigate(newHref)}>New working draft</Button>
    </header>
    {#if loading}
      <p role="status">Loading working drafts…</p>
    {:else if !listRows.length}
      <Card class="text-sm">
        <p>No working drafts here yet.</p>
        {#if !projectRoot}<p class="mt-1 text-muted">A draft needs a project: open one from the project page.</p>{/if}
      </Card>
    {:else}
      <ul class="grid min-w-0 gap-3">
        {#each listRows as row (row.key)}
          <li class="min-w-0">
            <Card class="flex min-w-0 flex-wrap items-center gap-3" data-testid="draft-row">
              <div class="min-w-0 flex-1">
                <a class="block min-w-0 break-words font-semibold text-accent underline" href={row.href} onclick={go(row.href)}>{row.title}</a>
                <p class="min-w-0 break-words text-xs text-muted">{row.detail}</p>
              </div>
              <Tag tone={row.state === 'local' ? 'warn' : row.state === 'converted' || row.state === 'deleted' ? 'neutral' : 'info'}>{row.state}</Tag>
              {#if row.itemId}
                <a class="text-sm text-accent underline" href={formatFactoryRoute({ itemId: row.itemId })} onclick={go(formatFactoryRoute({ itemId: row.itemId }))}>Factory item</a>
              {/if}
            </Card>
          </li>
        {/each}
      </ul>
    {/if}
  {:else}
    <header class="flex min-w-0 flex-wrap items-center justify-between gap-2">
      <h1 class="min-w-0 break-words text-xl font-semibold">{newDraft && !open ? 'New working draft' : 'Working draft'}</h1>
      <div class="flex flex-wrap gap-2">
        <a class="text-sm text-accent underline" href={listHref} onclick={go(listHref)}>All drafts</a>
        <a class="text-sm text-accent underline" href="#factory/new" onclick={go('#factory/new')}>New work item</a>
      </div>
    </header>

    {#if view}
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        <p class="min-w-0 break-words text-sm {status.tone}" role="status" data-testid="draft-status">{status.text}</p>
        {#if view.offline}
          <Button variant="ghost" class="min-h-9 px-2 py-1 text-xs" onclick={() => void controller?.flush()}>Retry now</Button>
        {/if}
      </div>
    {/if}

    {#if loading || !view || !controller}
      <p role="status">Loading draft…</p>
    {:else if view.localOnly}
      <p class="min-w-0 break-words text-xs text-muted">This draft has no project yet. It stays on this device until it is opened from a project.</p>
    {/if}

    {#if view && controller && !loading}
      {#if view.conflict && view.local && view.server}
        <WorkingDraftConflict
          base={view.base}
          local={view.local}
          server={view.server}
          onResolve={(combined) => void resolve(combined)}
          onKeepLocal={keepLocal}
          onDiscardLocal={discardLocal}
        />
      {:else if view.state === 'converted'}
        <Card class="flex min-w-0 flex-col gap-3" data-testid="draft-converted">
          <h2 class="text-sm font-semibold">This draft became factory work</h2>
          <p class="min-w-0 break-words text-sm text-muted">It is read-only here, so the item it became is not edited by accident.</p>
          {#if convertedItemId}
            <a class="text-sm text-accent underline" href={formatFactoryRoute({ itemId: convertedItemId })} onclick={go(formatFactoryRoute({ itemId: convertedItemId }))}>Open the factory item</a>
          {:else}
            <a class="text-sm text-accent underline" href="#factory" onclick={go('#factory')}>Open the factory list</a>
          {/if}
          {#if view.local}
            <dl class="grid min-w-0 gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_minmax(0,1fr)]">
              <dt class="text-muted">Title</dt><dd class="min-w-0 break-words">{view.local.title || 'Untitled draft'}</dd>
              <dt class="text-muted">Plan</dt><dd class="min-w-0 whitespace-pre-wrap break-words">{view.local.plan || 'Empty'}</dd>
              {#if view.local.spec}<dt class="text-muted">Specification</dt><dd class="min-w-0 whitespace-pre-wrap break-words">{view.local.spec}</dd>{/if}
            </dl>
          {/if}
          {#if view.pending}
            <div><Button variant="ghost" onclick={() => void copyToNewDraft()}>New working draft from these edits</Button></div>
          {/if}
        </Card>
      {:else}
        {#if view.state === 'deleted'}
          <Card class="flex min-w-0 flex-col gap-3 border-attention text-sm" role="alert" data-testid="draft-deleted">
            <p>This draft was deleted on the server. Your copy is still here and has not been sent anywhere.</p>
            {#if view.local}<Button variant="ghost" onclick={() => void copyToNewDraft()}>Copy to new working draft</Button>{/if}
          </Card>
        {/if}
        {#if view.recoverable.length}
          <Card class="flex min-w-0 flex-col gap-2 text-sm" data-testid="draft-recovery">
            <p>Another editor left a copy of this draft on this device.</p>
            {#each view.recoverable as copy (copy.editorId)}
              <div class="flex min-w-0 flex-wrap items-center gap-2">
                <span class="min-w-0 break-words text-xs text-muted">{copy.content.title || 'Untitled draft'}{#if copy.pending} · unsent{/if}</span>
                <Button variant="ghost" class="min-h-9 px-2 py-1 text-xs" onclick={() => restoreCopy(copy.editorId)}>Restore this copy</Button>
              </div>
            {/each}
          </Card>
        {/if}
        {#if view.state === 'converting'}
          <Card class="flex min-w-0 flex-col gap-3 border-attention" role="alert" data-testid="draft-converting">
            <h2 class="text-sm font-semibold">Conversion in progress — outcome unknown</h2>
            <p class="min-w-0 break-words text-sm">
              This draft was sent to become factory work, but the bridge has not confirmed it. Retrying sends the same
              request, so it cannot create a second item.
            </p>
            {#if view.conversion}
              <p class="min-w-0 break-words font-mono text-xs text-muted" data-testid="conversion-frozen">
                Request {view.conversion.requestId} · reviewed against project settings {view.conversion.settingsVersion || 'unversioned'}
              </p>
            {/if}
            {#if view.error}<p class="min-w-0 break-words text-xs text-attention">{view.error}</p>{/if}
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <Button disabled={reviewBusy} onclick={() => void retryConvert()}>{reviewBusy ? 'Retrying…' : 'Retry the conversion'}</Button>
              <a class="text-sm text-accent underline" href={listHref} onclick={go(listHref)}>Reload from the bridge</a>
            </div>
          </Card>
        {:else if view.state === 'editing' && view.draftId}
          <Card class="flex min-w-0 flex-col gap-3" data-testid="draft-convert">
            <div class="flex min-w-0 flex-wrap items-start justify-between gap-2">
              <div class="min-w-0">
                <h2 class="text-sm font-semibold">Convert to factory draft</h2>
                <p class="min-w-0 break-words text-xs text-muted">One reviewed draft becomes one unapproved factory item. Nothing runs until that item is approved.</p>
              </div>
              {#if !reviewOpen}
                <Button class="min-h-9 px-2 py-1 text-xs" disabled={convertBlocked !== ''} onclick={openReview}>Convert to factory draft</Button>
              {/if}
            </div>
            {#if !reviewOpen && convertBlocked}
              <p class="min-w-0 break-words text-xs text-muted" data-testid="convert-blocked">{convertBlocked}</p>
            {/if}
            {#if reviewOpen && reviewContent && reviewResolution}
              <div class="grid min-w-0 gap-2 border-t border-border pt-3" data-testid="convert-review">
                <h3 class="min-w-0 break-words text-sm font-semibold">Review this conversion</h3>
                <p class="min-w-0 break-words text-xs text-muted">This is the saved draft that will be submitted, and how the project settings resolve it.</p>
                <dl class="grid min-w-0 gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_minmax(0,1fr)]">
                  <dt class="text-muted">Title</dt>
                  <dd class="min-w-0 break-words" data-testid="convert-title">{reviewContent.title.trim() || 'Untitled draft'}</dd>
                  <dt class="text-muted">Plan</dt>
                  <dd class="min-w-0 whitespace-pre-wrap break-words">{reviewContent.plan || 'Empty'}</dd>
                  {#if reviewContent.spec}
                    <dt class="text-muted">Specification</dt>
                    <dd class="min-w-0 whitespace-pre-wrap break-words">{reviewContent.spec}</dd>
                  {/if}
                  <dt class="text-muted">Repository</dt>
                  <dd class="min-w-0 break-words" data-testid="convert-repo">{reviewContent.repoId || 'Not chosen'}</dd>
                  <dt class="text-muted">Target branch</dt>
                  <dd class="min-w-0 break-words" data-testid="convert-branch">{describeEffective(reviewResolution.branch, 'no branch to target')}</dd>
                  <dt class="text-muted">Environment</dt>
                  <dd class="min-w-0 break-words" data-testid="convert-environment">
                    {reviewContent.native ? 'Native environment — no workspace is used' : describeEffective(reviewResolution.workspace, 'choose a workspace or run natively')}
                  </dd>
                  <dt class="text-muted">Implementer</dt>
                  <dd class="min-w-0 break-words">{describeEffective(reviewResolution.implementer, 'set a project model profile or choose one')}</dd>
                  <dt class="text-muted">Reviewer</dt>
                  <dd class="min-w-0 break-words">{describeEffective(reviewResolution.reviewer, 'set a project model profile or choose one')}</dd>
                  <dt class="text-muted">Expected CI checks</dt>
                  <dd class="min-w-0 break-words" data-testid="convert-checks">{reviewResolution.checksEffective}</dd>
                  <dt class="text-muted">Rounds</dt>
                  <dd class="min-w-0 break-words">{describeEffective(reviewResolution.maxRounds, 'no round limit')}</dd>
                  <dt class="text-muted">CI poll / wait</dt>
                  <dd class="min-w-0 break-words">{describeEffective(reviewResolution.ciPoll, 'no polling interval')} / {describeEffective(reviewResolution.ciWait, 'no wait limit')}</dd>
                  <dt class="text-muted">Reviewer instructions</dt>
                  <dd class="min-w-0 whitespace-pre-wrap break-words">
                    {reviewContent.reviewerInstructions.trim() ? 'This handoff: its own instructions' : describeEffective(reviewResolution.reviewerInstructionsEffective, 'no instructions')}
                  </dd>
                  <dt class="text-muted">Settings version</dt>
                  <dd class="min-w-0 break-all font-mono text-xs" data-testid="convert-settings-version">{reviewEtag || 'Reading…'}</dd>
                </dl>
                {#if reviewResolution.workspaceConflict}
                  <p class="min-w-0 break-words text-xs text-attention">
                    The project's factory workspace "{reviewResolution.factoryWorkspace}" and a native run cannot both apply. Change this draft's environment, or the project's factory settings.
                  </p>
                {/if}
                {#if reviewProblem}
                  <p class="min-w-0 break-words text-xs text-attention" role="alert" data-testid="convert-problem">{reviewProblem}</p>
                {/if}
                {#if reviewNotice}
                  <p class="min-w-0 break-words text-xs text-warn" role="status" data-testid="convert-notice">{reviewNotice}</p>
                {/if}
                {#if reviewError}
                  <p class="min-w-0 break-words text-xs text-attention" role="alert" data-testid="convert-error">{reviewError}</p>
                {/if}
                {#if convertBlocked}
                  <p class="min-w-0 break-words text-xs text-muted" data-testid="convert-blocked">{convertBlocked}</p>
                {/if}
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <Button
                    disabled={reviewBusy || !reviewEtag || reviewProblem !== '' || convertBlocked !== ''}
                    onclick={() => void confirmConvert()}
                  >{reviewBusy ? 'Converting…' : 'Convert and create the item'}</Button>
                  <Button variant="ghost" onclick={closeReview}>Cancel</Button>
                </div>
              </div>
            {/if}
          </Card>
        {/if}
        <FactoryNew {projectRoot} {onNavigate} draft={{ controller, draftId: view.draftId ?? undefined }} />
      {/if}
    {/if}
  {/if}
</div>
