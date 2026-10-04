<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireThinking } from '../stack'
  import { compactDuration } from './format'

  let { thinking, live = false, open = false }: { thinking: WireThinking; live?: boolean; open?: boolean } = $props()
  let expanded = $state(false)
  const show = $derived(open || expanded)
</script>

<div class="py-0.5 text-xs text-muted">
  <button class="flex cursor-pointer items-center gap-2 text-left hover:text-sub" onclick={() => (expanded = !expanded)} aria-expanded={show}>
    <span class="text-violet">{glyph.Thinking}</span>
    <span>{live ? 'thinking…' : `thought${thinking.durationMs ? ` for ${compactDuration(thinking.durationMs)}` : ''}`}</span>
    <span>{show ? glyph.DisclosureExpanded : glyph.DisclosureCollapsed}</span>
  </button>
  {#if show}
    <p class="mt-1 border-l-2 border-line pl-3 whitespace-pre-wrap text-muted">{thinking.text}</p>
  {/if}
</div>
