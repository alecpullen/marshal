<script lang="ts">
  import type { Readable } from 'svelte/store'
  import type { StackState } from '../stack'
  import type { Density } from './density'
  import TurnNode from './TurnNode.svelte'
  import type { TranscriptCtx } from './ctx'

  let {
    store,
    density,
    overrides = new Map<string, Density>(),
    foldTasks = true,
    unfolded = new Set<string>(),
    cursor = null,
    onToggleFold,
    onToggleDensity,
  }: {
    store: Readable<StackState>
    density: Density
    overrides?: ReadonlyMap<string, Density>
    foldTasks?: boolean
    unfolded?: ReadonlySet<string>
    cursor?: string | null
    onToggleFold?: (id: string) => void
    onToggleDensity?: (id: string) => void
  } = $props()

  let now = $state(Date.now())
  const anyLive = $derived([...$store.nodes.values()].some((n) => n.live))

  // Live steps and tool rows show elapsed time, so tick while anything runs.
  $effect(() => {
    if (!anyLive) return
    now = Date.now()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  const ctx = $derived<TranscriptCtx>({
    nodes: $store.nodes,
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
  {#each $store.roots as id (id)}
    <TurnNode {id} {ctx} />
  {/each}
</div>
