<script lang="ts">
  import { onDestroy } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import {
    createWorkingDraft,
    errMessage,
    getWorkingDraft,
    getWorkingDraftInstallation,
    listWorkingDrafts,
    saveWorkingDraft,
  } from '../lib/api'
  import { formatFactoryRoute, formatProjectRoute, formatWorkingDraftsRoute } from '../lib/routes'
  import FactoryNew from './FactoryNew.svelte'
  import WorkingDraftConflict from '../lib/WorkingDraftConflict.svelte'
  import {
    createIndexedDbWorkingDraftStore,
    createWorkingDraftController,
    type WorkingDraft,
    type WorkingDraftContent,
    type WorkingDraftController,
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

  // ---- what the user is told ----------------------------------------------

  /**
   * Exactly one state is reported, most urgent first. The list of them is the
   * contract: a draft is saved, saving, offline, conflicted or failed, and a
   * draft with no project is only on this device.
   */
  const status = $derived.by((): { text: string; tone: string } => {
    const v = view
    if (!v) return { text: '', tone: 'text-muted' }
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
          {#if remote?.factoryItemId}
            <a class="text-sm text-accent underline" href={formatFactoryRoute({ itemId: remote.factoryItemId })} onclick={go(formatFactoryRoute({ itemId: remote.factoryItemId }))}>Open the factory item</a>
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
        <FactoryNew {projectRoot} {onNavigate} draft={{ controller, draftId: view.draftId ?? undefined }} />
      {/if}
    {/if}
  {/if}
</div>
