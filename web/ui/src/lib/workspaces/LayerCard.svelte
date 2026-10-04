<script lang="ts">
  import type { WSDiag } from '../api'

  let {
    layer,
    title,
    selected,
    diags,
    onSelect,
    children,
  }: { layer: number; title: string; selected: boolean; diags: WSDiag[]; onSelect: () => void; children: import('svelte').Snippet } = $props()
</script>

<section
  class="flex flex-col gap-2 rounded-lg border p-3 {selected ? 'border-accent bg-raise' : 'border-border bg-surface'}"
  data-testid="layer-card"
  data-layer={layer}
  data-selected={selected ? '' : undefined}
  aria-label="Layer {layer}: {title}"
>
  <button type="button" class="flex cursor-pointer items-center gap-2 text-left text-xs font-medium text-muted" onclick={onSelect}>
    <span class="font-mono">{layer}</span>
    <span class="text-fg">{title}</span>
  </button>
  {@render children()}
  {#each diags as d, i (i)}
    <p class="text-xs {d.severity === 'error' ? 'text-err' : 'text-warn'}" data-testid="card-diag">Line {d.line}: {d.message}</p>
  {/each}
</section>
