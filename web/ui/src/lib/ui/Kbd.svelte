<script lang="ts">
  import { cn } from '../utils'

  let { class: klass = '', children }: { class?: string; children?: import('svelte').Snippet } = $props()
</script>

<kbd class={cn('inline-flex min-w-5 items-center justify-center rounded border border-border bg-raise px-1 font-mono text-[11px] text-sub', klass)}>
  {@render children?.()}
</kbd>
