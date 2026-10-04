<script lang="ts">
  import Tag from '../ui/Tag.svelte'
  import { glyph, roleTone } from '../glyphs'
  import type { WireSubagent } from '../stack'

  let { sub }: { sub: WireSubagent } = $props()
  const mark = $derived(sub.status === 'done' ? glyph.OK : sub.status === 'failed' ? glyph.Error : glyph.Running)
  const tone = $derived(sub.status === 'done' ? 'text-ok' : sub.status === 'failed' ? 'text-err' : 'text-accent')
</script>

<div class="my-1 rounded-md border border-border bg-surface px-3 py-2 text-sm">
  <div class="flex flex-wrap items-center gap-2">
    <span class={tone}>{mark}</span>
    <span class="font-medium">{sub.label}</span>
    {#if sub.role}<Tag tone={roleTone(sub.role)}>{sub.role}</Tag>{/if}
    {#if sub.toolCalls}<span class="text-xs text-muted">{sub.toolCalls} tool{sub.toolCalls === 1 ? '' : 's'}</span>{/if}
    {#if sub.status === 'running' && sub.currentTool}<span class="font-mono text-xs text-muted">{sub.currentTool}</span>{/if}
  </div>
  {#if sub.summary}<p class="mt-1 text-xs whitespace-pre-wrap text-muted">{sub.summary}</p>{/if}
  {#if sub.error}<p class="mt-1 text-xs text-err">{sub.error}</p>{/if}
</div>
