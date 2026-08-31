<script lang="ts">
  import { cn } from '../utils'

  let {
    variant = 'default',
    class: klass = '',
    children,
    ...rest
  }: {
    variant?: 'default' | 'ghost' | 'danger'
    class?: string
    children?: import('svelte').Snippet
    [key: string]: unknown
  } = $props()

  const variants = {
    default: 'bg-accent text-bg hover:opacity-90',
    ghost: 'bg-transparent text-fg border border-border hover:bg-surface',
    danger: 'bg-danger text-bg hover:opacity-90',
  }
</script>

<!-- min-h-11 keeps every control at a 44px touch target on mobile. -->
<button
  class={cn(
    // A dimmed accent reads as broken rather than disabled, so disabled
    // gets its own flat treatment instead of an opacity knock-down.
    'inline-flex min-h-11 items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition',
    'disabled:pointer-events-none disabled:bg-border disabled:text-muted disabled:opacity-100 disabled:border-transparent',
    variants[variant],
    klass,
  )}
  {...rest}
>
  {@render children?.()}
</button>
