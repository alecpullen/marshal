<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Badge from './ui/Badge.svelte'
  import {
    APIError,
    FactoryGrantConflictError,
    createClient,
    deleteClient,
    emptyFactoryGrant,
    errMessage,
    getFactoryGrant,
    listClients,
    listProjects,
    putFactoryGrant,
    type CreateClientResult,
    type FactoryGrantState,
    type MCPClient,
    type MCPFactoryProjectGrant,
    type ProjectStatus,
  } from './api'

  let clients = $state<MCPClient[]>([])
  let loading = $state(false)
  let error = $state<string | null>(null)
  let newName = $state('')
  let newAutonomous = $state(false)
  // An emptied number input binds to null (Svelte's to_number), which —
  // like 0 — means "no quota" when the payload is built in create().
  let newMaxConcurrent = $state<number | null>(null)
  let newMaxPerDay = $state<number | null>(null)
  let newAllowedRepos = $state('')
  let created = $state<CreateClientResult | null>(null)
  let copied = $state(false)

  // Factory authority: one grant per (client, project) pair, and nothing is
  // inherited. A pair that was never granted has no capability and no
  // approval authority, which the bridge reports as the zero grant rather
  // than an error.
  let projects = $state<ProjectStatus[]>([])
  let projectsError = $state<string | null>(null)
  let grantClientId = $state('')
  // The opaque `mcpProjectId`, never a path: it is the only project identifier
  // a grant names.
  let grantProjectId = $state('')
  let grant = $state<MCPFactoryProjectGrant | null>(null)
  let grantVersion = $state('')
  let grantLoading = $state(false)
  let grantSaving = $state(false)
  let grantError = $state<string | null>(null)
  let grantNotice = $state<string | null>(null)
  // The value that won a 409, shown beside the unsaved edit. It is never
  // adopted: only the explicit reload replaces what the user typed.
  let grantWinner = $state<FactoryGrantState | null>(null)

  async function refresh() {
    loading = true
    error = null
    try {
      clients = await listClients()
      // A client deleted elsewhere cannot keep editing its grant.
      if (grantClientId && !clients.some((c) => c.id === grantClientId)) grantClientId = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loading = false
    }
    await loadProjects()
  }

  /** Only projects with a minted opaque ID can be named by a grant. */
  async function loadProjects() {
    projectsError = null
    try {
      projects = (await listProjects()).filter((p) => !!p.mcpProjectId)
    } catch (e) {
      projects = []
      projectsError = `Projects could not be read: ${errMessage(e)}`
    }
    if (grantProjectId && !projects.some((p) => p.mcpProjectId === grantProjectId)) grantProjectId = ''
  }

  async function create() {
    error = null
    try {
      // 0 or empty means "no quota": the API treats an absent field as
      // unlimited, so the keys are dropped rather than sent as 0.
      const maxConcurrent = newMaxConcurrent && newMaxConcurrent > 0 ? newMaxConcurrent : undefined
      const maxPerDay = newMaxPerDay && newMaxPerDay > 0 ? newMaxPerDay : undefined
      const allowedRepos = newAllowedRepos.split(',').map((r) => r.trim()).filter((r) => r !== '')
      created = await createClient({
        name: newName,
        autonomous: newAutonomous,
        ...(maxConcurrent ? { maxConcurrent } : {}),
        ...(maxPerDay ? { maxPerDay } : {}),
        ...(allowedRepos.length > 0 ? { allowedRepos } : {}),
      })
      newName = ''
      newAutonomous = false
      newMaxConcurrent = null
      newMaxPerDay = null
      newAllowedRepos = ''
      await refresh()
    } catch (e) {
      error = errMessage(e)
    }
  }

  async function remove(id: string) {
    error = null
    try {
      await deleteClient(id)
      // The pair is gone, so its editor goes with it rather than lingering
      // against an ID the bridge no longer has.
      if (grantClientId === id) grantClientId = ''
      await refresh()
    } catch (e) {
      error = errMessage(e)
    }
  }

  /**
   * Read the chosen pair's grant. Nothing is editable unless this succeeded:
   * a write needs the version the bridge showed, and guessing one would
   * replace authority the user never saw.
   */
  async function loadGrant(clientId: string, projectId: string) {
    grantLoading = true
    grant = null
    grantVersion = ''
    grantError = null
    grantNotice = null
    grantWinner = null
    // Two reads can be in flight when the pair changes quickly. Only the one
    // that still matches the selected pair may fill the form: a late answer
    // for the previous pair would otherwise describe authority the selects
    // no longer show, and the next save would be refused against it.
    const current = () => clientId === grantClientId && projectId === grantProjectId
    try {
      const state = await getFactoryGrant(clientId, projectId)
      if (!current()) return
      if (!state?.version) throw new APIError(0, { error: 'The bridge did not return a grant version, so this authority cannot be edited safely.' })
      grant = state.grant ? { ...state.grant, projectId } : emptyFactoryGrant(projectId)
      grantVersion = state.version
    } catch (e) {
      // An unknown pair is one 404 answer that names neither ID: the route is
      // authenticated, but it still must not become an oracle.
      if (!current()) return
      grantError =
        e instanceof APIError && e.status === 404
          ? 'This client and project pair is unknown to the bridge, so there is no authority to edit here.'
          : errMessage(e)
    } finally {
      if (current()) grantLoading = false
    }
  }

  function resetGrant() {
    grant = null
    grantVersion = ''
    grantError = null
    grantNotice = null
    grantWinner = null
    grantLoading = false
  }

  /** Approving without an authority is refused by the bridge, so the two are never left in a state that cannot be saved. */
  function setAuthority(value: MCPFactoryProjectGrant['approvalAuthority']) {
    if (!grant) return
    grant.approvalAuthority = value
    if (value === 'none') grant.approveAndQueue = false
  }

  function setCapability(key: 'read' | 'createDraft' | 'editDraft' | 'answerQuestion' | 'approveAndQueue', on: boolean) {
    if (grant) grant[key] = on
  }

  async function saveGrant() {
    if (!grant || !grantClientId || !grantProjectId || grantSaving) return
    if (grant.approveAndQueue && grant.approvalAuthority === 'none') {
      grantError = 'Approving and queueing needs user-directed or standing authority; pick one before saving.'
      return
    }
    grantSaving = true
    grantError = null
    grantNotice = null
    try {
      const state = await putFactoryGrant(grantClientId, grantProjectId, { ...grant, projectId: grantProjectId }, grantVersion)
      grant = state.grant ? { ...state.grant, projectId: grantProjectId } : grant
      grantVersion = state.version || grantVersion
      grantWinner = null
      grantNotice = 'Factory authority saved. Changes apply to future actions only.'
    } catch (e) {
      if (e instanceof FactoryGrantConflictError) {
        // The user's edit stays on screen; the winning version is reported
        // beside it, and only an explicit reload replaces the draft. A 409
        // this build cannot read still has to be reported: silence would
        // look like the save had succeeded.
        grantWinner = e.state
        grantError = e.state ? null : 'Another change won the race, and the bridge did not report what it stored. Reload the saved authority before saving again.'
      } else {
        grantError = errMessage(e)
      }
    } finally {
      grantSaving = false
    }
  }

  // Every pair change is a fresh read; a save never re-reads, so an unsaved
  // edit is never overwritten behind the user's back.
  $effect(() => {
    const clientId = grantClientId
    const projectId = grantProjectId
    if (clientId && projectId) void loadGrant(clientId, projectId)
    else resetGrant()
  })

  async function copyToken() {
    if (!created?.token) return
    try {
      await navigator.clipboard.writeText(created.token)
      copied = true
      setTimeout(() => (copied = false), 2000)
    } catch {
      // clipboard may be unavailable
    }
  }

  // Load on mount
  $effect(() => {
    refresh()
  })
