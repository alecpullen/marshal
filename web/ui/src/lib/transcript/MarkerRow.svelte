<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireMarker } from '../stack'
  import { compactDuration } from './format'

  let { marker, onJump }: { marker: WireMarker; onJump?: (id: string) => void } = $props()
  const word = $derived(marker.failed && marker.labels.length === 1 ? 'failed' : 'done')
</script>

<button class="flex w-full cursor-pointer items-baseline gap-2 text-left text-xs text-muted hover:text-sub" onclick={() => onJump?.(marker.refs[0])}>
  <span class={marker.failed ? 'text-err' : 'text-ok'}>{marker.failed ? glyph.Error : glyph.OK}</span>
  <span class="truncate">{marker.labels.join(' ')} {word}{marker.durationMs ? ` · ${compactDuration(marker.durationMs)}` : ''}</span>
</button>
