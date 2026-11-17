<script lang="ts">
  import { Dialog } from 'bits-ui'
  import Badge from '../ui/Badge.svelte'
  import Button from '../ui/Button.svelte'
  import { cn } from '../utils'
  import type { MobileNavProps, NavDestination } from './model'

  let { model, onNavigate }: MobileNavProps = $props()

  /*
    The phone and tablet shell: a top app bar, four primary destinations along
    the bottom, and everything else behind an accessible all-pages drawer.

    The drawer is a plain Dialog, deliberately not the shared decision Modal.
    Modal maps Escape to the caller's negative resolution because a stranded
    permission request never times out; here Escape and an outside click may
    only close this drawer, and nothing in this component resolves an agent's
    request. The drawer's content is `model.all`, the deduplicated global and
    project destination list, so no page is reachable on desktop and missing on
    a phone.
  */
  const BOTTOM_HASHES = ['#', '#inbox', '#library/skills', '#settings/models']
  const bottom = $derived(BOTTOM_HASHES.flatMap((hash) => model.all.filter((d) => d.hash === hash)))

  const sections = $derived.by(() => {
    // Grouped without reordering: the headings come from the shell's sections,
    // the rows and their order come from `model.all`.
    const group = (title: string, dests: NavDestination[]) => {
      const hashes = new Set(dests.map((d) => d.hash))
      return { title, items: model.all.filter((d) => hashes.has(d.hash)) }
    }
    return [
      group('Primary', model.primary),
      group('Project factories', model.projects),
      group('Resources', model.resources),
      group('Roadmap', model.roadmap),
    ].filter((section) => section.items.length > 0)
  })

  let open = $state(false)
  let menuButton = $state<HTMLButtonElement>()

  function navigate(hash: string) {
    onNavigate(hash)
    open = false
  }
</script>

<div class="flex h-full w-full min-w-0 flex-col bg-bg">
  <header class="flex items-center gap-2 border-b border-border bg-surface px-2 py-1">
    <!--
      A plain button, not the Button primitive: focus must return to this
      element when the drawer closes, and `bind:this` on a component gives its
      instance rather than the node.
    -->
    <button
      bind:this={menuButton}
      class="inline-flex size-11 shrink-0 cursor-pointer items-center justify-center rounded-md border border-transparent bg-transparent p-0 text-base hover:bg-surface"
      aria-label="Open navigation"
      aria-expanded={open}
      onclick={() => (open = true)}
    >
      ☰
    </button>
    <span class="truncate text-sm font-semibold tracking-wide">Marshal</span>
  </header>

  <div class="min-h-0 flex-1"></div>

  <nav
    aria-label="Primary"
    class="flex shrink-0 items-stretch border-t border-border bg-surface pb-[env(safe-area-inset-bottom)]"
  >
    {#each bottom as d (d.hash)}
      <Button
        variant="ghost"
        class={cn(
          'min-h-11 flex-1 basis-0 flex-col gap-0.5 rounded-none border-transparent bg-transparent px-1 py-1 text-[0.6875rem] font-normal',
          d.active && 'text-accent',
        )}
        aria-current={d.active ? 'page' : undefined}
        onclick={() => onNavigate(d.hash)}
      >
        <span class="text-base" aria-hidden="true">{d.glyph}</span>
        <span class="truncate">{d.label}</span>
        {#if (d.count ?? 0) > 0}<Badge tone="attention" class="tabular-nums">{d.count}</Badge>{/if}
      </Button>
    {/each}
  </nav>
</div>

<Dialog.Root open={open} onOpenChange={(next) => (open = next)}>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-100 bg-black/60" />
    <Dialog.Content
      escapeKeydownBehavior="close"
      interactOutsideBehavior="close"
      onCloseAutoFocus={(event) => {
        // Focus returns to the menu button, never to whatever the drawer had
        // last focused: the drawer is gone by the time this runs.
        event.preventDefault()
        menuButton?.focus()
      }}
      class="fixed inset-y-0 left-0 z-100 flex w-80 max-w-[85%] flex-col gap-2 overflow-y-auto border-r border-border bg-surface p-3 shadow-xl pb-[env(safe-area-inset-bottom)]"
    >
      <div class="flex items-center justify-between gap-2 px-1">
        <Dialog.Title class="text-sm font-semibold">All pages</Dialog.Title>
        <Button
          variant="ghost"
          class="size-11 min-h-0 shrink-0 justify-center border-transparent bg-transparent p-0 text-base"
          aria-label="Close navigation"
          onclick={() => (open = false)}
        >
          ✕
        </Button>
      </div>

      {#each sections as section (section.title)}
        <div>
          <p class="px-2 py-1 text-[0.6875rem] tracking-wide text-muted uppercase">{section.title}</p>
          <div class="flex flex-col gap-0.5">
            {#each section.items as d (d.hash)}
              <Button
                variant="ghost"
                class={cn(
                  'w-full justify-start gap-2 border-transparent bg-transparent px-2 text-left font-normal',
                  d.active && 'bg-raise font-medium text-accent',
                )}
                aria-current={d.active ? 'page' : undefined}
                onclick={() => navigate(d.hash)}
              >
                <span class="w-4 shrink-0 text-center text-base" aria-hidden="true">{d.glyph}</span>
                <span class="truncate">{d.label}</span>
                {#if d.planned}<Badge class="ml-auto">Planned</Badge>{/if}
                {#if (d.count ?? 0) > 0}<Badge tone="attention" class="ml-auto tabular-nums">{d.count}</Badge>{/if}
              </Button>
            {/each}
          </div>
        </div>
      {/each}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
