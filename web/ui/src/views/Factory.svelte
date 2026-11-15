<script lang="ts">
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import PageHeader from '../lib/ui/PageHeader.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import StateMessage from '../lib/ui/StateMessage.svelte'
  import FactoryDefaultsSummary from '../lib/studio/FactoryDefaultsSummary.svelte'
  import WorkItemCard from '../lib/studio/WorkItemCard.svelte'
  import { errMessage, getProjectSettings, type ProjectSettings } from '../lib/api'
  import type { FactoryFilter } from '../lib/factory'
  import type { FactoryState } from '../lib/factoryStore'
  import type { AgentRow } from '../lib/fleet'
  import { factoryWorkerIds, filterFactoryItems, summarizeFactoryProject } from '../lib/studio/model'
  import { formatChatRoute, formatFactoryRoute, formatProjectRoute } from '../lib/routes'
  import { shortName } from '../lib/utils'

  /*
    One list for both the global factory and a project's factory (design C5).
    The list renders from the shell's store rather than a poller of its own:
    one store polls for the list, the item view and the create view, so a
    command's own result is already in the list when it next redraws. The
    scope and the filter are projections of that one snapshot, so switching
    either never starts a second read and the list never remounts.

    The dashboard itself reads one extra thing, and only when scoped: the
    project's settings, for its factory defaults. That read is deliberately
    independent of the list — a failure is reported as a partial warning and
    leaves every work item on screen.
  */
  let {
    factory,
    onRefresh,
    onNavigate,
    projectRoot = '',
    filter = 'current',
    agents = [],
  }: {
    factory: FactoryState
    onRefresh: () => void
    onNavigate: (hash: string) => void
    projectRoot?: string
    filter?: FactoryFilter
    agents?: AgentRow[]
  } = $props()

  // The project scope is exact: a sibling directory that merely shares a
  // prefix is a different project. C2 owns the sort as well, so the order
  // here is the same one the fleet's counts use.
  const scoped = $derived(projectRoot ? factory.items.filter((item) => item.projectRoot === projectRoot) : factory.items)
  const items = $derived(filterFactoryItems(scoped, filter))

  /*
    Needs-you is C2's projection, not a second rule: an item the projection
    classes as waiting on a person is lifted above the work list and removed
    from it, so the same item can never render twice.
  */
  const needsIds = $derived(new Set(filterFactoryItems(items, 'needs-you').map((item) => item.id)))
  const decisionItems = $derived(items.filter((item) => needsIds.has(item.id)))
  const workItems = $derived(items.filter((item) => !needsIds.has(item.id)))

  const summary = $derived(projectRoot ? summarizeFactoryProject(projectRoot, factory.items) : null)
  const counts = $derived(
    summary
      ? `${summary.current} current · ${summary.needsYou} needs you · ${summary.ready} ready`
      : `${filterFactoryItems(scoped, 'current').length} current · ${filterFactoryItems(scoped, 'needs-you').length} needs you · ${filterFactoryItems(scoped, 'ready').length} ready`,
  )

  const filters: { value: FactoryFilter; label: string }[] = [
    { value: 'current', label: 'Current' },
    { value: 'needs-you', label: 'Needs you' },
    { value: 'ready', label: 'Ready' },
    { value: 'history', label: 'History' },
  ]

  // A filter is a route, not local state: it can never drift from the URL the
  // person is actually on, and Back returns to the previous filter.
  function applyFilter(value: string) {
    const next = value as FactoryFilter
    if (next === filter) return
    onNavigate(formatFactoryRoute({ projectRoot: projectRoot || undefined, filter: next }))
  }

  const newItemHash = $derived(formatFactoryRoute({ create: true, projectRoot: projectRoot || undefined }))
  const newAgentHash = $derived(projectRoot ? `#new?project=${encodeURIComponent(projectRoot)}` : '#new')

  /*
    The linked agents of the current work: unique worker IDs, with the fleet
    row when the shell has one. A worker with no row is still listed — the
    factory knows it holds the agent, and hiding it would say the opposite.
  */
  const currentItems = $derived(filterFactoryItems(scoped, 'current'))
  const linkedIds = $derived([...factoryWorkerIds(currentItems)].sort())
  const linked = $derived(
    linkedIds.map((id) => {
      const workers = currentItems.flatMap((item) => (item.workers ?? []).filter((worker) => worker.agentId === id))
      return {
        id,
        row: agents.find((agent) => agent.id === id),
        parked: workers.some((worker) => worker.parked),
        role: workers.find((worker) => worker.role)?.role ?? '',
      }
    }),
  )

  // The project's factory defaults, read on mount (and on a scope change).
  // Nothing here blocks the list: the items above never wait on this.
  let defaults = $state<ProjectSettings | null>(null)
  let defaultsError = $state('')
  $effect(() => {
    const root = projectRoot
    if (!root) return
    let stale = false
    defaults = null
    defaultsError = ''
    void getProjectSettings(root).then(
      (settings) => {
        if (stale) return
        defaults = settings
        defaultsError = ''
      },
      (e) => {
        if (stale) return
        defaults = null
        defaultsError = errMessage(e)
      },
    )
    return () => {
      stale = true
    }
  })

  // Empty is a claim, so it is only made about a scope that really holds
  // nothing: a filtered-out list inside a project with work is not empty.
  const filteredOut = $derived(items.length === 0 && scoped.length > 0)
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-4 sm:p-6">
  <PageHeader
    breadcrumbs={projectRoot
      ? [{ label: 'Projects', hash: '#projects' }, { label: shortName(projectRoot), hash: formatProjectRoute(projectRoot) }, { label: 'Factory' }]
      : [{ label: 'Fleet', hash: '#' }, { label: 'Factory' }]}
    eyebrow={projectRoot ? shortName(projectRoot) : 'Factory'}
    title="Factory"
    subtitle={projectRoot ? projectRoot : 'Supervised delivery work across every project'}
    {onNavigate}
  >
    {#snippet status()}
      <span class="text-xs text-muted" data-testid="factory-counts">{counts}</span>
    {/snippet}
    {#snippet actions()}
      <Button variant="ghost" onclick={() => onNavigate(newAgentHash)}>Start agent</Button>
      <Button onclick={() => onNavigate(newItemHash)}>New work item</Button>
    {/snippet}
  </PageHeader>

  {#if factory.error}
    <Card role="alert" class="border-attention text-sm">
      <p>Factory items unavailable: {factory.error}</p>
      <Button variant="ghost" onclick={onRefresh}>Retry</Button>
    </Card>
  {/if}

  {#if projectRoot && defaults}
    <FactoryDefaultsSummary root={projectRoot} settings={defaults} {onNavigate} />
  {:else if projectRoot && defaultsError}
    <!-- A failed settings read is partial: the work items below are unaffected. -->
    <Card class="border-attention text-sm" data-testid="defaults-error">
      <p>Factory defaults unavailable: {defaultsError}</p>
      <a class="text-accent underline" href={formatProjectRoute(projectRoot)} onclick={(e) => { e.preventDefault(); onNavigate(formatProjectRoute(projectRoot)) }}>Open project settings</a>
    </Card>
  {/if}

  <div class="flex flex-wrap items-center gap-2">
    <Segmented label="Factory filters" options={filters} value={filter} onchange={applyFilter} />
    <span class="flex-1"></span>
    <Button variant="ghost" onclick={onRefresh} disabled={factory.refreshing}>Refresh</Button>
  </div>

  {#if factory.loading}
    <p role="status">Loading factory items…</p>
  {:else if items.length}
    {#if decisionItems.length}
      <section aria-labelledby="factory-decisions" data-testid="factory-decisions">
        <h2 id="factory-decisions" class="mb-2 text-xs tracking-wide text-attention uppercase">Needs your decision · {decisionItems.length}</h2>
        <div class="flex flex-col gap-2">
          {#each decisionItems as item (item.id)}<WorkItemCard {item} {onNavigate} />{/each}
        </div>
      </section>
    {/if}
    {#if workItems.length}
      <section aria-labelledby="factory-work" data-testid="factory-work-items">
        <h2 id="factory-work" class="mb-2 text-xs tracking-wide text-muted uppercase">Work items · {workItems.length}</h2>
        <div class="grid gap-2">
          {#each workItems as item (item.id)}<WorkItemCard {item} {onNavigate} />{/each}
        </div>
      </section>
    {/if}
  {:else if !factory.error && filteredOut}
    <StateMessage tone="neutral" title="Nothing in this filter" body="No work item in this scope matches the selected filter." />
  {:else if !factory.error}
    <!-- A failed read says so above; it is never also "nothing exists yet". -->
    {#if projectRoot}
      <StateMessage
        tone="neutral"
        title="No factory work items in this project yet."
        body="Approve a handoff to start supervised delivery work here."
        actionLabel="New work item"
        onAction={() => onNavigate(newItemHash)}
      />
    {:else}
      <StateMessage
        tone="neutral"
        title="No factory work items yet."
        body="Create a draft, approve it, and the factory plans, implements and reviews the work."
        actionLabel="Create a draft"
        onAction={() => onNavigate(newItemHash)}
      />
    {/if}
  {/if}

  {#if linked.length > 0}
    <Card class="flex flex-col gap-2" data-testid="linked-agents">
      <details>
        <summary class="cursor-pointer text-sm font-semibold">Linked agents · {linked.length}</summary>
        <ul class="mt-2 flex flex-col gap-1">
          {#each linked as agent (agent.id)}
            <li class="flex flex-wrap items-baseline gap-2 text-sm" data-testid="linked-agent">
              <a class="text-accent underline" href={formatChatRoute({ id: agent.id, view: 'session' })} onclick={(e) => { e.preventDefault(); onNavigate(formatChatRoute({ id: agent.id, view: 'session' })) }}>{agent.row?.name || agent.id}</a>
              {#if agent.role}<span class="text-xs text-muted">{agent.role}</span>{/if}
              <!-- Parked and status come from the fleet only when it has the row; the worker is listed regardless. -->
              {#if agent.row && agent.parked}<span class="text-xs text-warn">parked</span>{/if}
              {#if agent.row}<span class="text-xs text-muted">{agent.row.status}</span>{/if}
            </li>
          {/each}
        </ul>
      </details>
    </Card>
  {/if}
</div>
