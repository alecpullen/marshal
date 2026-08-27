<script lang="ts">
  import { onMount } from 'svelte'
  import { createFleetStore, sortAttentionFirst, type AgentRow } from '../lib/fleet'
  import { connectFleetSSE } from '../lib/sse'
  import AgentCard from '../lib/AgentCard.svelte'
  import AttentionList from '../lib/AttentionList.svelte'
  import Button from '../lib/ui/Button.svelte'
  import PendingList from '../lib/PendingList.svelte'
  import ClientsPanel from '../lib/ClientsPanel.svelte'
  import { listPending, type PendingSubmission } from '../lib/api'

  let { onOpenAgent, onNewAgent }: { onOpenAgent: (id: string) => void; onNewAgent: () => void } = $props()

  const { state: fleet, actions } = createFleetStore()

  let pending = $state<PendingSubmission[]>([])

  async function refreshPending() {
    try {
      pending = await listPending()
    } catch {
      pending = []
    }
  }

  onMount(() => {
    const ctl = new AbortController()
    actions.refresh()
    refreshPending()
    const disconnect = connectFleetSSE({
      onDelta: (d) => {
        actions.applyDelta(d)
        // A pending delta carries only the kind; refetch to pick up the
        // payload the attention list needs to render a decision.
        if (d.kind === 'pending') actions.refresh()
      },
      // We lagged or reconnected past the ring: the snapshot is the
      // authority, so refetch rather than trying to patch the gap.
      onOverflow: () => actions.refresh(),
      signal: ctl.signal,
    })
    return () => {
      ctl.abort()
      disconnect()
    }
  })

  // Group by project so a multi-project fleet stays legible, with the
  // groups themselves ordered by their most-urgent agent.
  const grouped = $derived.by(() => {
    const byProject = new Map<string, AgentRow[]>()
    for (const a of sortAttentionFirst($fleet.agents)) {
      const list = byProject.get(a.project)
      if (list) list.push(a)
      else byProject.set(a.project, [a])
    }
    return [...byProject.entries()]
  })

  const unavailable = $derived($fleet.projects.filter((p) => !p.available))
  const untrusted = $derived($fleet.projects.filter((p) => p.available && p.trust === 'untrusted'))

  const collisions = $derived(
    Object.entries(
      $fleet.agents
        .filter((a) => !a.isolated && a.status !== 'idle')
        .reduce<Record<string, number>>((acc, a) => ({ ...acc, [a.project]: (acc[a.project] ?? 0) + 1 }), {}),
    ).filter(([, n]) => n > 1),
  )
</script>

<div class="mx-auto flex max-w-6xl flex-col gap-4 p-4">
  <header class="flex items-center justify-between gap-3">
    <h1 class="text-lg font-semibold">Fleet</h1>
    <div class="flex gap-2">
      <Button variant="ghost" onclick={() => actions.refresh()}>Refresh</Button>
      <Button onclick={onNewAgent}>New agent</Button>
    </div>
  </header>

  {#if $fleet.error}
    <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm">{$fleet.error}</div>
  {/if}

  {#each unavailable as p (p.root)}
    <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm">
      <div class="font-medium">{p.root} is unavailable</div>
      <div class="mt-1 break-words text-muted">{p.error}</div>
    </div>
  {/each}

  {#each untrusted as p (p.root)}
    <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">
      <div class="font-medium">{p.root} is not trusted</div>
      <div class="mt-1 text-muted">
        Its <code>.marshal/config.toml</code> and project-scope plugins are being ignored. Open the project in the
        marshal TUI once to trust it — this cannot be granted from the browser.
      </div>
    </div>
  {/each}

  {#each $fleet.projects.filter((p) => (p.orphanWorktrees ?? []).length > 0) as p (p.root)}
    <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">
      <div class="font-medium">
        {p.orphanWorktrees!.length} unclaimed worktree{p.orphanWorktrees!.length === 1 ? '' : 's'} in {p.root}
      </div>
      <div class="mt-1 text-muted">
        No live agent owns {p.orphanWorktrees!.length === 1 ? 'it' : 'them'}. They were left behind by an earlier
        run and may hold uncommitted work, so nothing was deleted. Remove them by hand when you are sure:
        <code class="break-all">{p.orphanWorktrees!.join(', ')}</code>
      </div>
    </div>
  {/each}

  {#each collisions as [project, n] (project)}
    <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">
      <div class="font-medium">{n} non-isolated agents are active in {project}</div>
      <div class="mt-1 text-muted">
        They share the same checkout and can overwrite each other's edits. Spawn agents with isolation to give
        each its own worktree.
      </div>
    </div>
  {/each}

  <AttentionList agents={$fleet.agents} onOpen={onOpenAgent} onResolved={() => actions.refresh()} />

  <PendingList {pending} onResolved={refreshPending} />

  {#if $fleet.agents.length === 0 && !$fleet.loading}
    <p class="text-sm text-muted">No agents yet. Create one to get started.</p>
  {/if}

  {#each grouped as [project, agents] (project)}
    <section class="flex flex-col gap-2">
      <h2 class="truncate text-xs tracking-wide text-muted uppercase">{project}</h2>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {#each agents as a (a.id)}
          <AgentCard agent={a} onOpen={onOpenAgent} />
        {/each}
      </div>
    </section>
  {/each}

  <ClientsPanel />
</div>
