<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import DraftApproval from '../lib/factory/DraftApproval.svelte'
  import WorkItemHeader from '../lib/factory/WorkItemHeader.svelte'
  import ActivityTimeline from '../lib/factory/ActivityTimeline.svelte'
  import DecisionPanel from '../lib/factory/DecisionPanel.svelte'
  import RecoveryPanel from '../lib/factory/RecoveryPanel.svelte'
  import DocumentsView from '../lib/factory/DocumentsView.svelte'
  import PullRequestsView from '../lib/factory/PullRequestsView.svelte'
  import ReviewView from '../lib/factory/ReviewView.svelte'
  import Button from '../lib/ui/Button.svelte'
  import AgentConversation from '../lib/session/AgentConversation.svelte'
  import MarshalAccessPanel from '../lib/marshal/MarshalAccessPanel.svelte'
  import FactoryRevision from '../lib/FactoryRevision.svelte'
  import { APIError, commandFactoryItem, errMessage, factoryRevisionCommand, getProjectSettings, listClients } from '../lib/api'
  import { effectiveAutoAssess, provenanceLine } from '../lib/factory'
  import { formatChatRoute, formatFactoryRoute, parseFactoryRoute } from '../lib/routes'
  import type { DockTabParam } from '../lib/routes'
  import { defaultFactoryItemView } from '../lib/studio/model'
  import type { AgentRow } from '../lib/fleet'
  import type { FactoryCommand, FactoryCreate, FactoryItem, FactoryItemView, FactoryRevisionCommand, FactoryRoute } from '../lib/factory'

  import type { FactoryState } from '../lib/factoryStore'

  /** C5: what a decision or recovery panel hands back with a command kind. */
  interface FactoryCommandInput {
    answer?: string
    reason?: string
    additionalRounds?: number
    replacement?: FactoryCreate
    implementerRouting?: Record<string, unknown>
    reviewerRouting?: Record<string, unknown>
    confirmPreviousWorkerStopped?: boolean
    /** A revision lifecycle command's own half, sent beside the envelope. */
    revision?: FactoryRevisionCommand
  }
  /**
   * What each command says when it lands. The revision kinds say what did *not*
   * happen as well, because saving a proposal and approving one are different
   * acts and a receipt is not a delivery.
   */
  const NOTICES: Partial<Record<FactoryCommand['kind'], string>> = {
    stop: 'Stop requested. Waiting for worker confirmation.',
    revision_propose: 'Proposal saved. It is not approved, and nothing has been started or changed.',
    revision_supersede: 'Proposal superseded. Nothing was approved.',
    revision_reject: 'Proposal rejected. The approved revision is unchanged.',
    revision_assess_request: 'Assessment requested. The result appears here when it arrives.',
    revision_authorize: 'Revision approved. Delivery is reported separately.',
    revision_delivery_choice: 'Delivery choice recorded. The receipt below says how far it got.',
    revision_follow_up_confirm: 'Follow-up requested. This item keeps its approved revision.',
  }
  /** The decision kinds the recovery panel presents; everything else is a question. */
  const RECOVERY_KINDS = ['exhausted', 'no_progress', 'worker_failed', 'invalid_result', 'ci_unavailable', 'ci_wait', 'conflict', 'recovery']
  /*
    One item, read through the shell's store instead of a poller of its own:
    the store answers the first read, the shell's list poll keeps it current,
    and a command's own result is published back so every view agrees. The
    callbacks are the shell's, so the poller and the error handling stay in
    one place.
  */
  let {
    itemId,
    route,
    factory,
    agents = [],
    getItem,
    upsert,
    onRefresh,
    onNavigate,
  }: {
    itemId: string
    route: FactoryRoute
    factory: FactoryState
    /** The fleet's live rows, so a drilled-in worker keeps its own gate and change count. */
    agents?: AgentRow[]
    getItem: (id: string) => Promise<FactoryItem>
    upsert: (item: FactoryItem) => void
    onRefresh: () => Promise<void>
    onNavigate: (hash: string) => void
  } = $props()
  let error = $state(''), notice = $state(''), busy = $state(false)
  /** The first read, until the shell's store carries this item. */
  let fetched = $state<FactoryItem | null>(null)
  /*
    Once the store carries the item it is the source of truth: the shell's list
    poll, another view's command and this view's own upsert all land there.
    Deriving rather than copying keeps one value, so a store update redraws the
    item in place and nothing here re-reads the preview.
  */
  const stored = $derived(factory.items.find((candidate) => candidate.id === itemId) ?? null)
  const item = $derived(stored ?? fetched)
  /*
    The selected view is the reader's, not the item's. It is seeded once from
    the state the item was in when this page opened — or from an explicit
    `view` the route named — and after that only an explicit route change
    moves it: a poll that reports a new state must never take the tab away
    from the person reading it.
  */
  let view = $state<FactoryItemView | null>(null)
  /** The route request last applied, so a repeated one cannot move the tab. */
  let appliedView: FactoryItemView | undefined
  $effect(() => {
    const requested = route.view
    if (!item) return
    if (view === null) {
      view = defaultFactoryItemView(item, requested)
      appliedView = requested
      return
    }
    if (requested === appliedView) return
    appliedView = requested
    view = defaultFactoryItemView(item, requested)
  })
  /*
    Polling never moves the tab, so an approval that was open when the draft was
    approved stays selected and says so rather than silently becoming another
    view. The tab strip keeps it visible for exactly that reason.
  */
  const selectedView = $derived<FactoryItemView>(view ?? 'activity')
  /*
    The side-by-side layout is a different structure, not a narrower version of
    the same one, so it is decided from one media query and the result is
    rendered as markup. At 1200px and above activity sits beside the selected
    worker's conversation; below it activity is the page and a conversation is
    a focused drill-in the route asks for. The listener is the shell's own:
    it is removed when this item unmounts, so a resize cannot write to a
    component that is gone.
  */
  const DESKTOP_QUERY = '(min-width: 1200px)'
  let desktop = $state(window.matchMedia(DESKTOP_QUERY).matches)
  onMount(() => {
    const width = window.matchMedia(DESKTOP_QUERY)
    desktop = width.matches
    const onWidth = (e: MediaQueryListEvent) => (desktop = e.matches)
    width.addEventListener('change', onWidth)
    return () => width.removeEventListener('change', onWidth)
  })
  /*
    The conversation the reader is looking at. An explicit `agent` on the route
    wins at every width: a drill-in and a return from the full session both
    leave one behind. A wide screen with no explicit agent selects the item's
    last worker — once, on entry — and later polls never move that selection,
    because a worker list that grows or reorders must not take the
    conversation away from the person reading it. Below 1200px nothing opens a
    conversation that the route did not ask for.
  */
  let pickedAgent = $state<string | null>(null)
  let pickedOnce = false
  $effect(() => {
    if (pickedOnce || !item || !desktop) return
    pickedOnce = true
    if (route.agent) return
    pickedAgent = item.workers?.at(-1)?.agentId ?? null
  })
  /** The worker whose conversation this page shows, or null for none. */
  const conversationId = $derived(route.agent ?? (desktop ? pickedAgent : null))
  /*
    The item's own worker list is the authority on whether an agent is still
    part of this work: an id it no longer carries is unavailable rather than a
    reason to fall back to whichever worker happens to be last.
  */
  const conversationWorker = $derived(
    conversationId ? (item?.workers?.find((worker) => worker.agentId === conversationId) ?? null) : null,
  )
  const conversationRow = $derived(agents.find((row) => row.id === conversationId))
  /*
    Below 1200px the drill-in replaces the page. Activity is hidden rather than
    unmounted, so the list keeps the reader's scroll position and its own cue
    state and gives them back on the way out; beside a conversation on a wide
    screen it simply stays where it is.
  */
  const activityHidden = $derived(!desktop && !!conversationId)
  /** This item at the route it is on, which a full session returns to verbatim. */
  const currentItemRoute = $derived(
    formatFactoryRoute({ itemId, view: selectedView, ...(conversationId ? { agent: conversationId } : {}) }),
  )
  /*
    Back to activity is the route minus its agent: the item, the selected view
    and anything else the route carries stay, so the reader lands on this work
    item rather than the factory list.
  */
  function backToActivity() {
    onNavigate(formatFactoryRoute({ ...route, itemId, agent: undefined }))
  }
  /*
    The full session is a page of its own, so the way back has to be carried to
    it: `from` names this item, this view and this worker, Chat's Back consumes
    it, and a later dock change keeps it because the chat route carries it on.
  */
  function openFull(tab?: DockTabParam) {
    if (!conversationId) return
    onNavigate(formatChatRoute({ id: conversationId, view: 'session', tab, from: currentItemRoute }))
  }
  /*
    The decisions' answers, reasons, allowances, replacement documents and
    attestations belong to the panels that collect them, so a refresh of the
    same decision cannot take them away. Only the in-flight command's identity
    is kept here, because request identity is the one thing every panel's
    submission shares.
  */
  let pending = $state<FactoryCommand | null>(null)
  let alive = true
  /*
    Client names decorate provenance: a failed read leaves the stable IDs,
    which are still accurate, so this never becomes a page error.
  */
  let clientNames = $state<Record<string, string>>({})
  async function loadClientNames() {
    try {
      const list = await listClients()
      const names: Record<string, string> = {}
      for (const c of list) if (c.name) names[c.id] = c.name
      clientNames = names
    } catch {
      // The stable IDs identify the client just as well.
    }
  }
  /*
    The first read goes through the shell store's getItem, so an item the last
    list snapshot already carries costs no request.
  */
  onMount(() => {
    void loadClientNames()
    void (async () => {
      try {
        const next = await getItem(itemId)
        if (!alive) return
        fetched = next
        error = ''
      } catch (e) {
        if (alive) error = errMessage(e)
      }
    })()
    return () => { alive = false }
  })
  /*
    One command path for every control (design C5). The panels own what a
    person typed; the identity a request must carry — its request ID, the
    version it was reviewed against, and the decision it answers — is built
    here from the latest item, so a panel can never submit a stale decision
    binding of its own. A revision command rides in the same envelope, so a
    proposal, its assessment and its delivery are never sent apart from it.
  */
  async function command(kind: FactoryCommand['kind'], input: FactoryCommandInput = {}): Promise<FactoryItem | null> {
    if (!item || busy) return null
    busy = true; error = ''; notice = ''
    try {
      {
        const base: FactoryCommand = { requestId: crypto.randomUUID(), kind, expectedVersion: item.version, decisionId: item.decision?.id, ...input }
        const c = input.revision ? factoryRevisionCommand(base, input.revision) : base
        pending = pending && JSON.stringify({ ...pending, requestId: '', replacement: pending.replacement && { ...pending.replacement, requestId: '' } }) === JSON.stringify({ ...c, requestId: '', replacement: c.replacement && { ...c.replacement, requestId: '' } }) ? pending : c
      }
      const applied = await commandFactoryItem(itemId, pending)
      // The command's own result is the newest truth: publish it before the
      // list can render again, then show it here.
      upsert(applied)
      fetched = applied
      pending = null; notice = NOTICES[kind] ?? `${kind} accepted`
      // Handed back as well as shown: a multi-step revision path takes its next
      // command's versions and base from this item, which is the only read that
      // carries what the bridge just wrote.
      return applied
    } catch (e) {
      error = errMessage(e)
      if (e instanceof APIError && e.status === 409) {
        // Only the obsolete command is dropped: the panel that submitted it
        // stays mounted, so the person keeps their input and retries it
        // against the version the refresh just showed them.
        pending = null
        // Awaited, and its own failure reported: a stale command must say so
        // rather than leave the person on a version that cannot succeed.
        try { await onRefresh(); notice = 'Item changed. Review the refreshed version before trying again.' } catch (refreshError) { error += `; refresh failed: ${errMessage(refreshError)}` }
      }
      return null
    } finally { busy = false }
  }
  /**
    The decision panels collect a person's input and hand it back with a kind;
    they need the command's effect, not whether it succeeded.
  */
  const panelRun = async (kind: FactoryCommand['kind'], input?: FactoryCommandInput): Promise<void> => {
    await command(kind, input)
  }
  /*
    The project's automatic-assessment preference decides what "automatic"
    means on this screen. It is read once per project and never guessed at: the
    handoff's default is on, but a failed read is reported as a read that
    failed — the screen must not claim a setting nobody managed to open.
  */
  let autoAssess = $state(true)
  /** False once a read failed, so the section says it could not read the setting. */
  let autoAssessKnown = $state(true)
  let assessRoot = $state('')
  $effect(() => {
    const root = item?.projectRoot
    if (!root || root === assessRoot) return
    assessRoot = root
    void (async () => {
      try {
        autoAssess = effectiveAutoAssess(await getProjectSettings(root))
        autoAssessKnown = true
      } catch {
        autoAssess = true
        autoAssessKnown = false
      }
    })()
  })
  /*
    A view tab moves within this item, so the worker drill-in the route names
    stays selected: the tab hash keeps only the view change, and `agent` is
    carried over from the route rather than dropped by the tab.
  */
  function navigateView(hash: string) {
    const next = parseFactoryRoute(hash)
    if (!next?.itemId) { onNavigate(hash); return }
    onNavigate(formatFactoryRoute({ itemId: next.itemId, view: next.view, agent: route.agent }))
  }
  /** The activity view, which is also where a decision is answered. */
  const activityRoute = $derived(formatFactoryRoute({ itemId, view: 'activity' }))
