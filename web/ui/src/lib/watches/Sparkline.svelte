<script lang="ts">
  import type { WatchSample } from '../api'
  import { sparkGeometry } from './spark'

  let {
    samples,
    w = 96,
    h = 24,
    threshold = null,
    marks = false,
    now,
    label = 'Last 24 hours',
  }: { samples: WatchSample[]; w?: number; h?: number; threshold?: number | null; marks?: boolean; now?: number; label?: string } = $props()

  const g = $derived(sparkGeometry(samples, w, h, { now, threshold, pad: marks ? 6 : 2 }))
</script>

<svg viewBox="0 0 {w} {h}" width={marks ? undefined : w} {h} class={marks ? 'w-full' : ''} preserveAspectRatio="none" role="img" aria-label={label}>
  {#if g.thresholdY !== null}
    <line data-testid="threshold-line" x1="0" x2={w} y1={g.thresholdY} y2={g.thresholdY} class="stroke-warn" stroke-width="1" stroke-dasharray="4 3" vector-effect="non-scaling-stroke" />
  {/if}
  {#if g.path}
    <path d={g.path} fill="none" class="stroke-accent" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke" />
  {/if}
  {#if marks}
    {#each g.points.filter((p) => p.tripped) as p (p.x)}
      <circle data-testid="trip-mark" cx={p.x} cy={p.y} r="3.5" class="fill-err" />
    {/each}
  {/if}
</svg>
