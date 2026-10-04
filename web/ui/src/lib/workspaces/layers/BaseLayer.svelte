<script lang="ts">
  import LayerCard from '../LayerCard.svelte'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch }: LayerProps = $props()

  // svelte-ignore state_referenced_locally
  let base = $state(doc.workspace.base)
  $effect(() => {
    base = doc.workspace.base
  })
  const commit = () => base !== doc.workspace.base && onPatch(1, { ...doc.workspace, base })
</script>

<LayerCard layer={1} title="Base" {selected} {diags} {onSelect}>
  <label class="flex flex-col gap-1 text-xs text-muted">
    Image
    <input
      class="rounded border border-border bg-bg px-2 py-1 font-mono text-xs text-fg"
      placeholder="debian:12"
      bind:value={base}
      onchange={commit}
      onkeydown={(e) => e.key === 'Enter' && commit()}
    />
  </label>
</LayerCard>
