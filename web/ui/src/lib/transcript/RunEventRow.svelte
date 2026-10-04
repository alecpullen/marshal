<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireRunEvent } from '../stack'

  let { event }: { event: WireRunEvent } = $props()
  const bad = $derived(event.kind === 'verifyFailed' || event.severity === 'high' || event.severity === 'blocker')
  const label: Record<string, string> = {
    verifyFailed: 'verify failed',
    gateSkipped: 'gate skipped',
    review: 'review',
    commit: 'commit',
    retry: 'retry',
    concern: 'concern',
    taskDone: 'task done',
  }
</script>

<div class="py-0.5 text-xs">
  <div class="flex gap-2 {bad ? 'text-err' : 'text-muted'}">
    <span>{bad ? glyph.Error : glyph.Ambient}</span>
    <span>{label[event.kind] ?? event.kind}{event.taskN ? ` · task ${event.taskN}` : ''}{event.title ? ` · ${event.title}` : ''}</span>
    {#if event.detail}<span class="text-muted">{event.detail}</span>{/if}
  </div>
  {#if event.body}<pre class="mt-1 max-h-40 overflow-auto rounded bg-surface p-2 font-mono whitespace-pre-wrap text-muted">{event.body}</pre>{/if}
</div>
