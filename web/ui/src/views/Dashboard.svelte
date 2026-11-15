<script lang="ts">
  import { sortAttentionFirst, type AgentRow } from '../lib/fleet'
  import type { ProjectStatus } from '../lib/api'
  import AgentCard from '../lib/AgentCard.svelte'
  import AttentionList from '../lib/AttentionList.svelte'
  import Button from '../lib/ui/Button.svelte'
  import ProjectWarnings from '../lib/studio/ProjectWarnings.svelte'

  /*
    The fleet store lives in the shell so the sidebar shares it. Dashboard
    renders whatever it is handed and asks for a refresh; it no longer owns
    a store or an SSE connection of its own.
  */
  interface FleetState {
    agents: AgentRow[]
    projects: ProjectStatus[]
    loading: boolean
    error: string | null
  }

  let {
    fleet,
    onRefresh,
    onOpenAgent,
    onNewAgent,
  }: {
    fleet: FleetState
    onRefresh: () => void
    onOpenAgent: (id: string) => void
    onNewAgent: () => void
  } = $props()

  // Group by project so a multi-project fleet stays legible, with the
  // groups themselves ordered by their most-urgent agent.
  const grouped = $derived.by(() => {
    const byProject = new Map<string, AgentRow[]>()
    for (const a of sortAttentionFirst(fleet.agents)) {
      const list = byProject.get(a.project)
      if (list) list.push(a)
      else byProject.set(a.project, [a])
    }
    return [...byProject.entries()]
  })
</script>

<div class="mx-auto flex max-w-7xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between gap-3">
    <h1 class="text-lg font-semibold">Fleet</h1>
    <div class="flex gap-2">
      <Button variant="ghost" onclick={onRefresh}>Refresh</Button>
      <Button onclick={onNewAgent}>New agent</Button>
    </div>
  </header>

  {#if fleet.error}
    <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm">{fleet.error}</div>
  {/if}

  <ProjectWarnings projects={fleet.projects} agents={fleet.agents} />

  <AttentionList agents={fleet.agents} onOpen={onOpenAgent} onResolved={onRefresh} />

  {#if fleet.agents.length === 0 && !fleet.loading}
    <p class="text-sm text-muted">No agents yet. Create one to get started.</p>
  {/if}

  {#each grouped as [project, agents] (project)}
    <section class="flex flex-col gap-2">
      <h2 class="truncate text-xs tracking-wide text-muted uppercase" title={project}>
        {project.split('/').filter(Boolean).pop() ?? project}
      </h2>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        {#each agents as a (a.id)}
          <AgentCard agent={a} onOpen={onOpenAgent} />
        {/each}
      </div>
    </section>
  {/each}
</div>