</script>

<div class="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
  {#if error}<Card role="alert" class="border-attention text-sm">{error}</Card>{/if}
  {#if notice}<Card role="status" class="text-sm">{notice}</Card>{/if}
  {#if item}
    <WorkItemHeader item={item} view={selectedView} onNavigate={navigateView} onStop={() => void command('stop')} stopping={busy} />
    {#if item.decision && selectedView !== 'activity'}
      <Card class="grid gap-2 border-attention" data-testid="factory-decision-banner">
        <h2 class="font-semibold">Action needed: {item.decision.kind}</h2>
        {#if item.decision.question}<p class="whitespace-pre-wrap break-words text-sm">{item.decision.question}</p>{/if}
        <a class="text-sm text-accent underline" href={activityRoute} onclick={(e) => { e.preventDefault(); onNavigate(activityRoute) }}>Answer it in activity</a>
      </Card>
    {/if}
    {#if selectedView === 'approval'}
      {#if item.state === 'draft'}
        <div data-testid="draft-approval">
          <DraftApproval item={item} busy={busy} onApprove={() => void command('approve')} />
        </div>
      {:else}
        <Card class="text-sm">This item is no longer a draft, so there is nothing to approve. <a class="text-accent underline" href={activityRoute} onclick={(e) => { e.preventDefault(); onNavigate(activityRoute) }}>Go to activity</a></Card>
      {/if}
    {:else if selectedView === 'review'}
      <div class="grid min-w-0 content-start gap-4">
        <ReviewView {item} />
      </div>
    {:else if selectedView === 'prs'}
      <PullRequestsView {item} />
    {:else if selectedView === 'documents'}
      <DocumentsView {item} />
    {:else}
      <!--
        Activity is the page, and beside it a wide screen shows the selected
        worker's conversation. Drilling in on a narrow screen hides activity
        without unmounting it, so the list's scroll position and its own state
        come back with the reader; the conversation is keyed by session, so
        switching workers disconnects the one that was showing.
      -->
      <div class={conversationId && desktop ? 'grid min-w-0 items-start gap-4 xl:grid-cols-2' : 'grid min-w-0 gap-4'} data-testid="factory-activity-layout">
        <div class={activityHidden ? 'hidden' : 'grid min-w-0 gap-4'} data-testid="factory-activity-pane">
          <!--
            Plan changes belong to the item, not to a decision: a proposal can
            be written and read at any time, so this is never gated on a
            planner, a decision or a worker being available. A draft has no
            approved revision to revise yet, so it is left to the approval tab.
          -->
          {#if item.state !== 'draft' || item.proposals?.length}
            <FactoryRevision {item} {busy} {autoAssess} {autoAssessKnown} run={(kind, revision) => command(kind, { revision })} />
          {/if}
          {#if item.decision}
            <!--
              Keyed by the decision's id, never by the item's version: the same
              decision refreshed in place keeps the panel — and what the person
              typed into it — mounted, while a poll that replaces the decision
              with a new one of the same kind remounts the panel, so its pickers
              and answer reseed.
            -->
            {#key item.decision.id}
              {#if RECOVERY_KINDS.includes(item.decision.kind)}
                <RecoveryPanel {item} {busy} run={panelRun} />
              {:else}
                <DecisionPanel {item} {busy} run={panelRun} />
              {/if}
            {/key}
          {/if}
          {#if item.workers?.length}
            <Card data-testid="worker-links">
              <h2 class="font-semibold">Agents</h2>
              {#each item.workers as worker}
                <!-- A worker is opened in this item, not on the chat page: the
                     same route, with the agent it names. Parked says so, but
                     the retained conversation stays openable. -->
                <a
                  class="block text-sm text-accent underline"
                  href={formatFactoryRoute({ itemId, view: 'activity', agent: worker.agentId })}
                  onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId, view: 'activity', agent: worker.agentId })) }}
                >{worker.role}: {worker.agentId}{worker.parked ? ' (parked)' : ''}</a>
              {/each}
            </Card>
          {/if}
          <ActivityTimeline {item} {clientNames} hidden={activityHidden} />
        </div>
        {#if conversationId}
          {#if conversationWorker}
            <div class="h-[70vh] min-h-[32rem] min-w-0 overflow-hidden rounded-lg border border-border" data-testid="agent-conversation">
              <!--
                Keyed by the conversation it owns, not by the item: switching
                workers remounts the conversation, which is what disconnects
                the previous session's stream. The id is the agent's, the one
                `#chat/<id>` uses and the fleet's rows are keyed by, so the
                header still resolves the name it reports.
              -->
              {#key conversationWorker.agentId}
                <AgentConversation
                  sessionId={conversationWorker.agentId}
                  onBack={backToActivity}
                  agent={conversationRow}
                  embedded
                  onOpenFull={openFull}
                />
              {/key}
            </div>
            <!--
              This worker's own Studio access. It is the same control set the
              session screen and the project settings use — only the identities
              differ — and the bridge treats a factory worker's override as the
              worker's, not as its session's.
            -->
            <details class="mt-3" data-testid="worker-studio-access">
              <summary class="cursor-pointer text-xs text-muted">Worker access</summary>
              <div class="mt-2">
                <MarshalAccessPanel
                  sessionId={conversationWorker.agentId}
                  workerId={conversationWorker.agentId}
                  levels={['session']}
                  heading="This worker"
                  testId="worker-marshal-access"
                />
              </div>
            </details>
          {:else}
            <!-- An id the item does not carry is not quietly swapped for
                 another worker: the pane says so and offers the way back. -->
            <Card class="grid gap-2" data-testid="agent-unavailable">
              <h2 class="font-semibold">This worker is unavailable</h2>
              <p class="text-sm">No worker named {conversationId} belongs to this work item, so there is no conversation to show.</p>
              <Button variant="ghost" onclick={backToActivity}>Back to activity</Button>
            </Card>
          {/if}
        {/if}
      </div>
    {/if}
  {:else}<p role="status">Loading work item…</p>{/if}
</div>
