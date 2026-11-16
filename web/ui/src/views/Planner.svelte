<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import {
    answerPlanner,
    cancelPlanner,
    createPlanner,
    createPlannerSnapshot,
    errMessage,
    getPlanner,
    getPlannerSnapshot,
    getProjectSettings,
    handoffPlanner,
    listRepos,
    plannerContext,
    plannerEvents,
    promptPlanner,
    type ProjectSettings,
    type RepoRow,
  } from '../lib/api'
  import { formatFactoryRoute, plannerRoute, type PlannerRoute } from '../lib/routes'
  import {
    PLANNER_DEFAULT_MODE,
    PLANNER_PERMISSIVE_NOTE,
    PLANNER_RESTRICTED_NOTE,
    artifactText,
    contextSummary,
    describePlannerEvent,
    occupancyNotice,
    occupancyWarning,
    permissionAnswer,
    plannerHandoffRequest,
    plannerModeLabel,
    plannerRequests,
    plannerTurnInFlight,
    questionAnswer,
    unknownTurn,
    type PlannerContextView,
    type PlannerEvent,
    type PlannerMode,
    type PlannerOccupancyNotice,
    type PlannerRequestCard,
    type PlannerSnapshotReply,
    type PlannerSnapshotView,
    type PlannerView,
  } from '../lib/planner'

  /**
   * The Studio planner: creation at `#planner/new`, one session at
   * `#planner/<id>`.
   *
   * Everything the person is told here comes from the bridge. The access mode
   * is shown as requested *and* confirmed, because the backend settles it; a
   * workspace that is not free is a question the person answers with one of
   * two explicit actions; the event stream is replayed by sequence, so a
   * reconnect re-reads and never resends a dispatched prompt; and a snapshot
   * is captured, read back and only then handed off, so what the draft
   * approves is the bytes a reviewer saw.
   */
  let { route, onNavigate }: { route: PlannerRoute; onNavigate: (hash: string) => void } = $props()

  const id = $derived(route.kind === 'session' ? (route.id ?? '') : '')

  /* ---------------------------------------------------------------- */
  /* Creation                                                          */
  /* ---------------------------------------------------------------- */

  let projectRoot = $state('')
  let repos = $state<RepoRow[]>([])
  let repoId = $state('')
  let mode = $state<PlannerMode>(PLANNER_DEFAULT_MODE)
  let workspaceChoice = $state<'separate' | 'existing'>('separate')
  let existingWorkspaceId = $state('')
  let creating = $state(false)
  let createError = $state('')
  let occupancy = $state<PlannerOccupancyNotice | null>(null)

  /**
   * The create body. A separate workspace is the default and names none: the
   * bridge mints one, so this can never adopt another planner's checkout by
   * accident.
   */
  function createBody(proceedShared: boolean): Parameters<typeof createPlanner>[0] {
    const workspaceId = workspaceChoice === 'existing' ? existingWorkspaceId.trim() : ''
    return {
      projectRoot: projectRoot.trim(),
      repoId,
      mode,
      ...(workspaceId ? { workspaceId } : {}),
      ...(proceedShared ? { proceedShared: true } : {}),
    }
  }

  async function start(proceedShared = false) {
    creating = true
    createError = ''
    occupancy = null
    try {
      const view = await createPlanner(createBody(proceedShared))
      onNavigate(plannerRoute({ kind: 'session', id: view.id }))
    } catch (e) {
      // A workspace question is not a failure: it is the answer that names the
      // owner, and the two actions below are how it is settled.
      const notice = occupancyNotice(e)
      if (notice) occupancy = notice
      else createError = errMessage(e)
    } finally {
      creating = false
    }
  }

  /** The first of the two explicit answers: stop asking about that workspace. */
  function useSeparateWorkspace() {
    workspaceChoice = 'separate'
    existingWorkspaceId = ''
    occupancy = null
    void start(false)
  }

  /* ---------------------------------------------------------------- */
  /* Session                                                           */
  /* ---------------------------------------------------------------- */

  let view = $state<PlannerView | null>(null)
  let events = $state<PlannerEvent[]>([])
  let lastSequence = $state(0)
  let connected = $state(true)
  let readError = $state('')
  let loadError = $state('')

  let settings = $state<ProjectSettings | null>(null)
  let settingsAsked = false

  let promptText = $state('')
  let promptExtra = $state('')
  let addContextOpen = $state(false)
  let promptBusy = $state(false)
  let promptError = $state('')
  /** The request the composer is currently dispatching, so an unchanged retry replays it. */
  let pendingPrompt = $state<{ requestId: string; signature: string } | null>(null)

  let answered = $state<string[]>([])
  let answering = $state('')
  let answers = $state<Record<string, string>>({})
  let answerError = $state('')

  let context = $state<PlannerContextView | null>(null)
  let contextExpanded = $state(false)
  let contextError = $state('')

  let freshBusy = $state(false)
  let freshError = $state('')

  const inFlight = $derived(plannerTurnInFlight(view))
  const unsaid = $derived(unknownTurn(view))
  const cards = $derived(plannerRequests(view, events).filter((c) => !answered.includes(c.requestId)))
  const lines = $derived(events.slice(-200).map((event) => ({ event, line: describePlannerEvent(event) })))
  const inheritedChecks = $derived.by(() => {
    const checks = settings?.factoryDefaults?.expectedChecks ?? []
    return checks.map((c) => (c.kind === 'commit_status' ? `${c.kind}:${c.name}` : `${c.kind}:${c.name}:${c.appId}`))
  })
  const needsAck = $derived(inheritedChecks.length === 0)

  /* ---------------------------------------------------------------- */
  /* Handoff                                                           */
  /* ---------------------------------------------------------------- */

  let planText = $state('')
  let specText = $state('')
  let paths = $state<string[]>([])
  let selected = $state<string[]>([])
  let newPath = $state('')
  let snapshot = $state<PlannerSnapshotReply | null>(null)
  let reviewed = $state<PlannerSnapshotView | null>(null)
  let captureBusy = $state(false)
  let captureError = $state('')
  let reviewBusy = $state(false)
  let draftTitle = $state('')
  let acknowledged = $state(false)
  let draftBusy = $state(false)
  let draftError = $state('')

  function addPath() {
    const path = newPath.trim()
    if (!path || paths.includes(path)) return
    paths = [...paths, path]
    newPath = ''
  }

  function togglePath(path: string) {
    selected = selected.includes(path) ? selected.filter((p) => p !== path) : [...selected, path]
  }

  /**
   * Freezes exactly what is typed and ticked. Capturing is its own action: it
   * writes nothing but the snapshot, and a failure keeps every word the person
   * typed.
   */
  async function capture() {
    if (!id) return
    captureBusy = true
    captureError = ''
    reviewed = null
    try {
      snapshot = await createPlannerSnapshot(id, { plan: planText, spec: specText, paths: selected })
    } catch (e) {
      captureError = errMessage(e)
    } finally {
      captureBusy = false
    }
  }

  /** Reads the frozen bytes back, so a review shows the hash it reports. */
  async function reviewSnapshot() {
    if (!id || !snapshot) return
    reviewBusy = true
    captureError = ''
    try {
      reviewed = await getPlannerSnapshot(id, snapshot.id)
    } catch (e) {
      captureError = errMessage(e)
    } finally {
      reviewBusy = false
    }
  }

  /** Creates the draft the reviewed snapshot approves, then opens the real item. */
  async function createDraft() {
    if (!id || !reviewed) return
    draftBusy = true
    draftError = ''
    try {
      const repo = repos.find((r) => r.id === (view?.repoId ?? ''))
      const item = await handoffPlanner(
        id,
        reviewed.id,
        plannerHandoffRequest({
          requestId: crypto.randomUUID(),
          title: draftTitle.trim(),
          plan: artifactText(reviewed, 'plan'),
          spec: artifactText(reviewed, 'spec'),
          repoId: view?.repoId ?? '',
          projectRoot: view?.projectRoot ?? '',
          projectWorkspace: settings?.workspace,
          factoryWorkspace: settings?.factoryDefaults?.workspace,
          factoryTargetBranch: settings?.factoryDefaults?.targetBranch,
          repoBranch: repo?.branch,
          noRequiredCiAcknowledged: acknowledged,
        }),
      )
      onNavigate(formatFactoryRoute({ itemId: item.id }))
    } catch (e) {
      draftError = errMessage(e)
    } finally {
      draftBusy = false
    }
  }

  /* ---------------------------------------------------------------- */
  /* Reads and the replay loop                                         */
  /* ---------------------------------------------------------------- */

  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const POLL_MS = 1500

  async function refreshView() {
    if (!id) return
    try {
      view = await getPlanner(id)
      loadError = ''
      void loadSettings()
    } catch (e) {
      connected = false
      readError = errMessage(e)
    }
  }

  /**
   * Reads every event after the highest sequence already shown. This is a
   * read, so it can be retried freely; the prompt path is the only one that
   * dispatches, and it never runs again on a reconnect.
   */
  async function readEvents() {
    if (!id) return
    try {
      const next = await plannerEvents(id, lastSequence)
      connected = true
      readError = ''
      if (next.length) {
        // A read can overlap the poll that scheduled it, so a sequence already
        // shown is not shown twice: the journal is replayed, not re-applied.
        const shown = new Set(events.map((e) => e.sequence))
        const fresh = next.filter((e) => !shown.has(e.sequence))
        if (fresh.length) events = [...events, ...fresh].slice(-400)
        lastSequence = next.reduce((high, e) => Math.max(high, e.sequence), lastSequence)
        // A new event can settle a request or end a turn, so the record is
        // re-read rather than inferred from the journal.
        await refreshView()
      }
    } catch (e) {
      connected = false
      readError = errMessage(e)
    }
  }

  async function retryReads() {
    await refreshView()
    await readEvents()
  }

  async function loadContext() {
    if (!id) return
    try {
      context = await plannerContext(id)
      contextError = ''
    } catch (e) {
      contextError = errMessage(e)
    }
  }

  /** Project defaults decide the draft's environment; a failure only means the draft is explicit. */
  async function loadSettings() {
    const root = view?.projectRoot ?? projectRoot
    if (!root || settings || settingsAsked) return
    settingsAsked = true
    try {
      settings = await getProjectSettings(root)
    } catch {
      settings = null
    }
  }

  function schedule() {
    if (stopped) return
    clearTimeout(timer)
    timer = setTimeout(() => void poll(), POLL_MS)
  }

  async function poll() {
    if (stopped) return
    await readEvents()
    schedule()
  }

  onMount(() => {
    void (async () => {
      // The entry point's project scopes creation; the field stays editable.
      if (route.kind === 'new' && !projectRoot) projectRoot = route.project ?? ''
      try {
        repos = await listRepos()
      } catch (e) {
        if (route.kind === 'new') createError = errMessage(e)
      }
      if (route.kind === 'new') {
        if (!repoId && repos.length === 1) repoId = repos[0].id
        return
      }
      await refreshView()
      await readEvents()
      await loadContext()
      schedule()
    })()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  })

  /* ---------------------------------------------------------------- */
  /* Prompts and answers                                               */
  /* ---------------------------------------------------------------- */

  /**
   * Dispatches one prompt. The composer is disabled while it is out, and its
   * text stays where it is if the send fails: a failed send is not a reason to
   * make the person type it again.
   */
  async function send() {
    if (!id || promptBusy || inFlight) return
    const text = promptText.trim()
    if (!text) return
    const extra = promptExtra.trim()
    const signature = `${text}\u0000${extra}`
    const prior = pendingPrompt
    const requestId = prior && prior.signature === signature ? prior.requestId : crypto.randomUUID()
    pendingPrompt = { requestId, signature }
    promptBusy = true
    promptError = ''
    try {
      await promptPlanner(id, { requestId, text, ...(extra ? { context: extra } : {}) })
      promptText = ''
      promptExtra = ''
      pendingPrompt = null
      await readEvents()
    } catch (e) {
      // The request ID is kept, so an unchanged retry is a replay, not a
      // second dispatch of the same words.
      promptError = errMessage(e)
      await readEvents()
    } finally {
      promptBusy = false
    }
  }

  async function cancelTurn() {
    if (!id) return
    try {
      await cancelPlanner(id)
      await readEvents()
    } catch (e) {
      promptError = errMessage(e)
    }
  }

  async function answer(card: PlannerRequestCard, response: unknown) {
    if (!id || answering) return
    answering = card.requestId
    answerError = ''
    try {
      await answerPlanner(id, card.requestId, response)
      answered = [...answered, card.requestId]
      await refreshView()
      await readEvents()
    } catch (e) {
      answerError = errMessage(e)
    } finally {
      answering = ''
    }
  }

  /**
   * The only offer after an unknown outcome: a fresh planner on the same
   * project. It is never an automatic resend, because the unknown turn may
   * already have changed the workspace.
   */
  async function freshSession() {
    if (!view) return
    freshBusy = true
    freshError = ''
    try {
      const created = await createPlanner({
        projectRoot: view.projectRoot,
        repoId: view.repoId,
        mode: view.requestedMode === 'default' ? 'default' : 'bypassPermissions',
      })
      onNavigate(plannerRoute({ kind: 'session', id: created.id }))
    } catch (e) {
      freshError = errMessage(e)
    } finally {
      freshBusy = false
    }
  }

  /** Back to the factory, where a planner's draft is approved. */
  const factoryHash = '#factory'
