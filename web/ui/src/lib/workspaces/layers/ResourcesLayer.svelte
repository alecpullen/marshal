<script lang="ts">
  import LayerCard from '../LayerCard.svelte'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch }: LayerProps = $props()

  // svelte-ignore state_referenced_locally
  let r = $state({ ...doc.resources })
  $effect(() => {
    r = { ...doc.resources }
  })
  const commit = () => onPatch(8, { ...r, cpu: Number(r.cpu) || 0 })
  const input = 'rounded border border-border bg-bg px-2 py-1 font-mono text-xs text-fg'
</script>

<LayerCard layer={8} title="Resources" {selected} {diags} {onSelect}>
  <div class="grid grid-cols-2 gap-2 text-xs text-muted">
    <label class="flex flex-col gap-1">CPU<input class={input} type="number" min="0" step="0.5" bind:value={r.cpu} onchange={commit} /></label>
    <label class="flex flex-col gap-1">Memory<input class={input} placeholder="8g" bind:value={r.memory} onchange={commit} /></label>
    <label class="flex flex-col gap-1">Disk<input class={input} placeholder="20g" bind:value={r.disk} onchange={commit} /></label>
    <label class="flex flex-col gap-1">Timeout<input class={input} placeholder="2h" bind:value={r.timeout} onchange={commit} /></label>
  </div>
</LayerCard>
