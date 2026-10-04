<script lang="ts">
  import { cn } from '../utils'

  let {
    options,
    value,
    onchange,
    label = '',
  }: {
    options: { value: string; label: string }[]
    value: string
    onchange: (value: string) => void
    label?: string
  } = $props()
</script>

<div role="group" aria-label={label} class="inline-flex rounded-md border border-border bg-surface p-0.5">
  {#each options as o (o.value)}
    <button
      type="button"
      aria-pressed={o.value === value}
      class={cn(
        'rounded px-2.5 py-1 text-xs transition',
        o.value === value ? 'bg-raise text-fg' : 'text-muted hover:text-fg',
      )}
      onclick={() => onchange(o.value)}
    >
      {o.label}
    </button>
  {/each}
</div>
