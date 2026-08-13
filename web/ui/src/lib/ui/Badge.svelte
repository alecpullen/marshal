<script lang="ts">
  import { cn } from '../utils'

  let {
    tone = 'muted',
    class: klass = '',
    children,
  }: {
    tone?: 'muted' | 'running' | 'attention' | 'danger'
    class?: string
    children?: import('svelte').Snippet
  } = $props()

  const tones = {
    muted: 'bg-border text-muted',
    running: 'bg-running/20 text-running',
    attention: 'bg-attention/20 text-attention',
    danger: 'bg-danger/20 text-danger',
  }
</script>

<span class={cn('inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium', tones[tone], klass)}>
  {@render children?.()}
</span>
