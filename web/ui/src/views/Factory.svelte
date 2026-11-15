<script lang="ts">
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import type { FactoryFilter } from '../lib/factory'
  import type { FactoryState } from '../lib/factoryStore'
  import { filterFactoryItems } from '../lib/studio/model'
  import { formatFactoryRoute } from '../lib/routes'

  /*
    The list renders from the shell's store rather than a poller of its own:
    one store polls for the list, the item view and the create view, so a
    command's own result is already in the list when it next redraws. The
    scope and the filter are projections of that one snapshot, so switching
    either never starts a second read.
  */
  let {
    factory,
    onRefresh,
    onNavigate,
    projectRoot = '',
    filter = 'current',
  }: {
    factory: FactoryState
    onRefresh: () => void
    onNavigate: (hash: string) => void
    projectRoot?: string
    filter?: FactoryFilter
  } = $props()

  // The project scope is exact: a sibling directory that merely shares a
  // prefix is a different project. C2 owns the sort as well, so the order
  // here is the same one the fleet's counts use.
  const scoped = $derived(projectRoot ? factory.items.filter((item) => item.projectRoot === projectRoot) : factory.items)
  const items = $derived(filterFactoryItems(scoped, filter))
</script>
<div class="mx-auto flex max-w-5xl flex-col gap-4 p-4 sm:p-6">
  <header class="flex flex-wrap items-center justify-between gap-3">
    <div><h1 class="text-xl font-semibold">Factory</h1><p class="text-sm text-muted">Supervised delivery work items</p></div>
    <Button onclick={() => onNavigate('#factory/new')}>New work item</Button>
  </header>
  {#if factory.error}
    <Card role="alert" class="border-attention text-sm">
      <p>Factory items unavailable: {factory.error}</p>
      <Button variant="ghost" onclick={onRefresh}>Retry</Button>
    </Card>
  {/if}
  {#if factory.loading}
    <p role="status">Loading factory items…</p>
  {:else if items.length}
    <div class="grid gap-3">
      {#each items as item (item.id)}
        <Card class="flex flex-wrap items-center gap-3">
          <div class="min-w-0 flex-1"><h2 class="truncate font-semibold">{item.title}</h2><p class="truncate text-xs text-muted">{item.repoId} · {item.stage || item.state}</p></div>
          <Tag tone={item.decision ? 'warn' : 'neutral'}>{item.state}</Tag>
          <a class="text-sm text-accent underline" href={formatFactoryRoute({ itemId: item.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: item.id })) }}>{item.decision ? 'Resolve decision' : item.state === 'draft' ? 'Review draft' : 'Open item'}</a>
        </Card>
      {/each}
    </div>
  {:else if !factory.error}
    <!-- A failed read says so above; it is never also "nothing exists yet". -->
    <Card><p>No factory work items yet.</p><a class="text-accent underline" href="#factory/new">Create a draft</a></Card>
  {/if}
</div>
