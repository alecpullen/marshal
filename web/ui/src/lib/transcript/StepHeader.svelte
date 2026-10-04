<script lang="ts">
  import Tag from '../ui/Tag.svelte'
  import { glyph } from '../glyphs'
  import { renderMarkdown } from '../markdown'
  import type { WireStep } from '../stack'
  import { compactDuration } from './format'
  import type { Density } from './density'
  import ThinkingRow from './ThinkingRow.svelte'

  let { step, live, density, now }: { step: WireStep; live: boolean; density: Density; now: number } = $props()

  const duration = $derived.by(() => {
    if (!step.startedAt) return ''
    const end = step.endedAt || (live ? now : 0)
    return end > step.startedAt ? compactDuration(end - step.startedAt) : ''
  })
  const owner = $derived(step.owner || step.role || '')
  // A line of rest at "steps"; the whole of it, rendered, at "full".
  const restLine = $derived((step.rest ?? '').split('\n').find((l) => l.trim()) ?? '')
</script>

<div class="py-1">
  <div class="flex items-baseline gap-2 text-sm">
    <span class={live ? 'animate-pulse text-accent' : 'text-dim'}>{live ? glyph.Running : glyph.Ambient}</span>
    {#if owner}<Tag role={step.role}>{owner}</Tag>{/if}
    <span class="min-w-0 truncate italic">{step.headline}</span>
    {#if step.inferred}<Tag tone="neutral">inferred</Tag>{/if}
    {#if duration}<span class="ml-auto shrink-0 font-mono text-xs text-muted">{duration}</span>{/if}
  </div>
  {#if step.rest && density === 'full'}
    <!-- renderMarkdown sanitises with DOMPurify. -->
    <div class="mt-1 ml-5 text-sm text-sub break-words">{@html renderMarkdown(step.rest)}</div>
  {:else if restLine && density === 'steps'}
    <div class="ml-5 truncate text-xs text-muted">{restLine}</div>
  {/if}
  {#if density !== 'outline'}
    {#each step.thoughts ?? [] as t, i (i)}
      <div class="ml-5"><ThinkingRow thinking={t} open={density === 'full'} /></div>
    {/each}
    {#if step.liveThinking}
      <div class="ml-5"><ThinkingRow thinking={{ text: step.liveThinking }} live open={density === 'full'} /></div>
    {/if}
  {/if}
</div>
