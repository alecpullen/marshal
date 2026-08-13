<script lang="ts">
  import { cn } from '../utils'

  let {
    class: klass = '',
    children,
    ...rest
  }: { class?: string; children?: import('svelte').Snippet; [key: string]: unknown } = $props()
</script>

<div class={cn('rounded-lg border border-border bg-surface p-4', klass)} {...rest}>
  {@render children?.()}
</div>
