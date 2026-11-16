<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import CIEvidence from './CIEvidence.svelte'
  import PreviewCard from './PreviewCard.svelte'
  import EngineeringReport from './EngineeringReport.svelte'
  import type { Tone } from '../glyphs'
  import type { FactoryItem, FactoryPR } from '../factory'
  import { currentStatusLinks, displayText, isHttpUrl, shortSha } from './reviewModel'

  /**
   * A work item's review (design: CI-first review). The order is the approved
   * one: failing CI with its findings and repair state first, passing checks
   * collapsed behind it, then the revision the previews belong to, then what
   * the implementer and reviewer said, and finally the real forge links.
   *
   * Readiness here is evidence about a revision, never a claim that the work
   * is merged: there is no Merge action until a merge contract exists, and
   * Open in forge is the working path.
   */
  export interface ReviewViewProps {
    item: FactoryItem
  }

  let { item }: ReviewViewProps = $props()

  const round = $derived(item.rounds?.at(-1))
  // The last head the factory itself recorded: the studio cannot read the
  // forge's current head, and must not imply that it can.
  const prHead = $derived(item.prs?.at(-1)?.headSha ?? '')
  const statusLinks = $derived(currentStatusLinks(round))
  const prs = $derived(item.prs ?? [])

  const tone = (pr: FactoryPR): Tone => (pr.state === 'open' ? (pr.draft ? 'neutral' : 'ok') : pr.state === 'closed' ? 'warn' : 'neutral')
  const label = (pr: FactoryPR) => (pr.draft ? 'draft' : pr.state || 'unknown')
</script>

<div class="grid min-w-0 content-start gap-4" data-testid="review-view">
  <CIEvidence {item} />
  {#if item.repoId}<PreviewCard repoId={item.repoId} branch={item.branch} {prHead} {statusLinks} />{/if}
  <EngineeringReport {item} />
  <Card class="grid min-w-0 gap-2" data-testid="review-forge-links">
    <h2 class="font-semibold">Pull requests</h2>
    {#if !prs.length}
      <p class="text-sm text-muted">No pull request recorded for this work item yet.</p>
    {:else}
      <ul class="grid gap-2">
        {#each prs as pr (pr.number)}
          <li class="grid gap-1 rounded border border-border p-2 text-sm" data-testid="review-pr">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-semibold">PR #{pr.number}</span>
              <Tag tone={tone(pr)}>{label(pr)}</Tag>
            </div>
            <p class="break-all text-xs text-muted"><code>{pr.headRef || 'unknown'}</code> → <code>{pr.baseRef || 'unknown'}</code></p>
            <p class="break-all text-xs text-muted">head {shortSha(pr.headSha)} · base {shortSha(pr.baseSha)}</p>
            {#if isHttpUrl(pr.url)}
              <a class="break-all text-accent underline" href={pr.url} target="_blank" rel="noreferrer noopener">{pr.url}</a>
            {:else}
              <span class="break-all text-xs">{displayText(pr.url || 'No URL recorded')}</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </Card>
</div>
