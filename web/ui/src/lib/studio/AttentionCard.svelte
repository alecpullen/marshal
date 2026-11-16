<script lang="ts">
  import type { Snippet } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'

  /**
   * One entry waiting on a person, from any source. The card names where the
   * entry came from, what it is, what it is waiting on, and where it opens;
   * the caller supplies the source's own decision controls as `actions`, so
   * the pending-approval and network-decision surfaces are used rather than
   * reimplemented here.
   *
   * The card navigates and renders only: no fetch, no policy. `onNavigate` is
   * the shell's hash callback, exactly like every other view.
   */
  export interface AttentionCardProps {
    /** Where the entry came from: Factory, Agent, Intake, Network. */
    source: string
    title: string
    body?: string
    meta?: string
    /** Where the entry's own view lives, as a `#…` hash. */
    href: string
    actionLabel: string
    onNavigate: (hash: string) => void
    /** The source's decision controls, when it has any. */
    actions?: Snippet
  }

  let { source, title, body, meta, href, actionLabel, onNavigate, actions }: AttentionCardProps = $props()
</script>

<Card class="flex flex-wrap items-center gap-3 p-3" data-testid="attention-card">
  <button class="min-w-0 flex-1 cursor-pointer text-left" onclick={() => onNavigate(href)}>
    <!-- Block spans, not divs: a button's content is phrasing only. -->
    <span class="block text-[11px] tracking-wide text-muted uppercase">{source}</span>
    <!-- A long title wraps inside the card instead of overflowing it. -->
    <span class="block break-words text-sm font-medium">{title}</span>
    {#if body}<span class="block break-words text-xs text-muted">{body}</span>{/if}
    {#if meta}<span class="block break-words text-xs text-muted">{meta}</span>{/if}
  </button>
  {#if actions}
    <div class="flex flex-wrap items-center gap-2">{@render actions()}</div>
  {/if}
  <Button variant="ghost" onclick={() => onNavigate(href)}>{actionLabel}</Button>
</Card>
