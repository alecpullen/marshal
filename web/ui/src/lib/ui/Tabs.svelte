<script lang="ts">
  import { cn } from '../utils'

  let {
    tabs,
    value,
    onchange,
    label = '',
  }: { tabs: { value: string; label: string }[]; value: string; onchange: (value: string) => void; label?: string } = $props()
</script>

<div class="flex flex-wrap gap-1 border-b border-border text-sm" role="tablist" aria-label={label}>
  {#each tabs as t (t.value)}
    <button
      type="button"
      role="tab"
      aria-selected={t.value === value}
      class={cn('px-3 py-1.5', t.value === value ? 'border-b-2 border-accent text-fg' : 'text-muted hover:text-fg')}
      onclick={() => onchange(t.value)}
    >
      {t.label}
    </button>
  {/each}
</div>
