<script lang="ts">
  import { renderMarkdown } from '../markdown'
  import { glyph } from '../glyphs'
  import type { WireMessage } from '../stack'

  let { message }: { message: WireMessage } = $props()
  const html = $derived(message.role === 'user' ? '' : renderMarkdown(message.content))
</script>

{#if message.role === 'user'}
  <div class="flex gap-2 py-2 text-sm font-medium">
    <span class="text-accent">{glyph.User}</span>
    <span class="whitespace-pre-wrap">{message.content}</span>
  </div>
{:else}
  <div class="py-1 text-sm {message.final ? 'border-l-2 border-accent pl-3' : ''}">
    {#if message.salvaged}<div class="text-xs text-warn">{glyph.Warning} salvaged{message.salvageReason ? ` · ${message.salvageReason}` : ''}</div>{/if}
    <!-- renderMarkdown sanitises with DOMPurify. -->
    <div class="prose-sm max-w-none break-words [&_a]:text-accent [&_code]:font-mono [&_pre]:overflow-x-auto [&_pre]:rounded [&_pre]:bg-surface [&_pre]:p-2">{@html html}</div>
    {#if message.usage}<div class="mt-1 text-xs text-muted">{message.usage}</div>{/if}
  </div>
{/if}
