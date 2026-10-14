<script lang="ts">
  import { glyph } from '../glyphs'
  import type { StackState, WireBand } from '../stack'
  import { compactDuration, plural } from '../transcript/format'

  /*
    The whole todo list: the TUI's Ctrl+T Tasks panel. The band node carries
    it, built by viewmodel with the same per-task counts the TUI shows, so the
    two views cannot disagree. Unlike the now bar it stays when the turn
    ends, which is when "what is left?" gets asked.
  */
  let {
    stack,
    now,
    onJump,
  }: {
    stack: StackState
    now: number
    /** Scrolls the transcript to the first beat worked under a task. */
    onJump?: (ref: string) => void
  } = $props()

  const band = $derived<WireBand | undefined>(stack.nodes.get('band')?.band)
  const tasks = $derived(band?.tasks ?? [])
  const done = $derived(tasks.filter((t) => t.status === 'completed').length)

  function detail(t: { steps?: number; workMs?: number }): string {
    if (!t.steps) return ''
    const parts = [plural(t.steps, 'step')]
    if (t.workMs) parts.push(compactDuration(t.workMs))
    return parts.join(' · ')
  }
</script>

<div class="flex flex-col text-xs" data-testid="tasks">
  {#if tasks.length === 0}
    <p class="p-3 text-muted">No task list in this session.</p>
  {:else}
    <div class="flex items-center gap-2 border-b border-border px-3 py-1.5">
      <span class="font-mono">Tasks {done}/{tasks.length}</span>
      {#if band?.stale}
        <span class="ml-auto text-warn" title="The list has not been updated for a while">
          stale {compactDuration(band.staleSince ? Math.max(0, now - band.staleSince) : (band.staleAgeMs ?? 0))}
        </span>
      {/if}
    </div>
    <ul class="flex flex-col py-1">
      {#each tasks as t, i (t.id || `#${i}`)}
        {@const d = detail(t)}
        <li>
          <button
            type="button"
            class="flex w-full items-baseline gap-2 px-3 py-1 text-left enabled:hover:bg-hover disabled:cursor-default"
            data-testid="task-row"
            data-status={t.status}
            disabled={!t.ref || !onJump}
            title={t.ref ? 'Jump to where this task started' : undefined}
            onclick={() => t.ref && onJump?.(t.ref)}
          >
            {#if t.status === 'completed'}
              <span class="w-3 shrink-0 text-ok">{glyph.OK}</span>
              <span class="min-w-0 flex-1 text-muted">{t.content}</span>
            {:else if t.status === 'in_progress'}
              <span class="w-3 shrink-0 text-accent">{glyph.Running}</span>
              <span class="min-w-0 flex-1 font-semibold">{t.content}</span>
            {:else}
              <span class="w-3 shrink-0 text-muted">{glyph.Ambient}</span>
              <span class="min-w-0 flex-1">{t.content}</span>
            {/if}
            {#if d}<span class="shrink-0 font-mono text-muted">{d}</span>{/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>