</script>

<div class="planner-shell mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-4 overflow-x-hidden break-words p-4 sm:p-6" data-testid="planner-shell">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h1 class="text-xl font-semibold">{route.kind === 'new' ? 'New Studio planner' : 'Studio planner'}</h1>
    <a class="text-sm text-accent underline" href={factoryHash} onclick={(e) => { e.preventDefault(); onNavigate(factoryHash) }}>Factory</a>
  </div>

  {#if route.kind === 'new'}
    <form class="grid gap-4" novalidate onsubmit={(e) => { e.preventDefault(); void start(false) }}>
      <Card class="grid gap-3">
        <label class="grid gap-1 text-sm">Project root
          <input class="rounded border border-border bg-bg p-2" bind:value={projectRoot} />
        </label>
        <label class="grid gap-1 text-sm">Repository
          <select class="rounded border border-border bg-bg p-2" bind:value={repoId}>
            <option value="">Choose repository</option>
            {#each repos as repo (repo.id)}<option value={repo.id}>{repo.id}</option>{/each}
          </select>
        </label>
      </Card>

      <fieldset class="grid gap-2 rounded-lg border border-border p-4">
        <legend class="px-1 text-sm font-semibold">Access mode</legend>
        <label class="flex items-start gap-2 text-sm">
          <input type="radio" name="planner-mode" value="bypassPermissions" checked={mode === 'bypassPermissions'} onchange={() => (mode = 'bypassPermissions')} />
          <span class="min-w-0"><span class="font-medium">Permissive</span> — <span class="text-muted">{PLANNER_PERMISSIVE_NOTE}</span></span>
        </label>
        <label class="flex items-start gap-2 text-sm">
          <input type="radio" name="planner-mode" value="default" checked={mode === 'default'} onchange={() => (mode = 'default')} />
          <span class="min-w-0"><span class="font-medium">Restricted</span> — <span class="text-muted">{PLANNER_RESTRICTED_NOTE}</span></span>
        </label>
      </fieldset>

      <fieldset class="grid gap-2 rounded-lg border border-border p-4">
        <legend class="px-1 text-sm font-semibold">Workspace</legend>
        <label class="flex items-center gap-2 text-sm">
          <input type="radio" name="planner-workspace" value="separate" checked={workspaceChoice === 'separate'} onchange={() => (workspaceChoice = 'separate')} /> Start in a separate workspace
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="radio" name="planner-workspace" value="existing" checked={workspaceChoice === 'existing'} onchange={() => (workspaceChoice = 'existing')} /> Use an existing workspace
        </label>
        {#if workspaceChoice === 'existing'}
          <label class="grid gap-1 text-sm">Existing workspace id
            <input class="min-w-0 break-all rounded border border-border bg-bg p-2 font-mono" bind:value={existingWorkspaceId} />
          </label>
        {/if}
      </fieldset>

      {#if occupancy}
        <Card class="grid gap-2 border-attention text-sm" role="alert" data-testid="planner-occupancy">
          <p class="break-words">{occupancyWarning(occupancy)}</p>
          <div class="flex flex-wrap gap-2">
            <Button type="button" variant="ghost" disabled={creating} onclick={useSeparateWorkspace}>Use a separate workspace</Button>
            <Button type="button" disabled={creating} onclick={() => void start(true)}>Proceed with shared use</Button>
          </div>
        </Card>
      {/if}
      {#if createError}
        <Card class="border-attention text-sm" role="alert" data-testid="planner-create-error">{createError}</Card>
      {/if}

      <div><Button type="submit" disabled={creating}>{creating ? 'Starting…' : 'Start planner'}</Button></div>
    </form>
  {:else}
    {#if loadError}
      <Card class="border-attention text-sm" role="alert">{loadError}</Card>
    {/if}

    {#if view}
      <Card class="grid gap-1 text-sm" aria-label="Planner mode and workspace">
        <p>Requested mode <span class="font-medium" data-testid="planner-mode">{plannerModeLabel(view.requestedMode)}</span></p>
        <p>Confirmed mode <span class="font-medium" data-testid="planner-confirmed-mode">{view.confirmedMode ? plannerModeLabel(view.confirmedMode) : 'Not confirmed yet'}</span></p>
        {#if !view.modeConfirmed}
          <p class="text-xs text-attention">The session has not confirmed this mode, so a prompt is refused until it does.</p>
        {/if}
        <p>Workspace <code class="break-all" data-testid="planner-workspace">{view.workspaceId}</code> <span class="text-muted">({view.occupancy})</span></p>
        <p>Project <code class="break-all">{view.projectRoot}</code></p>
      </Card>
    {/if}

    {#if !connected}
      <Card class="grid gap-2 border-attention text-sm" role="status" data-testid="planner-disconnected">
        <p>Disconnected — retrying reads. The replay resumes at the last sequence shown; a prompt already dispatched is not sent again.</p>
        <div><Button type="button" variant="ghost" onclick={() => void retryReads()}>Retry reads</Button></div>
      </Card>
    {/if}
    {#if readError && connected}
      <Card class="border-attention text-sm" role="alert">{readError}</Card>
    {/if}

    {#if unsaid}
      <Card class="grid gap-2 border-attention text-sm" role="alert" data-testid="planner-unknown">
        <p>This turn's outcome is unknown, so it was not resent.{#if unsaid.reason} <span class="text-muted">{unsaid.reason}</span>{/if}</p>
        <div><Button type="button" disabled={freshBusy} onclick={() => void freshSession()}>Start a fresh session with the project context</Button></div>
        {#if freshError}<p class="text-xs text-attention">{freshError}</p>{/if}
      </Card>
    {/if}

    <section class="grid gap-2" aria-label="Planner activity">
      <h2 class="text-sm font-semibold">Activity</h2>
      {#if lines.length === 0}
        <p class="text-sm text-muted">Nothing has happened yet.</p>
      {/if}
      <ol class="grid gap-2 text-sm">
        {#each lines as { event, line } (event.sequence)}
          <li class="grid gap-0.5" data-testid="planner-event">
            <span class="text-xs text-muted">#{event.sequence} · {line.label}</span>
            <span class="whitespace-pre-wrap break-words">{line.text}</span>
          </li>
        {/each}
      </ol>
    </section>

    <div class="grid gap-2">
      {#each cards as card (card.requestId)}
        <Card class="grid min-w-0 gap-2 text-sm" role="group" aria-label={card.title} data-testid="planner-request">
          <h3 class="font-semibold">{card.title}</h3>
          {#if card.detail}<p class="break-words">{card.detail}</p>{/if}
          {#if card.kind === 'permission'}
            <div class="flex flex-wrap gap-2">
              <Button type="button" disabled={answering === card.requestId} onclick={() => void answer(card, permissionAnswer(true))}>Allow once</Button>
              <Button type="button" variant="ghost" disabled={answering === card.requestId} onclick={() => void answer(card, permissionAnswer(false))}>Deny</Button>
            </div>
          {:else}
            <label class="grid gap-1">Your answer
              <input
                class="min-w-0 rounded border border-border bg-bg p-2"
                value={answers[card.requestId] ?? ''}
                oninput={(e) => (answers[card.requestId] = e.currentTarget.value)}
              />
            </label>
            <div>
              <Button type="button" disabled={answering === card.requestId} onclick={() => void answer(card, questionAnswer(card.detail, answers[card.requestId] ?? ''))}>Send answer</Button>
            </div>
          {/if}
        </Card>
      {/each}
      {#if answerError}<Card class="border-attention text-sm" role="alert">{answerError}</Card>{/if}
    </div>

    <form class="grid gap-2" onsubmit={(e) => { e.preventDefault(); void send() }}>
      <label class="grid gap-1 text-sm">Prompt
        <textarea
          class="min-h-24 min-w-0 rounded border border-border bg-bg p-2"
          bind:value={promptText}
          disabled={promptBusy || inFlight}
          aria-describedby="planner-prompt-note"
        ></textarea>
      </label>
      <p class="text-xs text-muted" id="planner-prompt-note">
        One prompt is active at a time. Its text stays here if the send fails.
      </p>
      <div class="flex flex-wrap gap-2">
        <Button type="submit" disabled={promptBusy || inFlight || !promptText.trim()}>{promptBusy ? 'Sending…' : 'Send prompt'}</Button>
        {#if inFlight}<Button type="button" variant="ghost" onclick={() => void cancelTurn()}>Cancel turn</Button>{/if}
        <Button type="button" variant="ghost" aria-expanded={addContextOpen} aria-controls="planner-context-add" onclick={() => (addContextOpen = !addContextOpen)}>
          {addContextOpen ? 'Hide added context' : 'Add context'}
        </Button>
      </div>
      {#if addContextOpen}
        <label class="grid gap-1 text-sm" id="planner-context-add">Additional context sent with the prompt
          <textarea class="min-h-20 min-w-0 rounded border border-border bg-bg p-2" bind:value={promptExtra}></textarea>
        </label>
      {/if}
    </form>
    {#if promptError}
      <Card class="border-attention text-sm" role="alert" data-testid="planner-prompt-error">{promptError}</Card>
    {/if}

    <section class="grid gap-2" aria-label="Planner context">
      <h2 class="text-sm font-semibold">Context</h2>
      <p class="break-words text-sm" data-testid="planner-context-summary">{contextSummary(context)}</p>
      {#if contextError}<p class="text-xs text-attention">{contextError}</p>{/if}
      <div>
        <Button type="button" variant="ghost" aria-expanded={contextExpanded} aria-controls="planner-context-detail" onclick={() => (contextExpanded = !contextExpanded)}>
          {contextExpanded ? 'Hide context' : 'Show context'}
        </Button>
      </div>
      {#if contextExpanded && context}
        <div class="grid min-w-0 gap-2 text-sm" id="planner-context-detail" data-testid="planner-context-detail">
          <div class="grid gap-1">
            <h3 class="font-semibold">Artifacts</h3>
            {#if context.artifacts.length === 0}<p class="text-muted">None recorded.</p>{/if}
            <ul class="grid gap-1">
              {#each context.artifacts as artifact, i (i)}
                <li class="break-words">
                  <span class="font-medium">{artifact.kind}</span> · {artifact.source}
                  {#if artifact.approved}<span> · captured</span>{:else}<span> · not captured</span>{/if}
                  {#if artifact.sha256}<span> · <code class="break-all">{artifact.sha256}</code></span>{/if}
                </li>
              {/each}
            </ul>
          </div>
          <div class="grid gap-1">
            <h3 class="font-semibold">Settled decisions</h3>
            {#if context.decisions.length === 0}<p class="text-muted">None recorded.</p>{/if}
            <ul class="grid gap-1">
              {#each context.decisions as decision, i (i)}
                <li class="break-words">{decision.method || 'request'} · {decision.state} · <code class="break-all">{decision.requestId}</code></li>
              {/each}
            </ul>
          </div>
          <div class="grid gap-1">
            <h3 class="font-semibold">Unresolved</h3>
            {#if context.unresolved.length === 0}<p class="text-muted">None recorded.</p>{/if}
            <ul class="grid gap-1">
              {#each context.unresolved as question, i (i)}
                <li class="break-words">{question.method || 'request'} · {question.state} · <code class="break-all">{question.requestId}</code></li>
              {/each}
            </ul>
          </div>
          <div class="grid gap-1">
            <h3 class="font-semibold">Absent sources</h3>
            {#if context.absent.length === 0}<p class="text-muted">None: the briefing holds every source it names.</p>{/if}
            <ul class="grid gap-1">
              {#each context.absent as absent, i (i)}<li class="break-words">{absent}</li>{/each}
            </ul>
          </div>
        </div>
      {/if}
    </section>

    <section class="grid gap-3" aria-label="Handoff">
      <h2 class="text-sm font-semibold">Handoff</h2>
      <label class="grid gap-1 text-sm">Plan
        <textarea class="min-h-32 min-w-0 rounded border border-border bg-bg p-2" bind:value={planText}></textarea>
      </label>
      <label class="grid gap-1 text-sm">Specification
        <textarea class="min-h-24 min-w-0 rounded border border-border bg-bg p-2" bind:value={specText}></textarea>
      </label>

      <fieldset class="grid min-w-0 gap-2 rounded-lg border border-border p-4">
        <legend class="px-1 text-sm font-semibold">Files to capture</legend>
        <p class="text-xs text-muted">Nothing is selected by default: a capture with no files freezes the plan and specification only.</p>
        <div class="flex flex-wrap items-end gap-2">
          <label class="grid min-w-0 flex-1 gap-1 text-sm">Add a file path
            <input class="min-w-0 rounded border border-border bg-bg p-2" bind:value={newPath} placeholder="internal/agent/runner.go" />
          </label>
          <Button type="button" variant="ghost" onclick={addPath}>Add file</Button>
        </div>
        {#each paths as path (path)}
          <label class="flex items-start gap-2 text-sm">
            <input type="checkbox" checked={selected.includes(path)} onchange={() => togglePath(path)} />
            <code class="min-w-0 break-all" data-testid="planner-file-option">{path}</code>
          </label>
        {/each}
      </fieldset>

      <div><Button type="button" disabled={captureBusy} onclick={() => void capture()}>{captureBusy ? 'Capturing…' : 'Capture snapshot'}</Button></div>

      {#if snapshot}
        <Card class="grid gap-2 text-sm" data-testid="planner-snapshot">
          <p>Base commit <code class="break-all" data-testid="planner-base-commit">{snapshot.baseCommit}</code></p>
          <p>Snapshot hash <code class="break-all" data-testid="planner-snapshot-hash">{snapshot.sha256}</code></p>
          <p class="text-muted">{snapshot.artifacts.length} document(s), {snapshot.files.length} file(s) frozen.</p>
          {#if snapshot.rejected.length}
            <div class="grid gap-1" data-testid="planner-rejected">
              <p class="text-attention">Rejected paths</p>
              <ul class="grid gap-1">
                {#each snapshot.rejected as rejection (rejection.path)}
                  <li class="break-words"><code class="break-all">{rejection.path}</code> — {rejection.reason}</li>
                {/each}
              </ul>
            </div>
          {/if}
          <div><Button type="button" variant="ghost" disabled={reviewBusy} onclick={() => void reviewSnapshot()}>{reviewBusy ? 'Reading…' : 'Review captured contents'}</Button></div>
        </Card>
      {/if}
      {#if captureError}
        <Card class="border-attention text-sm" role="alert" data-testid="planner-snapshot-error">{captureError}</Card>
      {/if}

      {#if reviewed}
        <Card class="grid min-w-0 gap-3 text-sm" data-testid="planner-review">
          <p>Base commit <code class="break-all">{reviewed.baseCommit}</code> · hash <code class="break-all" data-testid="planner-review-hash">{reviewed.sha256}</code></p>
          {#each reviewed.artifacts as artifact (artifact.kind)}
            <div class="grid min-w-0 gap-1">
              <h3 class="font-semibold">{artifact.kind}</h3>
              <pre class="min-w-0 whitespace-pre-wrap break-words" data-testid={`planner-artifact-${artifact.kind}`}>{artifact.content}</pre>
              <p class="text-xs text-muted">sha256 <code class="break-all">{artifact.sha256}</code></p>
            </div>
          {/each}
          {#each reviewed.files as file (file.path)}
            <div class="grid min-w-0 gap-1">
              <h3 class="font-semibold"><code class="break-all" data-testid="planner-file-path">{file.path}</code> — {file.operation}</h3>
              <pre class="min-w-0 whitespace-pre-wrap break-words" data-testid="planner-file-diff">{file.diff}</pre>
            </div>
          {/each}

          <label class="grid gap-1">Draft title
            <input class="min-w-0 rounded border border-border bg-bg p-2" bind:value={draftTitle} />
          </label>
          {#if inheritedChecks.length}
            <p>Inherited required checks: {inheritedChecks.join(', ')}</p>
          {:else}
            <label class="flex items-center gap-2"><input type="checkbox" bind:checked={acknowledged} /> This repository has no required CI</label>
          {/if}
          <div>
            <Button
              type="button"
              disabled={draftBusy || !draftTitle.trim() || (needsAck && !acknowledged)}
              onclick={() => void createDraft()}
            >{draftBusy ? 'Creating draft…' : 'Confirm and create factory draft'}</Button>
          </div>
        </Card>
      {/if}
      {#if draftError}
        <Card class="border-attention text-sm" role="alert" data-testid="planner-draft-error">{draftError}</Card>
      {/if}
    </section>
  {/if}
</div>
