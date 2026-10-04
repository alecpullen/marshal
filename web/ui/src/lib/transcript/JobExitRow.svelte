<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireJobExit } from '../stack'
  import { compactDuration } from './format'

  let { job, open = false }: { job: WireJobExit; open?: boolean } = $props()
</script>

<div class="py-0.5 text-xs">
  <div class="flex gap-2 font-mono {job.exitCode === 0 ? 'text-muted' : 'text-err'}">
    <span>{glyph.Job}</span>
    <span class="truncate">{job.command}</span>
    <span class="shrink-0">exit {job.exitCode}{job.durationMs ? ` · ${compactDuration(job.durationMs)}` : ''}</span>
  </div>
  {#if open && job.output}<pre class="mt-1 max-h-40 overflow-auto rounded bg-surface p-2 font-mono whitespace-pre-wrap text-muted">{job.output}</pre>{/if}
</div>
