<script lang="ts">
  import { groupAgents, type AgentRow, type AgentGroupKey } from './fleet'
  import type { ProjectStatus } from './api'
  import { shortName } from './utils'
  import { isScopedSessions } from './routes'

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

  const groups = $derived(groupAgents(agents))
  const needsCount = $derived(groups.needsYou.length)

  const sections: { key: AgentGroupKey; label: string; tone: string }[] = [
    { key: 'needsYou', label: 'Needs you', tone: 'text-attention' },
    { key: 'running', label: 'Running', tone: 'text-running' },
    { key: 'ready', label: 'Ready to ship', tone: 'text-accent' },
    { key: 'earlier', label: 'Earlier', tone: 'text-muted' },
  ]

  const dot: Record<AgentRow['status'], string> = {
    'awaiting-approval': 'bg-attention',
    'awaiting-question': 'bg-attention',
    error: 'bg-danger',
    running: 'bg-running',
    idle: 'bg-muted/50',
  }

  // Earlier is long-lived; the others are short and stay open.
  let collapsed = $state<Record<string, boolean>>({ earlier: true })
  const toggle = (k: string) => (collapsed = { ...collapsed, [k]: !collapsed[k] })

  /*
    The Sessions item stays highlighted on its scoped route
    (#sessions/<root>) as well as on the picker, so the nav reflects the
    panel the user is actually looking at.
  */
  const navActive = (navHash: string) =>
    route === navHash || (navHash === '#sessions' && isScopedSessions(route))
</script>

{#if !open}
  <nav aria-label="Agents" class="flex h-full w-12 shrink-0 flex-col items-center gap-3 border-r border-border bg-surface py-3">
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
    {#if needsCount > 0}
      <button
        class="relative cursor-pointer rounded-md px-2 py-1 hover:bg-bg"
        onclick={() => onNavigate(`#chat/${groups.needsYou[0].id}`)}
        title="{needsCount} agent(s) need you"
        aria-label="{needsCount} agents need you"
      >
        <span class="block size-2 rounded-full bg-attention"></span>
      </button>
    {/if}
  </nav>
{:else}
<nav aria-label="Agents" class="flex h-full w-64 shrink-0 flex-col overflow-y-auto border-r border-border bg-surface">
  <div class="flex items-center justify-between gap-2 px-3 py-3">
    <button class="cursor-pointer text-sm font-semibold tracking-wide" onclick={() => onNavigate('#')}>
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

  <div class="flex-1 px-2">
    {#each sections as sec (sec.key)}
      {@const list = groups[sec.key]}
      {#if list.length > 0}
        <div class="mb-2">
          <button
            class="flex w-full cursor-pointer items-center gap-1 rounded-md px-2 py-1 text-left text-[0.6875rem] tracking-wide uppercase hover:bg-bg {sec.tone}"
            onclick={() => toggle(sec.key)}
            aria-expanded={!collapsed[sec.key]}
          >
            <span class="w-3 shrink-0">{collapsed[sec.key] ? '▸' : '▾'}</span>
            <span>{sec.label}</span>
            <span class="ml-auto shrink-0 tabular-nums">{list.length}</span>
          </button>
          {#if !collapsed[sec.key]}
            {#each list as a (a.id)}
              <button
                class="flex w-full cursor-pointer items-center gap-2 rounded-md py-1.5 pr-2 pl-5 text-left text-sm hover:bg-bg
                       {activeAgentId === a.id ? 'bg-bg font-medium' : ''}"
                onclick={() => onNavigate(`#chat/${a.id}`)}
                title={a.activity || a.status}
              >
                <span class="size-1.5 shrink-0 rounded-full {dot[a.status]}"></span>
                <span class="truncate">{a.name || a.id}</span>
                <span class="ml-auto shrink-0 truncate text-[0.625rem] text-muted">{shortName(a.project)}</span>
              </button>
            {/each}
          {/if}
        </div>
      {/if}
    {:else}
      <div class="px-3 py-2 text-xs text-muted">No agents yet</div>
    {/each}
  </div>

  <div class="border-t border-border p-2">
    {#each [['#pending', 'Pending', pendingCount], ['#clients', 'Clients', clientCount], ['#projects', 'Projects', projects.length], ['#sessions', 'Sessions', 0], ['#disk', 'Disk', 0], ['#activity', 'Activity', 0]] as [hash, label, count] (hash)}
      <button
        class="flex w-full cursor-pointer items-center rounded-md px-2 py-1.5 text-left text-sm hover:bg-bg
               {navActive(hash as string) ? 'bg-bg font-medium' : ''}"
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
