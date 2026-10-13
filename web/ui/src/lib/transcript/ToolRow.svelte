<script lang="ts">
  import { glyph, toolGlyph } from '../glyphs'
  import type { WireCall, WireTool } from '../stack'
  import { compactDuration, subjectFirst } from './format'
  import type { Density } from './density'

  let { tool, density, now }: { tool: WireTool; density: Density; now: number } = $props()

  let expanded = $state(false)
  const calls = $derived(tool.calls ?? [])
  const merged = $derived(calls.length > 1)
  const open = $derived(density === 'full' || expanded)
  const failedAny = $derived(calls.some((c) => c.failed))
  const job = $derived(tool.job)
  const jobFailed = $derived(!!job && !job.running && !job.lost && job.exitCode !== 0)
  const lead = $derived(
    job
      ? job.running
        ? glyph.Running
        : job.lost
          ? glyph.Warning
          : jobFailed
            ? glyph.Error
            : glyph.Job
      : tool.running
        ? glyph.Running
        : failedAny
          ? glyph.Error
          : toolGlyph(tool.name),
  )
  const leadTone = $derived(
    job
      ? job.running
        ? 'text-accent'
        : job.lost
          ? 'text-warn'
          : jobFailed
            ? 'text-err'
            : 'text-muted'
      : tool.running
        ? 'text-accent'
        : failedAny
          ? 'text-err'
          : 'text-muted',
  )

  const failure = (c: WireCall) => c.error || (c.exitCode ? `exit ${c.exitCode}` : c.failed ? 'failed' : '')
  const elapsed = $derived(tool.running?.startedAt ? compactDuration(Math.max(0, now - tool.running.startedAt)) : '')
  const anyTruncated = $derived(calls.some((c) => c.truncated) || !!tool.running?.truncated)
  // A job card reports the job's own clock: running since it started, or the
  // duration it ran once it settled. A lost job has neither an exit code nor
  // a known run time.
  const jobTail = $derived.by(() => {
    if (!job) return ''
    if (job.running) return `running${job.startedAt ? ` · ${compactDuration(Math.max(0, now - job.startedAt))}` : ''}`
    if (job.lost) return 'ended with session'
    return `exit ${job.exitCode}${job.durationMs ? ` · ${compactDuration(job.durationMs)}` : ''}`
  })
</script>

{#snippet line(target: string | undefined, summary: string | undefined, fail: string)}
  <span class="flex min-w-0 items-baseline gap-2">
    {#if subjectFirst(tool.name)}
      <span class="truncate font-mono text-sub">{target}</span>
      <span class="shrink-0 text-muted">{tool.display}</span>
    {:else}
      <span class="shrink-0 text-sub">{tool.display}</span>
      <span class="truncate font-mono text-muted">{target}</span>
    {/if}
    {#if fail}<span class="shrink-0 text-err">{fail}</span>{:else if summary}<span class="truncate text-muted">{summary}</span>{/if}
  </span>
{/snippet}

<div class="py-px text-xs">
  {#if job}
    <button class="flex w-full cursor-pointer items-baseline gap-2 text-left" onclick={() => (expanded = !expanded)} aria-expanded={open}>
      <span class={leadTone}>{lead}</span>
      <span class="shrink-0 font-mono text-sub">{job.id}</span>
      <span class="truncate font-mono text-muted">{job.command}</span>
      <span class="ml-auto shrink-0 text-muted">{jobTail}</span>
    </button>
    {#if open && job.output}
      <pre class="mt-1 ml-5 max-h-48 overflow-auto rounded bg-surface p-2 font-mono whitespace-pre-wrap text-muted">{job.output}</pre>
    {/if}
  {:else if tool.running}
    <button class="flex w-full cursor-pointer items-baseline gap-2 text-left" onclick={() => (expanded = !expanded)}>
      <span class="animate-pulse {leadTone}">{lead}</span>
      {@render line(tool.running.target, '', '')}
      {#if elapsed}<span class="ml-auto shrink-0 text-muted">{elapsed}</span>{/if}
    </button>
    {#if open && tool.running.output}
      <pre class="mt-1 ml-5 max-h-48 overflow-auto rounded bg-surface p-2 font-mono whitespace-pre-wrap text-muted">{tool.running.output}</pre>
    {/if}
  {:else if merged}
    <button class="flex w-full cursor-pointer items-baseline gap-2 text-left" onclick={() => (expanded = !expanded)} aria-expanded={open}>
      <span class={leadTone}>{lead}</span>
      <span class="text-sub">{tool.display}</span>
      <span class="font-mono text-muted">×{calls.length}</span>
      <span class="ml-auto text-muted">{open ? glyph.DisclosureExpanded : glyph.DisclosureCollapsed}</span>
    </button>
    {#if open}
      {#each calls as c, i (c.callId ?? i)}
        <div class="ml-5 flex items-baseline gap-2 py-px">
          <span class={c.failed ? 'text-err' : 'text-muted'}>{c.failed ? glyph.Error : glyph.Ambient}</span>
          {@render line(c.target, c.summary, failure(c))}
        </div>
      {/each}
    {/if}
  {:else if calls.length === 1}
    {@const c = calls[0]}
    <button class="flex w-full cursor-pointer items-baseline gap-2 text-left" onclick={() => (expanded = !expanded)} aria-expanded={open}>
      <span class={leadTone}>{lead}</span>
      {@render line(c.target, c.summary, failure(c))}
      <span class="ml-auto shrink-0 text-muted">{open ? glyph.DisclosureExpanded : glyph.DisclosureCollapsed}</span>
    </button>
    {#if open && c.output}
      <pre class="mt-1 ml-5 max-h-48 overflow-auto rounded bg-surface p-2 font-mono whitespace-pre-wrap text-muted">{c.output}</pre>
    {/if}
  {/if}
  {#if open && anyTruncated}
    <div class="ml-5 text-[11px] text-dim">output truncated (full view in W2)</div>
  {/if}
</div>
