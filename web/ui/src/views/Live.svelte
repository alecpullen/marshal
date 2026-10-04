<script lang="ts">
  import Button from '../lib/ui/Button.svelte'
  import Tile from '../lib/live/Tile.svelte'
  import { paginate, wallAgents } from '../lib/live/wall'
  import { formatLiveRoute, type LiveRoute } from '../lib/routes'
  import type { AgentRow, NetworkDecisionItem } from '../lib/fleet'
  import type { DecideFn } from '../lib/inbox/decision'
  import type { ProjectStatus } from '../lib/api'
  import { shortName } from '../lib/utils'

  let {
    agents,
    projects,
    route,
    onRefreshPending,
    onNavigate,
    decisions = [],
    onDecide = async () => {},
  }: {
    agents: AgentRow[]
    projects: ProjectStatus[]
    route: LiveRoute
    onRefreshPending: () => void
    onNavigate: (hash: string) => void
    /** Blocked requests; a tile shows the oldest one of its own agent. */
    decisions?: NetworkDecisionItem[]
    onDecide?: DecideFn
  } = $props()

  const decisionFor = (id: string) => decisions.filter((d) => d.agentId === id).sort((a, b) => a.at - b.at)[0]

  const filtered = $derived(wallAgents(agents, { project: route.project, runsOnly: route.runsOnly }))
  const paged = $derived(paginate(filtered, route.page))

  const go = (over: Partial<LiveRoute>) => onNavigate(formatLiveRoute({ ...route, ...over }))
  const field = 'rounded-md border border-border bg-bg px-2 py-1 text-sm'
</script>

<div class="flex flex-col gap-4 p-6">
  <header class="flex flex-wrap items-center gap-3">
    <h1 class="text-lg font-semibold">Live</h1>
    <span class="flex-1"></span>
    <label class="flex items-center gap-2 text-xs text-muted">
      Project
      <select class={field} value={route.project ?? ''} aria-label="Project" onchange={(e) => go({ project: e.currentTarget.value || undefined, page: 1 })}>
        <option value="">All projects</option>
        {#each projects as p (p.root)}
          <option value={p.root}>{shortName(p.root)}</option>
        {/each}
      </select>
    </label>
    <label class="flex cursor-pointer items-center gap-2 text-xs text-muted">
      <input type="checkbox" checked={route.runsOnly} onchange={(e) => go({ runsOnly: e.currentTarget.checked, page: 1 })} />
      Runs only
    </label>
  </header>

  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 min-[1600px]:grid-cols-4" data-testid="wall">
    {#each paged.items as a (a.id)}
      <Tile agent={a} {onRefreshPending} {onNavigate} decision={decisionFor(a.id)} {onDecide} />
    {:else}
      <p class="col-span-full text-sm text-muted">{route.runsOnly ? 'No agents are running a plan or swarm.' : 'No agents to show.'}</p>
    {/each}
  </div>

  {#if paged.pages > 1}
    <nav class="flex items-center justify-center gap-3" aria-label="Pages">
      <Button variant="ghost" disabled={paged.page <= 1} onclick={() => go({ page: paged.page - 1 })}>Previous</Button>
      <span class="font-mono text-xs text-muted">Page {paged.page} of {paged.pages}</span>
      <Button variant="ghost" disabled={paged.page >= paged.pages} onclick={() => go({ page: paged.page + 1 })}>Next</Button>
    </nav>
  {/if}
</div>
