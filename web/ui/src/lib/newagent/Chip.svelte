<script lang="ts">
  let { label, value, children }: { label: string; value: string; children: import('svelte').Snippet } = $props()

  let open = $state(false)
  let root = $state<HTMLElement | null>(null)

  $effect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (root && !root.contains(e.target as Node)) open = false
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        open = false
      }
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  })
</script>

<div class="relative" bind:this={root}>
  <button
    type="button"
    aria-haspopup="true"
    aria-expanded={open}
    class="min-h-9 max-w-64 truncate rounded-full border border-border bg-surface px-3 py-1 text-xs hover:bg-hover"
    onclick={() => (open = !open)}
  >
    <span class="text-muted">{label}:</span> {value}
  </button>
  {#if open}
    <div class="absolute bottom-full z-20 mb-1 flex min-w-56 flex-col gap-1 rounded-md border border-border bg-raise p-2 text-sm shadow-lg" role="dialog" aria-label={label}>
      {@render children()}
    </div>
  {/if}
</div>
