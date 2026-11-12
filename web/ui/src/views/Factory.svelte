<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Tag from '../lib/ui/Tag.svelte'
  import { errMessage, listFactoryItems } from '../lib/api'
  import { createFactoryPoller, type FactoryItem } from '../lib/factory'
  import { formatFactoryRoute } from '../lib/routes'
  let { onNavigate }: { onNavigate: (hash: string) => void } = $props()
  let items = $state<FactoryItem[]>([])
  let error = $state('')
  let loading = $state(true)
  onMount(() => {
    const poll = createFactoryPoller(async () => { items = await listFactoryItems(); error = ''; loading = false }, () => items.some((i) => !['draft', 'ready', 'stopped', 'closed', 'needs_input'].includes(i.state)), (e) => { error = errMessage(e); loading = false })
    poll.start()
    return poll.stop
  })
</script>
<div class="mx-auto flex max-w-5xl flex-col gap-4 p-4 sm:p-6">
  <header class="flex flex-wrap items-center justify-between gap-3">
    <div><h1 class="text-xl font-semibold">Factory</h1><p class="text-sm text-muted">Supervised delivery work items</p></div>
    <Button onclick={() => onNavigate('#factory/new')}>New work item</Button>
  </header>
  {#if error}<Card role="alert" class="border-attention text-sm">Factory items unavailable: {error}</Card>{/if}
  {#if loading}<p role="status">Loading factory items…</p>
  {:else if !items.length}<Card><p>No factory work items yet.</p><a class="text-accent underline" href="#factory/new">Create a draft</a></Card>
  {:else}
    <div class="grid gap-3">
      {#each items as item (item.id)}
        <Card class="flex flex-wrap items-center gap-3">
          <div class="min-w-0 flex-1"><h2 class="truncate font-semibold">{item.title}</h2><p class="truncate text-xs text-muted">{item.repoId} · {item.stage || item.state}</p></div>
          <Tag tone={item.decision ? 'warn' : 'neutral'}>{item.state}</Tag>
          <a class="text-sm text-accent underline" href={formatFactoryRoute({ itemId: item.id })} onclick={(e) => { e.preventDefault(); onNavigate(formatFactoryRoute({ itemId: item.id })) }}>{item.decision ? 'Resolve decision' : item.state === 'draft' ? 'Review draft' : 'Open item'}</a>
        </Card>
      {/each}
    </div>
  {/if}
</div>
