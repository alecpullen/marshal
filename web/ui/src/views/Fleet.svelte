<script lang="ts">
  import { untrack } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import StateMessage from '../lib/ui/StateMessage.svelte'
  import AttentionCard from '../lib/studio/AttentionCard.svelte'
  import ProjectFactoryCard from '../lib/studio/ProjectFactoryCard.svelte'
  import IndependentAgentList from '../lib/studio/IndependentAgentList.svelte'
  import ProjectWarnings from '../lib/studio/ProjectWarnings.svelte'
  import {
    factoryItemRoute,
    factoryWorkerIds,
    filterFactoryItems,
    summarizeFactoryProject,
    type FactoryProjectSummary,
  } from '../lib/studio/model'
  import type { FactoryState } from '../lib/factoryStore'
  import { loadAutomationSummaries } from '../lib/automations/summary'
  import { prNumber, severityCounts } from '../lib/automations/model'
  import PendingActions from '../lib/inbox/PendingActions.svelte'
  import NetworkDecision from '../lib/inbox/NetworkDecision.svelte'
  import type { DecideFn } from '../lib/inbox/decision'
  import { describePending, type AgentRow, type NetworkDecisionItem, type RerouteNotice } from '../lib/fleet'
  import { APIError, errMessage, undoReroute, type CIHistory, type Inbox, type PendingSubmission, type ProjectStatus, type ReviewDraft } from '../lib/api'
  import { formatAutomationRoute } from '../lib/routes'
  import { shortName } from '../lib/utils'

  /*
    The prioritised operational summary: what needs a person first, then what
    is ready to review, then the projects and the agents around them.

    Fleet owns no transport. The fleet, its inbox and the factory are read
    from the shell's stores and handed in as props; this view derives its
    sections from them and asks for a refresh through the two callbacks it is
    given. Nothing here opens a stream or a poller of its own.
  */

  /** One needs-you entry: an agent the bridge named, or an intake submission it carried. */
  type NeedsItem =
    | { kind: 'agent'; agent: AgentRow; at: string }
    | { kind: 'intake'; submission: PendingSubmission; at: string }

  let {
    agents,
    inbox,
    onRefreshPending,
    onOpenAgent,
    onNavigate,
    notices = [],
    onDismissNotice = () => {},
    decisions = [],
    onDecide = async () => {},
    projects = [],
    automationTick = 0,
    factory,
    onRefreshFactory,
    onRefreshFleet,
  }: {
    agents: AgentRow[]
    /** The bridge's inbox: its groups name agents by ID, its queue carries intake. */
    inbox: Inbox
    /** Watches that rerouted a role, each undoable until dismissed. */
    notices?: RerouteNotice[]
    onDismissNotice?: (id: string) => void
    /** Requests the egress proxy blocked, each waiting on Block, Allow for this agent, or Add to workspace. */
    decisions?: NetworkDecisionItem[]
    onDecide?: DecideFn
    /** Where review drafts and CI fixes are looked up, one project at a time. */
    projects?: ProjectStatus[]
    /** Bumped when an `automation` delta arrives. */
    automationTick?: number
    /** The shell's factory list: what the factory holds, and how its last read went. */
    factory: FactoryState
    onRefreshFactory: () => void
    onRefreshFleet: () => void
    onRefreshPending: () => void
    onOpenAgent: (id: string) => void
    onNavigate: (hash: string) => void
  } = $props()

  const items = $derived(factory.items)
  // All items, not current-only: an agent an old factory item holds is still a factory worker.
  const workerIds = $derived(factoryWorkerIds(items))
  const needsFactory = $derived(filterFactoryItems(items, 'needs-you'))
  const readyFactory = $derived(filterFactoryItems(items, 'ready'))

  const byId = $derived(new Map(agents.map((a) => [a.id, a])))
  const rowsOf = (ids: string[]) => ids.flatMap((id) => { const row = byId.get(id); return row ? [row] : [] })
  const readyAgents = $derived(rowsOf(inbox.groups.ready))
  // The queue is already ordered, oldest first; intake reads its submission from it.
  const queue = $derived(
    inbox.queue.flatMap((item): NeedsItem[] => {
      if (item.kind === 'agent') {
        const agent = item.agentId ? byId.get(item.agentId) : undefined
        return agent ? [{ kind: 'agent', agent, at: item.at }] : []
      }
      return item.submission ? [{ kind: 'intake', submission: item.submission, at: item.at }] : []
    }),
  )
  // Oldest first, so the request that has waited longest is on top.
  const netDecisions = $derived([...decisions].filter((d) => agents.some((a) => a.id === d.agentId)).sort((a, b) => a.at - b.at))
  const agentName = (id: string) => agents.find((a) => a.id === id)?.name || id

  // Review drafts and CI fixes are optional extras: a bridge without automations just shows none.
  let drafts = $state<{ root: string; draft: ReviewDraft }[]>([])
  let fixes = $state<CIHistory[]>([])
  let automationErrors = $state<{ root: string; message: string }[]>([])
  const automationRoots = $derived(projects.map((p) => p.root).join('\n'))
  $effect(() => {
    void automationTick
    void automationRoots
    // The project rows are read untracked: `automationRoots` above decides when
    // to reload, so an unrelated fleet delta does not repeat every project's read.
    const list = untrack(() => projects)
    let stale = false
    void loadAutomationSummaries(list).then((s) => {
      if (stale) return
      drafts = s.drafts
      fixes = s.fixes
      automationErrors = s.errors
    })
    return () => {
      stale = true
    }
  })

  /*
    Every root that has either a project row or factory work, so a factory-only
    directory is summarised rather than dropped. Sorted by short name so the
    order does not depend on which source named a root first.
  */
  const summaries = $derived.by(() => {
    const seen = new Set<string>()
    const out: FactoryProjectSummary[] = []
    for (const p of projects) {
      if (seen.has(p.root)) continue
      seen.add(p.root)
      out.push(summarizeFactoryProject(p.root, items))
    }
    for (const item of items) {
      if (seen.has(item.projectRoot)) continue
      seen.add(item.projectRoot)
      out.push(summarizeFactoryProject(item.projectRoot, items))
    }
    return out.sort((a, b) => shortName(a.root).localeCompare(shortName(b.root)) || a.root.localeCompare(b.root))
  })
  const projectByRoot = $derived(new Map(projects.map((p) => [p.root, p])))

  const independents = $derived(agents.filter((a) => !workerIds.has(a.id)))

  const attentionCount = $derived(needsFactory.length + queue.length + netDecisions.length)
  const readyCount = $derived(readyFactory.length + readyAgents.length + drafts.length + fixes.length)

  /*
    Empty is a claim, so it is made only after every source has answered: the
    factory has finished a read, that read did not fail, and the fleet, inbox,
    decisions, projects and automations all reported nothing. A failed factory
    read keeps whatever it had and shows the error instead.
  */
  const isEmpty = $derived(
    !factory.loading &&
      !factory.error &&
      attentionCount === 0 &&
      readyCount === 0 &&
      summaries.length === 0 &&
      independents.length === 0 &&
      automationErrors.length === 0,
  )

  let notice = $state<string | null>(null)

  /*
    Undo is the one notice action that calls the bridge, so it stays with the
    view that shows the notice — the same handling Home has today, including
    the two refusals that mean the undo can never succeed any more.
  */
  async function undo(n: RerouteNotice) {
    try {
      await undoReroute(n.id)
      onDismissNotice(n.id)
    } catch (e) {
      notice = errMessage(e)
      // 409: the binding changed since (or it was undone); 404: the bridge restarted.
      if (e instanceof APIError && (e.status === 409 || e.status === 404)) onDismissNotice(n.id)
    }
  }

  const clock = (ms: number) => new Date(ms).toLocaleTimeString()

  // The resources the shell already exposes, as plain links: this section is a
  // directory, not another navigation model.
  const RESOURCES = [
    { hash: '#projects', label: 'Projects' },
    { hash: '#live', label: 'Live' },
    { hash: '#runs', label: 'Runs' },
    { hash: '#workspaces', label: 'Workspaces' },
    { hash: '#watches', label: 'Watches' },
    { hash: '#library/skills', label: 'Library' },
    { hash: '#usage', label: 'Usage' },
    { hash: '#settings/models', label: 'Settings' },
  ]
