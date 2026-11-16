<script lang="ts">
  import type { Snippet } from 'svelte'

  /**
   * The shared page header (design C5): breadcrumbs, an optional eyebrow, one
   * semantic h1, and slots for a status badge and page actions.
   *
   * It renders only. Navigation arrives as `onNavigate`, exactly like every
   * other page here, and the breadcrumb anchor keeps a real `href` so a
   * middle-click or a copied URL still works.
   */
  export interface Breadcrumb {
    label: string
    /** Omitted for the current page, which is then announced as such. */
    hash?: string
  }

  export interface PageHeaderProps {
    breadcrumbs?: Breadcrumb[]
    eyebrow?: string
    title: string
    subtitle?: string
    status?: Snippet
    actions?: Snippet
    onNavigate: (hash: string) => void
  }

  let { breadcrumbs = [], eyebrow, title, subtitle, status, actions, onNavigate }: PageHeaderProps = $props()

  function follow(e: MouseEvent, hash: string | undefined) {
    // Same contract as the rest of the shell: an in-app hash never reloads,
    // and a crumb without one is the current page, so it does nothing.
    if (!hash) return
    e.preventDefault()
    onNavigate(hash)
  }
</script>

<header class="flex flex-col gap-2">
  {#if breadcrumbs.length > 0}
    <nav aria-label="Breadcrumb">
      <ol class="flex flex-wrap items-center gap-1 text-xs text-muted">
        {#each breadcrumbs as crumb, i (i)}
          <li class="flex items-center gap-1">
            {#if i > 0}<span aria-hidden="true">/</span>{/if}
            {#if crumb.hash}
              <a class="text-accent hover:underline" href={crumb.hash} onclick={(e) => follow(e, crumb.hash)}>{crumb.label}</a>
            {:else}
              <span aria-current="page">{crumb.label}</span>
            {/if}
          </li>
        {/each}
      </ol>
    </nav>
  {/if}

  <div class="flex flex-wrap items-start justify-between gap-3">
    <div class="flex min-w-0 flex-col gap-1">
      {#if eyebrow}<p class="font-mono text-[11px] tracking-wide text-muted uppercase">{eyebrow}</p>{/if}
      <h1 class="text-lg font-semibold">{title}</h1>
      {#if subtitle}<p class="max-w-2xl text-sm text-muted">{subtitle}</p>{/if}
    </div>
    {#if status || actions}
      <div class="flex shrink-0 items-center gap-2">
        {#if status}{@render status()}{/if}
        {#if actions}{@render actions()}{/if}
      </div>
    {/if}
  </div>
</header>