</script>

<Card>
  <div class="mb-4 flex items-center justify-between">
    <h2 class="text-sm font-semibold">MCP Clients</h2>
    <Button variant="ghost" onclick={refresh} disabled={loading}>Refresh</Button>
  </div>

  {#if error}
    <div class="mb-3 rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger">{error}</div>
  {/if}

  {#if created}
    <div class="mb-4 rounded-md border border-attention bg-attention/10 p-3">
      <div class="text-sm font-medium text-attention">Token for {created.name} — shown once</div>
      <div class="mt-2 flex items-center gap-2">
        <code class="flex-1 break-all rounded bg-bg p-2 text-xs">{created.token}</code>
        <Button variant="ghost" onclick={copyToken}>{copied ? 'Copied' : 'Copy'}</Button>
      </div>
      <p class="mt-2 text-xs text-muted">
        This token will not be shown again. Store it now — you will need it to configure your MCP client.
      </p>
      <Button variant="ghost" class="mt-2" onclick={() => (created = null)}>Dismiss</Button>
    </div>
  {/if}

  <div class="mb-4 flex flex-col gap-2 border-t border-border pt-3">
    <input
      class="rounded-md border border-border bg-bg px-3 py-2 text-sm"
      placeholder="Client name (e.g. claude-code)"
      bind:value={newName}
    />
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={newAutonomous} />
      Autonomous (skip confirmation)
    </label>
    <div class="flex gap-2">
      <input
        type="number"
        min="0"
        class="w-full rounded-md border border-border bg-bg px-3 py-2 text-sm"
        placeholder="Max concurrent"
        bind:value={newMaxConcurrent}
      />
      <input
        type="number"
        min="0"
        class="w-full rounded-md border border-border bg-bg px-3 py-2 text-sm"
        placeholder="Max per day"
        bind:value={newMaxPerDay}
      />
    </div>
    <p class="text-xs text-muted">Quotas are optional — 0 or empty means unlimited.</p>
    <input
      type="text"
      class="rounded-md border border-border bg-bg px-3 py-2 text-sm"
      placeholder="Allowed repos (owner/repo, comma-separated)"
      bind:value={newAllowedRepos}
    />
    <Button onclick={create} disabled={!newName.trim()}>Create client</Button>
  </div>

  {#if clients.length === 0 && !loading}
    <p class="text-sm text-muted">No MCP clients registered.</p>
  {/if}

  <ul class="flex flex-col gap-2">
    {#each clients as c (c.id)}
      <li class="flex items-center justify-between gap-3 border-t border-border pt-2 first:border-0 first:pt-0">
        <div class="min-w-0">
          <div class="truncate text-sm font-medium">{c.name}</div>
          <div class="mt-1 flex flex-wrap gap-2 text-xs text-muted">
            {#if c.autonomous}
              <Badge tone="running">autonomous</Badge>
            {/if}
            {#if c.maxConcurrent > 0}
              <Badge tone="muted">max {c.maxConcurrent} concurrent</Badge>
            {/if}
            {#if c.maxPerDay > 0}
              <Badge tone="muted">max {c.maxPerDay}/day</Badge>
            {/if}
            {#if c.allowedRepos && c.allowedRepos.length > 0}
              <Badge tone="muted">{c.allowedRepos.length} repos</Badge>
            {/if}
          </div>
        </div>
        <Button variant="ghost" onclick={() => remove(c.id)}>Delete</Button>
      </li>
    {/each}
  </ul>

  <div class="mt-4 flex flex-col gap-3 border-t border-border pt-3" data-testid="factory-authority">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-sm font-semibold">Factory authority</h3>
      <Badge tone="muted">per client and project</Badge>
    </div>
    <p class="text-xs text-muted">
      A client has no factory authority until you grant it here, and each permission is independent:
      reading and drafting never imply approval. <strong>User-directed</strong> approval or answer is the
      client's own claim that a person asked for it — it is recorded as such and never treated as verified
      human approval. <strong>Standing</strong> authority is optional: it lets the client approve or queue
      without being asked each time, so grant it only to a client you trust to decide unsupervised. Changing
      or revoking authority affects future actions only; work already accepted, approved or queued keeps the
      provenance it was recorded with.
    </p>
    <div class="flex flex-col gap-2 sm:flex-row">
      <label class="flex min-w-0 flex-1 flex-col gap-1 text-xs">Client
        <select class="w-full min-w-0 rounded-md border border-border bg-bg px-3 py-2 text-sm" bind:value={grantClientId}>
          <option value="">Choose a client</option>
          {#each clients as c (c.id)}<option value={c.id}>{c.name || c.id}</option>{/each}
        </select>
      </label>
      <label class="flex min-w-0 flex-1 flex-col gap-1 text-xs">Project
        <select class="w-full min-w-0 rounded-md border border-border bg-bg px-3 py-2 text-sm" bind:value={grantProjectId}>
          <option value="">Choose a project</option>
          {#each projects as p (p.mcpProjectId)}<option value={p.mcpProjectId}>{p.root}</option>{/each}
        </select>
      </label>
    </div>
    {#if projectsError}
      <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger" role="alert">{projectsError}</div>
    {:else if projects.length === 0}
      <p class="text-xs text-muted">No project on this bridge has a factory project ID yet, so there is nothing to grant.</p>
    {/if}

    {#if grantLoading}
      <p class="text-sm text-muted" role="status">Reading factory authority…</p>
    {:else if grantError}
      <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger" role="alert">{grantError}</div>
    {/if}

    {#if grantWinner}
      <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm" data-testid="grant-conflict">
        <p class="font-medium text-attention">These permissions changed elsewhere. Your edits are kept.</p>
        <p class="mt-1 text-xs text-muted">
          Version {grantWinner.version} is saved now, with approval authority
          {grantWinner.grant?.approvalAuthority ?? 'none'}. Reloading discards what you typed and shows the
          saved values; comparing them first changes nothing on the bridge.
        </p>
        <Button variant="ghost" class="mt-2" onclick={() => void loadGrant(grantClientId, grantProjectId)}>Reload saved authority</Button>
      </div>
    {/if}

    {#if grant}
      <div class="flex flex-col gap-2">
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={grant.read} disabled={grantSaving} onchange={(e) => setCapability('read', e.currentTarget.checked)} />
          Read work items
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={grant.createDraft} disabled={grantSaving} onchange={(e) => setCapability('createDraft', e.currentTarget.checked)} />
          Create drafts
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={grant.editDraft} disabled={grantSaving} onchange={(e) => setCapability('editDraft', e.currentTarget.checked)} />
          Edit drafts
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={grant.answerQuestion} disabled={grantSaving} onchange={(e) => setCapability('answerQuestion', e.currentTarget.checked)} />
          Answer questions
        </label>
        <label class="flex flex-col gap-1 text-xs">Approval authority
          <select
            class="w-full min-w-0 rounded-md border border-border bg-bg px-3 py-2 text-sm"
            value={grant.approvalAuthority}
            disabled={grantSaving}
            onchange={(e) => setAuthority(e.currentTarget.value as MCPFactoryProjectGrant['approvalAuthority'])}
          >
            <option value="none">None</option>
            <option value="user_directed">User-directed (client assertion)</option>
            <option value="standing">Standing (approve without asking)</option>
          </select>
        </label>
        <label class="flex items-center gap-2 text-sm" class:opacity-60={grant.approvalAuthority === 'none'}>
          <input
            type="checkbox"
            checked={grant.approveAndQueue}
            disabled={grantSaving || grant.approvalAuthority === 'none'}
            onchange={(e) => setCapability('approveAndQueue', e.currentTarget.checked)}
          />
          Approve and queue
        </label>
        {#if grant.approvalAuthority === 'none'}
          <p class="text-xs text-muted">Approving and queueing needs user-directed or standing authority.</p>
        {/if}
        <div class="flex flex-wrap items-center gap-3">
          <Button onclick={saveGrant} disabled={grantSaving || grantLoading}>Save authority</Button>
          {#if grantVersion}<span class="text-xs text-muted">Version {grantVersion}</span>{/if}
        </div>
        {#if grantNotice}<p class="text-sm text-muted" role="status">{grantNotice}</p>{/if}
      </div>
    {/if}
  </div>
</Card>
