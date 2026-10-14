<script lang="ts">
  import Tag from '../ui/Tag.svelte'
  import Button from '../ui/Button.svelte'
  import { glyph } from '../glyphs'
  import type { StackState, WireBand } from '../stack'
  import { compactDuration } from './format'

  let {
    stack,
    now,
    hint = '',
    selecting = false,
    onStop,
    onJumpLive,
    onOpenTasks,
  }: {
    stack: StackState
    now: number
    hint?: string
    /** The dock is on a selected node rather than following the agent. */
    selecting?: boolean
    onStop: () => void
    onJumpLive?: () => void
    /** Opens the full list; the progress row is its handle when given. */
    onOpenTasks?: () => void
  } = $props()

  const live = $derived([...stack.nodes.values()].filter((n) => n.live))
  const step = $derived(live.find((n) => n.kind === 'step' && n.step))
  const running = $derived(live.find((n) => n.tool?.running)?.tool)
  const startedAt = $derived(running?.running?.startedAt || step?.step?.startedAt || 0)
  const elapsed = $derived(startedAt ? compactDuration(Math.max(0, now - startedAt)) : '')

  // The band is a node like any other: viewmodel builds it, so this bar and
  // the TUI cannot show different numbers for the same session.
  const band = $derived<WireBand | undefined>(stack.nodes.get('band')?.band)

  const progress = $derived.by(() => {
    const b = band
    if (!b?.todos || !b.total) return ''
    const cells = Math.min(b.total, 10)
    const filled = Math.min(cells, Math.max(0, Math.round((b.done / b.total) * cells)))
    return glyph.ProgressFull.repeat(filled) + glyph.ProgressEmpty.repeat(cells - filled)
  })

  // What fits on the checklist row, with the rest counted. The row is a fixed
  // width in the layout, so the count is honest rather than a clipped list.
  const checklist = $derived.by(() => {
    const b = band
    if (!b?.todos) return null
    const items = b.next ?? []
    const shown = items.slice(0, 2)
    return { done: b.done, shown, rest: items.length - shown.length }
  })

  // At most four entity rows, then a count: the band is a status strip, and
  // an unbounded list would push the transcript off the screen.
  const entities = $derived.by(() => {
    const all = band?.entities ?? []
    return { shown: all.slice(0, 4), rest: Math.max(0, all.length - 4) }
  })
</script>

{#if live.length > 0 || band?.todos}
  <div class="border-t border-border bg-surface text-xs" role="status" data-testid="now-bar">
    {#if selecting}
      <button
        type="button"
        class="flex w-full items-center gap-2 border-b border-border px-4 py-1 text-left text-muted hover:text-fg"
        data-testid="mirror-row"
        onclick={() => onJumpLive?.()}
      >
        <span>{glyph.FollowDown}</span>
        <span class="min-w-0 truncate">live: {step?.step?.headline ?? 'working'}</span>
      </button>
    {/if}

    {#if band?.todos}
      {#snippet progressRow()}
        {#if progress}<span class="shrink-0 font-mono text-accent">{progress}</span>{/if}
        <!-- Count and active item are one phrase ("2/9 · Task 2") in one
             element, so the count can never survive while the item it belongs
             to is clipped away. -->
        <span class="min-w-0 truncate font-mono">{band.done}/{band.total}{band.active ? ` · ${band.active}` : ''}</span>
        <span class="ml-auto flex shrink-0 items-center gap-2">
          {#if band.stale}
            <span class="text-warn">stale {compactDuration(band.staleSince ? Math.max(0, now - band.staleSince) : (band.staleAgeMs ?? 0))}</span>
          {/if}
          {#if elapsed}<span class="font-mono text-muted">{elapsed}</span>{/if}
        </span>
      {/snippet}
      {#if onOpenTasks}
        <button
          type="button"
          class="flex w-full cursor-pointer items-center gap-2 px-4 pt-1.5 text-left hover:text-fg"
          title="Show all tasks"
          onclick={onOpenTasks}
          data-testid="band-progress"
        >
          {@render progressRow()}
        </button>
      {:else}
        <div class="flex items-center gap-2 px-4 pt-1.5" data-testid="band-progress">{@render progressRow()}</div>
      {/if}
      {#if checklist}
        <div class="flex items-center gap-2 px-4 pt-0.5 text-muted" data-testid="band-checklist">
          <!-- One element per phrase, so the done count and the next items
               read as the two facts they are rather than one run-on. -->
          <span class="shrink-0">{glyph.OK} {checklist.done} done</span>
          {#if checklist.shown.length}
            <span class="min-w-0 truncate">{glyph.Running} next: {checklist.shown.join(' · ')}</span>
          {/if}
          {#if checklist.rest > 0}<span class="shrink-0">+{checklist.rest}</span>{/if}
        </div>
      {/if}
    {/if}

    {#if entities.shown.length}
      {#each entities.shown as e (e.ref)}
        <div class="flex items-center gap-2 px-4 pt-0.5 text-muted" data-testid="band-entity">
          <span class="min-w-0 truncate">{e.label}</span>
          {#if e.detail}<span class="min-w-0 truncate font-mono">{e.detail}</span>{/if}
          {#if e.startedAt}<span class="ml-auto shrink-0 font-mono">{compactDuration(Math.max(0, now - e.startedAt))}</span>{/if}
        </div>
      {/each}
    {/if}
    {#if entities.rest > 0}
      <div class="px-4 pt-0.5 text-muted" data-testid="band-more">… {entities.rest} more</div>
    {/if}

    {#if live.length > 0}
      <div class="flex items-center gap-2 px-4 py-2">
        <span class="animate-pulse text-accent">{glyph.Running}</span>
        {#if step?.step}
          {#if step.step.owner || step.step.role}<Tag role={step.step.role}>{step.step.owner || step.step.role}</Tag>{/if}
          <span class="min-w-0 truncate italic">{step.step.headline}</span>
        {:else}
          <span class="text-muted">working</span>
        {/if}
        {#if running}
          <span class="min-w-0 truncate font-mono text-muted">{running.display}{running.running?.target ? ` ${running.running.target}` : ''}</span>
        {/if}
        {#if elapsed}<span class="shrink-0 font-mono text-muted">{elapsed}</span>{/if}
        <span class="ml-auto flex shrink-0 items-center gap-2">
          {#if hint}<span class="text-warn">{hint}</span>{/if}
          <Button variant="ghost" class="min-h-0 px-2 py-0.5 text-xs" onclick={onStop}>Stop</Button>
        </span>
      </div>
    {/if}
  </div>
{/if}
