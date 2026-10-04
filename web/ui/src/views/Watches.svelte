<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import Badge from '../lib/ui/Badge.svelte'
  import Sparkline from '../lib/watches/Sparkline.svelte'
  import WatchDetail from '../lib/watches/WatchDetail.svelte'
  import WatchForm from '../lib/watches/WatchForm.svelte'
  import { listWatches, errMessage, type WatchInfo } from '../lib/api'
  import type { AgentRow } from '../lib/fleet'

  let {
    agents,
    tick = 0,
    onNavigate,
  }: { agents: AgentRow[]; /** Moves on every `watch` fleet delta. */ tick?: number; onNavigate: (hash: string) => void } = $props()

  let watches = $state<WatchInfo[]>([])
  let error = $state('')
  let loaded = $state(false)
  let selected = $state<string | null>(null)
  let creating = $state(false)

  const key = (w: WatchInfo) => `${w.agentId}/${w.id}`

  async function load() {
    try {
      watches = await listWatches()
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  // Loads on mount and again on each watch delta.
  $effect(() => {
    void tick
    untrack(() => void load())
  })
  onMount(() => {
    // Samples accrue between deltas, so keep the sparklines fresh.
    const t = setInterval(() => void load(), 30_000)
    return () => clearInterval(t)
  })

  const ownerName = (id: string) => (id === 'studio' ? 'Studio' : agents.find((a) => a.id === id)?.name || id)
  const detail = $derived(watches.find((w) => key(w) === selected))
  const trip = (w: WatchInfo) => (w.onTrip?.reroute ? `reroute ${w.onTrip.reroute.role} → ${w.onTrip.reroute.preset}` : w.resume ? 'resume' : 'notify')
  const source = (w: WatchInfo) => w.kind
  const tone = (s: string) => (s === 'fired' ? 'warn' : s === 'error' ? 'err' : s === 'stopped' ? 'neutral' : 'ok')
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between">
    <h1 class="text-lg font-semibold">Watches</h1>
    <Button onclick={() => (creating = true)} disabled={creating}>New watch</Button>
  </header>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if creating}
    <WatchForm
      {agents}
      onCancel={() => (creating = false)}
      onCreated={() => {
        creating = false
        void load()
      }}
    />
  {/if}

  <div class="overflow-x-auto">
    <table class="w-full text-left text-sm">
      <thead class="text-xs text-muted">
        <tr><th class="py-1 pr-3">Name</th><th class="pr-3">Owner</th><th class="pr-3">Source</th><th class="pr-3">Condition</th><th class="pr-3">24h</th><th class="pr-3">On trip</th><th>State</th></tr>
      </thead>
      <tbody>
        {#each watches as w (key(w))}
          <tr
            class="cursor-pointer border-t border-border hover:bg-hover {selected === key(w) ? 'bg-raise' : ''}"
            data-testid="watch-row"
            tabindex="0"
            aria-selected={selected === key(w)}
            onclick={() => (selected = key(w))}
            onkeydown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                selected = key(w)
              }
            }}
          >
            <td class="py-2 pr-3 font-medium">{w.name}</td>
            <td class="pr-3">{#if w.agentId === 'studio'}<Badge tone="running">Studio</Badge>{:else}{ownerName(w.agentId)}{/if}</td>
            <td class="pr-3 font-mono text-xs">{source(w)}</td>
            <td class="max-w-48 truncate pr-3 font-mono text-xs">{w.condition || 'change'}</td>
            <td class="pr-3"><Sparkline samples={w.samples} /></td>
            <td class="pr-3 text-xs">{trip(w)}</td>
            <td><Tag tone={tone(w.state)}>{w.state}</Tag></td>
          </tr>
        {:else}
          <tr><td colspan="7" class="py-3 text-sm text-muted">{loaded ? 'No watches yet. Start one with New watch.' : 'Loading…'}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#if detail}
    {#key selected}
      <WatchDetail
        watch={detail}
        ownerName={ownerName(detail.agentId)}
        onClose={() => (selected = null)}
        onStopped={() => {
          selected = null
          void load()
        }}
        onOpenAgent={(id) => onNavigate(`#chat/${id}`)}
      />
    {/key}
  {/if}
</div>
