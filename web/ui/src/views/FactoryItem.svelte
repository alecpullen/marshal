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
  import { APIError, commandFactoryItem, errMessage } from '../lib/api'
  import { formatFactoryRoute, parseFactoryRoute } from '../lib/routes'
  import { defaultFactoryItemView } from '../lib/studio/model'
  import type { FactoryCommand, FactoryCreate, FactoryItem, FactoryItemView, FactoryRoute } from '../lib/factory'

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
    getItem,
    upsert,
    onRefresh,
    onNavigate,
  }: {
    itemId: string
    route: FactoryRoute
    factory: FactoryState
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
    The decisions' answers, reasons, allowances, replacement documents and
    attestations belong to the panels that collect them, so a refresh of the
    same decision cannot take them away. Only the in-flight command's identity
    is kept here, because request identity is the one thing every panel's
    submission shares.
  */
  let pending = $state<FactoryCommand | null>(null)
  let alive = true
  /*
    The first read goes through the shell store's getItem, so an item the last
    list snapshot already carries costs no request.
  */
  onMount(() => {
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
    binding of its own.
  */
  async function command(kind: FactoryCommand['kind'], input: FactoryCommandInput = {}) {
    if (!item || busy) return
    busy = true; error = ''; notice = ''
    try {
      {
        const c: FactoryCommand = { requestId: crypto.randomUUID(), kind, expectedVersion: item.version, decisionId: item.decision?.id, ...input }
        pending = pending && JSON.stringify({ ...pending, requestId: '', replacement: pending.replacement && { ...pending.replacement, requestId: '' } }) === JSON.stringify({ ...c, requestId: '', replacement: c.replacement && { ...c.replacement, requestId: '' } }) ? pending : c
      }
      const applied = await commandFactoryItem(itemId, pending)
      // The command's own result is the newest truth: publish it before the
      // list can render again, then show it here.
      upsert(applied)
      fetched = applied
      pending = null; notice = kind === 'stop' ? 'Stop requested. Waiting for worker confirmation.' : `${kind} accepted`
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
    } finally { busy = false }
  }
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
      <div class="grid min-w-0 gap-4">
        {#if item.decision}
          <!--
            The panel is chosen by the decision kind, never keyed by the item's
            version: the same decision refreshed in place keeps the panel — and
            what the person typed into it — mounted. A different decision kind
            is a different decision, so it legitimately starts fresh.
          -->
          {#if RECOVERY_KINDS.includes(item.decision.kind)}
            <RecoveryPanel {item} {busy} run={command} />
          {:else}
            <DecisionPanel {item} {busy} run={command} />
          {/if}
        {/if}
        {#if item.workers?.length}<Card><h2 class="font-semibold">Agents</h2>{#each item.workers as worker}<a class="block text-sm text-accent underline" href={`#chat/${encodeURIComponent(worker.agentId)}`} onclick={(e) => { e.preventDefault(); onNavigate(`#chat/${encodeURIComponent(worker.agentId)}`) }}>{worker.role}: {worker.agentId}</a>{/each}</Card>{/if}
        <ActivityTimeline {item} />
      </div>
    {/if}
  {:else}<p role="status">Loading work item…</p>{/if}
</div>
