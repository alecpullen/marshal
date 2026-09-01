<script lang="ts">
  import type { AgentRow } from './fleet'
  import type { ProjectStatus } from './api'
  import { sortAttentionFirst } from './fleet'

  interface Props {
    open: boolean
    onToggle: () => void
    agents: AgentRow[]
    projects: ProjectStatus[]
    pendingCount: number
    clientCount: number
    route: string
    activeAgentId: string | null
    onNavigate: (hash: string) => void
  }

  let { open, onToggle, agents, projects, pendingCount, clientCount, route, activeAgentId, onNavigate }: Props = $props()

  const needsHuman = (a: AgentRow) => a.status === 'awaiting-approval' || a.status === 'awaiting-question'
  const attention = $derived(agents.filter(needsHuman))

  const dot: Record<AgentRow['status'], string> = {
    'awaiting-approval': 'bg-attention',
    'awaiting-question': 'bg-attention',
    error: 'bg-danger',
    running: 'bg-running',
    idle: 'bg-muted/50',
  }

  function shortProject(root: string): string {
    return root.split('/').filter(Boolean).pop() ?? root
  }

  /*
    Projects come from /api/projects, agents from /api/agents. Grouping by
    the project list rather than by the agents' own project field keeps a
    registered project visible while it has no agents — otherwise an empty
    project silently disappears from the index.
  */
  const groups = $derived(
    projects.map((p) => ({
      root: p.root,
      label: shortProject(p.root),
      unavailable: !p.available,
      agents: sortAttentionFirst(agents.filter((a) => a.project === p.root)),
    })),
  )

  // An agent whose project is not registered still has to appear.
  const orphaned = $derived(
    sortAttentionFirst(agents.filter((a) => !projects.some((p) => p.root === a.project))),
  )

  let collapsed = $state<Record<string, boolean>>({})
  const toggle = (root: string) => (collapsed = { ...collapsed, [root]: !collapsed[root] })
</script>

{#if !open}
  <nav class="flex h-full w-12 shrink-0 flex-col items-center gap-3 border-r border-border bg-surface py-3">
    <button
      class="cursor-pointer rounded-md px-2 py-1 text-sm hover:bg-bg"
      onclick={onToggle}
      title="Show sidebar"
      aria-label="Show sidebar"
    >
      ☰
    </button>
    <button
      class="cursor-pointer rounded-md px-2 py-1 text-sm hover:bg-bg"
      onclick={() => onNavigate('#new')}
      title="New agent"
      aria-label="New agent"
    >
      +
    </button>
    {#if attention.length > 0}
      <button
        class="relative cursor-pointer rounded-md px-2 py-1 hover:bg-bg"
        onclick={() => onNavigate(`#chat/${attention[0].id}`)}
        title="{attention.length} agent(s) need you"
        aria-label="{attention.length} agents need you"
      >
        <span class="block size-2 rounded-full bg-attention"></span>
      </button>
    {/if}
  </nav>
{:else}
<nav class="flex h-full w-64 shrink-0 flex-col overflow-y-auto border-r border-border bg-surface">
  <div class="flex items-center justify-between gap-2 px-3 py-3">
    <button
      class="cursor-pointer text-sm font-semibold tracking-wide"
      onclick={() => onNavigate('#')}
    >
      Marshal
    </button>
    <div class="flex items-center gap-1">
      <button
        class="cursor-pointer rounded-md border border-border px-2 py-1 text-xs hover:bg-bg"
        onclick={() => onNavigate('#new')}
      >
        + New
      </button>
      <button
        class="cursor-pointer rounded-md px-1.5 py-1 text-xs text-muted hover:bg-bg"
        onclick={onToggle}
        title="Hide sidebar"
        aria-label="Hide sidebar"
      >
        ⟨
      </button>
    </div>
  </div>

  {#if attention.length > 0}
    <div class="px-2 pb-2">
      <div class="mb-1 px-1 text-[0.6875rem] tracking-wide text-attention uppercase">
        Needs you · {attention.length}
      </div>
      {#each attention as a (a.id)}
        <button
          class="flex w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-bg
                 {activeAgentId === a.id ? 'bg-bg' : ''}"
          onclick={() => onNavigate(`#chat/${a.id}`)}
        >
          <span class="size-1.5 shrink-0 rounded-full bg-attention"></span>
          <span class="truncate">{a.name || a.id}</span>
        </button>
      {/each}
    </div>
  {/if}

  <div class="flex-1 px-2">
    {#each groups as g (g.root)}
      <div class="mb-1">
        <button
          class="flex w-full cursor-pointer items-center gap-1 rounded-md px-2 py-1 text-left text-[0.6875rem] tracking-wide text-muted uppercase hover:bg-bg"
          onclick={() => toggle(g.root)}
          title={g.root}
        >
          <span class="w-3 shrink-0">{collapsed[g.root] ? '▸' : '▾'}</span>
          <span class="truncate">{g.label}</span>
          {#if g.unavailable}<span class="text-danger">!</span>{/if}
          <span class="ml-auto tabular-nums">{g.agents.length}</span>
        </button>

        {#if !collapsed[g.root]}
          {#each g.agents as a (a.id)}
            <button
              class="flex w-full cursor-pointer items-center gap-2 rounded-md py-1.5 pr-2 pl-5 text-left text-sm hover:bg-bg
                     {activeAgentId === a.id ? 'bg-bg font-medium' : ''}"
              onclick={() => onNavigate(`#chat/${a.id}`)}
              title={a.activity || a.status}
            >
              <span class="size-1.5 shrink-0 rounded-full {dot[a.status]}"></span>
              <span class="truncate">{a.name || a.id}</span>
              {#if a.isolated}<span class="ml-auto shrink-0 text-[0.625rem] text-muted">iso</span>{/if}
            </button>
          {:else}
            <div class="px-5 py-1 text-xs text-muted">No agents</div>
          {/each}
        {/if}
      </div>
    {/each}

    {#if orphaned.length > 0}
      <div class="mb-1">
        <div class="px-2 py-1 text-[0.6875rem] tracking-wide text-muted uppercase">Other</div>
        {#each orphaned as a (a.id)}
          <button
            class="flex w-full cursor-pointer items-center gap-2 rounded-md py-1.5 pr-2 pl-5 text-left text-sm hover:bg-bg
                   {activeAgentId === a.id ? 'bg-bg font-medium' : ''}"
            onclick={() => onNavigate(`#chat/${a.id}`)}
          >
            <span class="size-1.5 shrink-0 rounded-full {dot[a.status]}"></span>
            <span class="truncate">{a.name || a.id}</span>
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <div class="border-t border-border p-2">
    {#each [['#pending', 'Pending', pendingCount], ['#clients', 'Clients', clientCount], ['#activity', 'Activity', 0]] as [hash, label, count] (hash)}
      <button
        class="flex w-full cursor-pointer items-center rounded-md px-2 py-1.5 text-left text-sm hover:bg-bg
               {route === hash ? 'bg-bg font-medium' : ''}"
        onclick={() => onNavigate(hash as string)}
      >
        <span>{label}</span>
        {#if (count as number) > 0}
          <span class="ml-auto rounded-full bg-border px-1.5 text-xs tabular-nums">{count}</span>
        {/if}
      </button>
    {/each}
  </div>
</nav>
{/if}