</script>

<div class="mx-auto flex max-w-6xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between gap-3">
    <h1 class="text-lg font-semibold">Fleet</h1>
    <Button variant="ghost" onclick={onRefreshFleet}>Refresh</Button>
  </header>

  {#if notice}
    <Card class="border-attention text-sm">{notice}</Card>
  {/if}

  <ProjectWarnings {projects} {agents} />

  {#each notices as n (n.id)}
    <Card class="flex items-center gap-3 p-3 text-sm" data-testid="reroute-notice">
      <span class="min-w-0 flex-1 truncate">
        A watch moved <span class="font-mono">{n.role}</span> from <span class="font-mono">{n.from}</span> to <span class="font-mono">{n.to}</span>.
      </span>
      <Button variant="ghost" onclick={() => undo(n)}>Undo</Button>
      <Button variant="ghost" aria-label="Dismiss" onclick={() => onDismissNotice(n.id)}>✕</Button>
    </Card>
  {/each}

  {#if factory.loading}
    <p class="text-sm text-muted" data-testid="factory-loading">Loading factory work…</p>
  {/if}

  {#if factory.error}
    <Card class="border-attention text-sm" data-testid="factory-error">
      <div class="flex flex-wrap items-center gap-3">
        <div class="min-w-0 flex-1">
          <p class="font-medium">Factory work could not be refreshed</p>
          <p class="break-words text-muted">{factory.error}</p>
          {#if factory.lastUpdated}
            <p class="text-xs text-muted">Showing the last update from {clock(factory.lastUpdated)}.</p>
          {/if}
        </div>
        <Button variant="ghost" onclick={onRefreshFactory}>Retry</Button>
      </div>
    </Card>
  {/if}

  {#if automationErrors.length}
    <Card class="border-attention text-sm" data-testid="automation-error">
      {#each automationErrors as e (e.root)}
        <p class="truncate">{shortName(e.root)} · {e.message}</p>
      {/each}
    </Card>
  {/if}

  {#if attentionCount > 0}
    <section aria-labelledby="fleet-needs">
      <h2 id="fleet-needs" class="mb-2 text-xs tracking-wide text-attention uppercase">Needs you · {attentionCount}</h2>
      <div class="flex flex-col gap-2">
        {#each needsFactory as item (item.id)}
          <AttentionCard
            source="Factory"
            title={item.title}
            body={item.decision
              ? item.decision.question || 'A factory decision is waiting on you.'
              : 'The plan is a draft and needs your approval.'}
            meta="{shortName(item.projectRoot)} · {item.stage || item.state}"
            href={factoryItemRoute(item)}
            actionLabel={item.state === 'draft' ? 'Review draft' : 'Decide'}
            {onNavigate}
          />
        {/each}
        {#each queue as item (item.kind === 'agent' ? 'a:' + item.agent.id : 'i:' + item.submission.id)}
          {#if item.kind === 'agent'}
            {@const a = item.agent}
            {#snippet actions()}
              <PendingActions agent={a} onResolved={onRefreshPending} onOpen={() => onNavigate(`#chat/${a.id}`)} />
            {/snippet}
            <AttentionCard
              source="Agent"
              title={a.name || a.id}
              body={a.pending ? describePending(a.pending) : ''}
              meta={shortName(a.project)}
              href={`#chat/${a.id}`}
              actionLabel="Open agent"
              {onNavigate}
              {actions}
            />
          {:else}
            {@const p = item.submission}
            <AttentionCard
              source="Intake"
              title={p.title}
              body="Intake request waiting for an operator"
              meta={p.clientId ? `${p.repoId} · from ${p.clientId}` : p.repoId}
              href="#inbox"
              actionLabel="Review in Inbox"
              {onNavigate}
            />
          {/if}
        {/each}
        {#each netDecisions as d (d.agentId + '|' + d.host)}
          {#snippet actions()}
            <NetworkDecision item={d} agentName={agentName(d.agentId)} {onDecide} />
          {/snippet}
          <AttentionCard
            source="Network"
            title="{agentName(d.agentId)} is blocked"
            body="The egress proxy stopped a request and is waiting on your decision."
            meta={d.workspace ? `workspace ${d.workspace}` : 'no workspace'}
            href={`#network?agent=${encodeURIComponent(d.agentId)}`}
            actionLabel="Open network"
            {onNavigate}
            {actions}
          />
        {/each}
      </div>
    </section>
  {/if}

  {#if readyCount > 0}
    <section aria-labelledby="fleet-ready">
      <h2 id="fleet-ready" class="mb-2 text-xs tracking-wide text-accent uppercase">Ready for review · {readyCount}</h2>
      <div class="flex flex-col gap-2">
        {#each readyFactory as item (item.id)}
          <AttentionCard
            source="Factory"
            title={item.title}
            body="Checks have passed; the work is ready for review."
            meta="{shortName(item.projectRoot)} · {item.branch || item.stage}"
            href={factoryItemRoute(item)}
            actionLabel="Review work"
            {onNavigate}
          />
        {/each}
        {#each readyAgents as a (a.id)}
          <AttentionCard
            source="Agent"
            title={a.name || a.id}
            body="{a.branch || shortName(a.project)} · {a.changedFiles} file{a.changedFiles === 1 ? '' : 's'}"
            meta={shortName(a.project)}
            href={`#chat/${a.id}`}
            actionLabel="Open agent"
            {onNavigate}
          />
        {/each}
        {#each drafts as { root, draft: d } (d.id)}
          <AttentionCard
            source="Review draft"
            title="Review draft for PR #{d.number}"
            body={severityCounts(d.findings).map((c) => `${c.count} ${c.severity}`).join(' · ') || 'No findings'}
            meta={shortName(root)}
            href={formatAutomationRoute(root, 'review-bot', d.id)}
            actionLabel="Open draft"
            {onNavigate}
          />
        {/each}
        {#each fixes as h (h.id)}
          {@const n = prNumber(h.prUrl)}
          <Card class="flex flex-wrap items-center gap-3 p-3" data-testid="ci-fix-ready">
            <div class="min-w-0 flex-1">
              <div class="text-[11px] tracking-wide text-muted uppercase">CI fix</div>
              <div class="break-words text-sm font-medium">CI fix ready{n ? `: PR #${n}` : ''}</div>
              <div class="break-words text-xs text-muted">{h.check} · {h.reason}</div>
            </div>
            <a class="inline-flex min-h-11 items-center rounded-md border border-border px-3 text-sm hover:bg-surface" href={h.prUrl} target="_blank" rel="noopener noreferrer">Open PR</a>
          </Card>
        {/each}
      </div>
    </section>
  {/if}

  {#if summaries.length > 0}
    <section aria-labelledby="fleet-factories">
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 id="fleet-factories" class="text-xs tracking-wide text-accent uppercase">Project factories · {summaries.length}</h2>
        <div class="flex items-center gap-2 text-xs text-muted">
          {#if factory.lastUpdated}
            <span data-testid="factory-updated">Updated {clock(factory.lastUpdated)}</span>
          {/if}
          <Button variant="ghost" onclick={onRefreshFactory}>Refresh factories</Button>
        </div>
      </div>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {#each summaries as summary (summary.root)}
          <ProjectFactoryCard project={projectByRoot.get(summary.root)} {summary} {onNavigate} />
        {/each}
      </div>
    </section>
  {/if}

  {#if independents.length > 0}
    <section aria-labelledby="fleet-agents">
      <h2 id="fleet-agents" class="mb-2 text-xs tracking-wide text-muted uppercase">Independent agents · {independents.length}</h2>
      <IndependentAgentList agents={independents} onOpen={onOpenAgent} />
    </section>
  {/if}

  {#if isEmpty}
    <StateMessage
      tone="neutral"
      title="Nothing needs you"
      body="No factory work, agent requests or automation results are waiting."
    />
  {/if}

  <section aria-labelledby="fleet-resources">
    <h2 id="fleet-resources" class="mb-2 text-xs tracking-wide text-muted uppercase">Fleet resources</h2>
    <nav class="flex flex-wrap gap-2" aria-label="Fleet resources">
      {#each RESOURCES as r (r.hash)}
        <a class="inline-flex min-h-11 items-center rounded-md border border-border px-3 text-sm hover:bg-surface" href={r.hash}>{r.label}</a>
      {/each}
    </nav>
  </section>
</div>
