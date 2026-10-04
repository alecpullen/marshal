<script lang="ts">
  import { cn } from './utils'

  let { route, onNavigate }: { route: string; onNavigate: (hash: string) => void } = $props()

  // Pages that do not exist yet are shown disabled so the shape of the
  // studio is visible. `phase` is the roadmap phase that enables them.
  const items: { glyph: string; label: string; hash?: string; phase?: string }[] = [
    { glyph: '⌂', label: 'Home', hash: '#' },
    { glyph: '⧉', label: 'Agents', hash: '#fleet' },
    { glyph: '≡', label: 'Projects', hash: '#projects' },
    { glyph: '◉', label: 'Live', hash: '#live' },
    { glyph: '⋔', label: 'Runs', hash: '#runs' },
    { glyph: '▦', label: 'Workspaces', phase: 'W4' },
    { glyph: '○', label: 'Watches', hash: '#watches' },
    { glyph: '◈', label: 'Library', hash: '#library/skills' },
    { glyph: '∿', label: 'Usage', hash: '#usage' },
  ]

  // A page owns its sub-routes: #runs/<id> and #live?page=2 keep their rail entry lit.
  const active = (hash: string) => {
    if (hash === '#') return route === '#' || route === ''
    // #library/skills is the entry point; every #library/<tab> keeps it lit.
    const base = hash.split('/')[0]
    return route === base || route.startsWith(base + '/') || route.startsWith(base + '?')
  }
</script>

<nav aria-label="Pages" class="flex h-full w-[52px] shrink-0 flex-col items-center gap-1 border-r border-border bg-bg py-3">
  <span class="mb-2 text-accent" aria-hidden="true">●</span>
  {#each items as it (it.label)}
    {#if it.hash}
      <button
        class={cn(
          'flex size-9 cursor-pointer items-center justify-center rounded-md text-base hover:bg-hover',
          active(it.hash) ? 'bg-raise text-accent' : 'text-muted',
        )}
        title={it.label}
        aria-label={it.label}
        aria-current={active(it.hash) ? 'page' : undefined}
        onclick={() => onNavigate(it.hash!)}
      >
        {it.glyph}
      </button>
    {:else}
      <button
        class="flex size-9 cursor-not-allowed items-center justify-center rounded-md text-base text-dim"
        title="Coming in {it.phase}"
        aria-label="{it.label} (coming in {it.phase})"
        disabled
      >
        {it.glyph}
      </button>
    {/if}
  {/each}
  <div class="flex-1"></div>
  <button
    class={cn(
      'flex size-9 cursor-pointer items-center justify-center rounded-md text-base hover:bg-hover',
      route.startsWith('#settings') ? 'bg-raise text-accent' : 'text-muted',
    )}
    title="Settings"
    aria-label="Settings"
    onclick={() => onNavigate('#settings/models')}
  >
    ⚙
  </button>
</nav>
