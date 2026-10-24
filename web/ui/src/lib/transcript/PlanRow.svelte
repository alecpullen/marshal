<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WirePlan } from '../stack'

  let { plan }: { plan: WirePlan } = $props()

  // A list longer than this draws the plan blocks instead of one mark per item.
  const MAX_MARKS = 12
  const BLOCKS = 8

  const marks = $derived(plan.status ?? [])
  const filled = $derived(plan.total > 0 ? Math.min(BLOCKS, Math.max(0, Math.round((plan.done / plan.total) * BLOCKS))) : 0)
  const pos = $derived(`${plan.active || plan.done}/${plan.total}`)
  const text = $derived(plan.activeText || (plan.done === plan.total ? 'done' : ''))
</script>

{#if plan.cleared}
  <div class="py-px text-xs text-muted" data-testid="plan-row">plan cleared</div>
{:else}
  <div class="flex items-baseline gap-2 py-px text-xs" data-testid="plan-row">
    <span class="shrink-0 text-muted">plan</span>
    {#if plan.total <= MAX_MARKS}
      <span class="shrink-0 font-mono">
        {#each marks as st, i (i)}<span class={st === 'completed' ? 'text-ok' : st === 'in_progress' ? 'text-accent' : 'text-muted'}>{st === 'completed' ? glyph.OK : st === 'in_progress' ? glyph.Running : glyph.Ambient}</span>{/each}
      </span>
    {:else}
      <span class="shrink-0 font-mono">
        <span class="text-accent">{glyph.ProgressFull.repeat(filled)}</span><span class="text-muted">{glyph.ProgressEmpty.repeat(BLOCKS - filled)}</span>
      </span>
    {/if}
    <span class="shrink-0 font-mono text-muted">{pos}</span>
    {#if text}<span class="truncate text-muted">{text}</span>{/if}
  </div>
{/if}
