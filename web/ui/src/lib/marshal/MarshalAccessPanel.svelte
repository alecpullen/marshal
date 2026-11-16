<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import {
    MarshalAccessConflictError,
    errMessage,
    getMarshalAccess,
    putMarshalAccessDefault,
    putMarshalAccessOverride,
    putMarshalSelection,
    type MarshalAccess,
    type MarshalAccessView,
  } from '../api'

  /*
    Connection and access for one built-in Studio session, or for a project's
    defaults. The same controls serve an ordinary Fleet session and a factory
    worker: the only difference is the identities the caller passes, so a worker
    never gets a second, drifted editor.

    The connection state and the policy are shown together and decided apart.
    `state` says what the server knows about the enrollment — connected,
    ambiguous, unavailable, unsupported or unauthorized — and it is rendered,
    never thrown: an offline or ambiguous session keeps every policy row usable,
    because the policy is stored whether or not a connection is up.
  */

  type Level = 'project' | 'session'
  type CapKey = 'read' | 'createDraft' | 'editDraft' | 'answerQuestion' | 'approveAndQueue'
  type Tri = 'allow' | 'deny' | 'inherit'

  let {
    projectId = '',
    sessionId = '',
    workerId = '',
    levels = ['project'],
    heading = 'Studio access',
    testId = 'marshal-access',
  }: {
    /** The project whose defaults (and whose session's override) this surface shows. */
    projectId?: string
    /** The session whose override and connection this surface shows. */
    sessionId?: string
    /** The worker among that session's workers, when this surface is a worker's. */
    workerId?: string
    /** The levels this surface edits: a project screen edits defaults, a session screen its override. */
    levels?: Level[]
    heading?: string
    testId?: string
  } = $props()

  const CAPS: { key: CapKey; label: string; hint: string }[] = [
    { key: 'read', label: 'Read', hint: "read the project's factory and its items" },
    { key: 'createDraft', label: 'Create drafts', hint: 'propose a draft for the project' },
    { key: 'editDraft', label: 'Edit drafts', hint: 'change a draft that already exists' },
    { key: 'answerQuestion', label: 'Answer questions', hint: 'answer a question a draft waits on' },
    { key: 'approveAndQueue', label: 'Approve and queue', hint: 'approve a draft and put it on the queue' },
  ]
  const AUTHORITIES = ['inherit', 'none', 'user_directed', 'standing']

  let view = $state<MarshalAccessView | null>(null)
  let error = $state('')
  let notice = $state('')
  let busy = $state(false)
  let projectDraft = $state<MarshalAccess>({})
  let sessionDraft = $state<MarshalAccess>({})
  let projectDirty = $state(false)
  let sessionDirty = $state(false)
  let selection = $state('')
  let selectionDirty = $state(false)

  const editsProject = $derived(levels.includes('project'))
  const editsSession = $derived(levels.includes('session') && sessionId !== '')
  const resolvedProject = $derived(view?.projectId ?? projectId)
  const canEdit = $derived(!!view && view.editable !== false && resolvedProject !== '')
  const ambiguous = $derived((view?.candidates?.length ?? 0) > 1)

  /** tri reads one capability of one level: unset inherits, false is a denial. */
  function tri(access: MarshalAccess | undefined, key: CapKey): Tri {
    const value = access?.[key]
    if (value === undefined) return 'inherit'
    return value ? 'allow' : 'deny'
  }

  /** withTri writes one capability, deleting it for the inherit that restores inheritance. */
  function withTri(access: MarshalAccess, key: CapKey, value: string): MarshalAccess {
    const next: MarshalAccess = { ...access }
    if (value === 'inherit') delete next[key]
    else next[key] = value === 'allow'
    return next
  }

  function authority(access: MarshalAccess | undefined): string {
    return access?.approvalAuthority ?? 'inherit'
  }

  function withAuthority(access: MarshalAccess, value: string): MarshalAccess {
    const next: MarshalAccess = { ...access }
    if (value === 'inherit') delete next.approvalAuthority
    else next.approvalAuthority = value
    return next
  }

  /** sourceOf names the level a field's effective value comes from, which the layers decide. */
  function sourceOf(key: CapKey): string {
    if (!view) return ''
    const sessionValue = view.override?.access?.[key]
    if (sessionValue !== undefined) return 'session'
    const projectValue = view.default.access[key]
    if (projectValue !== undefined) return 'project'
    return 'built-in default'
  }

  /** grantSummary renders a level's granted capabilities, for the read-only digest. */
  function grantSummary(access: MarshalAccess | undefined): string {
    const granted = CAPS.filter((cap) => access?.[cap.key] === true).map((cap) => cap.label.toLowerCase())
    return granted.length ? granted.join(', ') : 'nothing granted'
  }

  function apply(next: MarshalAccessView) {
    view = next
    projectDraft = { ...next.default.access }
    sessionDraft = { ...(next.override?.access ?? {}) }
    selection = next.selectedProjectId ?? ''
    projectDirty = false
    sessionDirty = false
    selectionDirty = false
  }

  async function load() {
    busy = true
    error = ''
    try {
      apply(await getMarshalAccess({ projectId, sessionId, workerId }))
    } catch (e) {
      error = errMessage(e)
      view = null
    } finally {
      busy = false
    }
  }

  /**
   * save reports what happened, and keeps the draft when the write lost so the
   * edit is not discarded. It answers whether the write landed, so a caller
   * that clears a draft (a reset) does so only once it has.
   */
  async function save(work: () => Promise<MarshalAccessView>, saved: string): Promise<boolean> {
    busy = true
    error = ''
    notice = ''
    try {
      apply(await work())
      notice = saved
      return true
    } catch (e) {
      if (e instanceof MarshalAccessConflictError) {
        // The write named a version that moved. The answer carries the value
        // that won, so it is shown while the draft stays as the user left it.
        if (e.view) {
          view = e.view
          if (e.view.default?.version) projectDraft = { ...projectDraft }
          selection = e.view.selectedProjectId ?? selection
        }
        notice = 'changed elsewhere; the stored value is shown, your edit is still here'
      } else {
        error = errMessage(e)
      }
      return false
    } finally {
      busy = false
    }
  }

  function saveProject() {
    if (!view) return
    const layer = view.default.version
    void save(() => putMarshalAccessDefault(resolvedProject, projectDraft, layer), 'Project defaults saved')
  }

  async function resetProject() {
    if (!view) return
    const layer = view.default.version
    // The draft is cleared only once the write has landed. Clearing it first
    // would leave the selects reading "inherit" after a refused write while the
    // stored layer still holds values, and Save would then be disabled.
    const saved = await save(() => putMarshalAccessDefault(resolvedProject, {}, layer), 'Project defaults reset to inherit')
    if (saved) projectDraft = {}
  }

  function saveSession() {
    if (!view) return
    const version = view.overrideVersion ?? view.override?.version ?? ''
    void save(() => putMarshalAccessOverride(resolvedProject, sessionId, sessionDraft, version, workerId), 'This session’s access saved')
  }

  async function revokeSession() {
    if (!view) return
    const version = view.overrideVersion ?? view.override?.version ?? ''
    const saved = await save(() => putMarshalAccessOverride(resolvedProject, sessionId, {}, version, workerId), 'This session inherits the project defaults again')
    if (saved) sessionDraft = {}
  }

  function saveSelection() {
    if (!view) return
    const version = view.selectionVersion ?? ''
    void save(() => putMarshalSelection(sessionId, selection, version), selection ? 'Instance selected' : 'Instance selection cleared')
  }

  onMount(load)
