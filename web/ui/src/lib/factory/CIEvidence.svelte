<script lang="ts">
  import Card from '../ui/Card.svelte'
  import { partitionFactoryChecks } from '../studio/model'
  import type { FactoryItem } from '../factory'
  import { checkState, ciEvidence, displayText, isHttpUrl, reviewVerdict } from './reviewModel'

  /**
   * The review's evidence card (design: CI-first). Failures lead and are
   * expanded with their logs; the round's review findings sit with them,
   * because a finding is what a failing review leaves behind; the repair state
   * says whether that review is clean. Passing checks start collapsed — they
   * are confirmation, not the thing to read first.
   *
   * The grouping is C2's `partitionFactoryChecks`, so this card and the
   * bridge's own PR body never disagree about which check failed.
   */
  export interface CIEvidenceProps {
    item: FactoryItem
  }

  let { item }: CIEvidenceProps = $props()

  const round = $derived(item.rounds?.at(-1))
  const groups = $derived(partitionFactoryChecks(round))
  const evidence = $derived(ciEvidence(round))
  const verdict = $derived(reviewVerdict(round))
  const findings = $derived(round?.review?.findings ?? [])
  /** A round that did work has a review state worth stating; an empty one does not. */
  const reviewed = $derived(!!round?.implementation || !!round?.review)
</script>

<Card class="grid min-w-0 gap-3" data-testid="ci-evidence">
  <h2 class="font-semibold">CI evidence</h2>
  <p class={evidence.warning ? 'text-sm text-attention' : 'text-sm text-muted'} data-testid="ci-state">{evidence.text}</p>
  {#each groups.failed as check}
    <div class="min-w-0 rounded border border-attention p-2" data-testid="ci-failure">
      <strong>{check.key.name}: {checkState(check)}</strong>
      {#if check.url}
        {#if isHttpUrl(check.url)}
          <a class="ml-2 break-all text-accent underline" href={check.url} target="_blank" rel="noopener noreferrer">Open check</a>
        {:else}
          <span class="ml-2 break-all text-xs">{displayText(check.url)}</span>
        {/if}
      {/if}
      {#if check.logTail}<pre class="max-h-60 max-w-full overflow-auto whitespace-pre-wrap break-words text-xs">{check.logTail}</pre>{/if}
    </div>
  {/each}
  {#if findings.length}
    <div class="grid gap-1">
      <h3 class="text-sm font-semibold">Review findings</h3>
      {#each findings as finding}
        <div class="min-w-0 rounded border border-attention p-2 text-sm" data-testid="review-finding">
          <p class="font-semibold">{finding.severity}: {finding.title}</p>
          {#if finding.path}<p class="break-all font-mono text-xs">{finding.path}{#if finding.line}:{finding.line}{/if}</p>{/if}
          {#if finding.body}<p class="whitespace-pre-wrap break-words">{finding.body}</p>{/if}
        </div>
      {/each}
    </div>
  {/if}
  {#each groups.other as check}<p class="text-sm">{check.key.name}: {checkState(check)}</p>{/each}
  {#if reviewed}
    <p class={verdict.warning ? 'text-sm text-attention' : 'text-sm text-muted'} data-testid="review-state">{verdict.text}</p>
  {/if}
  {#if groups.passed.length}
    <details data-testid="ci-passes">
      <summary>{groups.passed.length} passing checks</summary>
      <ul class="text-sm">{#each groups.passed as check}<li>{check.key.name}</li>{/each}</ul>
    </details>
  {/if}
</Card>
