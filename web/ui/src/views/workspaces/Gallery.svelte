<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import CreateModal from '../../lib/workspaces/CreateModal.svelte'
  import { listWorkspaces, errMessage, type WorkspaceListItem } from '../../lib/api'
  import type { AgentRow } from '../../lib/fleet'
  import { shortName } from '../../lib/utils'
  import { buildTone, contentChips, latestVersion, poolLabel } from '../../lib/workspaces/model'

  let { agents, onNavigate }: { agents: AgentRow[]; onNavigate: (hash: string) => void } = $props()

  let items = $state<WorkspaceListItem[]>([])
  let loaded = $state(false)
  let error = $state('')
  let creating = $state(false)

  onMount(async () => {
    try {
      items = await listWorkspaces()
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  })

  const dot = { ok: 'bg-ok', err: 'bg-err', warn: 'bg-warn', neutral: 'bg-muted' }

  const open = (w: WorkspaceListItem) => {
    // Repo templates live in the project's .marshal/workspaces; the bridge serves editing for Studio templates only.
    if (w.source === 'studio') onNavigate(`#workspaces/${encodeURIComponent(w.name)}/edit`)
  }
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header class="flex items-center justify-between">
    <h1 class="text-lg font-semibold">Workspaces</h1>
    <Button onclick={() => (creating = true)}>New workspace</Button>
  </header>

  {#if error}<p role="alert" class="text-sm text-err">{error}</p>{/if}

  {#if loaded && !error && items.length === 0}
    <Card class="text-sm text-muted">No workspaces yet. Create one from a starter, a devcontainer.json or a running agent.</Card>
  {/if}

  <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
    {#each items as w (`${w.source}:${w.project ?? ''}:${w.name}`)}
      {@const v = latestVersion(w)}
      {@const chips = contentChips(w.doc)}
      {@const pool = poolLabel(w.pool)}
      <button
        type="button"
        class="text-left {w.source === 'studio' ? 'cursor-pointer' : 'cursor-default'}"
        data-testid="ws-card"
        aria-label={w.name}
        onclick={() => open(w)}
      >
        <Card class="flex h-full flex-col gap-2 hover:bg-hover">
          <div class="flex items-center gap-2">
            <span class="truncate font-medium">{w.name}</span>
            <span class="flex-1"></span>
            {#if v}<span class="size-2 shrink-0 rounded-full {dot[buildTone(v.buildStatus)]}" title="build {v.buildStatus}" data-testid="build-dot" data-status={v.buildStatus}></span>{/if}
          </div>
          <div class="flex flex-wrap items-center gap-1.5 text-xs">
            <Tag tone={w.source === 'studio' ? 'accent' : 'info'}>{w.source === 'studio' ? 'Studio' : `Repo · ${shortName(w.project ?? '')}`}</Tag>
            {#if w.published}<Tag tone="neutral">v{w.published}</Tag>{:else}<Tag tone="warn">unpublished</Tag>{/if}
            {#if w.draftChanges}<Tag tone="warn">draft changes</Tag>{/if}
            {#if pool}<Tag tone="ok">{pool}</Tag>{/if}
          </div>
          {#if chips.length}
            <div class="flex flex-wrap gap-1">
              {#each chips as c (c)}<Tag tone="neutral">{c}</Tag>{/each}
            </div>
          {/if}
          <div class="mt-auto text-xs text-muted">{w.usage} agent{w.usage === 1 ? '' : 's'}</div>
        </Card>
      </button>
    {/each}
  </div>
</div>

{#if creating}
  <CreateModal
    {agents}
    onClose={() => (creating = false)}
    onCreated={(name) => {
      creating = false
      onNavigate(`#workspaces/${encodeURIComponent(name)}/edit`)
    }}
  />
{/if}