</script>

<Card class="flex min-w-0 flex-col gap-3" data-testid={testId}>
  <div class="flex flex-wrap items-center gap-2">
    <h2 class="text-sm font-semibold">{heading}</h2>
    {#if view}
      <Tag tone={view.state === 'connected' ? 'ok' : view.state === 'ambiguous' ? 'warn' : 'neutral'}>{view.state}</Tag>
      {#if view.effective.read !== true}<Tag tone="warn">read denied</Tag>{/if}
      {#if view.effective.approvalAuthority === 'standing'}<Tag tone="ok">standing approval</Tag>{/if}
    {/if}
    {#if busy}<span class="text-xs text-muted">working…</span>{/if}
  </div>

  {#if error}<p class="text-xs text-danger" role="alert">{error}</p>{/if}
  {#if notice}<p class="text-xs text-muted" role="status">{notice}</p>{/if}
  {#if view?.reason}<p class="text-xs text-muted" data-testid={`${testId}-reason`}>{view.reason}</p>{/if}

  {#if view}
    <dl class="grid grid-cols-1 gap-x-4 gap-y-1 text-xs sm:grid-cols-2">
      <div class="flex min-w-0 justify-between gap-2"><dt class="text-muted">Instance</dt><dd class="truncate" data-testid={`${testId}-instance`}>{view.instanceId ?? '—'}</dd></div>
      <div class="flex min-w-0 justify-between gap-2"><dt class="text-muted">Project</dt><dd class="truncate" data-testid={`${testId}-project`}>{view.projectId ?? '—'}</dd></div>
      <div class="flex min-w-0 justify-between gap-2"><dt class="text-muted">Session</dt><dd class="truncate" data-testid={`${testId}-session`}>{view.sessionId ?? '—'}</dd></div>
      {#if view.workerId}<div class="flex min-w-0 justify-between gap-2"><dt class="text-muted">Worker</dt><dd class="truncate" data-testid={`${testId}-worker`}>{view.workerId}</dd></div>{/if}
    </dl>

    <section class="flex flex-col gap-1" aria-label="Effective access">
      <h3 class="text-xs font-semibold">Effective access</h3>
      <ul class="flex flex-col gap-1 text-xs" data-testid={`${testId}-effective`}>
        {#each CAPS as cap (cap.key)}
          <li class="flex min-w-0 items-center justify-between gap-2">
            <span class={view.effective[cap.key] === true ? '' : 'text-muted'}>{cap.label}</span>
            <span class="flex items-center gap-2">
              <span class="text-muted">{sourceOf(cap.key)}</span>
              <Tag tone={view.effective[cap.key] === true ? 'ok' : 'neutral'}>{view.effective[cap.key] === true ? 'allow' : 'deny'}</Tag>
            </span>
          </li>
        {/each}
        <li class="flex min-w-0 items-center justify-between gap-2">
          <span>Approval authority</span>
          <span class="flex items-center gap-2">
            <span class="text-muted">{view.override?.access?.approvalAuthority !== undefined ? 'session' : view.default.access.approvalAuthority !== undefined ? 'project' : 'built-in default'}</span>
            <Tag tone={view.effective.approvalAuthority === 'standing' ? 'ok' : 'neutral'}>{view.effective.approvalAuthority ?? 'none'}</Tag>
          </span>
        </li>
      </ul>
    </section>

    {#if ambiguous && sessionId}
      <section class="flex flex-col gap-2" aria-label="Ambiguous instance">
        <h3 class="text-xs font-semibold">This session is tracked by more than one project</h3>
        <p class="text-xs text-muted" data-testid={`${testId}-candidates`}>Candidates: {(view.candidates ?? []).join(', ')}</p>
        <label class="flex min-w-0 flex-col gap-1 text-xs">Project this session acts on
          <select
            aria-label="Studio instance"
            class="rounded-md border border-border bg-surface px-2 py-1"
            value={selection}
            onchange={(e) => { selection = (e.currentTarget as HTMLSelectElement).value; selectionDirty = true }}
          >
            <option value="">Not selected</option>
            {#each view.candidates ?? [] as candidate (candidate)}<option value={candidate}>{candidate}</option>{/each}
          </select>
        </label>
        <div><Button variant="ghost" disabled={busy || !selectionDirty} onclick={saveSelection} data-testid={`${testId}-select-save`}>Save instance</Button></div>
      </section>
    {/if}

    {#if editsProject && canEdit}
      <section class="flex flex-col gap-2" aria-label="Project defaults">
        <h3 class="text-xs font-semibold">Project defaults</h3>
        <p class="text-xs text-muted">Every session of this project inherits these until it overrides one. Inherit follows the built-in read-only default.</p>
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          {#each CAPS as cap (cap.key)}
            <label class="flex min-w-0 flex-col gap-1 text-xs">{cap.label}
              <select
                aria-label={`Project ${cap.label}`}
                class="rounded-md border border-border bg-surface px-2 py-1"
                value={tri(projectDraft, cap.key)}
                onchange={(e) => { projectDraft = withTri(projectDraft, cap.key, (e.currentTarget as HTMLSelectElement).value); projectDirty = true }}
              >
                {#each ['inherit', 'allow', 'deny'] as option (option)}<option value={option}>{option}</option>{/each}
              </select>
              <span class="text-muted">{cap.hint}</span>
            </label>
          {/each}
          <label class="flex min-w-0 flex-col gap-1 text-xs">Approval authority
            <select
              aria-label="Project approval authority"
              class="rounded-md border border-border bg-surface px-2 py-1"
              value={authority(projectDraft)}
              onchange={(e) => { projectDraft = withAuthority(projectDraft, (e.currentTarget as HTMLSelectElement).value); projectDirty = true }}
            >
              {#each AUTHORITIES as option (option)}<option value={option}>{option}</option>{/each}
            </select>
            <span class="text-muted">standing lets this project approve without asking each time; user_directed asks a person per approval</span>
          </label>
        </div>
        <div class="flex flex-wrap gap-2">
          <Button disabled={busy || !projectDirty} onclick={saveProject} data-testid={`${testId}-project-save`}>Save project defaults</Button>
          <Button variant="ghost" disabled={busy} onclick={resetProject} data-testid={`${testId}-project-reset`}>Reset to inherit</Button>
        </div>
      </section>
    {/if}

    {#if editsSession && canEdit}
      <section class="flex flex-col gap-2" aria-label="This session's access">
        <h3 class="text-xs font-semibold">{workerId ? 'This worker' : 'This session'}</h3>
        <p class="text-xs text-muted">Anything left to inherit follows the project defaults{view.override ? '; this level currently grants ' + grantSummary(view.override.access) : ''}.</p>
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          {#each CAPS as cap (cap.key)}
            <label class="flex min-w-0 flex-col gap-1 text-xs">{cap.label}
              <select
                aria-label={`Session ${cap.label}`}
                class="rounded-md border border-border bg-surface px-2 py-1"
                value={tri(sessionDraft, cap.key)}
                onchange={(e) => { sessionDraft = withTri(sessionDraft, cap.key, (e.currentTarget as HTMLSelectElement).value); sessionDirty = true }}
              >
                {#each ['inherit', 'allow', 'deny'] as option (option)}<option value={option}>{option}</option>{/each}
              </select>
              <span class="text-muted">{cap.hint}</span>
            </label>
          {/each}
          <label class="flex min-w-0 flex-col gap-1 text-xs">Approval authority
            <select
              aria-label="Session approval authority"
              class="rounded-md border border-border bg-surface px-2 py-1"
              value={authority(sessionDraft)}
              onchange={(e) => { sessionDraft = withAuthority(sessionDraft, (e.currentTarget as HTMLSelectElement).value); sessionDirty = true }}
            >
              {#each AUTHORITIES as option (option)}<option value={option}>{option}</option>{/each}
            </select>
          </label>
        </div>
        <div class="flex flex-wrap gap-2">
          <Button disabled={busy || !sessionDirty} onclick={saveSession} data-testid={`${testId}-session-save`}>Save for this session</Button>
          <Button variant="ghost" disabled={busy} onclick={revokeSession} data-testid={`${testId}-session-revoke`}>Revoke override</Button>
        </div>
      </section>
    {/if}

    {#if !canEdit}
      <p class="text-xs text-muted" data-testid={`${testId}-read-only`}>
        This surface cannot change the policy: {view.state === 'unauthorized' ? 'the server does not track this session for that project.' : 'no project resolved for this session.'}
      </p>
    {/if}
  {:else if !busy}
    <div class="flex flex-wrap gap-2">
      <Button variant="ghost" onclick={load} data-testid={`${testId}-reload`}>Re-read connection</Button>
    </div>
  {/if}
</Card>
