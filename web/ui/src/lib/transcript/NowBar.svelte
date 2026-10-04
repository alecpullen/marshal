<script lang="ts">
  import Tag from '../ui/Tag.svelte'
  import Button from '../ui/Button.svelte'
  import { glyph } from '../glyphs'
  import type { StackState } from '../stack'
  import { compactDuration } from './format'

  let {
    stack,
    now,
    hint = '',
    selecting = false,
    onStop,
    onJumpLive,
  }: {
    stack: StackState
    now: number
    hint?: string
    /** The dock is on a selected node rather than following the agent. */
    selecting?: boolean
    onStop: () => void
    onJumpLive?: () => void
  } = $props()

  const live = $derived([...stack.nodes.values()].filter((n) => n.live))
  const step = $derived(live.find((n) => n.kind === 'step' && n.step))
  const running = $derived(live.find((n) => n.tool?.running)?.tool)
  const startedAt = $derived(running?.running?.startedAt || step?.step?.startedAt || 0)
  const elapsed = $derived(startedAt ? compactDuration(Math.max(0, now - startedAt)) : '')
</script>

{#if live.length > 0}
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
  </div>
{/if}
