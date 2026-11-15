<script lang="ts">
  import Card from '../ui/Card.svelte'
  import type { FactoryItem } from '../factory'
  import { narrationSections } from './reviewModel'

  /**
   * The engineering report (design: CI-first review, step 4): what the
   * implementer and reviewer said they did, in the worker's own narration
   * order, with the flat summary as the fallback when no narration was
   * supplied. Questions and reasons stay with the summary that raised them.
   *
   * Review findings do not live here: they are part of the evidence card's
   * failure section, and a finding shown twice reads as two findings.
   */
  export interface EngineeringReportProps {
    item: FactoryItem
  }

  let { item }: EngineeringReportProps = $props()

  const round = $derived(item.rounds?.at(-1))
  const implementation = $derived(round?.implementation ?? null)
  const review = $derived(round?.review ?? null)
  const narration = $derived(implementation?.narration ?? null)
  const sections = $derived(narrationSections(narration))
  const excerpts = $derived(narration?.excerpts ?? [])
</script>

<Card class="grid min-w-0 gap-2" data-testid="engineering-report">
  <h2 class="font-semibold">Engineering report</h2>
  {#if implementation}
    {#if narration}
      <div class="grid gap-2" data-testid="factory-narration">
        {#each sections as section}
          <div class="grid gap-1"><h3 class="text-sm font-semibold">{section.heading}</h3>{#each section.paragraphs as paragraph}<p class="whitespace-pre-wrap break-words text-sm">{paragraph}</p>{/each}</div>
        {/each}
        {#if excerpts.length}
          <div class="grid gap-1">
            <h3 class="text-sm font-semibold">Selected code</h3>
            {#each excerpts as excerpt}
              <div class="min-w-0 rounded border border-border p-2">
                <div class="flex flex-wrap items-baseline gap-2"><p class="break-all font-mono text-xs">{excerpt.path}</p>{#if excerpt.lang}<span class="text-xs text-muted">{excerpt.lang}</span>{/if}</div>
                <pre class="max-h-60 overflow-auto whitespace-pre-wrap break-words text-xs">{excerpt.code}</pre>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {:else}
      <p class="whitespace-pre-wrap break-words text-sm">{implementation.summary}</p>
    {/if}
    {#if implementation.question}<p class="text-sm">Implementer question: {implementation.question}</p>{/if}
    {#if implementation.reason}<p class="text-sm">Implementer reason: {implementation.reason}</p>{/if}
  {/if}
  {#if review}
    <p class="whitespace-pre-wrap break-words text-sm">Review: {review.summary}</p>
    {#if review.question}<p class="text-sm">Reviewer question: {review.question}</p>{/if}
    {#if review.reason}<p class="text-sm">Reviewer reason: {review.reason}</p>{/if}
  {/if}
  {#if !implementation && !review}<p class="text-sm text-muted">No report yet.</p>{/if}
</Card>
