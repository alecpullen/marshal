<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireCompletion } from '../stack'
  import { compactDuration } from './format'

  let { completion }: { completion: WireCompletion } = $props()

  let expanded = $state(false)
  const isJob = $derived(completion.source === 'job')
  const tone = $derived(completion.outcome === 'done' ? 'text-ok' : completion.outcome === 'outcome unknown' ? 'text-warn' : 'text-err')
  const outcome = $derived(completion.stopCause ? `${completion.outcome} · ${completion.stopCause}` : completion.outcome)
</script>

<div class="py-px text-xs">
  <button
    type="button"
    class="flex w-full cursor-pointer items-baseline gap-2 text-left"
    data-testid="completion-row"
    aria-expanded={expanded}
    onclick={() => (expanded = !expanded)}
  >
    <span class={isJob ? 'text-info' : 'text-violet'}>{isJob ? glyph.Job : glyph.Agent}</span>
    <span class="shrink-0 font-mono text-sub">{completion.id}</span>
    <span class="truncate text-sub">{completion.label}</span>
    <span class="shrink-0 {tone}">{completion.outcome}</span>
    {#if completion.measure}<span class="shrink-0 text-muted">{completion.measure}</span>{/if}
    {#if completion.durationMs}<span class="shrink-0 text-muted">{compactDuration(completion.durationMs)}</span>{/if}
    <span class="ml-auto shrink-0 text-muted">{expanded ? glyph.DisclosureExpanded : glyph.DisclosureCollapsed}</span>
  </button>
  {#if !expanded && completion.summary}
    <div class="ml-5 truncate text-muted">{completion.summary}</div>
  {/if}
  {#if expanded}
    <dl class="mt-1 ml-5 flex flex-col gap-0.5 text-muted">
      <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">outcome</dt><dd class={tone}>{outcome}</dd></div>
      {#if isJob && completion.command}
        <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">command</dt><dd class="truncate font-mono">{completion.command}</dd></div>
      {:else if !isJob}
        <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">task</dt><dd class="truncate">{completion.label}</dd></div>
      {/if}
      {#if completion.cwd}
        <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">cwd</dt><dd class="truncate font-mono">{completion.cwd}</dd></div>
      {/if}
      {#if completion.durationMs}
        <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">duration</dt><dd>{compactDuration(completion.durationMs)}</dd></div>
      {/if}
      {#if completion.measure}
        <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">progress</dt><dd>{completion.measure}</dd></div>
      {/if}
      {#if completion.output}
        <div class="flex flex-col gap-0.5">
          <dt class="text-dim">output</dt>
          <dd><pre class="max-h-48 overflow-auto rounded bg-surface p-2 font-mono whitespace-pre-wrap text-muted">{completion.output}</pre></dd>
        </div>
      {/if}
      {#if completion.report}
        <div class="flex flex-col gap-0.5">
          <dt class="text-dim">report</dt>
          <dd class="whitespace-pre-wrap">{completion.report}</dd>
        </div>
      {/if}
      {#if completion.error}
        <div class="flex gap-2"><dt class="w-16 shrink-0 text-dim">error</dt><dd class="text-err">{completion.error}</dd></div>
      {/if}
    </dl>
  {/if}
</div>
