<script lang="ts">
  import type { Readable } from 'svelte/store'
  import type { StackState } from '../stack'
  import type { Density } from './density'
  import TurnNode from './TurnNode.svelte'
  import type { TranscriptCtx } from './ctx'
  import { restrict } from './restrict'

  let {
    store,
    density,
    overrides = new Map<string, Density>(),
    cursor = null,
    onlyNodes = undefined,
    onToggleDensity,
  }: {
    store: Readable<StackState>
    density: Density
    overrides?: ReadonlyMap<string, Density>
    cursor?: string | null
    /** Show only these nodes with their ancestors and descendants (the Run page's stage filter). */
    onlyNodes?: ReadonlySet<string>
    onToggleDensity?: (id: string) => void
  } = $props()

  let now = $state(Date.now())
  const view = $derived(onlyNodes ? restrict($store, onlyNodes) : $store)
  const anyLive = $derived([...view.nodes.values()].some((n) => n.live))

  // Live steps and tool rows show elapsed time, so tick while anything runs.
  $effect(() => {
    if (!anyLive) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  const ctx = $derived<TranscriptCtx>({
    nodes: view.nodes,
    global: density,
    overrides,
    cursor,
    now,
    onToggleDensity,
  })
</script>

<div class="flex flex-col" data-testid="transcript">
  {#each view.roots as id (id)}
    <TurnNode {id} {ctx} />
  {/each}
</div>
