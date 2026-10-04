<script lang="ts">
  import { glyph } from '../glyphs'
  import type { WireReceipt } from '../stack'
  import { receiptText } from './format'

  let { receipt }: { receipt: WireReceipt } = $props()
</script>

<div class="flex gap-2 py-1 font-mono text-xs text-muted">
  <span class={receipt.salvaged ? 'text-warn' : 'text-ok'}>{receipt.salvaged ? glyph.Warning : glyph.OK}</span>
  <span>{receiptText(receipt)}</span>
</div>
