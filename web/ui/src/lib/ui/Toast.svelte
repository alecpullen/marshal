<script lang="ts">
  /** A transient status line; clears itself after `ms` and whenever `text` is set to ''. */
  let { text = $bindable(''), ms = 6000 }: { text?: string; ms?: number } = $props()

  $effect(() => {
    if (!text) return
    const t = setTimeout(() => (text = ''), ms)
    return () => clearTimeout(t)
  })
</script>

{#if text}
  <div class="fixed right-4 bottom-4 z-50 max-w-sm rounded-md border border-border bg-raise p-3 text-sm shadow-lg" role="status">{text}</div>
{/if}
