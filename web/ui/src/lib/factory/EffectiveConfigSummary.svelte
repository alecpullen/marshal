<script lang="ts">
  import Card from '../ui/Card.svelte'
  import type { ConfigSummaryRow } from './config'

  /**
   * The effective configuration, read-only (design C5): a labelled row per
   * field, the source each value came from, and the consequence of acting on
   * it. Creation and the saved draft's approval show the same rows, and a
   * routing object is never printed here as JSON.
   */
  export interface EffectiveConfigSummaryProps {
    rows: ConfigSummaryRow[]
    title?: string
    consequence?: string
    plan?: string
    spec?: string
  }

  let { rows, title = 'Approval summary', consequence, plan, spec }: EffectiveConfigSummaryProps = $props()

  /** A stable hook per row, so a test or a scan reads a labelled value. */
  const idOf = (label: string): string => label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '')
</script>

<Card class="flex flex-col gap-3" data-testid="effective-config">
  <h2 class="text-sm font-semibold">{title}</h2>
  <dl class="grid gap-x-4 gap-y-2 text-sm sm:grid-cols-[auto_1fr] sm:gap-y-1">
    {#each rows as row (row.label)}
      <dt class="text-xs text-muted sm:text-sm">{row.label}</dt>
      <dd class="min-w-0 break-words" data-testid={'config-' + idOf(row.label)} data-warning={row.warning ? 'true' : undefined}><span class={row.warning ? 'text-attention' : ''}>{row.value}</span>{#if row.source} <span class="block text-xs text-muted">{row.source}</span>{/if}</dd>
    {/each}
  </dl>
  {#if consequence}<p class="text-xs text-muted" data-testid="config-consequence">{consequence}</p>{/if}
  {#if plan}
    <details class="min-w-0">
      <summary class="cursor-pointer text-xs text-muted">Plan</summary>
      <p class="mt-2 whitespace-pre-wrap break-words text-sm" data-testid="summary-plan">{plan}</p>
    </details>
  {/if}
  {#if spec}
    <details class="min-w-0">
      <summary class="cursor-pointer text-xs text-muted">Specification</summary>
      <p class="mt-2 whitespace-pre-wrap break-words text-sm" data-testid="summary-spec">{spec}</p>
    </details>
  {/if}
</Card>
