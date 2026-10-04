<script lang="ts">
  import { cn } from '../utils'
  import { roleTone, type Tone } from '../glyphs'

  let {
    tone,
    role,
    class: klass = '',
    children,
  }: { tone?: Tone; role?: string; class?: string; children?: import('svelte').Snippet } = $props()

  const tones: Record<Tone, string> = {
    ok: 'bg-ok/15 text-ok',
    err: 'bg-err/15 text-err',
    warn: 'bg-warn/15 text-warn',
    info: 'bg-info/15 text-info',
    accent: 'bg-accent/15 text-accent',
    violet: 'bg-violet/15 text-violet',
    gold: 'bg-gold/15 text-gold',
    neutral: 'bg-line text-muted',
  }
  const resolved = $derived(tone ?? roleTone(role))
</script>

<span class={cn('inline-flex shrink-0 items-center rounded px-1.5 py-px font-mono text-[11px] whitespace-nowrap', tones[resolved], klass)}>
  {#if children}{@render children()}{:else}{role}{/if}
</span>
