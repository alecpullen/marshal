<script lang="ts">
  import Badge from '../ui/Badge.svelte'
  import Button from '../ui/Button.svelte'
  import { cn } from '../utils'
  import type { NavDestination, NavProjectDestination, StudioNavProps } from './model'

  let { model, open, onToggle, onNavigate }: StudioNavProps = $props()

  /*
    Two shapes of one model, not two navigations. The labelled sidebar carries
    what the icon rail cannot: section names, the active project's current work
    items, and Planned badges. The rail keeps the same destinations from
    `model.all` with the same aria-current, and moves the label into aria-label
    so a screen reader hears what the eye no longer can.
  */
  const labelOf = (d: NavDestination) => (d.planned ? `${d.label} (planned)` : d.label)
  const titleOf = (d: NavDestination) => {
    const parts = [labelOf(d)]
    if ((d.count ?? 0) > 0) parts.push(`${d.count} need you`)
    return parts.join(' · ')
  }

  /*
    Work items are contextual: a project shows its current work while the user
    is in that project or in one of its items. A work item is its own route
    (`#factory/<id>`), so the item being open is what makes the project
    contextual — nothing else on that route names the project.
  */
  const showsWorkItems = (p: NavProjectDestination) => p.active || p.workItems.some((w) => w.active)
</script>

{#snippet labelledRow(d: NavDestination)}
  <Button
    variant="ghost"
    class={cn(
      'w-full justify-start gap-2 border-transparent bg-transparent px-2 text-left font-normal',
      d.active && 'bg-raise font-medium text-accent',
    )}
    aria-current={d.active ? 'page' : undefined}
    onclick={() => onNavigate(d.hash)}
  >
    <span class="w-4 shrink-0 text-center text-base" aria-hidden="true">{d.glyph}</span>
    <span class="truncate">{d.label}</span>
    {#if d.planned}<Badge>Planned</Badge>{/if}
    {#if (d.count ?? 0) > 0}<Badge tone="attention" class="ml-auto tabular-nums">{d.count}</Badge>{/if}
  </Button>
{/snippet}

{#snippet projectRow(p: NavProjectDestination)}
  {@render labelledRow(p)}
  {#if showsWorkItems(p) && p.workItems.length > 0}
    <div class="mb-1 flex flex-col gap-0.5 border-l border-border pl-3">
      {#each p.workItems as w (w.hash)}
        <Button
          variant="ghost"
          class={cn(
            'w-full justify-start gap-2 border-transparent bg-transparent px-2 text-left text-xs font-normal text-muted',
            w.active && 'bg-raise font-medium text-accent',
          )}
          aria-current={w.active ? 'page' : undefined}
          onclick={() => onNavigate(w.hash)}
        >
          <span class="w-4 shrink-0 text-center" aria-hidden="true">{w.glyph}</span>
          <span class="truncate">{w.label}</span>
        </Button>
      {/each}
    </div>
  {/if}
{/snippet}

{#snippet iconRow(d: NavDestination)}
  <Button
    variant="ghost"
    class={cn(
      'size-10 min-h-0 shrink-0 justify-center border-transparent bg-transparent p-0 text-base',
      d.active && 'bg-raise text-accent',
    )}
    aria-label={labelOf(d)}
    title={titleOf(d)}
    aria-current={d.active ? 'page' : undefined}
    onclick={() => onNavigate(d.hash)}
  >
    <span aria-hidden="true">{d.glyph}</span>
  </Button>
{/snippet}

{#if open}
  <nav
    aria-label="Studio navigation"
    class="flex h-full w-64 shrink-0 flex-col gap-3 overflow-y-auto border-r border-border bg-surface p-2 text-sm"
  >
    <div class="flex items-center justify-between gap-2 px-1 py-1">
      <button class="cursor-pointer text-sm font-semibold tracking-wide" onclick={() => onNavigate('#')}>
        Marshal
      </button>
      <button
        class="cursor-pointer rounded-md px-1.5 py-1 text-xs text-muted hover:bg-bg"
        onclick={onToggle}
        aria-label="Collapse navigation"
        title="Collapse navigation"
      >
        ⟨
      </button>
    </div>

    <div class="flex flex-col gap-0.5">
      {#each model.primary as d (d.hash)}
        {@render labelledRow(d)}
      {/each}
    </div>

    {#if model.projects.length > 0}
      <div>
        <p class="px-2 py-1 text-[0.6875rem] tracking-wide text-muted uppercase">Project factories</p>
        <div class="flex flex-col gap-0.5">
          {#each model.projects as p (p.root)}
            {@render projectRow(p)}
          {/each}
        </div>
      </div>
    {/if}

    <div>
      <p class="px-2 py-1 text-[0.6875rem] tracking-wide text-muted uppercase">Resources</p>
      <div class="flex flex-col gap-0.5">
        {#each model.resources as d (d.hash)}
          {@render labelledRow(d)}
        {/each}
      </div>
    </div>

    <div>
      <p class="px-2 py-1 text-[0.6875rem] tracking-wide text-muted uppercase">Roadmap</p>
      <div class="flex flex-col gap-0.5">
        {#each model.roadmap as d (d.hash)}
          {@render labelledRow(d)}
        {/each}
      </div>
    </div>
  </nav>
{:else}
  <nav
    aria-label="Studio navigation"
    class="flex h-full w-14 shrink-0 flex-col items-center gap-1 overflow-y-auto border-r border-border bg-surface py-3"
  >
    <Button
      variant="ghost"
      class="size-10 min-h-0 shrink-0 justify-center border-transparent bg-transparent p-0"
      onclick={onToggle}
      aria-label="Expand navigation"
      title="Expand navigation"
    >
      ☰
    </Button>
    <div class="mb-2 text-accent" aria-hidden="true">●</div>
    {#each model.all as d (d.hash)}
      {@render iconRow(d)}
    {/each}
  </nav>
{/if}
