<script lang="ts">
  import Card from './ui/Card.svelte'
  import Badge from './ui/Badge.svelte'
  import { describePending, type AgentRow } from './fleet'

  let { agent, onOpen }: { agent: AgentRow; onOpen: (id: string) => void } = $props()

  const needsHuman = $derived(agent.status === 'awaiting-approval' || agent.status === 'awaiting-question')

  const tone = $derived(
    needsHuman ? 'attention' : agent.status === 'running' ? 'running' : agent.status === 'error' ? 'danger' : 'muted',
  )

  const label = $derived(
    {
      idle: 'idle',
      running: 'running',
      'awaiting-approval': 'needs approval',
      'awaiting-question': 'needs an answer',
      error: 'error',
    }[agent.status],
  )

  const projectName = $derived(agent.project.split('/').filter(Boolean).pop() ?? agent.project)

  // What the agent is waiting on beats the last tool it ran: if it is
  // parked, that is the only thing the reader cares about.
  const detail = $derived(agent.pending ? describePending(agent.pending) : agent.activity)
</script>

<Card class="p-0 {needsHuman ? 'border-attention' : ''}">
  <button
    class="block w-full cursor-pointer p-4 text-left transition hover:bg-bg/40"
    onclick={() => onOpen(agent.id)}
  >
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0">
        <div class="truncate font-medium">{agent.name || agent.id}</div>
        <div class="truncate text-xs text-muted">{projectName}</div>
      </div>
      <Badge {tone}>{label}</Badge>
    </div>

    {#if detail}
      <div class="mt-3 truncate text-sm text-muted">{detail}</div>
    {/if}

    <div class="mt-3 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
      {#if agent.mode}<span>mode {agent.mode}</span>{/if}
      {#if agent.isolated && agent.branch}<span class="font-mono">{agent.branch}</span>{/if}
      {#if agent.contextPct > 0}<span>ctx {agent.contextPct}%</span>{/if}
      {#if agent.changedFiles > 0}<span>{agent.changedFiles} changed</span>{/if}
      {#if agent.interrupted}<span class="text-attention">interrupted by restart</span>{/if}
    </div>
  </button>
</Card>
