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
    foldTasks = true,
    unfolded = new Set<string>(),
    cursor = null,
    onlyNodes = undefined,
    onToggleFold,
    onToggleDensity,
  }: {
    store: Readable<StackState>
    density: Density
    overrides?: ReadonlyMap<string, Density>
    foldTasks?: boolean
    unfolded?: ReadonlySet<string>
    cursor?: string | null
    /** Show only these nodes with their ancestors and descendants (the Run page's stage filter). */
    onlyNodes?: ReadonlySet<string>
    onToggleFold?: (id: string) => void
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
    foldTasks,
    unfolded,
    cursor,
    now,
    onToggleFold,
    onToggleDensity,
  })
</script>

<div class="flex flex-col" data-testid="transcript">
  {#each view.roots as id (id)}
    <TurnNode {id} {ctx} />
  {/each}
</div>
