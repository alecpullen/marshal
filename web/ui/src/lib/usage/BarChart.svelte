<script lang="ts">
  let {
    bars,
    cap = 0,
    format = (n: number) => `$${n.toFixed(2)}`,
    label = 'Cost by day',
  }: { bars: { key: string; value: number }[]; cap?: number; format?: (n: number) => string; label?: string } = $props()

  const W = 640
  const H = 160
  const PAD = { top: 8, right: 8, bottom: 20, left: 8 }

  const peak = $derived(Math.max(cap > 0 ? cap : 0, ...bars.map((b) => b.value), 0))
  const plotW = W - PAD.left - PAD.right
  const plotH = H - PAD.top - PAD.bottom
  const slot = $derived(bars.length ? plotW / bars.length : plotW)
  const barW = $derived(Math.max(2, slot * 0.7))
  const y = (v: number) => PAD.top + plotH - (peak > 0 ? (v / peak) * plotH : 0)
</script>

<svg viewBox="0 0 {W} {H}" role="img" aria-label={label} class="w-full" preserveAspectRatio="none" style="height: {H}px">
  <line x1={PAD.left} x2={W - PAD.right} y1={y(0)} y2={y(0)} class="stroke-border" stroke-width="1" />
  {#each bars as b, i (b.key)}
    {@const h = y(0) - y(b.value)}
    <rect
      data-testid="bar"
      x={PAD.left + i * slot + (slot - barW) / 2}
      y={y(b.value)}
      width={barW}
      height={Math.max(h, b.value > 0 ? 1 : 0)}
      rx="2"
      class="fill-accent"
    >
      <title>{b.key}: {format(b.value)}</title>
    </rect>
  {/each}
  {#if cap > 0}
    <line data-testid="cap-line" x1={PAD.left} x2={W - PAD.right} y1={y(cap)} y2={y(cap)} class="stroke-err" stroke-width="1.5" stroke-dasharray="5 4">
      <title>Daily cap {format(cap)}</title>
    </line>
  {/if}
  {#if bars.length > 0}
    <text x={PAD.left} y={H - 5} class="fill-muted" font-size="11">{bars[0].key}</text>
    <text x={W - PAD.right} y={H - 5} text-anchor="end" class="fill-muted" font-size="11">{bars[bars.length - 1].key}</text>
  {/if}
</svg>
