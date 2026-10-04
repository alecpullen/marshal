<script lang="ts">
  import LayerCard from '../LayerCard.svelte'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch }: LayerProps = $props()

  // svelte-ignore state_referenced_locally
  let run = $state(doc.setup.run)
  $effect(() => {
    run = doc.setup.run
  })
  const commit = () => run !== doc.setup.run && onPatch(9, { run })
</script>

<LayerCard layer={9} title="Setup" {selected} {diags} {onSelect}>
  <textarea
    class="h-20 rounded border border-border bg-bg p-2 font-mono text-xs"
    placeholder="Runs once before the agent starts, in a one-shot container."
    aria-label="Setup script"
    bind:value={run}
    onblur={commit}
  ></textarea>
</LayerCard>
