<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import type { Tone } from '../glyphs'
  import type { FactoryItem, FactoryPR } from '../factory'

  /**
   * One work item's pull requests (design C5): the PRs the factory actually
   * recorded, with the revision each one was opened against and the state the
   * forge reported. Nothing here is inferred — an item without a recorded PR
   * shows that it has none.
   *
   * A PR URL is followed only as a literal absolute http(s) URL: the forge
   * body is data, so an opaque or non-http URL is shown as text rather than
   * turned into something to click. The same validator gates the preview
   * links in FactoryItem.
   */
  export interface PullRequestsViewProps {
    item: FactoryItem
  }

  let { item }: PullRequestsViewProps = $props()

  const prs = $derived(item.prs ?? [])

  function safeUrl(value: string) {
    if (!value || !/^https?:\/\//i.test(value) || value.length > 2048) return false
    try {
      const parsed = new URL(value)
      return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !!parsed.hostname
    } catch {
      return false
    }
  }

  const shortSha = (sha: string) => (sha ? sha.slice(0, 7) : 'unknown')
  const label = (pr: FactoryPR) => (pr.draft ? 'draft' : pr.state || 'unknown')
  const tone = (pr: FactoryPR): Tone => (pr.state === 'open' ? (pr.draft ? 'neutral' : 'ok') : pr.state === 'closed' ? 'warn' : 'neutral')
</script>

<Card class="grid min-w-0 gap-3" data-testid="pull-requests-view">
  <h2 class="font-semibold">Pull requests</h2>
  {#if !prs.length}
    <p class="text-sm text-muted">No pull requests recorded for this work item.</p>
  {:else}
    <ul class="grid gap-2">
      {#each prs as pr (pr.number)}
        <li class="grid gap-1 rounded border border-border p-2 text-sm" data-testid="pull-request">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-semibold">PR #{pr.number}</span>
            <Tag tone={tone(pr)}>{label(pr)}</Tag>
          </div>
          <p class="break-all text-xs text-muted"><code>{pr.headRef || 'unknown'}</code> → <code>{pr.baseRef || 'unknown'}</code></p>
          <p class="break-all text-xs text-muted">head {shortSha(pr.headSha)} · base {shortSha(pr.baseSha)}</p>
          {#if safeUrl(pr.url)}
            <a class="break-all text-accent underline" href={pr.url} target="_blank" rel="noreferrer noopener">{pr.url}</a>
          {:else}
            <span class="break-all text-xs">{pr.url || 'No URL recorded'}</span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</Card>
