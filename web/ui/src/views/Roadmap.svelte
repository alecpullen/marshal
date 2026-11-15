<script lang="ts">
  import Badge from '../lib/ui/Badge.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import PageHeader from '../lib/ui/PageHeader.svelte'
  import { ROADMAP } from '../lib/roadmap'
  import type { RoadmapArea } from '../lib/routes'

  /**
   * One roadmap area (design C4). The route supplies the area; this page only
   * renders the registry entry for it, so it reads no hash and fetches
   * nothing. What it shows is current capability and planned work — never a
   * progress bar, a count or a date, because none of those exist.
   */
  let { area, onNavigate }: { area: RoadmapArea; onNavigate: (hash: string) => void } = $props()

  const entry = $derived(ROADMAP[area])
</script>

<div class="studio-safe-bottom mx-auto flex max-w-3xl flex-col gap-4 p-6" data-testid="roadmap" data-area={area}>
  <PageHeader
    breadcrumbs={[{ label: 'Roadmap' }, { label: entry.title }]}
    eyebrow="Roadmap"
    title={entry.title}
    subtitle={entry.summary}
    {onNavigate}
  >
    {#snippet status()}
      <span data-testid="planned-badge"><Badge tone="attention">Planned</Badge></span>
    {/snippet}
  </PageHeader>

  <Card class="studio-panel flex flex-col gap-2">
    <h2 class="text-sm font-semibold">Available now</h2>
    <p class="text-sm text-sub" data-testid="roadmap-current">{entry.current}</p>
  </Card>

  <Card class="studio-panel flex flex-col gap-2">
    <h2 class="text-sm font-semibold">Planned</h2>
    <ul class="flex flex-col gap-1 text-sm text-sub" data-testid="roadmap-planned">
      {#each entry.planned as item (item)}
        <li class="flex gap-2">
          <span aria-hidden="true" class="text-muted">·</span>
          <span>{item}</span>
        </li>
      {/each}
    </ul>
  </Card>

  <Card class="studio-panel flex flex-col gap-2">
    <h2 class="text-sm font-semibold">Available today</h2>
    {#if entry.alternatives.length > 0}
      <div class="flex flex-wrap gap-2" data-testid="roadmap-alternatives">
        {#each entry.alternatives as action (action.hash)}
          <Button variant="ghost" onclick={() => onNavigate(action.hash)}>{action.label}</Button>
        {/each}
      </div>
    {:else}
      <p class="text-sm text-sub" data-testid="roadmap-no-alternatives">No page in the app does this yet.</p>
    {/if}
  </Card>

  {#if entry.designHref}
    <p class="text-xs text-muted">
      <a class="text-accent hover:underline" href={entry.designHref} target="_blank" rel="noopener noreferrer" data-testid="roadmap-design-link">
        Design notes for {entry.title}
      </a>
    </p>
  {/if}
</div>
