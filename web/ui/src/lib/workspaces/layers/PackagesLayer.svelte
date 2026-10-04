<script lang="ts">
  import ChipList from '../ChipList.svelte'
  import LayerCard from '../LayerCard.svelte'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch }: LayerProps = $props()

  const KINDS = ['apt', 'go', 'npm', 'pip'] as const
</script>

<LayerCard layer={3} title="Packages" {selected} {diags} {onSelect}>
  {#each KINDS as k (k)}
    <div class="flex items-start gap-2">
      <span class="w-8 pt-0.5 font-mono text-xs text-muted">{k}</span>
      <div class="min-w-0 flex-1">
        <ChipList items={doc.packages[k]} label="{k} packages" onChange={(items) => onPatch(3, { ...doc.packages, [k]: items })} />
      </div>
    </div>
  {/each}
</LayerCard>
