<script lang="ts">
  import { onMount } from 'svelte'
  import { createFleetStore, sortAttentionFirst } from '../lib/fleet'
  import { connectFleetSSE } from '../lib/sse'
  let { onOpenAgent, onNewAgent }: { onOpenAgent: (id: string) => void; onNewAgent: () => void } = $props()
  const { state: fleet, actions } = createFleetStore()
  onMount(() => { const ctl = new AbortController(); actions.refresh(); const disconnect = connectFleetSSE({ onDelta: actions.applyDelta, onOverflow: actions.refresh, signal: ctl.signal }); return () => { ctl.abort(); disconnect() } })
</script>
<div class="mx-auto flex max-w-6xl flex-col gap-4 p-4">
  <header class="flex items-center justify-between"><h1 class="text-lg font-semibold">Fleet</h1><button class="rounded border px-3 py-2" onclick={onNewAgent}>New agent</button></header>
  {#if $fleet.error}<div class="rounded border border-danger p-3">{$fleet.error}</div>{/if}
  {#each $fleet.projects.filter(p => !p.available) as p (p.root)}<div class="rounded border border-danger p-3">{p.root} unavailable: {p.error}</div>{/each}
  {#each $fleet.projects.filter(p => p.available && p.trust === 'untrusted') as p (p.root)}<div class="rounded border border-attention p-3 text-sm"><strong>{p.root} is not trusted</strong><div class="text-muted">Project config and plugins are ignored until trusted in the marshal TUI.</div></div>{/each}
  {#each sortAttentionFirst($fleet.agents) as a (a.id)}<button class="block w-full rounded border border-border bg-surface p-4 text-left" onclick={() => onOpenAgent(a.id)}><div class="flex justify-between"><span>{a.name || a.id}</span><span>{a.status}</span></div><div class="text-sm text-muted">{a.project} {a.activity}</div></button>{/each}
  {#if $fleet.agents.length === 0}<p class="text-muted">No agents yet.</p>{/if}
</div>